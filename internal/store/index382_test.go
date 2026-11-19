package store

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/protocol"
)

// R-382：PreToolStart 走 events_tooluse 索引按 tool_use_id 直接定位，找得到与找不到都和原来 payload LIKE 的结果一致；
// 证据、代答计数的查询走新索引，不再整表扫描。
func TestPreToolStartUsesIndex(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	end := time.Now()
	for i, k := range []string{"s0", "s1"} {
		for j := 0; j < 6; j++ {
			name := []string{"PreToolUse", "PostToolUse"}[j%2]
			ev := protocol.Event{ID: protocol.NewID(), Tool: "claude", SessionID: k, Name: name, Time: end.Add(-time.Duration(j+i) * time.Minute),
				ToolName: "Bash", ToolUseID: fmt.Sprintf("toolu_%s_%d", k, j/2), ToolBrief: `echo "tool_use_id":"toolu_s0_9"`}
			if _, err := s.InsertEvent(ctx, "dev1", ev); err != nil {
				t.Fatal(err)
			}
		}
	}
	ev, ok := s.PreToolStart(ctx, "claude:s0", "toolu_s0_1", end)
	if !ok || ev.Name != "PreToolUse" || ev.ToolUseID != "toolu_s0_1" {
		t.Fatalf("应找到工具调用的开始：%+v %v", ev, ok)
	}
	if _, ok := s.PreToolStart(ctx, "claude:s0", "toolu_s0_9", end); ok {
		t.Fatal("没有这次调用时应找不到（别的字段里出现同样的文字不算）")
	}
	if _, ok := s.PreToolStart(ctx, "claude:s1", "toolu_s0_1", end); ok {
		t.Fatal("别的会话的调用不算")
	}
	if _, ok := s.PreToolStart(ctx, "claude:s0", "toolu_s0_1", end.Add(-25*time.Hour)); ok {
		t.Fatal("只往前找一天")
	}
	for q, ix := range map[string]string{
		`SELECT payload FROM events WHERE session_key = ? AND name = 'PreToolUse' AND json_extract(payload, '$.tool_use_id') = ? AND time >= ? AND time <= ? ORDER BY time DESC LIMIT 1`: "events_tooluse",
		`SELECT id FROM evidence WHERE requirement_id = ? AND kind <> 'review' ORDER BY created_at`:                                                                                      "evidence_req",
		`SELECT COUNT(*) FROM decisions d WHERE d.proxy_by = ? AND d.project_id = ? AND d.proxy_at >= ?`:                                                                                 "decisions_proxy",
	} {
		if plan := explainPlan(t, s, q); !strings.Contains(plan, ix) {
			t.Fatalf("应走 %s 索引，查询计划：%s", ix, plan)
		}
	}
}

func explainPlan(t *testing.T, s *Store, q string) string {
	t.Helper()
	args := make([]any, strings.Count(q, "?"))
	for i := range args {
		args[i] = "x"
	}
	rows, err := s.DB().Query(`EXPLAIN QUERY PLAN `+q, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatal(err)
		}
		b.WriteString(detail + "; ")
	}
	return b.String()
}

// 慢请求日志的查库次数（R-382）：挂了计数器的 ctx 上每条语句加一，没挂的不受影响。
func TestQueryCount(t *testing.T) {
	s := openTest(t)
	ctx, n := WithQueryCount(context.Background())
	for i := 0; i < 3; i++ {
		if _, err := s.ListProjects(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.ListProjects(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n.Load() != 3 {
		t.Fatalf("查库次数应为 3，实际 %d", n.Load())
	}
}
