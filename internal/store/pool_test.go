package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// 需求池盘面指标（R-133）：口径见 pool.go。
func TestComputePool(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	now := time.Date(2026, 10, 24, 10, 0, 0, 0, loc)
	at := func(d, h int) time.Time { return time.Date(2026, 10, d, h, 0, 0, 0, loc) }
	reqs := []Requirement{
		{ID: "a", Code: "R-1", Status: ReqDraft, CreatedBy: "agent", CreatedAt: at(23, 9), UpdatedAt: at(23, 9)},
		{ID: "b", Code: "R-2", Status: ReqDraft, CreatedBy: "user", CreatedAt: at(20, 9), UpdatedAt: at(23, 9)},
		{ID: "c", Code: "R-3", Status: ReqReady, CreatedBy: "agent", CreatedAt: at(23, 1), UpdatedAt: at(23, 1)},
		{ID: "d", Code: "R-4", Status: ReqInProgress, CreatedBy: "agent", CreatedAt: at(22, 23), UpdatedAt: at(24, 1)},
		{ID: "e", Code: "R-5", Status: ReqBlocked, CreatedBy: "agent", CreatedAt: at(18, 9), UpdatedAt: at(24, 1)},
		{ID: "f", Code: "R-6", Status: ReqReview, CreatedBy: "user", CreatedAt: at(18, 9), UpdatedAt: at(24, 1)},
		// 昨天进入已完成，但今天又被改过（updated_at 是今天）：按进入 done 的时间算昨天
		{ID: "g", Code: "R-7", Status: ReqDone, CreatedBy: "agent", CreatedAt: at(15, 9), UpdatedAt: at(24, 8)},
		// 没有状态变更记录的旧需求：按 updated_at 回退，算前天
		{ID: "h", Code: "R-8", Status: ReqDone, CreatedBy: "agent", CreatedAt: at(10, 9), UpdatedAt: at(22, 9)},
		// 太早完成的不进近 7 天
		{ID: "i", Code: "R-9", Status: ReqDone, CreatedBy: "agent", CreatedAt: at(1, 9), UpdatedAt: at(2, 9)},
		{ID: "j", Code: "R-10", Status: ReqShelved, CreatedBy: "user", CreatedAt: at(23, 12), UpdatedAt: at(23, 12)},
	}
	since := map[string]time.Time{
		"b": at(21, 9), // 草稿：从 21 号 9 点放到现在，3 天
		"g": at(23, 22),
		// 时区边界：UTC 23 号 17 点 = 本地 24 号 1 点，算今天，不算昨天
		"d": time.Date(2026, 10, 23, 17, 0, 0, 0, time.UTC),
	}
	p := ComputePool(reqs, since, now, loc)
	want := map[string]int{
		"total": 10, "new": 3, "doneY": 1, "drafts": 2, "oldest": 3, "flowing": 4, "user": 2, "agent": 4,
	}
	got := map[string]int{
		"total": p.Total, "new": p.NewYesterday, "doneY": p.DoneYesterday, "drafts": p.Drafts, "oldest": p.DraftOldestDays, "flowing": p.Flowing, "user": p.OpenByUser, "agent": p.OpenByAgent,
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %d，应为 %d（全部：%+v）", k, got[k], v, got)
		}
	}
	if len(p.Counts) != len(ReqStatuses) || p.Counts[ReqDone] != 3 || p.Counts[ReqShelved] != 1 || p.Counts[ReqDraft] != 2 {
		t.Errorf("各状态条数不对：%+v", p.Counts)
	}
	if len(p.DoneDaily) != 7 || p.DoneDaily[0].Date != "2026-10-18" || p.DoneDaily[6].Date != "2026-10-24" {
		t.Fatalf("近 7 天日期不对：%+v", p.DoneDaily)
	}
	daily := map[string]int{}
	for _, d := range p.DoneDaily {
		daily[d.Date] = d.Count
	}
	if daily["2026-10-23"] != 1 || daily["2026-10-22"] != 1 || daily["2026-10-24"] != 0 {
		t.Errorf("每日完成不对：%+v", p.DoneDaily)
	}
	items := map[string]PoolItem{}
	for _, it := range p.Items {
		items[it.Code] = it
	}
	if items["R-2"].Days != 3 || items["R-1"].Days != 1 || !items["R-1"].Since.Equal(at(23, 9)) {
		t.Errorf("草稿已放天数不对：%+v %+v", items["R-2"], items["R-1"])
	}
	if !items["R-8"].Since.Equal(at(22, 9)) {
		t.Errorf("没有记录的已完成应回退到 updated_at：%+v", items["R-8"])
	}
}

// 从数据库算：进入当前状态的时间取状态变更历史里最近一次变为当前状态的记录，按项目筛选。
func TestRequirementPoolFromStore(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "ks.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	a, err := s.CreateRequirement(ctx, Requirement{Title: "甲", CreatedBy: "agent"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateRequirement(ctx, Requirement{Title: "乙", CreatedBy: "user", Status: ReqReview})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetRequirementStatus(ctx, b.Code, ReqDone); err != nil {
		t.Fatal(err)
	}
	// 把乙进入 done 的记录挪到昨天中午（本地 UTC）
	now := time.Now().UTC()
	y := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, time.UTC).AddDate(0, 0, -1)
	if _, err := s.DB().Exec(`UPDATE status_changes SET at = ? WHERE requirement_id = ? AND to_status = 'done'`, ts(y), b.ID); err != nil {
		t.Fatal(err)
	}
	// 甲的草稿记录挪到 5 天前
	if _, err := s.DB().Exec(`UPDATE status_changes SET at = ? WHERE requirement_id = ?`, ts(now.Add(-5*24*time.Hour-time.Hour)), a.ID); err != nil {
		t.Fatal(err)
	}
	p, err := s.RequirementPool(ctx, "", now, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if p.Total != 2 || p.DoneYesterday != 1 || p.Drafts != 1 || p.DraftOldestDays != 5 || p.OpenByAgent != 1 || p.OpenByUser != 0 {
		t.Fatalf("盘面指标不对：%+v", p)
	}
	if p.DoneDaily[5].Count != 1 {
		t.Fatalf("昨天应有 1 条完成：%+v", p.DoneDaily)
	}
	p, err = s.RequirementPool(ctx, "nope", now, time.UTC)
	if err != nil || p.Total != 0 {
		t.Fatalf("按不存在的项目筛应为空：%+v %v", p, err)
	}
}
