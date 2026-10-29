package hub

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/protocol"
	"github.com/NekroAI/kitestring/internal/store"
)

// 缓存保留时长（R-207）：默认空；没填分钟的不存；同一来源只留一个；读回带近 30 天有用量的来源。
func TestCacheHolds(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	h := New(st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx := context.Background()
	out, err := h.getCacheHolds(ctx, nil)
	if err != nil || len(out.Body.Holds) != 0 || len(out.Body.Tools) != 0 {
		t.Fatalf("默认 = %+v %v", out.Body, err)
	}
	m := time.Now().Unix() / 60
	if _, _, err := st.ApplyUsage(ctx, []protocol.UsageMinute{
		{Key: "codex:s", Model: "gpt", Minute: m - 3, Input: 5000, CacheR: 20000, Requests: 1},
		{Key: "claude:s", Model: "opus", Minute: m - 1, Input: 1, CacheW: 100, CacheW1h: 100, Requests: 1},
	}, nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	out, err = h.putCacheHolds(ctx, &struct{ Body CacheHolds }{Body: CacheHolds{Holds: []CacheHold{{Tool: " codex ", Minutes: 10}, {Tool: "codex", Minutes: 30}, {Tool: "dsh", Minutes: 0}}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Body.Holds) != 1 || out.Body.Holds[0].Tool != "codex" || out.Body.Holds[0].Minutes != 10 {
		t.Fatalf("保存后 = %+v", out.Body.Holds)
	}
	if len(out.Body.Tools) != 2 || out.Body.Tools[0] != "claude" || out.Body.Tools[1] != "codex" {
		t.Fatalf("来源 = %v", out.Body.Tools)
	}
	// /api/lanes 带上每个会话最近一次请求的缓存状态
	d, err := h.getLanes(ctx, &lanesIn{})
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]store.LaneWarm{}
	for _, w := range d.Body.Warm {
		by[w.Key] = w
	}
	if by["claude:s"].TTL != 3600 || by["codex:s"].TTL != 0 || by["codex:s"].Ctx != 25000 {
		t.Fatalf("warm = %+v", d.Body.Warm)
	}
}
