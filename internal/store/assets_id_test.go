package store

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/NekroAI/kitestring/internal/protocol"
)

// R-376：按素材 id 推送——id 为主键、批次是多值元数据（内容没变也并入）、来源许可 / 变体 / 项目字段存下、
// 预览图带机位；与旧的按批次推送的条目共存；按 id / 前缀 / 批次查；分享挂在批次上时包含带这个批次的 id 素材。
func TestPushAssetsByID(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	file := func(name, mime, sha string) protocol.AssetFile {
		f, err := s.CreateFile(ctx, name, mime, sha, 10, "device:x")
		if err != nil {
			t.Fatal(err)
		}
		return protocol.AssetFile{ID: f.ID, Name: name}
	}
	glb1, glb2 := file("carbine.glb", "model/gltf-binary", "c1"), file("carbine.glb", "model/gltf-binary", "c2")
	front, side := file("weapon.carbine__front.png", "image/png", "pf"), file("weapon.carbine__side.png", "image/png", "ps")
	front.View, side.View = "front", "side"
	item := func(g protocol.AssetFile, batches ...string) protocol.AssetItem {
		return protocol.AssetItem{Key: "core:weapon.carbine", Kind: protocol.AssetPart, Category: "weapon", File: &g, Previews: []protocol.AssetFile{front, side},
			Batches: batches, Source: "自制", License: "CC0", Variants: json.RawMessage(`[{"name":"desert"}]`), Meta: json.RawMessage(`{"lod":2}`)}
	}
	push := func(items ...protocol.AssetItem) protocol.AssetPushResult {
		t.Helper()
		r, err := s.PushAssets(ctx, NewAssetPush{ProjectID: "p1", ProjectName: "rollcall", ByID: true, Batch: "ignored", Submitter: AssetSubmitter{Kind: SubmitterSession, Ref: "claude:a"}, Items: items})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	// 旧的按批次推送的条目照常
	if _, err := s.PushAssets(ctx, NewAssetPush{ProjectID: "p1", Batch: "G11e1", Items: []protocol.AssetItem{{Key: "carbine", Kind: protocol.AssetPart, Category: "weapon", File: &glb1}}}); err != nil {
		t.Fatal(err)
	}
	r := push(item(glb1, "G11e1"))
	if !r.ByID || r.Items[0].Result != "created" || r.Items[0].Code != "A-2" {
		t.Fatalf("按 id 首推应是新素材 A-2（不和旧条目混）：%+v", r)
	}
	// 内容没变、带了新批次：不新增版本，批次并入
	if r := push(item(glb1, "G12")); r.Items[0].Result != "unchanged" || r.Items[0].Version != 1 {
		t.Fatalf("内容没变不应新增版本：%+v", r.Items)
	}
	d, err := s.FindAsset(ctx, "p1", "core:weapon.carbine")
	if err != nil {
		t.Fatal(err)
	}
	if d.UID != "core:weapon.carbine" || d.Batch != "" || !slices.Equal(d.Batches, []string{"G11e1", "G12"}) {
		t.Fatalf("id 与批次：%+v", d.Asset)
	}
	v := d.Versions[0]
	if v.Source != "自制" || v.License != "CC0" || string(v.Variants) != `[{"name":"desert"}]` || string(v.Meta) != `{"lod":2}` || len(v.Previews) != 2 || v.Previews[0].View != "front" || v.Previews[1].View != "side" {
		t.Fatalf("版本字段：%+v", v)
	}
	// 内容变了：新版本，批次照样并入
	if r := push(item(glb2, "G13")); r.Items[0].Result != "updated" || r.Items[0].Version != 2 {
		t.Fatalf("内容有变应记第 2 版：%+v", r.Items)
	}
	// 只改机位名也算内容有变
	it := item(glb2)
	it.Previews = []protocol.AssetFile{front, {ID: side.ID, Name: side.Name, View: "back"}}
	if r := push(it); r.Items[0].Version != 3 {
		t.Fatalf("机位改名应记新版本：%+v", r.Items)
	}

	// 查询：按 id、前缀、编号、批次（含多值批次）、状态
	for _, c := range []struct {
		f    AssetFilter
		want []string
	}{
		{AssetFilter{ProjectID: "p1", ID: "core:weapon.carbine"}, []string{"A-2"}},
		{AssetFilter{ProjectID: "p1", ID: "core:weapon.*"}, []string{"A-2"}},
		{AssetFilter{ProjectID: "p1", ID: "core:prop.*"}, nil},
		{AssetFilter{ProjectID: "p1", ID: "A-1"}, []string{"A-1"}},
		{AssetFilter{ProjectID: "p1", Batch: "G11e1"}, []string{"A-1", "A-2"}},
		{AssetFilter{ProjectID: "p1", Batch: "G13"}, []string{"A-2"}},
		{AssetFilter{ProjectID: "p1", Batch: "G13", Status: AssetApproved}, nil},
		{AssetFilter{ProjectID: "p1", Category: "weapon", Status: AssetPending}, []string{"A-1", "A-2"}},
	} {
		l, err := s.ListAssets(ctx, c.f)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, a := range l {
			got = append(got, a.Code)
		}
		slices.Sort(got)
		if !slices.Equal(got, c.want) {
			t.Errorf("%+v：%v，应为 %v", c.f, got, c.want)
		}
	}
	f, _ := s.ListAssetFacets(ctx, "p1")
	if !slices.Equal(f.Batches, []string{"G11e1", "G12", "G13"}) {
		t.Fatalf("筛选项的批次应含多值批次：%v", f.Batches)
	}
	old, _ := s.GetAsset(ctx, "A-1")
	if old.UID != "" || !slices.Equal(old.Batches, []string{"G11e1"}) || !slices.Equal(old.Latest.Batches, []string{"G11e1"}) {
		t.Fatalf("旧条目的批次应是 [batch]：%+v", old.Asset)
	}

	// 别的项目有同一个 id：不指定项目时报不明确，指定后能找到
	if _, err := s.PushAssets(ctx, NewAssetPush{ProjectID: "p2", ByID: true, Items: []protocol.AssetItem{item(glb1)}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetAsset(ctx, "core:weapon.carbine"); !errors.Is(err, ErrAssetAmbiguous) {
		t.Fatalf("两个项目都有时应报不明确：%v", err)
	}
	other, err := s.FindAsset(ctx, "p2", "core:weapon.carbine")
	if err != nil || other.ProjectID != "p2" {
		t.Fatalf("指定项目：%v", err)
	}
	if _, err := s.FindAsset(ctx, "p1", "core:nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("没有的 id：%v", err)
	}

	// 分享挂在批次上（R-365）：带这个批次的 id 素材也算在里面
	sc, err := s.ResolveShare(ctx, []ShareItem{{Kind: ShareKindAssetBatch, Ref: AssetScopeRef("p1", "G12"), Auto: true}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := sc.Assets[other.ID]; ok {
		t.Fatal("别的项目的素材不应算进来")
	}
	got, _ := s.FindAsset(ctx, "p1", "core:weapon.carbine")
	if _, ok := sc.Assets[got.ID]; !ok || len(sc.Assets) != 1 {
		t.Fatalf("批次 G12 的分享应含 id 素材：%v", sc.Assets)
	}
	if _, err := s.NormShareItems(ctx, []ShareItem{{Kind: ShareKindAssetBatch, Ref: AssetScopeRef("p1", "G13")}}); err != nil {
		t.Fatalf("只出现在多值批次里的批次也能挂分享：%v", err)
	}
}

// R-394：细类取项目字段里的 subtype，列表和详情的最新版本都带上；没写或不是字符串时为空。
func TestAssetSubtype(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	f, err := s.CreateFile(ctx, "a.png", "image/png", "s1", 10, "device:x")
	if err != nil {
		t.Fatal(err)
	}
	file := protocol.AssetFile{ID: f.ID, Name: "a.png"}
	items := []protocol.AssetItem{
		{Key: "core:concept.cargo_mule", Kind: protocol.AssetImage, Category: "concept", File: &file, Meta: json.RawMessage(`{"subtype":" vehicle ","generation":{"method":"external"}}`)},
		{Key: "core:concept.hats", Kind: protocol.AssetImage, Category: "concept", File: &file, Meta: json.RawMessage(`{"subtype":3}`)},
		{Key: "core:ui.icon", Kind: protocol.AssetImage, Category: "ui", File: &file},
	}
	if _, err := s.PushAssets(ctx, NewAssetPush{ProjectID: "p1", ByID: true, Items: items}); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListAssets(ctx, AssetFilter{ProjectID: "p1"})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, a := range list {
		got[a.UID] = a.Latest.Subtype
	}
	if got["core:concept.cargo_mule"] != "vehicle" || got["core:concept.hats"] != "" || got["core:ui.icon"] != "" {
		t.Fatalf("细类：%v", got)
	}
	d, err := s.FindAsset(ctx, "p1", "core:concept.cargo_mule")
	if err != nil || d.Latest.Subtype != "vehicle" {
		t.Fatalf("详情的细类：%+v %v", d.Latest, err)
	}
}
