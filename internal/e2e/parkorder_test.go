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

// R-108：常驻 PM 在一轮中途就在后台挂上 ks wait，之后继续干活（子 Agent 交回结果、你在终端里插话），
// 再结束这一轮，应显示在线待命；“先结束、等待登记稍晚到”也一样；ks wait 退出后显示正确状态。
func TestParkedSurvivesTurnOrder(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	c := client.New(30 * time.Second)
	ctx := context.Background()
	prompt := func(sid, id, text string) {
		hook(t, "claude", map[string]any{"session_id": sid, "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": text, "prompt_id": id})
	}
	tool := func(sid string) {
		hook(t, "claude", map[string]any{"session_id": sid, "cwd": cwd, "hook_event_name": "PreToolUse", "tool_name": "Bash", "tool_input": map[string]any{"command": "go test ./..."}})
		hook(t, "claude", map[string]any{"session_id": sid, "cwd": cwd, "hook_event_name": "PostToolUse", "tool_name": "Bash", "tool_input": map[string]any{"command": "go test ./..."}})
	}
	stop := func(sid string) string {
		return hook(t, "claude", map[string]any{"session_id": sid, "cwd": cwd, "hook_event_name": "Stop"})
	}
	blocked := func(out string) bool { return strings.Contains(out, `"decision":"block"`) }
	wait := func(key, timeout string) chan daemon.WaitResult {
		done := make(chan daemon.WaitResult, 1)
		go func() {
			var res daemon.WaitResult
			_ = c.Do(ctx, "GET", "/wait?session_key="+key+"&timeout="+timeout, nil, &res)
			done <- res
		}()
		return done
	}
	presence := func(key string) string { return e.session(key).Presence() }

	prompt("pm-1", "p0", "开工")
	e.waitFor("会话入库", func() bool { return e.session("claude:pm-1").Key != "" })
	if err := c.Do(ctx, "POST", "/agent/sessions/role", map[string]any{"session_key": "claude:pm-1", "role": "pm"}, nil); err != nil {
		t.Fatal(err)
	}
	e.waitFor("身份入库", func() bool { return e.session("claude:pm-1").Role == "pm" })

	// 顺序一：这一轮还在跑时先在后台挂 ks wait，然后继续干活
	done := wait("claude:pm-1", "2s")
	e.waitFor("挂起登记", func() bool { return e.session("claude:pm-1").Parked })
	if s := e.session("claude:pm-1"); s.State != store.StateRunning || s.Presence() != "standby" {
		t.Fatalf("这一轮还在跑：状态应为运行中、身份显示待命，得到 %s / %s", s.State, s.Presence())
	}
	tool("pm-1")
	// 后台子 Agent 交回结果：Claude Code 以一条 <agent-message> 开启新一轮，这不是你发的消息
	prompt("pm-1", "p1", "<agent-message from=\"a934d12dd16437a3e\">\n[Subagent hand-back] 做完了</agent-message>")
	tool("pm-1")
	// 你在终端里直接插话：ks wait 仍在后台跑，结束这一轮后照样待命
	prompt("pm-1", "p2", "顺便看看这个")
	tool("pm-1")
	// hook 事件经 daemon 队列异步上送，先等这句插话到 Hub 再查
	e.waitFor("插话入库", func() bool { return e.session("claude:pm-1").LastPrompt == "顺便看看这个" })
	if s := e.session("claude:pm-1"); !s.Parked || s.Presence() != "standby" {
		t.Fatalf("ks wait 仍在后台，应保留挂起、显示待命：parked=%v presence=%s", s.Parked, s.Presence())
	}
	if s := e.session("claude:pm-1"); s.LastPrompt != "顺便看看这个" || s.Turns != 2 {
		t.Fatalf("子 Agent 交回结果不应算你的一轮：turns=%d last=%q", s.Turns, s.LastPrompt)
	}
	if out := stop("pm-1"); blocked(out) {
		t.Fatalf("挂着 ks wait 时结束不应被拦：%s", out)
	}
	e.waitFor("顺序一：在线待命", func() bool {
		s := e.session("claude:pm-1")
		return s.State == store.StateParked && s.Presence() == "standby"
	})
	// ks wait 超时退出：常驻会话会被唤醒接着等，显示工作中而不是离线
	if res := <-done; res.Reason != "timeout" {
		t.Fatalf("应超时：%+v", res)
	}
	e.waitFor("超时后工作中", func() bool {
		s := e.session("claude:pm-1")
		return !s.Parked && s.State == store.StateRunning && s.Presence() == "working"
	})

	// 顺序二：先结束这一轮，ks wait 的登记稍晚一点到（后台刚启动），仍应待命
	prompt("pm-1", "p3", "下一件事")
	stopOut := make(chan string, 1)
	go func() { stopOut <- stop("pm-1") }()
	time.Sleep(300 * time.Millisecond)
	done = wait("claude:pm-1", "1h")
	if out := <-stopOut; blocked(out) {
		t.Fatalf("宽限期内挂上 ks wait，不应拦：%s", out)
	}
	e.waitFor("顺序二：在线待命", func() bool {
		s := e.session("claude:pm-1")
		return s.Parked && s.State == store.StateParked && presence("claude:pm-1") == "standby"
	})

	// 你在纸鸢上新建待开始需求（R-85 交给 PM）：ks wait 送达后退出，会话继续运行
	e.call("POST", "/api/requirements", map[string]any{"project_id": e.session("claude:pm-1").ProjectID, "title": "新需求", "status": "ready"}, nil)
	select {
	case res := <-done:
		if res.Reason != "delivered" {
			t.Fatalf("应送达：%+v", res)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("新建待开始需求后 ks wait 应被唤醒")
	}
	e.waitFor("送达后工作中", func() bool {
		s := e.session("claude:pm-1")
		return !s.Parked && s.State == store.StateRunning && s.Presence() == "working"
	})
	// 这一轮做完、没再挂 ks wait（连续拦两次后放行）：离线
	for range 3 {
		stop("pm-1")
	}
	e.waitFor("没挂 ks wait 结束为离线", func() bool { return presence("claude:pm-1") == "offline" })
}
