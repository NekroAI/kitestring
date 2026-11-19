package store

import (
	"context"
	"errors"
	"testing"

	"github.com/NekroAI/kitestring/internal/protocol"
)

// R-351：同一素材（项目 + 批次 + key）内容有变的重推记新版本、旧版本保留；内容相同不新增；同一 push_id 重放原样返回。
func TestPushAssetsVersions(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	file := func(name, mime, sha string) protocol.AssetFile {
		f, err := s.CreateFile(ctx, name, mime, sha, 10, "device:x")
		if err != nil {
			t.Fatal(err)
		}
		return protocol.AssetFile{ID: f.ID, Name: name}
	}
	glb1, glb2 := file("w.glb", "model/gltf-binary", "g1"), file("w.glb", "model/gltf-binary", "g2")
	prev := file("w_closeup.png", "image/png", "p1")
	cat := file("cat.png", "image/png", "c1")
	part := func(g protocol.AssetFile) protocol.AssetItem {
		return protocol.AssetItem{Key: "wheel", Kind: protocol.AssetPart, Category: "车轮", File: &g, Previews: []protocol.AssetFile{prev}, Triangles: 1496, Budget: 1500, Size: []float64{0.3, 0.76, 0.76}, Meta: []byte(`{"id":"wheel"}`)}
	}
	push := func(id string, items ...protocol.AssetItem) protocol.AssetPushResult {
		t.Helper()
		r, err := s.PushAssets(ctx, NewAssetPush{PushID: id, ProjectID: "p1", ProjectName: "rollcall", SessionKey: "claude:a", Batch: "G11e1", Branch: "main", Commit: "abc1234", Items: items})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	r := push("j1", part(glb1), protocol.AssetItem{Key: "g11e1_catalog", Kind: protocol.AssetImage, Category: "目录图", File: &cat})
	if len(r.Items) != 2 || r.Items[0].Result != "created" || r.Items[0].Code != "A-1" || r.Items[1].Code != "A-2" {
		t.Fatalf("首推应建两条素材：%+v", r.Items)
	}
	if again := push("j1", part(glb2)); again.Items[0].Version != 1 || len(again.Items) != 2 {
		t.Fatalf("同一 push_id 重放应原样返回第一次的结果：%+v", again.Items)
	}
	if r := push("j2", part(glb1)); r.Items[0].Result != "unchanged" || r.Items[0].Version != 1 {
		t.Fatalf("内容相同不应新增版本：%+v", r.Items)
	}
	a, _ := s.GetAsset(ctx, "A-1")
	if err := s.SetAssetStatus(ctx, a.ID, AssetPending, AssetRework); err != nil {
		t.Fatal(err)
	}
	if r := push("j3", part(glb2)); r.Items[0].Result != "updated" || r.Items[0].Version != 2 {
		t.Fatalf("内容有变应记为第 2 版：%+v", r.Items)
	}
	d, err := s.GetAsset(ctx, "a-1")
	if err != nil {
		t.Fatal(err)
	}
	if d.Status != AssetPending || len(d.Versions) != 2 || d.Versions[0].File.ID != glb2.ID || d.Versions[1].File.ID != glb1.ID {
		t.Fatalf("返工后重推应回到待审，两版都在、新的在前：%+v", d)
	}
	if v := d.Versions[1]; v.Triangles != 1496 || len(v.Size) != 3 || len(v.Previews) != 1 || v.Commit != "abc1234" || string(v.Meta) != `{"id":"wheel"}` {
		t.Fatalf("版本字段不对：%+v", v)
	}
	// 别的批次同名 key 是另一条素材
	r2, err := s.PushAssets(ctx, NewAssetPush{ProjectID: "p1", Batch: "G11e2", Items: []protocol.AssetItem{part(glb1)}})
	if err != nil || r2.Items[0].Code != "A-3" {
		t.Fatalf("跨批次同名应是新素材：%+v %v", r2.Items, err)
	}
	list, err := s.ListAssets(ctx, AssetFilter{ProjectID: "p1", Batch: "G11e1"})
	if err != nil || len(list) != 2 || list[0].Latest == nil || list[0].Latest.Version != 2 {
		t.Fatalf("按批次列出：%+v %v", list, err)
	}
	if l, _ := s.ListAssets(ctx, AssetFilter{ProjectID: "p1", Kind: protocol.AssetImage}); len(l) != 1 || l[0].Key != "g11e1_catalog" {
		t.Fatalf("按类型筛选：%+v", l)
	}
	f, err := s.ListAssetFacets(ctx, "p1")
	if err != nil || len(f.Batches) != 2 || len(f.Categories) != 2 || f.Counts[AssetPending] != 3 {
		t.Fatalf("筛选项：%+v %v", f, err)
	}
	if _, err := s.PushAssets(ctx, NewAssetPush{ProjectID: "p1", Batch: "G11e1", Status: AssetApproved, Items: []protocol.AssetItem{part(glb1)}}); err == nil {
		t.Fatal("推送不能直接设为通过")
	}
}

func TestAssetMoves(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	f, _ := s.CreateFile(ctx, "a.png", "image/png", "x", 1, "device:x")
	r, err := s.PushAssets(ctx, NewAssetPush{ProjectID: "p", Batch: "B", Status: AssetWIP, Items: []protocol.AssetItem{{Key: "a", Kind: protocol.AssetImage, File: &protocol.AssetFile{ID: f.ID}}}})
	if err != nil {
		t.Fatal(err)
	}
	a, _ := s.GetAsset(ctx, r.Items[0].Code)
	if a.Status != AssetWIP {
		t.Fatalf("指定制作中：%s", a.Status)
	}
	if err := s.SetAssetStatus(ctx, a.ID, "", AssetApproved); !errors.Is(err, ErrAssetMove) {
		t.Fatalf("制作中不能直接通过：%v", err)
	}
	for _, step := range [][2]string{{AssetWIP, AssetPending}, {AssetPending, AssetApproved}, {AssetApproved, AssetRework}} {
		if err := s.SetAssetStatus(ctx, a.ID, step[0], step[1]); err != nil {
			t.Fatalf("%v：%v", step, err)
		}
	}
	if err := s.SetAssetStatus(ctx, a.ID, AssetPending, AssetApproved); !errors.Is(err, ErrAssetMove) {
		t.Fatalf("当前状态不符应拒绝：%v", err)
	}
	if _, err := s.GetAsset(ctx, "A-99"); !errors.Is(err, ErrNotFound) {
		t.Fatal("不存在的编号")
	}
}
