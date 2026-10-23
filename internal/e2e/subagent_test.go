package e2e

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/client"
	"github.com/NekroAI/kitestring/internal/daemon"
	"github.com/NekroAI/kitestring/internal/store"
)

// R-115：Claude Code 在子 Agent 里触发的 hook 带主会话的 session_id 和子 Agent 的 agent_id。
// PM 结束一轮、挂着 ks wait 待命后，后台子 Agent 调用工具不应把 PM 改回运行中，只记作后台任务；
// 子 Agent 请求授权仍要你处理，处理完回到待命；没身份的会话“等待后台任务”（R-3）也不被改回运行中。
func TestSubagentEventsKeepParent(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	c := client.New(30 * time.Second)
	ctx := context.Background()
	ev := func(sid, name string, extra map[string]any) {
		p := map[string]any{"session_id": sid, "cwd": cwd, "hook_event_name": name}
		for k, v := range extra {
			p[k] = v
		}
		hook(t, "claude", p)
	}
	sub := map[string]any{"agent_id": "a934d12dd16437a3e", "agent_type": "general-purpose"}
	subTool := func(sid string) {
		in := map[string]any{"agent_id": sub["agent_id"], "agent_type": sub["agent_type"], "tool_name": "Bash", "tool_input": map[string]any{"command": "go test ./..."}}
		ev(sid, "PreToolUse", in)
		ev(sid, "PostToolUse", in)
	}

	// PM：开工，派一个后台子 Agent，挂上 ks wait，结束这一轮
	ev("pm-1", "UserPromptSubmit", map[string]any{"prompt": "开工", "prompt_id": "p0"})
	e.waitFor("会话入库", func() bool { return e.session("claude:pm-1").Key != "" })
	if err := c.Do(ctx, "POST", "/agent/sessions/role", map[string]any{"session_key": "claude:pm-1", "role": "pm"}, nil); err != nil {
		t.Fatal(err)
	}
	e.waitFor("身份入库", func() bool { return e.session("claude:pm-1").Role == "pm" })
	ev("pm-1", "SubagentStart", sub)
	e.waitFor("子 Agent 计数", func() bool { return e.session("claude:pm-1").Subagents == 1 })
	done := make(chan daemon.WaitResult, 1)
	go func() {
		var res daemon.WaitResult
		_ = c.Do(ctx, "GET", "/wait?session_key=claude:pm-1&timeout=1h", nil, &res)
		done <- res
	}()
	e.waitFor("挂起登记", func() bool { return e.session("claude:pm-1").Parked })
	ev("pm-1", "Stop", nil)
	e.waitFor("在线待命", func() bool { return e.session("claude:pm-1").State == store.StateParked })
	turnTools := e.session("claude:pm-1").TurnTools

	// 后台子 Agent 接着干活：仍是待命 · 后台 1 个任务
	subTool("pm-1")
	subTool("pm-1")
	ev("pm-1", "PreCompact", nil) // 作为“前面的事件都已处理”的标记：只加压缩计数
	e.waitFor("子 Agent 事件已处理", func() bool { return e.session("claude:pm-1").Compactions == 1 })
	if s := e.session("claude:pm-1"); s.State != store.StateParked || s.Presence() != "standby" || s.Subagents != 1 || s.TurnTools != turnTools {
		t.Fatalf("子 Agent 的工具调用不应改主会话状态：state=%s presence=%s subagents=%d turnTools=%d→%d", s.State, s.Presence(), s.Subagents, turnTools, s.TurnTools)
	}

	// 子 Agent 请求授权：等你授权；你批准后它继续，回到待命
	ev("pm-1", "PermissionRequest", map[string]any{"agent_id": sub["agent_id"], "tool_name": "Bash", "tool_input": map[string]any{"command": "rm -rf build"}})
	e.waitFor("子 Agent 请求授权", func() bool { return e.session("claude:pm-1").State == store.StateWaitApproval })
	ev("pm-1", "PostToolUse", map[string]any{"agent_id": sub["agent_id"], "tool_name": "Bash"})
	e.waitFor("授权后回到待命", func() bool { return e.session("claude:pm-1").State == store.StateParked })

	// 子 Agent 结束：计数归零，仍待命
	ev("pm-1", "SubagentStop", sub)
	e.waitFor("子 Agent 结束", func() bool { return e.session("claude:pm-1").Subagents == 0 })
	if s := e.session("claude:pm-1"); s.State != store.StateParked || !s.Parked {
		t.Fatalf("子 Agent 结束后仍应待命：state=%s parked=%v", s.State, s.Parked)
	}

	// 主线程自己的工具调用（没有 agent_id）照常算运行中
	ev("pm-1", "UserPromptSubmit", map[string]any{"prompt": "再看看", "prompt_id": "p1"})
	ev("pm-1", "PreToolUse", map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "ls"}})
	e.waitFor("主线程运行中", func() bool {
		s := e.session("claude:pm-1")
		return s.State == store.StateRunning && s.TurnTools == 1
	})

	// R-3：没身份的会话结束时还有后台子 Agent，显示等待后台任务；子 Agent 的工具调用不改回运行中
	ev("solo-1", "UserPromptSubmit", map[string]any{"prompt": "查一下", "prompt_id": "q0"})
	ev("solo-1", "SubagentStart", sub)
	ev("solo-1", "Stop", map[string]any{"background_tasks": []any{map[string]any{"status": "running", "description": "查资料"}}})
	e.waitFor("等待后台任务", func() bool { return e.session("claude:solo-1").State == store.StateBackground })
	subTool("solo-1")
	ev("solo-1", "PreCompact", nil)
	e.waitFor("子 Agent 事件已处理", func() bool { return e.session("claude:solo-1").Compactions == 1 })
	if s := e.session("claude:solo-1"); s.State != store.StateBackground || s.StateDetail != "查资料" {
		t.Fatalf("子 Agent 的工具调用不应把等待后台任务改回运行中：%s / %q", s.State, s.StateDetail)
	}
	select {
	case <-done:
		t.Fatal("ks wait 不应被子 Agent 事件唤醒")
	default:
	}
}
