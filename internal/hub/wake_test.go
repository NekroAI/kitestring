package hub

import (
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/protocol"
)

// 自动唤醒的来源（R-287）：待命结束对上的是纸鸢送达 / 待命结束；新记录按 Wake；老记录按子任务结束推断，推不出的记后台任务通知。
func TestWakeTrack(t *testing.T) {
	ups := protocol.Event{Name: "UserPromptSubmit"}
	auto := func(kind, note string) protocol.Event {
		return protocol.Event{Name: "UserPromptSubmit", Source: protocol.SourceAuto, Wake: kind, WakeNote: note, Time: time.Date(2026, 11, 6, 1, 0, 5, 0, time.UTC)}
	}
	stop := protocol.Event{Name: "Stop"}
	end := func(r string) protocol.Event { return protocol.Event{Name: protocol.EventWaitEnd, Reason: r} }
	t0 := time.Date(2026, 11, 6, 1, 0, 0, 0, time.UTC)
	subStop := protocol.Event{Name: "SubagentStop", AgentID: "a1", AgentType: "Explore", Time: t0}
	inner := protocol.Event{Name: "SubagentStop", AgentID: "a2", Time: t0}
	late := auto("", "")
	late.Time = t0.Add(3 * time.Minute)
	cases := []struct {
		name       string
		evs        []protocol.Event
		wake       protocol.Event
		kind, note string
	}{
		{"待命送达（老记录）", []protocol.Event{ups, stop, end("delivered")}, auto("", ""), "kite", ""},
		{"待命送达（新记录）", []protocol.Event{ups, stop, end("delivered")}, auto(protocol.WakeBg, "Wait"), "kite", ""},
		{"待命结束", []protocol.Event{ups, stop, end("timeout")}, auto(protocol.WakeBg, "Wait"), "timeout", ""},
		{"待命被取消算后台任务", []protocol.Event{ups, stop, end("canceled")}, auto(protocol.WakeBg, "Wait"), protocol.WakeBg, "Wait"},
		{"子任务回报（新记录）", []protocol.Event{ups, stop, end("delivered")}, auto(protocol.WakeAgent, "x"), protocol.WakeAgent, "x"},
		{"子任务回报（老记录）", []protocol.Event{ups, stop, subStop}, auto("", ""), protocol.WakeAgent, ""},
		{"内部子代理结束不算", []protocol.Event{ups, stop, inner}, auto("", ""), protocol.WakeBg, ""},
		{"子任务结束很久后的唤醒不算", []protocol.Event{ups, stop, subStop}, late, protocol.WakeBg, ""},
		{"本轮里跑完的子任务不算", []protocol.Event{ups, subStop, stop}, auto("", ""), protocol.WakeBg, ""},
		{"上一轮的待命结束不算", []protocol.Event{ups, stop, end("delivered"), ups, stop}, auto("", ""), protocol.WakeBg, ""},
		{"监视", []protocol.Event{ups, stop}, auto(protocol.WakeBg, "CI"), protocol.WakeBg, "CI"},
	}
	for _, c := range cases {
		var w wakeTrack
		for _, ev := range c.evs {
			w.see(ev)
		}
		k, n := w.kind(c.wake)
		if k != c.kind || n != c.note {
			t.Errorf("%s：得到 %q %q，期望 %q %q", c.name, k, n, c.kind, c.note)
		}
	}
}
