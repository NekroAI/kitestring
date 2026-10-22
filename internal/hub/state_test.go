package hub

import (
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/protocol"
	"github.com/NekroAI/kitestring/internal/store"
)

func TestApplyEventLifecycle(t *testing.T) {
	t0 := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	ev := func(sec int, name string) protocol.Event {
		return protocol.Event{Tool: "codex", SessionID: "s1", Name: name, Time: t0.Add(time.Duration(sec) * time.Second)}
	}
	steps := []struct {
		ev   protocol.Event
		want string
	}{
		{ev(0, "SessionStart"), store.StateIdle},
		{ev(1, "UserPromptSubmit"), store.StateRunning},
		{ev(2, "PreToolUse"), store.StateRunning},
		{ev(3, "PermissionRequest"), store.StateWaitApproval},
		{ev(4, "PreToolUse"), store.StateWaitApproval}, // 授权未决时其他工具事件不解除
		{ev(5, "PostToolUse"), store.StateRunning},
		{ev(6, "Stop"), store.StateWaitReply},
		{ev(7, "UserPromptSubmit"), store.StateRunning},
		{ev(8, "Interrupt"), store.StateWaitReply},
		{ev(9, "SessionEnd"), store.StateEnded},
	}
	var cur *store.Session
	for i, st := range steps {
		x := applyEvent(cur, "dev", "", st.ev)
		if x.State != st.want {
			t.Fatalf("第 %d 步 %s：状态 %s，期望 %s", i, st.ev.Name, x.State, st.want)
		}
		cur = &x
	}
	if cur.EndedAt == nil {
		t.Fatal("SessionEnd 后应有结束时间")
	}
	if cur.Key != "codex:s1" {
		t.Fatalf("会话键错误：%s", cur.Key)
	}
}

func TestAskUserQuestionIsWaitingReply(t *testing.T) {
	t0 := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	cur := applyEvent(nil, "dev", "", protocol.Event{Tool: "claude", SessionID: "s", Name: "UserPromptSubmit", Time: t0})
	ask := applyEvent(&cur, "dev", "", protocol.Event{Tool: "claude", SessionID: "s", Name: "PermissionRequest", ToolName: "AskUserQuestion", ToolBrief: "先做哪个？", Time: t0.Add(time.Second)})
	if ask.State != store.StateWaitReply || ask.StateDetail != "AskUserQuestion · 先做哪个？" {
		t.Fatalf("提问应为等你回复，得到 %s / %q", ask.State, ask.StateDetail)
	}
	back := applyEvent(&ask, "dev", "", protocol.Event{Tool: "claude", SessionID: "s", Name: "PostToolUse", ToolName: "AskUserQuestion", Time: t0.Add(2 * time.Second)})
	if back.State != store.StateRunning {
		t.Fatalf("回答后应继续运行，得到 %s", back.State)
	}
}

func TestAutoPromptKeepsLastPrompt(t *testing.T) {
	t0 := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	cur := applyEvent(nil, "dev", "", protocol.Event{Tool: "claude", SessionID: "s", Name: "UserPromptSubmit", Prompt: "修登录页", Time: t0})
	cur = applyEvent(&cur, "dev", "", protocol.Event{Tool: "claude", SessionID: "s", Name: "Stop", Time: t0.Add(time.Second)})
	auto := applyEvent(&cur, "dev", "", protocol.Event{Tool: "claude", SessionID: "s", Name: "UserPromptSubmit", Source: protocol.SourceAuto, Time: t0.Add(2 * time.Second)})
	if auto.LastPrompt != "修登录页" || auto.Turns != 1 || auto.State != store.StateRunning {
		t.Fatalf("自动消息不应覆盖最后消息或增加轮次：%q %d %s", auto.LastPrompt, auto.Turns, auto.State)
	}
}

func TestStopWithBackgroundTasks(t *testing.T) {
	t0 := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	cur := applyEvent(nil, "dev", "", protocol.Event{Tool: "claude", SessionID: "s", Name: "UserPromptSubmit", Prompt: "等 CI", Time: t0})
	bg := applyEvent(&cur, "dev", "", protocol.Event{Tool: "claude", SessionID: "s", Name: "Stop", BgTasks: 1, BgBrief: "等待 CI 构建", Time: t0.Add(time.Second)})
	if bg.State != store.StateBackground || bg.StateDetail != "等待 CI 构建" {
		t.Fatalf("有后台任务时应为后台运行，得到 %s / %q", bg.State, bg.StateDetail)
	}
	back := applyEvent(&bg, "dev", "", protocol.Event{Tool: "claude", SessionID: "s", Name: "UserPromptSubmit", Source: protocol.SourceAuto, Time: t0.Add(time.Minute)})
	if back.State != store.StateRunning || back.LastPrompt != "等 CI" {
		t.Fatalf("后台任务完成通知后应继续运行且保留最后消息，得到 %s / %q", back.State, back.LastPrompt)
	}
}

// 回合结束后才到达的子 Agent / 压缩事件不应把“等你回复”改回“运行中”（R-3 修改意见）。
func TestLateSubagentEventsKeepWaiting(t *testing.T) {
	t0 := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	cur := applyEvent(nil, "dev", "", protocol.Event{Tool: "claude", SessionID: "s", Name: "UserPromptSubmit", Time: t0})
	cur = applyEvent(&cur, "dev", "", protocol.Event{Tool: "claude", SessionID: "s", Name: "Stop", Time: t0.Add(time.Minute)})
	for i, name := range []string{"Notification", "SubagentStart", "SubagentStop", "PreCompact", "PostCompact"} {
		cur = applyEvent(&cur, "dev", "", protocol.Event{Tool: "claude", SessionID: "s", Name: name, Time: t0.Add(time.Duration(2+i) * time.Minute)})
		if cur.State != store.StateWaitReply {
			t.Fatalf("%s 之后应仍为等你回复，得到 %s", name, cur.State)
		}
	}
	if !cur.StateSince.Equal(t0.Add(time.Minute)) {
		t.Fatalf("状态起始时间不应被刷新：%v", cur.StateSince)
	}
}

func TestApplyEventStaleDoesNotRegress(t *testing.T) {
	t0 := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	cur := applyEvent(nil, "dev", "", protocol.Event{Tool: "claude", SessionID: "s", Name: "Stop", Time: t0.Add(10 * time.Second)})
	old := applyEvent(&cur, "dev", "", protocol.Event{Tool: "claude", SessionID: "s", Name: "UserPromptSubmit", Time: t0, Model: "m1"})
	if old.State != store.StateWaitReply {
		t.Fatalf("乱序旧事件不应回退状态，得到 %s", old.State)
	}
	if old.Model != "m1" {
		t.Fatal("乱序旧事件仍应补充字段")
	}
}

func TestApplyEventResumeAfterEnd(t *testing.T) {
	t0 := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	cur := applyEvent(nil, "dev", "", protocol.Event{Tool: "claude", SessionID: "s", Name: "SessionEnd", Time: t0})
	next := applyEvent(&cur, "dev", "", protocol.Event{Tool: "claude", SessionID: "s", Name: "SessionStart", Source: "resume", Time: t0.Add(time.Minute)})
	if next.EndedAt != nil || next.State != store.StateIdle {
		t.Fatalf("恢复会话后应重新激活，得到 %s", next.State)
	}
}

func TestOnlyLatestTerminalDrivesState(t *testing.T) {
	t0 := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	ev := func(name, inst string, min int) protocol.Event {
		return protocol.Event{Tool: "claude", SessionID: "s", Name: name, Instance: inst, Time: t0.Add(time.Duration(min) * time.Minute)}
	}
	// 公司终端开着；回家后在新终端恢复并发消息
	cur := applyEvent(nil, "dev", "", ev("UserPromptSubmit", "office", 0))
	cur = applyEvent(&cur, "dev", "", ev("Stop", "office", 1))
	cur = applyEvent(&cur, "dev", "", ev("SessionStart", "home", 10))
	cur = applyEvent(&cur, "dev", "", ev("UserPromptSubmit", "home", 11))
	if cur.Instance != "home" || cur.PrevInstance != "office" || cur.State != store.StateRunning {
		t.Fatalf("新终端应成为最新：%+v", cur)
	}
	// 旧终端的事件不改状态，它结束也不结束会话
	for i, name := range []string{"Stop", "Notification", "SessionEnd"} {
		cur = applyEvent(&cur, "dev", "", ev(name, "office", 12+i))
		if cur.State != store.StateRunning || cur.EndedAt != nil {
			t.Fatalf("旧终端的 %s 不应改状态：%s", name, cur.State)
		}
	}
	if cur.PrevInstance != "" {
		t.Fatal("旧终端结束后应清除“另一终端也开着”")
	}
	// 回到旧终端继续发消息：它重新成为最新
	cur = applyEvent(&cur, "dev", "", ev("Stop", "home", 20))
	cur = applyEvent(&cur, "dev", "", ev("UserPromptSubmit", "office2", 21))
	if cur.Instance != "office2" || cur.State != store.StateRunning {
		t.Fatalf("最近发消息的终端应成为最新：%+v", cur)
	}
}

func TestNewPromptClearsDone(t *testing.T) {
	t0 := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	cur := applyEvent(nil, "dev", "", protocol.Event{Tool: "claude", SessionID: "s", Name: "Stop", Time: t0})
	done := t0.Add(time.Minute)
	cur.DoneAt = &done
	cur = applyEvent(&cur, "dev", "", protocol.Event{Tool: "claude", SessionID: "s", Name: "Notification", NotifType: "idle_prompt", Time: t0.Add(2 * time.Minute)})
	if cur.DoneAt == nil {
		t.Fatal("没有新消息时应保持已完成")
	}
	cur = applyEvent(&cur, "dev", "", protocol.Event{Tool: "claude", SessionID: "s", Name: "UserPromptSubmit", Prompt: "再改一下", Time: t0.Add(3 * time.Minute)})
	if cur.DoneAt != nil {
		t.Fatal("你再发消息后应自动取消已完成")
	}
}

func TestParkedWaitIsWaitingForYou(t *testing.T) {
	t0 := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	ev := func(name string, min int, f func(*protocol.Event)) protocol.Event {
		e := protocol.Event{Tool: "claude", SessionID: "s", Name: name, Time: t0.Add(time.Duration(min) * time.Minute)}
		if f != nil {
			f(&e)
		}
		return e
	}
	cur := applyEvent(nil, "dev", "", ev("UserPromptSubmit", 0, nil))
	cur = applyEvent(&cur, "dev", "", ev(protocol.EventWait, 1, nil))
	cur = applyEvent(&cur, "dev", "", ev("Stop", 2, func(e *protocol.Event) { e.BgTasks = 1; e.BgBrief = "等待反馈" }))
	if !cur.Parked || cur.State != store.StateParked {
		t.Fatalf("挂起等待时应为待命：%+v", cur)
	}
	// Claude Code 空闲一分钟后会发 idle_prompt；子 Agent 也可能晚到结束
	cur = applyEvent(&cur, "dev", "", ev("Notification", 3, func(e *protocol.Event) { e.NotifType = "idle_prompt" }))
	cur = applyEvent(&cur, "dev", "", ev("SubagentStop", 4, nil))
	if cur.State != store.StateParked {
		t.Fatalf("空闲提醒不应把待命改成等你回复：%s", cur.State)
	}
	cur = applyEvent(&cur, "dev", "", ev(protocol.EventWaitEnd, 5, func(e *protocol.Event) { e.Reason = "delivered" }))
	if cur.Parked || cur.State != store.StateRunning {
		t.Fatalf("收到答复后应继续运行：%+v", cur)
	}
}
