package store

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// 撤回决策的 replaced_by 校验（R-168）：目标须仍待答、不能回环、检查与关闭同事务。
func TestWithdrawReplacedBy(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "ks.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	raise := func(q string) Decision {
		d, err := s.CreateDecision(ctx, Decision{Kind: KindDecision, Question: q, Options: []string{"甲", "乙"}})
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	// CreateDecision 无会话，OriginSession / SessionKey 都空；这里直接走 CloseDecision（WithdrawDecision 只多了归属检查）。
	a, b, c := raise("a"), raise("b"), raise("c")

	// 正常撤回：目标仍待答
	got, err := s.CloseDecision(ctx, a.Code, "修订", b.Code)
	if err != nil || got.Status != DecClosed || got.ReplacedBy != b.Code {
		t.Fatalf("正常撤回应成功：%+v %v", got, err)
	}
	// 目标已关闭：被拒，且自己仍待答
	if _, err := s.CloseDecision(ctx, c.Code, "", a.Code); err == nil || !strings.Contains(err.Error(), "已关闭") {
		t.Fatalf("目标已关闭应被拒：%v", err)
	}
	if d, _ := s.GetDecision(ctx, c.Code); d.Status != DecPending {
		t.Fatalf("被拒后不应关闭：%s", d.Status)
	}
	// 目标不存在
	if _, err := s.CloseDecision(ctx, c.Code, "", "D-999"); err == nil || !strings.Contains(err.Error(), "不存在") {
		t.Fatalf("目标不存在应被拒：%v", err)
	}
	// 目标已答复
	x := raise("x")
	if _, err := s.db.ExecContext(ctx, `UPDATE decisions SET status = ? WHERE id = ?`, DecAnswered, x.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CloseDecision(ctx, c.Code, "", x.Code); err == nil || !strings.Contains(err.Error(), "已答复") {
		t.Fatalf("目标已答复应被拒：%v", err)
	}
	// 两跳回环：c→p→c（p 仍待答但 replaced_by 指回 c，属历史脏数据），拒绝并提示回环
	p := raise("p")
	if _, err := s.db.ExecContext(ctx, `UPDATE decisions SET replaced_by = ? WHERE id = ?`, c.Code, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CloseDecision(ctx, c.Code, "", p.Code); err == nil || !strings.Contains(err.Error(), "回环") {
		t.Fatalf("回环应被拒：%v", err)
	}
	// 多跳：c→q→p→c
	q := raise("q")
	if _, err := s.db.ExecContext(ctx, `UPDATE decisions SET replaced_by = ? WHERE id = ?`, p.Code, q.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CloseDecision(ctx, c.Code, "", q.Code); err == nil || !strings.Contains(err.Error(), "回环") {
		t.Fatalf("多跳回环应被拒：%v", err)
	}
}

// 两个会话同时互相撤回：最多成功一条，另一条仍待答。
func TestWithdrawMutualConcurrent(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "ks.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for round := 0; round < 20; round++ {
		a, _ := s.CreateDecision(ctx, Decision{Kind: KindDecision, Question: "a"})
		b, _ := s.CreateDecision(ctx, Decision{Kind: KindDecision, Question: "b"})
		var wg sync.WaitGroup
		errs := make([]error, 2)
		for i, pair := range [][2]string{{a.Code, b.Code}, {b.Code, a.Code}} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, errs[i] = s.CloseDecision(ctx, pair[0], "互相取代", pair[1])
			}()
		}
		wg.Wait()
		ok := 0
		for _, e := range errs {
			if e == nil {
				ok++
			}
		}
		da, _ := s.GetDecision(ctx, a.Code)
		db, _ := s.GetDecision(ctx, b.Code)
		if ok != 1 || (da.Status == DecClosed) == (db.Status == DecClosed) {
			t.Fatalf("应恰好一条成功、一条仍待答：ok=%d %s %s %v", ok, da.Status, db.Status, errs)
		}
	}
}
