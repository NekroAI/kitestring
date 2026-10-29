package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/protocol"
)

// 缓存余温（R-207）：取主会话最近一次请求；档位看最近一次写缓存的那一分钟（有 1 小时档写入记 3600，只有 5 分钟档记 300）；
// 子任务不算；Codex 不提供档位记 0；7 天前的不给。
func TestLaneWarm(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	now := time.Now()
	m := unixMinute(now)
	u := func(key, agent string, min int64, in, cw, cw1h, cr int64, n int) protocol.UsageMinute {
		return protocol.UsageMinute{Key: key, Agent: agent, Model: "m", Minute: min, Input: in, CacheW: cw, CacheW1h: cw1h, CacheR: cr, Output: 10, Requests: n}
	}
	us := []protocol.UsageMinute{
		u("claude:a", "", m-30, 10, 5000, 5000, 0, 1),
		u("claude:a", "", m-10, 20, 0, 0, 100000, 2), // 之后只读缓存：档位沿用 1 小时，输入总量按次数平均
		u("claude:a", "sub", m-1, 1, 99999, 0, 0, 1), // 子任务不算
		u("claude:b", "", m-20, 5, 8000, 0, 0, 1),    // 只有 5 分钟档写入
		u("codex:c", "", m-5, 7000, 0, 0, 20000, 1),  // Codex：无档位
		u("claude:old", "", m-8*24*60, 1, 1, 0, 1, 1),
	}
	if _, _, err := st.ApplyUsage(ctx, us, nil, now); err != nil {
		t.Fatal(err)
	}
	ws, err := st.laneWarm(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]LaneWarm{}
	for _, w := range ws {
		by[w.Key] = w
	}
	if len(by) != 3 {
		t.Fatalf("会话数 = %d：%+v", len(by), ws)
	}
	if a := by["claude:a"]; a.TTL != 3600 || a.Ctx != 50010 || a.At/60000 != m-10 {
		t.Fatalf("a = %+v", a)
	}
	if b := by["claude:b"]; b.TTL != 300 || b.Ctx != 8005 {
		t.Fatalf("b = %+v", b)
	}
	if c := by["codex:c"]; c.TTL != 0 || c.Ctx != 27000 {
		t.Fatalf("c = %+v", c)
	}
	tools, err := st.TokenTools(ctx, now)
	if err != nil || len(tools) != 2 || tools[0] != "claude" || tools[1] != "codex" {
		t.Fatalf("tools = %v %v", tools, err)
	}
}
