package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// 暂时离开（R-210）的旧库迁移：没有 aways / away_events 表、decisions.hard、grants.away_id 的旧库，打开时自动补齐，已有数据不受影响。
func TestAwayMigratesOldDB(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "ks.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	d, err := s.CreateDecision(ctx, Decision{Question: "旧决策"})
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`DROP TABLE away_events`, `DROP TABLE aways`, `ALTER TABLE decisions DROP COLUMN hard`, `ALTER TABLE grants DROP COLUMN away_id`} {
		if _, err := s.DB().Exec(q); err != nil {
			t.Fatalf("%s：%v", q, err)
		}
	}
	s.Close()

	if s, err = Open(path); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got, err := s.GetDecision(ctx, d.ID); err != nil || got.Question != "旧决策" || got.Hard != "" {
		t.Fatalf("旧决策应保留、hard 为空：%+v %v", got, err)
	}
	a, err := s.StartAway(ctx, time.Now(), 120, RiskLow, false)
	if err != nil || a.Code != "A-1" || !a.Active() {
		t.Fatalf("迁移后能开始离开：%+v %v", a, err)
	}
	if _, err := s.StartAway(ctx, time.Now(), 0, AwayScopeNone, false); !errors.Is(err, ErrAwayActive) {
		t.Fatalf("离开中不能再开：%v", err)
	}
	if err := s.AddAwayEvent(ctx, a.ID, AwayEvent{Tier: AwayPush, Event: "decision_raised", Ref: "D-1", Title: "待决策"}); err != nil {
		t.Fatal(err)
	}
	if evs, err := s.AwayEvents(ctx, a.ID); err != nil || len(evs) != 1 {
		t.Fatalf("离开期间的事应记下：%+v %v", evs, err)
	}
	if a, err = s.EndAway(ctx, a.ID, time.Now()); err != nil || a.Active() {
		t.Fatalf("结束离开：%+v %v", a, err)
	}
	if _, err := s.EndAway(ctx, a.ID, time.Now()); err == nil {
		t.Fatal("已结束的离开不能再结束")
	}
}
