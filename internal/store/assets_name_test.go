package store

import (
	"context"
	"testing"

	"github.com/NekroAI/kitestring/internal/protocol"
)

// R-400：素材中文名随版本存下；改名记新版本；按关键字（id 或中文名）找；不带中文名的照旧。
func TestAssetDisplayName(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	f, err := s.CreateFile(ctx, "a.glb", "model/gltf-binary", "n1", 10, "device:x")
	if err != nil {
		t.Fatal(err)
	}
	g, err := s.CreateFile(ctx, "b.glb", "model/gltf-binary", "n2", 10, "device:x")
	if err != nil {
		t.Fatal(err)
	}
	push := func(key, name string, file protocol.AssetFile) protocol.AssetPushResult {
		t.Helper()
		r, err := s.PushAssets(ctx, NewAssetPush{ProjectID: "p1", ProjectName: "rollcall", ByID: true, Submitter: AssetSubmitter{Kind: SubmitterSession, Ref: "claude:a"},
			Items: []protocol.AssetItem{{Key: key, Kind: protocol.AssetPart, Category: "weapon", Name: name, File: &file}}})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	fa, fb := protocol.AssetFile{ID: f.ID, Name: "a.glb"}, protocol.AssetFile{ID: g.ID, Name: "b.glb"}
	push("core:weapon.carbine", "卡宾枪", fa)
	push("core:weapon.pistol", "", fb)
	if r := push("core:weapon.carbine", "卡宾枪", fa); r.Items[0].Result != "unchanged" {
		t.Fatalf("同名重推不应新增版本：%+v", r.Items)
	}
	if r := push("core:weapon.carbine", "突击卡宾枪", fa); r.Items[0].Result != "updated" || r.Items[0].Version != 2 {
		t.Fatalf("改中文名应记新版本：%+v", r.Items)
	}
	d, err := s.FindAsset(ctx, "p1", "core:weapon.carbine")
	if err != nil || d.Versions[0].Name != "突击卡宾枪" || d.Versions[1].Name != "卡宾枪" || d.Latest.Name != "突击卡宾枪" {
		t.Fatalf("版本里的中文名：%+v %v", d.Versions, err)
	}
	for q, want := range map[string]int{"卡宾": 1, "突击": 1, "CARBINE": 1, "pistol": 1, "weapon.": 2, "枪": 1, "不存在": 0} {
		list, err := s.ListAssets(ctx, AssetFilter{ProjectID: "p1", Q: q})
		n, err2 := s.CountAssets(ctx, AssetFilter{ProjectID: "p1", Q: q})
		if err != nil || err2 != nil || len(list) != want || n != want {
			t.Errorf("搜「%s」应 %d 件：%d / %d %v %v", q, want, len(list), n, err, err2)
		}
	}
}
