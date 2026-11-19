package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/NekroAI/kitestring/internal/protocol"
)

// R-380 测试用：p1 里一件按 id 推送的素材（带一个变体）和一件旧的按批次推送的条目。
func linkFixture(t *testing.T, s *Store) (file func(sha string) *protocol.AssetFile, push func(items ...protocol.AssetItem)) {
	t.Helper()
	ctx := context.Background()
	file = func(sha string) *protocol.AssetFile {
		f, err := s.CreateFile(ctx, sha+".glb", "model/gltf-binary", sha, 10, "device:x")
		if err != nil {
			t.Fatal(err)
		}
		return &protocol.AssetFile{ID: f.ID, Name: f.Name}
	}
	push = func(items ...protocol.AssetItem) {
		t.Helper()
		if _, err := s.PushAssets(ctx, NewAssetPush{ProjectID: "p1", ByID: true, Items: items}); err != nil {
			t.Fatal(err)
		}
	}
	return file, push
}

func carbine(f *protocol.AssetFile, batches ...string) protocol.AssetItem {
	return protocol.AssetItem{Key: "core:weapon.carbine", Kind: protocol.AssetPart, Category: "weapon", File: f, Batches: batches, Variant: "default",
		Variants: json.RawMessage(`{"default":{},"desert":{}}`), Meta: json.RawMessage(`{"subtype":"rifle"}`)}
}

func desert(f *protocol.AssetFile) protocol.AssetItem {
	return protocol.AssetItem{Key: "core:weapon.carbine@desert", Parent: "core:weapon.carbine", Variant: "desert", Kind: protocol.AssetPart, Category: "weapon", File: f, Meta: json.RawMessage(`{"subtype":"rifle"}`)}
}

// 变体各是一条素材：有自己的版本和审核；素材本身写默认变体名；详情列出变体；变体所属的素材要先在库里。
func TestAssetVariants(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	file, push := linkFixture(t, s)
	push(carbine(file("c1"), "G12"), desert(file("d1")))
	d, err := s.FindAsset(ctx, "p1", "core:weapon.carbine")
	if err != nil {
		t.Fatal(err)
	}
	if d.Variant != "default" || d.Parent != nil || len(d.Variants) != 1 || d.Variants[0].Variant != "desert" || d.Variants[0].UID != "core:weapon.carbine@desert" {
		t.Fatalf("素材本身：%+v 变体 %+v", d.Asset, d.Variants)
	}
	v, err := s.FindAsset(ctx, "p1", "core:weapon.carbine@desert")
	if err != nil {
		t.Fatal(err)
	}
	if v.Variant != "desert" || v.Parent == nil || v.Parent.ID != d.ID || v.Parent.Variant != "default" {
		t.Fatalf("变体：%+v", v.Asset)
	}
	// 只改变体的文件：变体记第 2 版，素材本身不动
	push(carbine(file("c1")), desert(file("d2")))
	d, _ = s.GetAsset(ctx, d.ID)
	v, _ = s.GetAsset(ctx, v.ID)
	if d.Version != 1 || v.Version != 2 {
		t.Fatalf("版本应各自独立：素材 v%d，变体 v%d", d.Version, v.Version)
	}
	// 变体单独审核
	if _, _, err := s.ReviewAsset(ctx, NewAssetReview{Asset: v.ID, Verdict: AssetApproved, Author: AssetAuthor{Kind: SubmitterUser}}); err != nil {
		t.Fatal(err)
	}
	d, _ = s.GetAsset(ctx, d.ID)
	v, _ = s.GetAsset(ctx, v.ID)
	if d.Status != AssetPending || v.Status != AssetApproved || len(v.Reviews) != 1 || len(d.Reviews) != 0 {
		t.Fatalf("审核应各自独立：素材 %s，变体 %s", d.Status, v.Status)
	}
	// 列表里带变体名和上级
	list, _ := s.ListAssets(ctx, AssetFilter{ProjectID: "p1", ID: "core:weapon.*"})
	if len(list) != 2 || list[1].Parent == nil || list[1].Parent.Code != d.Code || list[0].Variant != "default" {
		t.Fatalf("列表：%+v", list)
	}
	// 变体所属的素材不在库里：整批不收
	if _, err := s.PushAssets(ctx, NewAssetPush{ProjectID: "p1", ByID: true, Items: []protocol.AssetItem{{Key: "core:x@a", Parent: "core:x", Variant: "a", Kind: protocol.AssetPart, Category: "weapon", File: file("x")}}}); err == nil {
		t.Fatal("没有所属素材的变体应拒收")
	}
}

// 取代：批量标记、没有变化、取消；找不到、自己取代自己、跨项目、循环都算错，有错时一条都不改；dry run 不改；旧条目可写「批次/文件名」。
func TestSupersedeAssets(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	file, push := linkFixture(t, s)
	if _, err := s.PushAssets(ctx, NewAssetPush{ProjectID: "p1", Batch: "G11e1", Items: []protocol.AssetItem{
		{Key: "carbine", Kind: protocol.AssetPart, Category: "weapon", File: file("o1")},
		{Key: "pistol", Kind: protocol.AssetPart, Category: "weapon", File: file("o2")},
	}}); err != nil {
		t.Fatal(err)
	}
	push(carbine(file("c1")), desert(file("d1")))
	if _, err := s.PushAssets(ctx, NewAssetPush{ProjectID: "p2", ByID: true, Items: []protocol.AssetItem{{Key: "core:other", Kind: protocol.AssetPart, Category: "weapon", File: file("z")}}}); err != nil {
		t.Fatal(err)
	}
	// A-1 G11e1/carbine，A-2 G11e1/pistol，A-3 素材，A-4 变体，A-5 别的项目

	res, ok, err := s.SupersedeAssets(ctx, "p1", []SupersedePair{{"G11e1/carbine", "core:weapon.carbine"}, {"A-2", "core:weapon.carbine@desert"}}, true)
	if err != nil || ok || res[0].Result != "set" || res[1].Result != "set" {
		t.Fatalf("dry run：%+v %v %v", res, ok, err)
	}
	if a, _ := s.GetAsset(ctx, "A-1"); a.SupersededBy != nil {
		t.Fatal("dry run 不应改")
	}
	res, ok, err = s.SupersedeAssets(ctx, "p1", []SupersedePair{{"G11e1/carbine", "core:weapon.carbine"}, {"A-2", "core:weapon.carbine@desert"}}, false)
	if err != nil || !ok {
		t.Fatalf("标记：%+v %v", res, err)
	}
	a1, _ := s.GetAsset(ctx, "A-1")
	if a1.SupersededBy == nil || a1.SupersededBy.UID != "core:weapon.carbine" || a1.SupersededAt == nil {
		t.Fatalf("A-1 应被 core:weapon.carbine 取代：%+v", a1.Asset)
	}
	main, _ := s.GetAsset(ctx, "A-3")
	if len(main.Supersedes) != 1 || main.Supersedes[0].Code != "A-1" {
		t.Fatalf("A-3 应列出取代了 A-1：%+v", main.Supersedes)
	}
	list, _ := s.ListAssets(ctx, AssetFilter{ProjectID: "p1", Batch: "G11e1"})
	if len(list) != 2 || list[1].SupersededBy == nil || list[1].SupersededBy.Code != "A-4" {
		t.Fatalf("列表里的取代关系：%+v", list)
	}
	// 重复标记：没有变化，时间不变
	res, ok, _ = s.SupersedeAssets(ctx, "p1", []SupersedePair{{"A-1", "A-3"}}, false)
	if !ok || res[0].Result != "unchanged" {
		t.Fatalf("重复标记：%+v", res)
	}
	if x, _ := s.GetAsset(ctx, "A-1"); !x.SupersededAt.Equal(*a1.SupersededAt) {
		t.Fatal("没有变化时不应改时间")
	}
	// 有错的一批：一条都不改
	res, ok, _ = s.SupersedeAssets(ctx, "p1", []SupersedePair{
		{"A-2", "A-3"},                 // 本身没问题
		{"A-9", "A-3"},                 // 找不到
		{"A-3", "A-3"},                 // 自己取代自己
		{"A-3", "A-5"},                 // 跨项目
		{"A-3", "A-1"},                 // 循环：A-1 已被 A-3 取代
		{"core:weapon.carbine", "A-2"}, // 重复写 A-3
	}, false)
	want := []string{"set", "error", "error", "error", "error", "error"}
	for i, r := range res {
		if r.Result != want[i] {
			t.Errorf("第 %d 条：%+v，应为 %s", i+1, r, want[i])
		}
	}
	if ok {
		t.Fatal("有错时不应改")
	}
	if x, _ := s.GetAsset(ctx, "A-2"); x.SupersededBy == nil || x.SupersededBy.Code != "A-4" {
		t.Fatal("有错的一批不应改 A-2")
	}
	// 取消
	res, ok, _ = s.SupersedeAssets(ctx, "p1", []SupersedePair{{Old: "A-2"}, {Old: "A-3"}}, false)
	if !ok || res[0].Result != "cleared" || res[1].Result != "unchanged" {
		t.Fatalf("取消：%+v", res)
	}
	if x, _ := s.GetAsset(ctx, "A-2"); x.SupersededBy != nil || x.SupersededAt != nil {
		t.Fatal("取消后不应再有取代关系")
	}
}

// 分享挂在批次上：迁移、取代后照常能看，取代它的素材（和它的变体）也算在里面。
func TestShareKeepsSuperseded(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	file, push := linkFixture(t, s)
	if _, err := s.PushAssets(ctx, NewAssetPush{ProjectID: "p1", Batch: "G11e1", Items: []protocol.AssetItem{{Key: "carbine", Kind: protocol.AssetPart, Category: "weapon", File: file("o1")}}}); err != nil {
		t.Fatal(err)
	}
	push(carbine(file("c1"), "G20"), desert(file("d1"))) // 新 id 不带 G11e1
	items := []ShareItem{{Kind: ShareKindAssetBatch, Ref: AssetScopeRef("p1", "G11e1"), Auto: true}}
	sc, _ := s.ResolveShare(ctx, items)
	if len(sc.Assets) != 1 {
		t.Fatalf("取代前只有旧条目：%v", sc.Assets)
	}
	if _, ok, err := s.SupersedeAssets(ctx, "p1", []SupersedePair{{"A-1", "core:weapon.carbine"}}, false); !ok || err != nil {
		t.Fatal(err)
	}
	sc, _ = s.ResolveShare(ctx, items)
	main, _ := s.FindAsset(ctx, "p1", "core:weapon.carbine")
	old, _ := s.GetAsset(ctx, "A-1")
	if _, ok := sc.Assets[old.ID]; !ok {
		t.Fatal("旧条目应照常能看")
	}
	if _, ok := sc.Assets[main.ID]; !ok {
		t.Fatal("取代它的素材应算在分享里")
	}
	if _, ok := sc.Assets[main.Variants[0].ID]; !ok || len(sc.Assets) != 3 {
		t.Fatalf("取代它的素材的变体也算：%v", sc.Assets)
	}
	// 单独取消取代它的素材：不带回来
	sc, _ = s.ResolveShare(ctx, append(items, ShareItem{Kind: ShareKindAsset, Ref: main.ID, Excluded: true}))
	if _, ok := sc.Assets[main.ID]; ok {
		t.Fatal("单独取消的不应带回来")
	}
}

// 删除：检查列出审核记录、变体、被它取代的条目、引用着它的分享；删后连同变体一起没了，取代标记清掉，单独挂着的分享项删掉。
func TestDeleteAsset(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	file, push := linkFixture(t, s)
	if _, err := s.PushAssets(ctx, NewAssetPush{ProjectID: "p1", Batch: "G11e1", Items: []protocol.AssetItem{{Key: "carbine", Kind: protocol.AssetPart, Category: "weapon", File: file("o1")}}}); err != nil {
		t.Fatal(err)
	}
	push(carbine(file("c1"), "G20"), desert(file("d1")))
	main, _ := s.FindAsset(ctx, "p1", "core:weapon.carbine")
	if _, _, err := s.ReviewAsset(ctx, NewAssetReview{Asset: main.Variants[0].ID, Verdict: AssetRework, Comment: "颜色太浅", Author: AssetAuthor{Kind: SubmitterUser}}); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.SupersedeAssets(ctx, "p1", []SupersedePair{{"A-1", "A-2"}}, false); !ok {
		t.Fatal("标记取代")
	}
	shA, err := s.CreateShare(ctx, Share{Name: "外包看枪"}, []ShareItem{{Kind: ShareKindAsset, Ref: main.ID}, {Kind: ShareKindAsset, Ref: main.Variants[0].ID}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateShare(ctx, Share{Name: "武器类"}, []ShareItem{{Kind: ShareKindAssetCat, Ref: AssetScopeRef("p1", "weapon"), Auto: true}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateShare(ctx, Share{Name: "别的批次"}, []ShareItem{{Kind: ShareKindAssetBatch, Ref: AssetScopeRef("p1", "G99"), Auto: true}}); err != nil {
		t.Fatal(err)
	}

	info, err := s.AssetDeleteCheck(ctx, "core:weapon.carbine")
	if err != nil {
		t.Fatal(err)
	}
	if info.Asset.Code != "A-2" || info.Versions != 2 || info.Reviews != 1 || len(info.Variants) != 1 || len(info.Supersedes) != 1 || info.Supersedes[0].Code != "A-1" {
		t.Fatalf("删除检查：%+v", info)
	}
	if len(info.Shares) != 2 || info.Shares[0].Name != "外包看枪" || info.Shares[0].Via != ShareKindAsset || info.Shares[1].Via != ShareKindAssetCat || info.Shares[1].Ref != "weapon" {
		t.Fatalf("引用着它的分享（单独挂着的只算一次，别的批次不算）：%+v", info.Shares)
	}

	gone, err := s.DeleteAsset(ctx, main.ID)
	if err != nil || len(gone) != 2 {
		t.Fatalf("删除：%v %v", gone, err)
	}
	for _, ref := range []string{"A-2", "A-3", "core:weapon.carbine@desert"} {
		if _, err := s.GetAsset(ctx, ref); err == nil {
			t.Fatalf("%s 应已删除", ref)
		}
	}
	if old, _ := s.GetAsset(ctx, "A-1"); old.SupersededBy != nil {
		t.Fatal("被它取代的旧条目应取消取代标记")
	}
	if items, _ := s.shareItems(ctx, shA.ID); len(items) != 0 {
		t.Fatalf("单独挂着它的分享项应删掉：%+v", items)
	}
	var n int
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM asset_versions WHERE asset_id IN (?, ?)`, main.ID, main.Variants[0].ID).Scan(&n)
	if n != 0 {
		t.Fatal("版本应一起删掉")
	}
}

// 老库升级：去掉 R-380 的新列后重开，补上列，旧数据照常读出。
func TestAssetLinksMigrate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ks.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	f, _ := s.CreateFile(ctx, "a.glb", "model/gltf-binary", "a", 10, "device:x")
	if _, err := s.PushAssets(ctx, NewAssetPush{ProjectID: "p1", Batch: "G1", Items: []protocol.AssetItem{{Key: "a", Kind: protocol.AssetPart, File: &protocol.AssetFile{ID: f.ID}}}}); err != nil {
		t.Fatal(err)
	}
	for _, ix := range []string{"assets_parent", "assets_superseded"} {
		if _, err := s.db.ExecContext(ctx, `DROP INDEX `+ix); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []string{"variant", "parent_id", "superseded_by", "superseded_at"} {
		if _, err := s.db.ExecContext(ctx, `ALTER TABLE assets DROP COLUMN `+c); err != nil {
			t.Fatal(err)
		}
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	d, err := s.GetAsset(ctx, "A-1")
	if err != nil || d.Variant != "" || d.SupersededBy != nil || len(d.Variants) != 0 {
		t.Fatalf("升级后读旧数据：%+v %v", d.Asset, err)
	}
	if _, ok, err := s.SupersedeAssets(ctx, "p1", []SupersedePair{{Old: "A-1"}}, false); !ok || err != nil {
		t.Fatalf("升级后能写新列：%v", err)
	}
}
