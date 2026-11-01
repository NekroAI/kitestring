package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/protocol"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "ks.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func testProject(t *testing.T, s *Store, name string) string {
	t.Helper()
	id := protocol.NewID()
	if _, err := s.DB().Exec(`INSERT INTO projects(id, name, created_at) VALUES(?, ?, ?)`, id, name, now()); err != nil {
		t.Fatal(err)
	}
	return id
}

// R-245：当前安排——会话和用户都能记，带到期或撤销，状态按时间推算，留痕不删；旧库打开时补上表和交接的新列。
func TestArrangements(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	p := testProject(t, s, "纸鸢")
	q := testProject(t, s, "别的项目")
	if _, err := s.DB().Exec(`INSERT INTO devices(id, name, token_hash, created_at) VALUES('dev1', 'mini', 'h', ?)`, now()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`INSERT INTO sessions(key, tool, session_id, device_id, project_id, title, role, state, started_at, last_active_at) VALUES('claude:pm', 'claude', 'pm', 'dev1', ?, '纸鸢接任主调度', 'pm', 'idle', ?, ?)`, p, now(), now()); err != nil {
		t.Fatal(err)
	}
	soon := time.Now().Add(2 * time.Hour)
	a, err := s.AddArrangement(ctx, p, "  额度紧，做完 R-12、R-13 就停 ", "claude:pm", &soon)
	if err != nil || a.Code != "N-1" || a.Text != "额度紧，做完 R-12、R-13 就停" || a.ByTitle != "纸鸢接任主调度" || a.ByRole != "pm" || a.Status != ArrangeActive || a.Who() != "纸鸢接任主调度" {
		t.Fatalf("会话记的安排不对：%+v %v", a, err)
	}
	b, err := s.AddArrangement(ctx, p, "这次可以 push", "", nil)
	if err != nil || b.Code != "N-2" || b.Who() != "用户" || b.ExpiresAt != nil {
		t.Fatalf("用户记的安排不对：%+v %v", b, err)
	}
	if _, err := s.AddArrangement(ctx, q, "别的项目的安排", "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddArrangement(ctx, p, " ", "", nil); err == nil {
		t.Fatal("空安排应拒收")
	}
	past := time.Now().Add(-time.Minute)
	if _, err := s.AddArrangement(ctx, p, "过期的", "", &past); err == nil {
		t.Fatal("到期时间已过应拒收")
	}
	if list, _ := s.ListArrangements(ctx, p, false); len(list) != 2 || list[0].Code != "N-2" {
		t.Fatalf("只列本项目生效中的、新的在前：%+v", list)
	}
	// 到期：时间一过就不再生效，但记录还在
	if _, err := s.DB().Exec(`UPDATE arrangements SET expires_at = ? WHERE id = ?`, ts(time.Now().Add(-time.Second)), a.ID); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.ListArrangements(ctx, p, false); len(list) != 1 || list[0].Code != "N-2" {
		t.Fatalf("到期的不该再列：%+v", list)
	}
	if _, err := s.RevokeArrangement(ctx, "N-1", "user"); err == nil {
		t.Fatal("已到期的不用再撤")
	}
	r, err := s.RevokeArrangement(ctx, "n-2", ArrangeByUser)
	if err != nil || r.Status != ArrangeRevoked || r.RevokedBy != "user" || r.RevokedAt == nil {
		t.Fatalf("撤销不对：%+v %v", r, err)
	}
	if _, err := s.RevokeArrangement(ctx, "N-2", "user"); err == nil {
		t.Fatal("不能重复撤销")
	}
	all, _ := s.ListArrangements(ctx, p, true)
	if len(all) != 2 || all[0].Status != ArrangeRevoked || all[1].Status != ArrangeExpired {
		t.Fatalf("留痕：已撤销、已到期的都还在：%+v", all)
	}
	if list, _ := s.ListArrangements(ctx, "", false); len(list) != 1 || list[0].ProjectName != "别的项目" {
		t.Fatalf("不限项目时列全部生效中的：%+v", list)
	}
}

// R-245 迁移：旧库没有 arrangements 表、handoffs 的 agreements / caveats 列，打开时补齐，老交接照样读得出。
func TestArrangeMigratesOldDB(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "ks.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	p := testProject(t, s, "纸鸢")
	old, err := s.WriteHandoff(ctx, p, "pm", "claude:old", Handoff{Doing: "R-1", Next: "交验收", Notes: "坑"})
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`DROP TABLE arrangements`, `ALTER TABLE handoffs DROP COLUMN agreements`, `ALTER TABLE handoffs DROP COLUMN caveats`} {
		if _, err := s.DB().Exec(q); err != nil {
			t.Fatalf("%s：%v", q, err)
		}
	}
	s.Close()
	if s, err = Open(path); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if h, err := s.GetHandoff(ctx, old.ID); err != nil || h.Doing != "R-1" || h.Notes != "坑" || h.Agreements != "" || h.Caveats != "" {
		t.Fatalf("老交接应保留：%+v %v", h, err)
	}
	if h, err := s.WriteHandoff(ctx, p, "pm", "claude:old", Handoff{Doing: "R-2", Agreements: "可以 push", Caveats: "别停 5199"}); err != nil || h.Agreements != "可以 push" || h.Caveats != "别停 5199" {
		t.Fatalf("迁移后能写三栏：%+v %v", h, err)
	}
	if a, err := s.AddArrangement(ctx, p, "这次可以 push", "", nil); err != nil || a.Code != "N-1" {
		t.Fatalf("迁移后能记安排：%+v %v", a, err)
	}
}

func TestParseUntil(t *testing.T) {
	now := time.Date(2026, 11, 1, 14, 0, 0, 0, time.Local)
	for in, want := range map[string]time.Time{
		"2h":               now.Add(2 * time.Hour),
		"30m":              now.Add(30 * time.Minute),
		"1d":               now.Add(24 * time.Hour),
		"18:00":            time.Date(2026, 11, 1, 18, 0, 0, 0, time.Local),
		"09:00":            time.Date(2026, 11, 2, 9, 0, 0, 0, time.Local),
		"2026-11-03":       time.Date(2026, 11, 3, 23, 59, 59, 0, time.Local),
		"2026-11-03 10:30": time.Date(2026, 11, 3, 10, 30, 0, 0, time.Local),
		"11-05 08:00":      time.Date(2026, 11, 5, 8, 0, 0, 0, time.Local),
	} {
		got, err := ParseUntil(in, now)
		if err != nil || got == nil || !got.Equal(want) {
			t.Fatalf("%q → %v %v，应为 %v", in, got, err, want)
		}
	}
	if got, err := ParseUntil("", now); got != nil || err != nil {
		t.Fatal("空表示一直有效")
	}
	for _, bad := range []string{"明天", "-2h", "0s"} {
		if _, err := ParseUntil(bad, now); err == nil {
			t.Fatalf("%q 应报错", bad)
		}
	}
}

// R-245：合入判定只看最近一轮——多轮验收时，早一轮的提交不算；没退回过时全部提交都算，交验收之后才有的（cherry-pick 合入的那份）也算。
func TestRoundCommits(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	p := testProject(t, s, "纸鸢")
	r, err := s.CreateRequirement(ctx, Requirement{ProjectID: p, Title: "多轮", Status: ReqInProgress})
	if err != nil {
		t.Fatal(err)
	}
	day := func(d int) time.Time { return time.Date(2026, 10, d, 10, 0, 0, 0, time.UTC) }
	change := func(at time.Time, from, to string) {
		if _, err := s.DB().Exec(`INSERT INTO status_changes(id, requirement_id, from_status, to_status, actor, at) VALUES(?, ?, ?, ?, 'agent', ?)`, protocol.NewID(), r.ID, from, to, ts(at)); err != nil {
			t.Fatal(err)
		}
	}
	commit := func(sha string, at time.Time) {
		if _, err := s.DB().Exec(`INSERT INTO commits(project_id, sha, subject, committed_at, reqs, created_at) VALUES(?, ?, 'x', ?, ?, ?)`, p, sha, ts(at), ","+r.Code+",", now()); err != nil {
			t.Fatal(err)
		}
	}
	// 第一轮：10-25 提交、交验收，10-26 被退回
	commit("c711419aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", day(25))
	change(day(25).Add(time.Hour), ReqInProgress, ReqReview)
	change(day(26), ReqReview, ReqInProgress)
	// 第二轮：10-29 提交、交验收、验收通过；11-01 才 cherry-pick 合入 main
	commit("e854a32bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", day(29))
	change(day(29).Add(time.Hour), ReqInProgress, ReqReview)
	change(day(30), ReqReview, ReqDone)
	full, _ := s.GetRequirement(ctx, r.ID)
	if got := RoundStart(full.StatusChanges); !got.Equal(day(26)) {
		t.Fatalf("最近一轮应从退回那一刻算起：%v", got)
	}
	cs, err := s.RoundCommits(ctx, full)
	if err != nil || len(cs) != 1 || cs[0].Link[:7] != "e854a32" {
		t.Fatalf("最近一轮只有 e854a32：%+v %v", cs, err)
	}
	commit("a48d7d2ccccccccccccccccccccccccccccccccc", time.Date(2026, 11, 1, 4, 26, 0, 0, time.UTC))
	cs, _ = s.RoundCommits(ctx, full)
	if len(cs) != 2 || cs[1].Link[:7] != "a48d7d2" {
		t.Fatalf("交验收后 cherry-pick 合入的那份也算这一轮：%+v", cs)
	}
	// 没退回过：全部提交都算
	if got := RoundStart([]StatusChange{{From: ReqInProgress, To: ReqReview, At: day(25)}}); !got.IsZero() {
		t.Fatalf("没退回过应从头算：%v", got)
	}
	// 验收通过后又重开返工、再交验收：重开那一刻起算
	if got := RoundStart([]StatusChange{{From: ReqInProgress, To: ReqReview, At: day(25)}, {From: ReqReview, To: ReqDone, At: day(26)}, {From: ReqDone, To: ReqInProgress, At: day(27)}, {From: ReqInProgress, To: ReqReview, At: day(28)}}); !got.Equal(day(27)) {
		t.Fatalf("重开返工应从重开那一刻算：%v", got)
	}
}
