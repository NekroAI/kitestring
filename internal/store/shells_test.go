package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/protocol"
)

// R-208：后台命令整条覆盖；按时间范围取（在跑的一直算，不知道结束时间的按开始后 10 分钟算）；30 天前结束的压缩掉。
func TestApplyShellsAndRange(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	now := time.Now()
	h := func(d time.Duration) int64 { return now.Add(-d).UnixMilli() }
	one := 1
	cs := []protocol.ShellCmd{
		{Key: "claude:s", Ref: "t1", Kind: "build", Label: "Run tests", Cmd: "go test ./...", Start: h(30 * time.Minute), Status: protocol.ShellRunning},
		{Key: "claude:s", Ref: "t2", Kind: "weird", Label: "x", Start: h(3 * time.Hour), End: h(170 * time.Minute), Status: protocol.ShellFailed, Exit: &one},
		{Key: "claude:s", Ref: "t3", Start: h(5 * time.Hour), Status: protocol.ShellLost},
		{Key: "claude:s", Ref: "bad", Start: h(time.Hour), Status: "nope"},
	}
	out, err := st.ApplyShells(ctx, "dev1", cs)
	if err != nil || len(out) != 3 || out[1].Kind != protocol.ShellOther {
		t.Fatalf("应存 3 条、认不得的类型记为其他：%v %+v", err, out)
	}
	// 同一条覆盖：结束了
	cs[0].Status, cs[0].End, cs[0].Last = protocol.ShellOK, h(time.Minute), "ok  github.com/x 0.3s"
	if _, err := st.ApplyShells(ctx, "dev1", cs[:1]); err != nil {
		t.Fatal(err)
	}
	d, err := st.Lanes(ctx, now.Add(-time.Hour), now, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Shells) != 1 || d.Shells[0].Status != protocol.ShellOK || d.Shells[0].Last == "" {
		t.Fatalf("近 1 小时只有 t1，且已覆盖为成功：%+v", d.Shells)
	}
	d, _ = st.Lanes(ctx, now.Add(-6*time.Hour), now, false)
	if len(d.Shells) != 3 || d.Shells[1].Exit == nil || *d.Shells[1].Exit != 1 {
		t.Fatalf("近 6 小时三条都在（按开始排）：%+v", d.Shells)
	}
	d, _ = st.Lanes(ctx, now.Add(-4*time.Hour), now, false)
	if len(d.Shells) != 2 {
		t.Fatalf("不知道结束时间的只算开始后 10 分钟：%+v", d.Shells)
	}
	if _, err := st.CompactLanes(ctx, now.Add(LaneKeep+6*time.Hour)); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = st.DB().QueryRow(`SELECT COUNT(*) FROM lane_shells`).Scan(&n)
	if n != 0 {
		t.Fatalf("30 天前的应删掉：还剩 %d", n)
	}
}
