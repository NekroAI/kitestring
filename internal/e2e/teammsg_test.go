package e2e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/client"
	"github.com/NekroAI/kitestring/internal/store"
)

type delivery struct {
	Code   string `json:"code"`
	To     string `json:"to"`
	Tool   string `json:"tool"`
	Status string `json:"status"`
	Reason string `json:"reason"`
}

type sentRes struct {
	Messages []struct {
		Code       string     `json:"code"`
		Broadcast  bool       `json:"broadcast"`
		FYI        bool       `json:"fyi"`
		Recipients []delivery `json:"recipients"`
	} `json:"messages"`
	Text string `json:"text"`
}

// R-249 P3：点对点（发给顾问、发给无身份的会话、发给离线会话被退回）、广播（按身份过滤、跳过自己 / 后台任务 / 失联会话、
// 开发发「要动手」广播被拒）、送达状态在 等对方醒来 → 已送达 / 对方不在线 之间转换、不能冒用发送方。真实编译 ks 跑命令行一侧。
func TestTeamP2PBroadcastDelivery(t *testing.T) {
	e := setup(t)
	bin := filepath.Join(t.TempDir(), "ks")
	if out, err := exec.Command("go", "build", "-o", bin, "../../cmd/ks").CombinedOutput(); err != nil {
		t.Fatalf("编译 ks 失败：%v\n%s", err, out)
	}
	baseEnv := []string{}
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "CLAUDE_CODE_SESSION_ID=") || strings.HasPrefix(kv, "DSH_") {
			continue
		}
		baseEnv = append(baseEnv, kv)
	}
	// 按父进程认会话时会往上找 5 层进程里的 Claude Code / Codex 本体：在 Claude Code 里跑测试会认到最近一次 hook 的会话。
	// 隔几层 shell，让它认不到，退回按 CLAUDE_CODE_SESSION_ID 认当前会话（与 Claude Code 里一致）。
	wrap := filepath.Join(t.TempDir(), "wrap.sh")
	if err := os.WriteFile(wrap, []byte("#!/bin/sh\n\"$@\"\nexit $?\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	ks := func(sid string, args ...string) (string, error) {
		cmd := exec.Command(wrap, append([]string{wrap, wrap, wrap, bin}, args...)...)
		cmd.Env = append(append([]string{}, baseEnv...), "CLAUDE_CODE_SESSION_ID="+sid)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	cwd, _ := os.Getwd()
	c := client.New(30 * time.Second)
	ctx := context.Background()
	up := func(tool, sid string) {
		t.Helper()
		ev := map[string]any{"session_id": sid, "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "上线"}
		if tool == "codex" {
			ev["turn_id"] = "t0"
		} else {
			ev["prompt_id"] = "p0"
		}
		hook(t, tool, ev)
		e.waitFor(sid+" 入库", func() bool { return e.session(tool+":"+sid).Key != "" })
	}
	setRole := func(key, role string) {
		t.Helper()
		if err := c.Do(ctx, "POST", "/agent/sessions/role", map[string]any{"session_key": key, "role": role}, nil); err != nil {
			t.Fatal(err)
		}
	}
	for _, sid := range []string{"pm", "adv", "dev", "rev", "gone", "lost", "bg"} {
		up("claude", sid)
	}
	up("codex", "cx") // 没有身份的 Codex 会话
	setRole("claude:pm", "pm")
	setRole("claude:adv", "advisor")
	setRole("claude:dev", "dev")
	setRole("claude:rev", "reviewer")
	setRole("claude:lost", "dev")
	hook(t, "codex", map[string]any{"session_id": "cx", "cwd": cwd, "hook_event_name": "Stop", "turn_id": "t0"})
	e.waitFor("Codex 一轮结束", func() bool { return e.session("codex:cx").State == store.StateWaitReply })
	hook(t, "claude", map[string]any{"session_id": "gone", "cwd": cwd, "hook_event_name": "SessionEnd"})
	e.waitFor("会话结束", func() bool { return e.session("claude:gone").State == store.StateEnded })
	if _, err := e.store.DB().Exec(`UPDATE sessions SET state = ? WHERE key = 'claude:lost'`, store.StateLost); err != nil {
		t.Fatal(err)
	}
	if _, err := e.store.DB().Exec(`UPDATE sessions SET background = 1 WHERE key = 'claude:bg'`); err != nil {
		t.Fatal(err)
	}

	// 1. 点对点发给顾问：顾问挂着后台 ks wait，马上被叫醒；返回逐个收件人的送达状态
	// 设身份时留下的通知先收掉；顾问第一次挂 ks wait 时 Hub 还会补发一次身份调整提示（R-190），
	// 它是挂上之后才生成的，可能晚到，所以一直收到它为止，免得它在后面把顾问叫醒。
	advWait := parkWait(t, e, c, "claude:adv")
	for i := 0; i < 5; i++ {
		got := woke(t, advWait)
		advWait = parkWait(t, e, c, "claude:adv")
		if strings.Contains(got, "身份调整") {
			break
		}
	}
	hook(t, "claude", map[string]any{"session_id": "adv", "cwd": cwd, "hook_event_name": "Stop"})
	e.waitFor("顾问待命", func() bool { return e.session("claude:adv").State == store.StateParked })
	out, err := ks("pm", "team", "tell", "R-249 方案看一眼", "--to", "顾问")
	if err != nil || !strings.Contains(out, "已送给顾问 claude:adv（Claude Code）") || !strings.Contains(out, "ks team sent D-") {
		t.Fatalf("发给顾问：%v\n%s", err, out)
	}
	woke(t, advWait, "R-249 方案看一眼", "（claude:pm） 留言")
	var sent sentRes
	e.waitFor("顾问已送达", func() bool {
		sent = sentRes{}
		_ = c.Do(ctx, "GET", "/agent/team/sent?session_key=claude:pm", nil, &sent)
		return len(sent.Messages) == 1 && sent.Messages[0].Recipients[0].Status == "delivered"
	})
	if r := sent.Messages[0].Recipients[0]; r.To != "claude:adv" || !strings.Contains(r.Reason, "已送达（") {
		t.Fatalf("送达状态：%+v", r)
	}

	// 2. 点对点发给没有身份的 Codex 会话（--to 填会话键）：等对方醒来，写明 Codex 要等用户下次发消息；用户发消息后变为已送达
	var tr struct {
		Code       string     `json:"code"`
		Message    string     `json:"message"`
		Recipients []delivery `json:"recipients"`
	}
	if err := c.Do(ctx, "POST", "/agent/team/tell", map[string]any{"session_key": "claude:pm", "text": "R-249 帮忙跑一下 Codex", "to": "codex:cx"}, &tr); err != nil {
		t.Fatal(err)
	}
	if len(tr.Recipients) != 1 || tr.Recipients[0].Status != "pending_wake" || !strings.Contains(tr.Recipients[0].Reason, "Codex 不能后台叫醒，等用户下次发消息") || tr.Recipients[0].Tool != "Codex 终端" && tr.Recipients[0].Tool != "Codex" {
		t.Fatalf("发给无身份 Codex 会话：%+v", tr)
	}
	if !strings.Contains(tr.Message, "已送给会话 codex:cx") || !strings.Contains(tr.Message, "等对方醒来") {
		t.Fatalf("返回文字：%s", tr.Message)
	}
	cxCode := tr.Code
	out, _ = ks("pm", "team", "sent", cxCode)
	if !strings.Contains(out, cxCode+" 点对点 · 要动手") || !strings.Contains(out, "无身份会话 codex:cx") || !strings.Contains(out, "等对方醒来——Codex 不能后台叫醒") {
		t.Fatalf("ks team sent 应显示等对方醒来：\n%s", out)
	}
	// Hub 建好消息后异步推给 daemon，用户这次发消息时 daemon 可能还没收到：重试到注入出现为止
	turn := 0
	e.waitFor("用户下次发消息时注入", func() bool {
		turn++
		got := hook(t, "codex", map[string]any{"session_id": "cx", "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "继续", "turn_id": fmt.Sprintf("t1-%d", turn)})
		return strings.Contains(got, "帮忙跑一下 Codex")
	})
	e.waitFor("Codex 已送达", func() bool {
		var s sentRes
		_ = c.Do(ctx, "GET", "/agent/team/sent?session_key=claude:pm&ref="+cxCode, nil, &s)
		return len(s.Messages) == 1 && s.Messages[0].Recipients[0].Status == "delivered"
	})

	// 3. 点对点发给已结束 / 失联的会话：报错退回
	for _, k := range []string{"claude:gone", "claude:lost"} {
		if err := c.Do(ctx, "POST", "/agent/team/tell", map[string]any{"session_key": "claude:pm", "text": "在吗", "to_session": k}, nil); err == nil || !strings.Contains(err.Error(), "不在线") || !strings.Contains(err.Error(), "退回") {
			t.Fatalf("发给离线会话 %s 应报错退回：%v", k, err)
		}
	}
	if out, err := ks("pm", "team", "tell", "在吗", "--to", "claude:gone"); err == nil || !strings.Contains(out, "不在线") {
		t.Fatalf("ks team tell 发给离线会话应报错：%v\n%s", err, out)
	}

	// 4. 广播按身份过滤：主调度发「要动手」给开发和评审；失联的开发跳过，不含自己、不含后台任务会话
	out, err = ks("pm", "team", "broadcast", "R-249 合并前请自查", "--role", "dev,评审")
	if err != nil {
		t.Fatalf("广播失败：%v\n%s", err, out)
	}
	for _, want := range []string{"已按「要动手」广播给身份为「dev,评审」的会话 2 个", "跳过离线 1 个", "开发 claude:dev（Claude Code）：等对方醒来", "评审 claude:rev（Claude Code）：等对方醒来", "开发 claude:lost（Claude Code）：对方不在线——失联"} {
		if !strings.Contains(out, want) {
			t.Errorf("广播输出应包含「%s」：\n%s", want, out)
		}
	}
	for _, no := range []string{"claude:adv", "codex:cx", "claude:bg", "claude:pm）"} {
		if strings.Contains(out, no) {
			t.Errorf("广播不应发给 %s：\n%s", no, out)
		}
	}
	// 全体广播：除了自己、后台任务、已结束的都收到（含没身份的 Codex 会话）
	var br struct {
		BroadcastID string     `json:"broadcast_id"`
		Codes       []string   `json:"codes"`
		Sent        int        `json:"sent"`
		Skipped     int        `json:"skipped"`
		Recipients  []delivery `json:"recipients"`
	}
	if err := c.Do(ctx, "POST", "/agent/team/broadcast", map[string]any{"session_key": "claude:pm", "text": "今晚 22 点部署", "fyi": true}, &br); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, r := range br.Recipients {
		got[r.To] = r.Status
	}
	if br.Sent != 4 || br.Skipped != 1 || br.BroadcastID == "" || len(br.Codes) != 4 || got["codex:cx"] != "pending_wake" || got["claude:lost"] != "offline" || got["claude:bg"] != "" || got["claude:gone"] != "" || got["claude:pm"] != "" {
		t.Fatalf("全体广播范围不对：%+v", br)
	}
	for _, r := range br.Recipients {
		if r.Status == "pending_wake" && !strings.Contains(r.Reason, "只告知") {
			t.Errorf("只告知的广播应写明不叫醒：%+v", r)
		}
	}
	rows, _ := e.store.BroadcastTells(ctx, br.BroadcastID)
	if len(rows) != 4 || !rows[0].FYI || rows[0].AssignedBy != "claude:pm" {
		t.Fatalf("一次广播拆成每个收件人一行、共用 broadcast_id、记 fyi：%+v", rows)
	}

	// 5. 开发发「要动手」广播被拒，提示改用 --fyi；加 --fyi 可以发
	if out, err := ks("dev", "team", "broadcast", "大家注意"); err == nil || !strings.Contains(out, "只有主调度、主助理能发") || !strings.Contains(out, "--fyi") {
		t.Fatalf("开发发要动手广播应被拒：%v\n%s", err, out)
	}
	if out, err := ks("dev", "team", "broadcast", "R-249 已推到分支", "--fyi"); err != nil || !strings.Contains(out, "已按「只告知」广播") {
		t.Fatalf("开发发只告知广播应可以：%v\n%s", err, out)
	}

	// 6. 送达状态 等对方醒来 → 对方不在线：评审没收到就结束了
	var s sentRes
	if err := c.Do(ctx, "GET", "/agent/team/sent?session_key=claude:pm", nil, &s); err != nil {
		t.Fatal(err)
	}
	if len(s.Messages) != 4 || !s.Messages[0].Broadcast || !s.Messages[0].FYI || len(s.Messages[0].Recipients) != 4 || !s.Messages[1].Broadcast || len(s.Messages[1].Recipients) != 2 {
		t.Fatalf("ks team sent 一次广播算一条：%+v", s)
	}
	revCode := ""
	for _, r := range s.Messages[1].Recipients {
		if r.To == "claude:rev" {
			revCode = r.Code
			if r.Status != "pending_wake" {
				t.Fatalf("评审还没收到：%+v", r)
			}
		}
	}
	hook(t, "claude", map[string]any{"session_id": "rev", "cwd": cwd, "hook_event_name": "SessionEnd"})
	e.waitFor("评审结束", func() bool { return e.session("claude:rev").State == store.StateEnded })
	out, _ = ks("pm", "team", "sent", revCode)
	if !strings.Contains(out, "广播（2 个收件人）") || !strings.Contains(out, "评审 claude:rev（Claude Code）：对方不在线——会话已结束") {
		t.Fatalf("评审结束后应显示对方不在线：\n%s", out)
	}

	// 7. 不能冒用发送方：识别到当前会话时 --session 写成别的会话被拒；只能查自己发的
	if out, err := ks("dev", "team", "broadcast", "冒用", "--session", "claude:pm"); err == nil || !strings.Contains(out, "不一致") {
		t.Fatalf("--session 冒用应被拒：%v\n%s", err, out)
	}
	if out, err := ks("dev", "team", "tell", "冒用", "--to", "claude:adv", "--session", "claude:pm"); err == nil || !strings.Contains(out, "不一致") {
		t.Fatalf("tell --session 冒用应被拒：%v\n%s", err, out)
	}
	if out, err := ks("dev", "team", "sent", cxCode); err == nil || !strings.Contains(out, "不是你发的") {
		t.Fatalf("查别人发的消息应被拒：%v\n%s", err, out)
	}
	if err := c.Do(ctx, "POST", "/agent/team/broadcast", map[string]any{"session_key": "claude:gone", "text": "x", "fyi": true, "session_claimed": true}, nil); err == nil || !strings.Contains(err.Error(), "没结束") {
		t.Fatalf("自报已结束的会话不能广播：%v", err)
	}
	// 收件人看到的发送方是真实的开发会话
	rows, _ = e.store.SentTells(ctx, "claude:dev", 10)
	if len(rows) == 0 || rows[0].FromRole != "dev" || !strings.Contains(rows[0].Answer, "Dev 「") || !strings.Contains(rows[0].Answer, "（claude:dev） 广播：") {
		t.Fatalf("广播署名应是开发自己：%+v", rows)
	}
}
