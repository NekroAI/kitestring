package hub

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/protocol"
	"github.com/NekroAI/kitestring/internal/store"
)

// 泳道时间线（R-205）：实时只推变了的那一格（不再整条会话重发），已推过的分钟不再出现；
// 还没结束、跑了超过 20 秒的工具调用在取数时作为“还在跑的长命令”返回。
func TestLanePushOnlyChangedMinute(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	h := New(st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	oldDelay := lanePushDelay
	lanePushDelay = 20 * time.Millisecond
	defer func() { lanePushDelay = oldDelay }()
	ctx := context.Background()
	dev, err := st.CreateDevice(ctx, "d", "hash")
	if err != nil {
		t.Fatal(err)
	}
	s := &sub{ch: make(chan string, 64)}
	h.broker.subs[s] = struct{}{}
	n := 0
	pre := func(at time.Time, id, agent string) {
		n++
		ev := protocol.Event{ID: "e" + string(rune('a'+n)), Tool: "claude", SessionID: "s1", Name: "PreToolUse", Time: at, ToolName: "Bash", ToolUseID: id, AgentID: agent}
		if err := h.ingest(ctx, dev, ev); err != nil {
			t.Fatal(err)
		}
	}
	lanes := func() LanePush {
		t.Helper()
		deadline := time.After(2 * time.Second)
		for {
			select {
			case m := <-s.ch:
				if !strings.HasPrefix(m, "event: lane\n") {
					continue // 会话推送等其它消息
				}
				var p LanePush
				if err := json.Unmarshal([]byte(strings.TrimSuffix(strings.TrimPrefix(m, "event: lane\ndata: "), "\n\n")), &p); err != nil {
					t.Fatal(err)
				}
				return p
			case <-deadline:
				t.Fatal("没收到 lane 推送")
			}
		}
	}
	now := time.Now()
	old := now.Add(-5 * time.Minute)
	pre(old, "t1", "")
	pre(old.Add(time.Second), "t2", "")
	pre(old.Add(2*time.Second), "t3", "a1")
	p := lanes()
	if len(p.Minutes) != 2 {
		t.Fatalf("第一批：主会话和子任务各一格，实际 %+v", p.Minutes)
	}
	pre(now, "t4", "")
	p = lanes()
	if len(p.Minutes) != 1 || p.Minutes[0].Minute != now.Unix()/60 || p.Minutes[0].Calls != 1 {
		t.Fatalf("新调用只推当前这一格，实际 %+v", p.Minutes)
	}
	if len(p.Agents) != 0 {
		t.Fatalf("子任务名单没变，不该重推：%+v", p.Agents)
	}

	// t1～t3 没收到结束：开始已经 5 分钟，取数时作为还在跑的长命令返回
	out, err := h.getLanes(ctx, &lanesIn{From: now.Add(-time.Hour).UnixMilli()})
	if err != nil {
		t.Fatal(err)
	}
	open := 0
	for _, sp := range out.Body.Spans {
		if sp.Kind == store.SpanTool && sp.End == nil {
			open++
		}
	}
	if open != 3 {
		t.Fatalf("还在跑的长命令 = %d，想要 3：%+v", open, out.Body.Spans)
	}
	if len(out.Body.Sessions) != 1 || out.Body.Sessions[0].Key != "claude:s1" {
		t.Fatalf("会话简要信息 = %+v", out.Body.Sessions)
	}
}
