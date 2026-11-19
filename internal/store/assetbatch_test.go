package store

import (
	"context"
	"testing"

	"github.com/NekroAI/kitestring/internal/protocol"
)

// R-393：批量审核预检后状态变了的跳过；撤销时之后又被改过的素材不还原，其余还原为审核前的状态。
func TestAssetBatchUndo(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	f, _ := s.CreateFile(ctx, "a.png", "image/png", "x", 1, "device:x")
	var items []protocol.AssetItem
	for _, k := range []string{"a", "b", "c"} {
		items = append(items, protocol.AssetItem{Key: k, Kind: protocol.AssetImage, File: &protocol.AssetFile{ID: f.ID}})
	}
	if _, err := s.PushAssets(ctx, NewAssetPush{ProjectID: "p", Batch: "B", Items: items}); err != nil {
		t.Fatal(err)
	}
	xs, err := s.AssetTargets(ctx, "p", nil, AssetFilter{Batch: "B"})
	if err != nil || len(xs) != 3 {
		t.Fatalf("按筛选选中 3 件：%v %v", xs, err)
	}
	if _, err := s.AssetTargets(ctx, "", nil, AssetFilter{}); err == nil {
		t.Fatal("没有筛选条件应拒绝")
	}
	// 预检后 c 已被单独审过：批量时跳过
	if _, _, err := s.ReviewAsset(ctx, NewAssetReview{Asset: xs[2].ID, Verdict: AssetRework, Comment: "不行"}); err != nil {
		t.Fatal(err)
	}
	user := AssetAuthor{Kind: SubmitterUser}
	done, err := s.ReviewAssetBatch(ctx, NewAssetBatch{Assets: xs, Verdict: AssetApproved, Author: user})
	if err != nil || len(done.Reviews) != 2 || len(done.Skipped) != 1 || done.Skipped[0].Code != xs[2].Code {
		t.Fatalf("预检后变了的跳过：%+v %v", done, err)
	}
	// a 之后又被改判：撤销时不还原
	if _, _, err := s.ReviewAsset(ctx, NewAssetReview{Asset: xs[0].ID, Verdict: AssetRework, Comment: "改判"}); err != nil {
		t.Fatal(err)
	}
	b, err := s.LatestAssetBatch(ctx, user)
	if err != nil || b.ID != done.BatchID || b.Count != 2 {
		t.Fatalf("最近一次：%+v %v", b, err)
	}
	u, err := s.UndoAssetBatch(ctx, b.ID)
	if err != nil || len(u.Restored) != 1 || u.Restored[0].Code != xs[1].Code || len(u.Kept) != 1 {
		t.Fatalf("只还原没再改过的：%+v %v", u, err)
	}
	if a, _ := s.GetAsset(ctx, xs[1].ID); a.Status != AssetPending {
		t.Fatalf("还原为待审：%s", a.Status)
	}
	if a, _ := s.GetAsset(ctx, xs[0].ID); a.Status != AssetRework {
		t.Fatalf("改判过的不动：%s", a.Status)
	}
	if _, err := s.UndoAssetBatch(ctx, b.ID); err == nil {
		t.Fatal("不能撤销两次")
	}
	if _, err := s.LatestAssetBatch(ctx, user); err != ErrNotFound {
		t.Fatalf("撤销后没有可撤销的：%v", err)
	}
}
