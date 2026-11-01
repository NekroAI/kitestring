package e2e

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/client"
	"github.com/NekroAI/kitestring/internal/store"
)

// R-249 P2：在线会话列表（ks team sessions，与 MCP team_sessions 同一接口）——真实编译 ks 跑一遍：
// Claude 主调度挂着后台 ks wait（待命、能立刻叫醒）、Claude 开发在跑（忙、这一轮结束后收到）、
// Codex 前台 ks wait（待命、能立刻叫醒）、Codex 一轮结束没挂 wait（在线、不能后台叫醒）、已结束的默认不列、--all 才列。
func TestTeamSessions(t *testing.T) {
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
	ks := func(env []string, args ...string) (string, error) {
		cmd := exec.Command(bin, args...)
		cmd.Env = append(append([]string{}, baseEnv...), env...)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	cwd, _ := os.Getwd()
	c := client.New(30 * time.Second)
	ctx := context.Background()

	// 开发（Claude）：在跑，手上有一条需求
	hook(t, "claude", map[string]any{"session_id": "ts-dev", "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "做 R-1", "prompt_id": "p1"})
	e.waitFor("开发入库", func() bool { return e.session("claude:ts-dev").Key != "" })
	if err := c.Do(ctx, "POST", "/agent/sessions/role", map[string]any{"session_key": "claude:ts-dev", "role": "dev"}, nil); err != nil {
		t.Fatal(err)
	}
	var r store.Requirement
	if err := c.Do(ctx, "POST", "/agent/requirements", map[string]any{"title": "会话通信", "session_key": "claude:ts-dev", "status": "in_progress"}, &r); err != nil {
		t.Fatal(err)
	}

	// 主调度（Claude）：一轮里挂上后台 ks wait，这一轮结束 → 待命
	hook(t, "claude", map[string]any{"session_id": "ts-pm", "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "上线", "prompt_id": "p0"})
	e.waitFor("主调度入库", func() bool { return e.session("claude:ts-pm").Key != "" })
	if err := c.Do(ctx, "POST", "/agent/sessions/role", map[string]any{"session_key": "claude:ts-pm", "role": "pm"}, nil); err != nil {
		t.Fatal(err)
	}
	pmWait := parkWait(t, e, c, "claude:ts-pm")
	hook(t, "claude", map[string]any{"session_id": "ts-pm", "cwd": cwd, "hook_event_name": "Stop"})
	e.waitFor("主调度待命", func() bool { return e.session("claude:ts-pm").State == store.StateParked })

	// Codex：一个挂着前台 ks wait（这一轮还在跑命令，算待命），一个一轮结束没挂 wait
	hook(t, "codex", map[string]any{"session_id": "ts-cx", "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "做完先待命", "turn_id": "t0"})
	e.waitFor("Codex 入库", func() bool { return e.session("codex:ts-cx").State == store.StateRunning })
	parkWait(t, e, c, "codex:ts-cx") // 测试结束时 daemon 退出，等待随之结束
	hook(t, "codex", map[string]any{"session_id": "ts-cx2", "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "看一眼", "turn_id": "t1"})
	e.waitFor("Codex 2 入库", func() bool { return e.session("codex:ts-cx2").Key != "" })
	hook(t, "codex", map[string]any{"session_id": "ts-cx2", "cwd": cwd, "hook_event_name": "Stop", "turn_id": "t1"})
	e.waitFor("Codex 2 一轮结束", func() bool { return e.session("codex:ts-cx2").State == store.StateWaitReply })

	// 已结束的会话
	hook(t, "claude", map[string]any{"session_id": "ts-old", "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "旧的", "prompt_id": "p2"})
	e.waitFor("旧会话入库", func() bool { return e.session("claude:ts-old").Key != "" })
	hook(t, "claude", map[string]any{"session_id": "ts-old", "cwd": cwd, "hook_event_name": "SessionEnd"})
	e.waitFor("旧会话结束", func() bool { return e.session("claude:ts-old").State == store.StateEnded })

	// 测试进程里按父进程认会话会认到别的会话，这里用 --session 指定
	pmEnv, me := []string{}, []string{"--session", "claude:ts-pm"}
	out, err := ks(pmEnv, append([]string{"team", "sessions"}, me...)...)
	if err != nil {
		t.Fatalf("ks team sessions 失败：%v\n%s", err, out)
	}
	for _, want := range []string{
		"4 个没结束的会话（待命 2 · 忙 1 · 在线 1）",
		"claude:ts-pm（你）「", "主调度 · Claude Code@test-device · 待命 · 能立刻叫醒：挂着后台 ks wait · 手上没有需求",
		"开发 · Claude Code@test-device · 忙 · 不能立刻叫醒：正在跑，这一轮结束后收到 · 手上：" + r.Code + "「会话通信」",
		"codex:ts-cx「", "· 待命 · 能立刻叫醒：挂着前台 ks wait（到 ",
		"codex:ts-cx2「", "· 在线 · 不能立刻叫醒：Codex 不能后台叫醒，等用户下次发消息",
		"无身份",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("输出应包含「%s」：\n%s", want, out)
		}
	}
	if strings.Contains(out, "claude:ts-old") {
		t.Errorf("默认不列已结束的会话：\n%s", out)
	}
	// 按身份过滤
	if out, err := ks(pmEnv, append([]string{"team", "sessions", "--role", "开发"}, me...)...); err != nil || !strings.Contains(out, "claude:ts-dev") || strings.Contains(out, "claude:ts-pm") {
		t.Errorf("--role 开发 只列开发：%v\n%s", err, out)
	}
	// --all 也列已结束的
	if out, err := ks(pmEnv, append([]string{"team", "sessions", "--all"}, me...)...); err != nil || !strings.Contains(out, "claude:ts-old") || !strings.Contains(out, "会话已结束，离线") {
		t.Errorf("--all 应列已结束的：%v\n%s", err, out)
	}
	// --json 给原始结构；--all-projects 不依赖当前会话
	raw, err := ks(nil, "team", "sessions", "--all-projects", "--json")
	if err != nil {
		t.Fatalf("--all-projects --json 失败：%v\n%s", err, raw)
	}
	var got struct {
		AllProjects bool `json:"all_projects"`
		Sessions    []struct {
			Key     string `json:"key"`
			Status  string `json:"status"`
			WakeNow bool   `json:"wake_now"`
			Harness string `json:"harness"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("--json 输出不是 JSON：%v\n%s", err, raw)
	}
	byKey := map[string]string{}
	for _, s := range got.Sessions {
		byKey[s.Key] = s.Status + "/" + s.Harness
		if (s.Status == store.AvailStandby) != s.WakeNow {
			t.Errorf("%s：待命才能立刻叫醒：%+v", s.Key, s)
		}
	}
	if !got.AllProjects || byKey["codex:ts-cx"] != "standby/Codex 终端" || byKey["claude:ts-dev"] != "busy/Claude Code" || byKey["codex:ts-cx2"] != "online/Codex 终端" {
		t.Errorf("JSON 结果不对：%v", byKey)
	}

	// 列表判为「能立刻叫醒」的，发消息真的立刻叫醒（主调度）
	var tr tellRes
	if err := c.Do(ctx, "POST", "/agent/team/tell", map[string]any{"session_key": "claude:ts-dev", "text": r.Code + " 有新留言，请看", "requirement": r.Code}, &tr); err != nil || !tr.Delivered {
		t.Fatalf("发给主调度：%+v %v", tr, err)
	}
	woke(t, pmWait, "有新留言")
}
