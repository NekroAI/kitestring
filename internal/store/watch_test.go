package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// 订阅需求（R-249 P4）：订阅者 = 显式订阅 ∪ 关联且没结束的会话，去掉退订的；退订优先于因关联而来的默认订阅；
// 只含没结束的会话；mode 保留；watching 区分显式 / 因关联 / 已退订；MergeTell 只改还没送达的团队消息。
func TestReqWatches(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	d, err := st.CreateDevice(ctx, "mac", "h1")
	if err != nil {
		t.Fatal(err)
	}
	n := time.Now().UTC().Format(time.RFC3339Nano)
	for _, x := range [][2]string{{"claude:dev", "idle"}, {"claude:obs", "idle"}, {"claude:gone", "ended"}, {"codex:quiet", "idle"}} {
		if _, err := st.DB().Exec(`INSERT INTO sessions(key, tool, session_id, device_id, state, started_at, last_active_at) VALUES(?, 'claude', ?, ?, ?, ?, ?)`, x[0], x[0], d.ID, x[1], n, n); err != nil {
			t.Fatal(err)
		}
	}
	r, err := st.CreateRequirement(ctx, Requirement{Title: "订阅", Status: ReqInProgress, Priority: "P2"})
	if err != nil {
		t.Fatal(err)
	}
	keys := func() map[string]Watch {
		t.Helper()
		ws, err := st.Watchers(ctx, r.ID)
		if err != nil {
			t.Fatal(err)
		}
		m := map[string]Watch{}
		for _, w := range ws {
			m[w.SessionKey] = w
		}
		return m
	}
	if len(keys()) != 0 {
		t.Fatal("没人关联、没人订阅时没有订阅者")
	}
	// 关联 = 隐式订阅（不写行）；已结束的关联会话不算
	for _, k := range []string{"claude:dev", "claude:gone", "codex:quiet"} {
		if err := st.LinkSession(ctx, r.ID, k); err != nil {
			t.Fatal(err)
		}
	}
	m := keys()
	if w, ok := m["claude:dev"]; !ok || !w.Implicit || !w.Linked || w.Mode != WatchAll || len(m) != 2 {
		t.Fatalf("关联的没结束会话应隐式订阅：%+v", m)
	}
	var rows int
	st.DB().QueryRow(`SELECT COUNT(*) FROM req_watches`).Scan(&rows)
	if rows != 0 {
		t.Fatal("隐式订阅不写行")
	}
	// 显式订阅（只收要动手的）
	w, err := st.SetWatch(ctx, r.ID, "claude:obs", WatchAction, false)
	if err != nil || w.Mode != WatchAction || w.Implicit || w.Off || w.Code != r.Code {
		t.Fatalf("显式订阅：%+v %v", w, err)
	}
	if w := keys()["claude:obs"]; w.Implicit || w.Linked || w.Mode != WatchAction {
		t.Fatalf("显式订阅者：%+v", w)
	}
	// 退订优先于隐式订阅
	if _, err := st.SetWatch(ctx, r.ID, "codex:quiet", "", true); err != nil {
		t.Fatal(err)
	}
	if _, ok := keys()["codex:quiet"]; ok {
		t.Fatal("退订后不应再是订阅者，哪怕还关联着")
	}
	if w, ok, _ := st.WatchOf(ctx, r.ID, "codex:quiet"); ok || !w.Off || !w.Linked {
		t.Fatalf("WatchOf 应写明已退订、仍关联：%+v %v", w, ok)
	}
	// 重新订阅：mode 不给时保留原档位
	if w, _ := st.SetWatch(ctx, r.ID, "claude:obs", "", true); w.Mode != WatchAction || !w.Off {
		t.Fatalf("退订保留档位：%+v", w)
	}
	if w, _ := st.SetWatch(ctx, r.ID, "claude:obs", WatchAll, false); w.Mode != WatchAll || w.Off {
		t.Fatalf("重新订阅改档位：%+v", w)
	}
	// watching：显式在前，因关联在后；退订单列
	on, off, err := st.Watching(ctx, "claude:dev")
	if err != nil || len(on) != 1 || !on[0].Implicit || on[0].Code != r.Code || len(off) != 0 {
		t.Fatalf("dev 因关联而订阅：%+v %+v %v", on, off, err)
	}
	on, _, _ = st.Watching(ctx, "claude:obs")
	if len(on) != 1 || on[0].Implicit {
		t.Fatalf("obs 显式订阅：%+v", on)
	}
	on, off, _ = st.Watching(ctx, "codex:quiet")
	if len(on) != 0 || len(off) != 1 || !off[0].Linked {
		t.Fatalf("quiet 已退订：%+v %+v", on, off)
	}

	// MergeTell 只改还没送达的
	tell, err := st.CreateDecision(ctx, Decision{Kind: KindTell, SessionKey: "claude:dev", RequirementID: r.ID, Question: "需求订阅", Answer: "a", FYI: true})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := st.MergeTell(ctx, tell.ID, "a；b"); !ok || err != nil {
		t.Fatalf("待送的应能合并：%v %v", ok, err)
	}
	if _, err := st.MarkDelivered(ctx, tell.ID, "claude:dev", "t1", time.Now()); err != nil {
		t.Fatal(err)
	}
	if ok, _ := st.MergeTell(ctx, tell.ID, "a；b；c"); ok {
		t.Fatal("已送达的不应再改")
	}
	if got, _ := st.GetDecision(ctx, tell.ID); got.Answer != "a；b" {
		t.Fatalf("正文：%q", got.Answer)
	}
}
