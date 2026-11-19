package store

import (
	"context"
	"errors"
	"testing"

	"github.com/NekroAI/kitestring/internal/protocol"
)

// R-351 第 4 步：审核按状态机改状态、记审核记录；返工必须写意见；作者区分你 / 会话 / 访客（D-1782）；改判是新的一行。
func TestReviewAsset(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	f, _ := s.CreateFile(ctx, "a.png", "image/png", "x", 1, "device:x")
	r, err := s.PushAssets(ctx, NewAssetPush{ProjectID: "p", Batch: "B", Status: AssetWIP, Items: []protocol.AssetItem{{Key: "a", Kind: protocol.AssetImage, File: &protocol.AssetFile{ID: f.ID}}}})
	if err != nil {
		t.Fatal(err)
	}
	code := r.Items[0].Code
	if _, _, err := s.ReviewAsset(ctx, NewAssetReview{Asset: code, Verdict: AssetApproved, Author: AssetAuthor{Kind: SubmitterUser}}); !errors.Is(err, ErrAssetMove) {
		t.Fatalf("制作中不能审核：%v", err)
	}
	a, _ := s.GetAsset(ctx, code)
	_ = s.SetAssetStatus(ctx, a.ID, AssetWIP, AssetPending)
	if _, _, err := s.ReviewAsset(ctx, NewAssetReview{Asset: code, Verdict: AssetRework, Comment: "  "}); err == nil {
		t.Fatal("返工不写意见应拒绝")
	}
	if _, _, err := s.ReviewAsset(ctx, NewAssetReview{Asset: code, Verdict: "done"}); err == nil {
		t.Fatal("结论只能是通过或返工")
	}
	if _, _, err := s.ReviewAsset(ctx, NewAssetReview{Asset: code, Verdict: AssetApproved, Version: 2}); !errors.Is(err, ErrAssetStale) {
		t.Fatalf("版本不符应拒绝：%v", err)
	}
	rv, got, err := s.ReviewAsset(ctx, NewAssetReview{Asset: code, Verdict: AssetApproved, From: AssetPending, Version: 1, Author: AssetAuthor{Kind: SubmitterSession, Ref: "claude:x", Name: "美术主管"}})
	if err != nil || got.Status != AssetApproved || rv.Version != 1 {
		t.Fatalf("通过：%+v %v", got, err)
	}
	if _, _, err := s.ReviewAsset(ctx, NewAssetReview{Asset: code, Verdict: AssetRework, Comment: "再看了一遍，阴影太重", Author: AssetAuthor{Kind: SubmitterGuest, Ref: "share-1", Name: "小王"}}); err != nil {
		t.Fatalf("通过后改判返工：%v", err)
	}
	d, _ := s.GetAsset(ctx, code)
	if d.Status != AssetRework || len(d.Reviews) != 2 || d.Reviews[0].Author.Kind != SubmitterGuest || d.Reviews[0].Author.Name != "小王" || d.Reviews[1].Author.Kind != SubmitterSession || d.Reviews[0].Delivery != "" {
		t.Fatalf("审核记录（新的在前，作者各记各的）：%+v", d.Reviews)
	}
	if c, _ := s.AssetCounts(ctx); c[AssetRework] != 1 || c[AssetPending] != 0 {
		t.Fatalf("各状态数：%v", c)
	}
}
