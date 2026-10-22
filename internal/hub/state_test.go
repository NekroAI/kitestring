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
