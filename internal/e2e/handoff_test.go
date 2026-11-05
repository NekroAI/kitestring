package e2e

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/client"
	"github.com/NekroAI/kitestring/internal/daemon"
	"github.com/NekroAI/kitestring/internal/store"
)

// R-224：压缩后身份提示词附关键状态；完整交接——用户点「交接」通知旧会话、主调度有在跑的子任务时不让写、
// handoff_write 存进 Hub、新会话 ks role 时自动注入交接、旧会话卸任（身份清除、ks wait 直接返回、Stop 不再拦）。
func TestHandoff(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	c := client.New(30 * time.Second)
	ctx := context.Background()
	ev := func(sid, name string, extra map[string]any) string {
		p := map[string]any{"session_id": sid, "cwd": cwd, "hook_event_name": name}
		for k, v := range extra {
			p[k] = v
		}
		return hook(t, "claude", p)
	}
	ev("ho-1", "UserPromptSubmit", map[string]any{"prompt": "接管 PM", "prompt_id": "p0"})
	ev("ho-2", "UserPromptSubmit", map[string]any{"prompt": "接任", "prompt_id": "p0"})
	e.waitFor("两个会话入库", func() bool { return e.session("claude:ho-1").Key != "" && e.session("claude:ho-2").Key != "" })
	if err := c.Do(ctx, "POST", "/agent/sessions/role", map[string]any{"session_key": "claude:ho-1", "role": "pm"}, nil); err != nil {
		t.Fatal(err)
	}
	var r store.Requirement
	if err := c.Do(ctx, "POST", "/agent/requirements", map[string]any{"title": "交接演示需求", "session_key": "claude:ho-1"}, &r); err != nil {
		t.Fatal(err)
	}
	sub := map[string]any{"agent_id": "a-handoff", "agent_type": "general-purpose"}
	ev("ho-1", "SubagentStart", sub)
	e.waitFor("子任务在跑", func() bool { return len(e.session("claude:ho-1").Tasks) == 1 })

	// 压缩后：身份提示词 + 关键状态（关联的需求、在跑的子任务）
	e.waitFor("压缩后补注入关键状态", func() bool {
		got := ev("ho-1", "SessionStart", map[string]any{"source": "compact"})
		return strings.Contains(got, "你的会话身份：主调度（PM）") && strings.Contains(got, "关键状态") && strings.Contains(got, r.Code) && strings.Contains(got, "子任务")
	})

	// 用户点「交接」：挂着 ks wait 的旧会话被唤醒，收到交接通知（含子任务数）
	wait := func() chan daemon.WaitResult {
		ch := make(chan daemon.WaitResult, 1)
		go func() {
			var res daemon.WaitResult
			_ = c.Do(ctx, "GET", "/wait?session_key=claude:ho-1&timeout=20s", nil, &res)
			ch <- res
		}()
		e.waitFor("旧会话挂起", func() bool { return e.session("claude:ho-1").Parked })
		return ch
	}
	recv := func(ch chan daemon.WaitResult, want string) {
		t.Helper()
		select {
		case res := <-ch:
			if res.Reason != "delivered" || !strings.Contains(res.Text, want) {
				t.Fatalf("旧会话应收到 %q：%+v", want, res)
			}
		case <-time.After(15 * time.Second):
			t.Fatalf("旧会话没有被唤醒（等 %q）", want)
		}
	}
	ch := wait()
	var ho struct {
		Command string        `json:"command"`
		Waiting int           `json:"waiting"`
		Handoff store.Handoff `json:"handoff"`
	}
	e.call("POST", "/api/sessions/claude:ho-1/handoff", nil, &ho)
	if ho.Command != "ks takeover pm" || ho.Waiting != 1 || ho.Handoff.Status != store.HandoffRequested {
		t.Fatalf("交接请求返回不对：%+v", ho)
	}
	recv(ch, "还有 1 个子任务在跑")

	// 子任务没交完：handoff_write 被拒
	body := map[string]any{"session_key": "claude:ho-1", "doing": r.Code + " 做到一半，分支 line3", "waiting": "D-9 等用户决策", "rules": "部署只走脚本", "next": "跑全量检查后交验收", "notes": "别停 5199 端口的 pnpm dev"}
	if err := c.Do(ctx, "POST", "/agent/handoff", body, nil); err == nil || !strings.Contains(err.Error(), "子任务在跑") {
		t.Fatalf("有在跑的子任务时应拒绝写交接：%v", err)
	}
	ev("ho-1", "SubagentStop", sub)
	e.waitFor("子任务交完", func() bool { return len(e.session("claude:ho-1").Tasks) == 0 })
	var msg struct {
		Message string `json:"message"`
	}
	if err := c.Do(ctx, "POST", "/agent/handoff", body, &msg); err != nil {
		t.Fatal(err)
	}
	// R-245：旧栏目仍接受，但提示这些纸鸢里已有
	if !strings.Contains(msg.Message, "交接已存") || !strings.Contains(msg.Message, "纸鸢里已有") || !strings.Contains(msg.Message, "waiting") {
		t.Fatalf("写了旧栏目应提示不必写：%s", msg.Message)
	}

	// 新会话声明身份：自动注入交接；旧会话被唤醒、收到卸任通知
	ch = wait()
	var out struct {
		Prompt  string   `json:"prompt"`
		Demoted []string `json:"demoted"`
		Handoff bool     `json:"handoff"`
	}
	if err := c.Do(ctx, "POST", "/agent/sessions/role", map[string]any{"session_key": "claude:ho-2", "role": "主调度"}, &out); err != nil {
		t.Fatal(err)
	}
	if !out.Handoff || !strings.Contains(out.Prompt, "## 交接") || !strings.Contains(out.Prompt, "别停 5199 端口的 pnpm dev") || !strings.Contains(out.Prompt, "先回用户一句确认") {
		t.Fatalf("新会话应读到交接：%+v", out)
	}
	if len(out.Demoted) != 1 || out.Demoted[0] != "claude:ho-1" {
		t.Fatalf("应卸下旧主调度：%+v", out.Demoted)
	}
	recv(ch, "身份已交接给")
	old := e.session("claude:ho-1")
	if old.Role != "" || old.RetiredAt == nil {
		t.Fatalf("旧会话应已卸任：role=%q retired=%v", old.Role, old.RetiredAt)
	}

	// 卸任后：ks wait 自查直接返回卸任提示；Stop 不再拦
	var chk struct {
		Retired bool   `json:"retired"`
		Hint    string `json:"hint"`
	}
	if err := c.Do(ctx, "GET", "/wait/check?session_key=claude:ho-1", nil, &chk); err != nil || !chk.Retired || !strings.Contains(chk.Hint, "不要再挂 ks wait") {
		t.Fatalf("卸任的会话 ks wait 应直接返回：%+v %v", chk, err)
	}
	e.waitFor("Stop 不再拦", func() bool { return !strings.Contains(ev("ho-1", "Stop", nil), "block") })

	// 时间线：旧会话和新会话都能看到这份交接，已被接任
	var detail struct {
		Handoffs []store.Handoff `json:"handoffs"`
	}
	e.call("GET", "/api/sessions/claude:ho-1", nil, &detail)
	if len(detail.Handoffs) != 1 || detail.Handoffs[0].Status != store.HandoffTaken || detail.Handoffs[0].TakenBy != "claude:ho-2" || detail.Handoffs[0].Caveats == "" {
		t.Fatalf("旧会话的交接记录不对：%+v", detail.Handoffs)
	}
	e.call("GET", "/api/sessions/claude:ho-2", nil, &detail)
	if len(detail.Handoffs) != 1 {
		t.Fatalf("新会话应看到接手的交接：%+v", detail.Handoffs)
	}

	// 卸任的会话重新声明身份：回到正常
	if err := c.Do(ctx, "POST", "/agent/sessions/role", map[string]any{"session_key": "claude:ho-1", "role": "dev"}, nil); err != nil {
		t.Fatal(err)
	}
	if e.session("claude:ho-1").RetiredAt != nil {
		t.Fatal("重新声明身份后应清除卸任")
	}
}
