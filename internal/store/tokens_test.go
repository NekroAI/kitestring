package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/protocol"
)

// token 用量（R-206）：daemon 重发同一格（累计值）覆盖而不是相加；小时级由分钟重新求和；太早的不收；按档位读回。
func TestApplyUsageIdempotentAndHourly(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	now := time.Now()
	h := unixMinute(now)/60*60 - 60 // 上一个整点
	u := func(m int64, in, cr int64, n int) protocol.UsageMinute {
		return protocol.UsageMinute{Key: "claude:s", Model: "claude-opus-5-5", Minute: m, Input: in, CacheW: 100, CacheW1h: 100, CacheR: cr, Output: 10, Requests: n}
	}
	if _, _, err := st.ApplyUsage(ctx, []protocol.UsageMinute{u(h+1, 5, 1000, 1), u(h+2, 7, 2000, 1)}, []protocol.ColdStart{{Key: "claude:s", At: (h + 1) * 60000, Tokens: 20000, Cause: protocol.ColdIdle, IdleMin: 12}}, now); err != nil {
		t.Fatal(err)
	}
	// 同一格重发（又多了一条回复）：覆盖
	mins, colds, err := st.ApplyUsage(ctx, []protocol.UsageMinute{u(h+2, 9, 5000, 2), u(unixMinute(now.Add(-LaneKeep))-10, 1, 1, 1)},
		[]protocol.ColdStart{{Key: "claude:s", At: (h + 1) * 60000, Tokens: 20000, Cause: protocol.ColdIdle, IdleMin: 12}}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(mins) != 1 || mins[0].Tok.In != 9 || mins[0].Hour.In != 14 || mins[0].Hour.CR != 6000 || mins[0].Hour.N != 3 {
		t.Fatalf("推送 = %+v", mins)
	}
	if len(colds) != 0 {
		t.Fatalf("重复的冷启动又推了：%+v", colds)
	}
	d, err := st.Lanes(ctx, time.Unix(h*60, 0), now, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Tokens) != 1 || len(d.Tokens[0].Minutes) != 2 || d.Tokens[0].In[1] != 9 || d.Tokens[0].N[1] != 2 {
		t.Fatalf("分钟级 = %+v", d.Tokens)
	}
	if len(d.Colds) != 1 || d.Colds[0].Cause != protocol.ColdIdle || d.Colds[0].IdleMin != 12 {
		t.Fatalf("冷启动 = %+v", d.Colds)
	}
	dh, err := st.Lanes(ctx, now.Add(-6*24*time.Hour), now, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(dh.Tokens) != 1 || len(dh.Tokens[0].Minutes) != 1 || dh.Tokens[0].In[0] != 14 || dh.Tokens[0].CW[0] != 200 {
		t.Fatalf("小时级 = %+v", dh.Tokens)
	}
	// 30 天前的分钟压缩掉
	if _, err := st.CompactLanes(ctx, now.Add(LaneKeep+2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	d, _ = st.Lanes(ctx, time.Unix(h*60, 0), now, false)
	if len(d.Tokens) != 0 || len(d.Colds) != 0 {
		t.Fatalf("压缩后还有 %+v %+v", d.Tokens, d.Colds)
	}
}
