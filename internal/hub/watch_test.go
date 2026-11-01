package hub

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/store"
)

// R-249 P4：需求订阅提醒的统一入口 reqEvent——两档（mode = action 只收要动手的）、自己引起的不送、
// 同一次操作里已被别的通知告诉过的不送、60 秒内的只告知并成一条（已送达后另起一条）、退订优先于因关联而订阅；
// 关联决策被作答（含主助理代答）不送给提问方和代答的人。
func TestReqEvent(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	h := New(st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	bg := context.Background()
	d, err := st.CreateDevice(bg, "mac", "h1")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, x := range [][3]string{{"claude:dev", "dev", "做订阅"}, {"claude:all", "", "观察"}, {"codex:act", "advisor", "只看要动手"}, {"claude:aide", "aide", "主助理"}, {"claude:off", "", "退订了"}} {
		if _, err := st.DB().Exec(`INSERT INTO sessions(key, tool, session_id, device_id, role, title, state, started_at, last_active_at) VALUES(?, 'claude', ?, ?, ?, ?, 'idle', ?, ?)`, x[0], x[0], d.ID, x[1], x[2], now, now); err != nil {
			t.Fatal(err)
		}
	}
	r, err := st.CreateRequirement(bg, store.Requirement{Title: "订阅需求", Status: store.ReqInProgress, Priority: "P1"})
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"claude:dev", "claude:off"} {
		_ = st.LinkSession(bg, r.ID, k)
	}
	st.SetWatch(bg, r.ID, "claude:all", "", false)
	st.SetWatch(bg, r.ID, "codex:act", store.WatchAction, false)
	st.SetWatch(bg, r.ID, "claude:aide", "", false)
	st.SetWatch(bg, r.ID, "claude:off", "", true) // 关联着但退订了
	r, _ = st.GetRequirement(bg, r.ID)

	tells := func(key string) []store.Decision {
		t.Helper()
		var out []store.Decision
		list, _ := st.ListDecisions(bg, nil, 500)
		for _, x := range list {
			if x.Kind == store.KindTell && x.SessionKey == key && x.Question == watchTellQ {
				out = append(out, x)
			}
		}
		return out
	}
	last := func(key string) store.Decision {
		t.Helper()
		ts := tells(key)
		if len(ts) == 0 {
			t.Fatalf("%s 没收到提醒", key)
		}
		return ts[0] // ListDecisions 按时间倒序
	}

	// 只告知：开发留言 → all 收到（fyi）；act 只收要动手的、dev 自己、off 退订了，都不收
	h.reqEvent(withTold(bg), r, evComment, "claude:dev", "有新留言 D-1")
	if x := last("claude:all"); !x.FYI || !strings.Contains(x.Answer, "（只告知）") || !strings.Contains(x.Answer, r.Code) || !strings.Contains(x.Answer, "有新留言 D-1（开发 「做订阅」（claude:dev））") {
		t.Fatalf("只告知提醒：%+v", x)
	}
	for _, k := range []string{"codex:act", "claude:dev", "claude:off"} {
		if len(tells(k)) != 0 {
			t.Fatalf("%s 不应收到只告知的留言提醒", k)
		}
	}
	// 60 秒内的只告知并进同一条
	h.reqEvent(withTold(bg), r, evEvidence, "claude:dev", "附了证据")
	if ts := tells("claude:all"); len(ts) != 1 || !strings.Contains(ts[0].Answer, "有新留言 D-1") || !strings.Contains(ts[0].Answer, "；附了证据") {
		t.Fatalf("应合并成一条：%+v", ts)
	}
	// 已送达后另起一条
	x := last("claude:all")
	if _, err := st.MarkDelivered(bg, x.ID, "claude:all", "t1", time.Now()); err != nil {
		t.Fatal(err)
	}
	h.reqEvent(withTold(bg), r, evDetail, "claude:dev", "改了描述")
	if ts := tells("claude:all"); len(ts) != 2 || strings.Contains(ts[0].Answer, "附了证据") {
		t.Fatalf("送达后应另起一条：%+v", ts)
	}

	// 要动手：用户（网页，没有会话）改验收标准 → act、all、dev、aide 都收到，不是只告知；off 仍然不收
	h.reqEvent(withTold(bg), r, evAcceptance, "", "改了验收标准")
	for _, k := range []string{"codex:act", "claude:all", "claude:dev", "claude:aide"} {
		if x := last(k); x.FYI || !strings.Contains(x.Answer, "（要动手）") || !strings.Contains(x.Answer, "改了验收标准（用户）") {
			t.Fatalf("%s 要动手提醒：%+v", k, x)
		}
	}
	if len(tells("claude:off")) != 0 {
		t.Fatal("退订优先于因关联而订阅")
	}
	// 要动手的不合并
	n := len(tells("claude:all"))
	h.reqEvent(withTold(bg), r, evReviewFail, "claude:aide", "评审意见：不通过 1 条")
	if len(tells("claude:all")) != n+1 {
		t.Fatal("要动手的每次另发")
	}

	// 去重：同一次操作里 h.tell 已经告诉过 dev（例如 tellEdited / onReview），订阅提醒不再送
	ctx := withTold(bg)
	nDev := len(tells("claude:dev"))
	if _, err := h.tell(ctx, "claude:dev", r.ProjectID, r.ID, "按新的内容做"); err != nil {
		t.Fatal(err)
	}
	h.reqEvent(ctx, r, evAcceptance, "claude:aide", "改了验收标准")
	if len(tells("claude:dev")) != nDev {
		t.Fatal("已被通知过的会话不应再收到订阅提醒")
	}
	if !strings.Contains(last("claude:all").Answer, "改了验收标准（主助理") {
		t.Fatal("别的订阅者照常收到")
	}

	// 关联决策被作答：主助理代答 → 提问的 dev、代答的 aide 都不收，all 收到（要动手）
	q, err := st.CreateDecision(bg, store.Decision{SessionKey: "claude:dev", RequirementID: r.ID, Question: "每页 50 还是 100"})
	if err != nil {
		t.Fatal(err)
	}
	nDev, nAide := len(tells("claude:dev")), len(tells("claude:aide"))
	h.watchDecisionAnswered(withTold(bg), q, "claude:aide")
	if len(tells("claude:dev")) != nDev || len(tells("claude:aide")) != nAide {
		t.Fatal("提问方、代答的主助理不应收到")
	}
	if x := last("claude:all"); x.FYI || !strings.Contains(x.Answer, "关联决策 "+q.Code) || !strings.Contains(x.Answer, "已代答") {
		t.Fatalf("决策作答提醒：%+v", x)
	}

	// 已结束的会话不收
	st.DB().Exec(`UPDATE sessions SET state = 'ended' WHERE key = 'claude:all'`)
	n = len(tells("claude:all"))
	h.reqEvent(withTold(bg), r, evBlocked, "", "被设为受阻")
	if len(tells("claude:all")) != n {
		t.Fatal("已结束的会话不应收到")
	}
}

// 状态变化归档：受阻、从待验收退回要动手，其余只告知。
func TestStatusEvent(t *testing.T) {
	for _, c := range []struct{ before, after, kind, text string }{
		{store.ReqInProgress, store.ReqBlocked, evBlocked, "被设为受阻"},
		{store.ReqReview, store.ReqInProgress, evReturned, "被退回"},
		{store.ReqReview, store.ReqDraft, evReturned, "被退回"},
		{store.ReqReview, store.ReqDone, evStatus, "状态从待验收改为已完成"},
		{store.ReqInProgress, store.ReqReview, evStatus, "状态从进行中改为待验收"},
		{store.ReqReady, store.ReqInProgress, evStatus, "状态从待开始改为进行中"},
	} {
		k, txt := statusEvent(c.before, c.after)
		if k != c.kind || !strings.Contains(txt, c.text) {
			t.Errorf("%s → %s：%s %s", c.before, c.after, k, txt)
		}
		if watchAction[k] != (c.kind == evBlocked || c.kind == evReturned) {
			t.Errorf("%s 档位不对", k)
		}
	}
	for _, k := range []string{evAcceptance, evReviewFail, evDecision} {
		if !watchAction[k] {
			t.Errorf("%s 应是要动手", k)
		}
	}
	for _, k := range []string{evDetail, evComment, evEvidence, evReviewPass, evStatus} {
		if watchAction[k] {
			t.Errorf("%s 应是只告知", k)
		}
	}
}

// ks req watching / MCP req_watching 的输出：因关联而订阅的写明，已完成的因关联订阅只计数，退订单列。
func TestWatchingText(t *testing.T) {
	on := []store.Watch{
		{Code: "R-3", Title: "显式", Status: store.ReqReview, Mode: store.WatchAction},
		{Code: "R-2", Title: "在做", Status: store.ReqInProgress, Implicit: true, Linked: true, Mode: store.WatchAll},
		{Code: "R-1", Title: "老的", Status: store.ReqDone, Implicit: true, Linked: true, Mode: store.WatchAll},
	}
	off := []store.Watch{{Code: "R-9", Linked: true, Off: true}}
	s := watchingText(on, off)
	for _, want := range []string{"你订阅了 3 条需求", "R-3「显式」（待验收）· 显式订阅 · 只收要动手的", "R-2「在做」（进行中）· 因关联而订阅 · 两档都收", "另有 1 条已完成 / 已搁置", "已退订：R-9（关联了，已退掉默认订阅）", "ks req unwatch"} {
		if !strings.Contains(s, want) {
			t.Fatalf("缺「%s」：%s", want, s)
		}
	}
	if strings.Contains(s, "R-1「") {
		t.Fatalf("已完成的因关联订阅不逐条列：%s", s)
	}
	if !strings.Contains(watchingText(nil, nil), "没有订阅任何需求") {
		t.Fatal("空列表")
	}
}
