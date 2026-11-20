package store

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/NekroAI/kitestring/internal/protocol"
)

// R-411：类别 / 细类中文名按项目存最新一份；搜索按它们找；汇总带上；换一份整份替换；空表清掉。
func TestAssetCategoryNames(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	f, err := s.CreateFile(ctx, "a.glb", "model/gltf-binary", "n1", 10, "device:x")
	if err != nil {
		t.Fatal(err)
	}
	file := protocol.AssetFile{ID: f.ID, Name: "a.glb"}
	item := func(key, cat, meta string) protocol.AssetItem {
		return protocol.AssetItem{Key: key, Kind: protocol.AssetPart, Category: cat, File: &file, Meta: json.RawMessage(meta)}
	}
	if _, err := s.PushAssets(ctx, NewAssetPush{ProjectID: "p1", ProjectName: "rollcall", ByID: true, Submitter: AssetSubmitter{Kind: SubmitterSession, Ref: "claude:a"},
		Items: []protocol.AssetItem{item("core:prop.a", "prop", `{"subtype":"crate"}`), item("core:vehicle.b", "vehicle", `{"subtype":"car"}`), item("core:vehicle.c", "vehicle", `{}`)}}); err != nil {
		t.Fatal(err)
	}
	count := func(q string) int {
		t.Helper()
		n, err := s.CountAssets(ctx, AssetFilter{ProjectID: "p1", Q: q})
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	if count("道具") != 0 {
		t.Fatal("没存中文名时搜不到")
	}
	names := map[string]protocol.AssetCategoryName{"prop": {Name: "道具", Subtypes: map[string]string{"crate": "箱子"}}, "vehicle": {Subtypes: map[string]string{"car": "汽车"}}}
	if err := s.SetAssetCategoryNames(ctx, "p1", names); err != nil {
		t.Fatal(err)
	}
	for q, want := range map[string]int{"道具": 1, "箱子": 1, "汽车": 1, "prop": 1, "vehicle": 2, "不存在": 0} {
		if got := count(q); got != want {
			t.Errorf("搜「%s」应 %d 件，得 %d", q, want, got)
		}
	}
	f1, err := s.ListAssetFacets(ctx, "p1")
	if err != nil || f1.CategoryNames["prop"].Name != "道具" || f1.CategoryNames["vehicle"].Subtypes["car"] != "汽车" {
		t.Fatalf("facets 带上中文名：%+v %v", f1.CategoryNames, err)
	}
	// 另一个项目不受影响
	if f2, _ := s.ListAssetFacets(ctx, "p2"); len(f2.CategoryNames) != 0 {
		t.Errorf("别的项目不该有：%+v", f2.CategoryNames)
	}
	// 整份替换；空表清掉
	if err := s.SetAssetCategoryNames(ctx, "p1", map[string]protocol.AssetCategoryName{"prop": {Name: "杂物"}}); err != nil {
		t.Fatal(err)
	}
	if count("道具") != 0 || count("杂物") != 1 || count("箱子") != 0 {
		t.Error("重存应整份替换")
	}
	if err := s.SetAssetCategoryNames(ctx, "p1", nil); err != nil {
		t.Fatal(err)
	}
	if f3, _ := s.ListAssetFacets(ctx, "p1"); len(f3.CategoryNames) != 0 || count("杂物") != 0 {
		t.Error("空表应清掉")
	}
}
