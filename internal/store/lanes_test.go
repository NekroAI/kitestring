package store

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/protocol"
)

// 泳道时间线（R-205）：每分钟汇总、长命令、待命、子任务名单；实时写入和旧库补录口径一致。

const laneKey = "claude:s1"

type laneEvs struct {
	n   int
	at  time.Time
	out []protocol.Event
}

func (l *laneEvs) add(sec float64, name string, f func(*protocol.Event)) {
	l.n++
	ev := protocol.Event{ID: fmt.Sprintf("e%05d", l.n), Tool: "claude", SessionID: "s1", Name: name, Time: l.at.Add(time.Duration(sec * float64(time.Second)))}
	if f != nil {
		f(&ev)
	}
	l.out = append(l.out, ev)
}

func tool(id, agent, brief string) func(*protocol.Event) {
	return func(e *protocol.Event) { e.ToolName, e.ToolUseID, e.AgentID, e.ToolBrief = "Bash", id, agent, brief }
}

// sample 是一段典型的会话：主线程几次调用（其中一次跑了 45 秒）、一个子任务、一次 ks wait 待命。
func sample(base time.Time) []protocol.Event {
	l := &laneEvs{at: base}
	l.add(0, "PreToolUse", tool("t1", "", "ls"))
	l.add(1, "PostToolUse", tool("t1", "", "ls"))
	l.add(5, "PreToolUse", tool("t2", "", "go test ./..."))
	l.add(50, "PostToolUse", tool("t2", "", "go test ./...")) // 45 秒：长命令
	l.add(52, "SubagentStart", func(e *protocol.Event) { e.AgentID, e.AgentType, e.ToolBrief = "a1", "Explore", "查代码" })
	for i := 0; i < 5; i++ {
		s := 60 + float64(i)*10
		l.add(s, "PreToolUse", tool(fmt.Sprintf("x%d", i), "a1", "grep"))
		l.add(s+1, "PostToolUse", tool(fmt.Sprintf("x%d", i), "a1", "grep"))
	}
	l.add(115, "SubagentStop", func(e *protocol.Event) { e.AgentID = "a1" })
	l.add(116, "SubagentStop", func(e *protocol.Event) { e.AgentID = "internal" }) // 内部小代理：不进名单
	l.add(118, "PreToolUse", tool("t3", "", "ks wait --timeout 3600"))             // 前台 ks wait 不算长命令
	l.add(119, "PostToolUse", tool("t3", "", "ks wait --timeout 3600"))
	l.add(120, "Stop", nil)
	l.add(121, protocol.EventWait, nil)
	l.add(300, "UserPromptSubmit", nil) // 待命中你在终端里插话：跑这一轮时不算待命
	l.add(320, "Stop", nil)
	l.add(600, protocol.EventWaitEnd, func(e *protocol.Event) { e.Reason = "delivered" })
	l.add(610, "PreToolUse", tool("t4", "", "sleep 100"))
	l.add(640, "Stop", nil) // 被打断：到这一轮结束为止，30 秒，也算长命令
	return l.out
}

func openTemp(t *testing.T) (*Store, string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "hub.db")
	st, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() }) // Windows 上没关的数据库文件删不掉
	return st, p
}

func applyLive(t *testing.T, st *Store, evs []protocol.Event) {
	t.Helper()
	tr := NewLaneTracker()
	for _, ev := range evs {
		if _, err := st.ApplyLane(context.Background(), tr.Apply(laneKey, ev)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLaneLiveAndBackfillAgree(t *testing.T) {
	ctx := context.Background()
	base := time.Now().Add(-30 * time.Minute).Truncate(time.Minute)
	evs := sample(base)

	live, _ := openTemp(t)
	applyLive(t, live, evs)
	got, err := live.Lanes(ctx, base.Add(-time.Hour), time.Now().Add(time.Minute), false)
	if err != nil {
		t.Fatal(err)
	}

	// 主会话：第 0 分钟 2 次（ls、go test），第 1 分钟 1 次（ks wait），第 10 分钟 1 次；子任务第 1 分钟 5 次
	m0 := unixMinute(base)
	want := []LaneRow{
		{Key: laneKey, Agent: "", Minutes: []int64{m0, m0 + 1, m0 + 10}, Calls: []int{2, 1, 1}},
		{Key: laneKey, Agent: "a1", Minutes: []int64{m0 + 1}, Calls: []int{5}},
	}
	if !reflect.DeepEqual(got.Rows, want) {
		t.Fatalf("分钟汇总 = %+v，想要 %+v", got.Rows, want)
	}
	var tools, waits []LaneSpan
	for _, s := range got.Spans {
		if s.Kind == SpanTool {
			tools = append(tools, s)
		} else {
			waits = append(waits, s)
		}
	}
	if len(tools) != 2 || tools[0].Label != "Bash · go test ./..." || *tools[0].End-tools[0].Start != 45000 || *tools[1].End-tools[1].Start != 30000 {
		t.Fatalf("长命令 = %+v", tools)
	}
	if len(waits) != 2 || *waits[0].End-waits[0].Start != 179000 || *waits[1].End-waits[1].Start != 280000 {
		t.Fatalf("待命 = %+v", waits)
	}
	if len(got.Agents) != 1 || got.Agents[0].Label != "查代码" || got.Agents[0].Type != "Explore" || got.Agents[0].End == nil {
		t.Fatalf("子任务 = %+v（结束后要留着，内部小代理不进名单）", got.Agents)
	}

	// 旧库升级：事件已经在库里，打开时从事件记录补出来，和实时写入的一样（刷新、换设备看到的都一样）
	old, p := openTemp(t)
	for _, ev := range evs {
		if _, err := old.InsertEvent(ctx, "d1", ev); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := old.DB().Exec(`DELETE FROM settings WHERE key = ?`, laneBackfillKey); err != nil {
		t.Fatal(err)
	}
	old.Close()
	re, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { re.Close() })
	back, err := re.Lanes(ctx, base.Add(-time.Hour), time.Now().Add(time.Minute), false)
	if err != nil {
		t.Fatal(err)
	}
	back.Now, got.Now = 0, 0
	if !reflect.DeepEqual(back, got) {
		t.Fatalf("补录 = %+v\n实时 = %+v", back, got)
	}
	// 只补一次：再打开不重复累加
	re.Close()
	re, _ = Open(p)
	again, _ := re.Lanes(ctx, base.Add(-time.Hour), time.Now().Add(time.Minute), false)
	if !reflect.DeepEqual(again.Rows, got.Rows) {
		t.Fatalf("再次打开后 = %+v", again.Rows)
	}
}

// 历史只增不改：新事件只改它所在那一分钟，已有的分钟原样不动。
func TestLaneHistoryAppendOnly(t *testing.T) {
	ctx := context.Background()
	st, _ := openTemp(t)
	tr := NewLaneTracker()
	base := time.Now().Add(-10 * time.Minute).Truncate(time.Minute)
	l := &laneEvs{at: base}
	for i := 0; i < 3; i++ {
		l.add(float64(i), "PreToolUse", tool(fmt.Sprintf("a%d", i), "", "x"))
	}
	for _, ev := range l.out {
		st.ApplyLane(ctx, tr.Apply(laneKey, ev))
	}
	before, _ := st.Lanes(ctx, base.Add(-time.Hour), time.Now().Add(time.Minute), false)
	l.out = nil
	l.add(9*60, "PreToolUse", tool("b", "", "x"))
	w, err := st.ApplyLane(ctx, tr.Apply(laneKey, l.out[0]))
	if err != nil {
		t.Fatal(err)
	}
	if w.Minute == nil || w.Minute.Minute != unixMinute(base)+9 || w.Minute.Calls != 1 {
		t.Fatalf("推送的格子 = %+v，只该是新的那一分钟", w.Minute)
	}
	after, _ := st.Lanes(ctx, base.Add(-time.Hour), time.Now().Add(time.Minute), false)
	if after.Rows[0].Calls[0] != before.Rows[0].Calls[0] || after.Rows[0].Minutes[0] != before.Rows[0].Minutes[0] || len(after.Rows[0].Minutes) != 2 {
		t.Fatalf("前 = %+v 后 = %+v", before.Rows, after.Rows)
	}
}

// hub 重启丢了内存里的开始：结束时没配上，交给 hub 去事件记录里找。
func TestLaneUnmatchedPost(t *testing.T) {
	ctx := context.Background()
	st, _ := openTemp(t)
	base := time.Now().Add(-5 * time.Minute)
	l := &laneEvs{at: base}
	l.add(0, "PreToolUse", tool("t9", "", "make"))
	l.add(90, "PostToolUse", tool("t9", "", "make"))
	st.InsertEvent(ctx, "d1", l.out[0])
	e := NewLaneTracker().Apply(laneKey, l.out[1])
	if e.Unmatched != "t9" {
		t.Fatalf("Unmatched = %q", e.Unmatched)
	}
	pre, ok := st.PreToolStart(ctx, laneKey, "t9", l.out[1].Time)
	if !ok || !pre.Time.Equal(l.out[0].Time) {
		t.Fatalf("找到的开始 = %v %v", pre.Time, ok)
	}
	if sp, ok := LongSpan(laneKey, "", "t9", "Bash · make", pre.Time, l.out[1].Time); !ok || *sp.End-sp.Start != 90000 {
		t.Fatalf("长命令 = %+v", sp)
	}
}

// 小时级和分钟级同时累计；30 天前的分钟删掉，小时级还在，次数不变。
func TestCompactLanes(t *testing.T) {
	ctx := context.Background()
	st, _ := openTemp(t)
	now := time.Now()
	old := now.Add(-40 * 24 * time.Hour).Truncate(time.Hour)
	tr := NewLaneTracker()
	l := &laneEvs{at: old}
	for i := 0; i < 90; i++ { // 一个半小时，每分钟 2 次
		l.add(float64(i*60), "PreToolUse", tool(fmt.Sprintf("o%d", i), "", "x"))
		l.add(float64(i*60+1), "PreToolUse", tool(fmt.Sprintf("p%d", i), "", "x"))
	}
	l.at = now.Add(-5 * time.Minute)
	l.add(0, "PreToolUse", tool("r", "", "x"))
	for _, ev := range l.out {
		if _, err := st.ApplyLane(ctx, tr.Apply(laneKey, ev)); err != nil {
			t.Fatal(err)
		}
	}
	n, err := st.CompactLanes(ctx, now)
	if err != nil || n != 90 {
		t.Fatalf("删掉 %d 个分钟行，err %v", n, err)
	}
	d, _ := st.Lanes(ctx, old.Add(-time.Hour), now.Add(time.Minute), false)
	if len(d.Rows) != 1 || len(d.Rows[0].Minutes) != 1 {
		t.Fatalf("分钟级只该剩近 30 天的：%+v", d.Rows)
	}
	h, _ := st.Lanes(ctx, old.Add(-time.Hour), now.Add(time.Minute), true)
	h0 := unixMinute(old)
	r := h.Rows[0]
	if h.Step != 60 || !reflect.DeepEqual(r.Minutes[:2], []int64{h0, h0 + 60}) || !reflect.DeepEqual(r.Calls, []int{120, 60, 1}) {
		t.Fatalf("小时级 = %+v", h)
	}
	if n, _ := st.CompactLanes(ctx, now); n != 0 {
		t.Fatalf("重复删了 %d 行", n)
	}
}

// 性能：一周、30 个会话（每个 3 个子任务）、三成的分钟有调用（约 36 万个分钟格，远多于真实用量），
// 近 1 周档读小时级，取数要远低于 200ms；近 1 天档读分钟级（网页在后台预取）也记一下。
func TestLanesWeekFast(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	ctx := context.Background()
	st, _ := openTemp(t)
	seedWeek(t, st)
	start := time.Now()
	d, err := st.Lanes(ctx, time.Now().Add(-7*24*time.Hour), time.Now().Add(time.Minute), true)
	took := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, r := range d.Rows {
		n += len(r.Calls)
	}
	start = time.Now()
	day, _ := st.Lanes(ctx, time.Now().Add(-24*time.Hour), time.Now().Add(time.Minute), false)
	dayTook := time.Since(start)
	dn := 0
	for _, r := range day.Rows {
		dn += len(r.Calls)
	}
	t.Logf("近 1 周：%d 条泳道、%d 个小时格，取数 %v；近 1 天：%d 个分钟格，取数 %v", len(d.Rows), n, took, dn, dayTook)
	if n < 15000 || took > 100*time.Millisecond {
		t.Fatalf("%d 个小时格用了 %v", n, took)
	}
}

func seedWeek(t testing.TB, st *Store) {
	now := unixMinute(time.Now())
	tx, _ := st.DB().Begin()
	ins, _ := tx.Prepare(`INSERT INTO lane_minutes(session_key, agent_id, minute, calls) VALUES(?, ?, ?, ?)`)
	insH, _ := tx.Prepare(`INSERT INTO lane_hours(session_key, agent_id, minute, calls) VALUES(?, ?, ?, ?) ON CONFLICT DO UPDATE SET calls = calls + excluded.calls`)
	for s := 0; s < 30; s++ {
		for a := 0; a < 4; a++ {
			agent := ""
			if a > 0 {
				agent = fmt.Sprintf("a%d", a)
			}
			for m := now - 7*24*60; m <= now; m++ {
				if (m*7+int64(s)*13+int64(a)*29)%10 < 3 {
					ins.Exec(fmt.Sprintf("claude:s%d", s), agent, m, 1+int(m%5))
					insH.Exec(fmt.Sprintf("claude:s%d", s), agent, m/60*60, 1+int(m%5))
				}
			}
		}
	}
	ins.Close()
	insH.Close()
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func BenchmarkLanesWeek(b *testing.B) {
	p := filepath.Join(b.TempDir(), "hub.db")
	st, err := Open(p)
	if err != nil {
		b.Fatal(err)
	}
	seedWeek(b, st)
	from := time.Now().Add(-7 * 24 * time.Hour)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := st.Lanes(context.Background(), from, time.Now().Add(time.Minute), false); err != nil {
			b.Fatal(err)
		}
	}
}

// Codex 在前台跑 ks wait：一挂上就是待命，插话不打断这段虚线；Claude Code 在后台跑，一轮结束才算。
func TestLaneWaitCodexVsClaude(t *testing.T) {
	base := time.Now()
	ev := func(sec int, name string) protocol.Event {
		return protocol.Event{ID: fmt.Sprintf("w%d%s", sec, name), Name: name, Time: base.Add(time.Duration(sec) * time.Second)}
	}
	for _, c := range []struct {
		key  string
		open bool
	}{{"codex:s2", true}, {"claude:s3", false}} {
		tr := NewLaneTracker()
		tr.Apply(c.key, ev(0, "UserPromptSubmit"))
		e := tr.Apply(c.key, ev(5, protocol.EventWait))
		if got := len(e.Spans) == 1; got != c.open {
			t.Fatalf("%s 挂上 ks wait 时开始待命 = %v", c.key, got)
		}
		e = tr.Apply(c.key, ev(9, "Stop"))
		if got := len(e.Spans) == 1; got == c.open {
			t.Fatalf("%s 一轮结束时开始待命 = %v", c.key, got)
		}
	}
}
