package assetpush

import (
	"path/filepath"
	"slices"
	"testing"
)

// R-380：非默认变体各推一条（排在素材后面），带自己的文件和预览图；预览图也能按「<名字>@<变体名>__<机位>.png」归属；
// 变体有错时报在变体上；所属的素材有错时变体也不推。
func TestBuildVariants(t *testing.T) {
	dir := fixture(t, `{"assets": [
	    {"id": "core:weapon.carbine", "category": "weapon", "batches": ["G20"], "license": "CC0", "lod": 2,
	     "default_variant": "base",
	     "variants": {"base": {"files": {"model": "carbine.glb"}}, "desert": {"file": "carbine_desert.glb", "previews": [{"path": "shots/desert.png", "view": "front"}], "license": "自有"},
	                  "snow": {"file": "carbine_snow.glb"}, "bad name": {"file": "carbine.glb"}, "ghost": {"file": "nope.glb"}}},
	    {"id": "core:weapon.pistol", "category": "weapon", "variants": [{"name": "default", "file": "pistol.glb"}, {"name": "gold", "file": "pistol_gold.glb"}]},
	    {"id": "core:weapon.broken", "category": "weapon", "file": "missing.glb", "variants": {"default": {}, "x": {"file": "pistol.glb"}}}
	  ]}`,
		"carbine.glb", "carbine_desert.glb", "carbine_snow.glb", "pistol.glb", "pistol_gold.glb", "shots/desert.png", "previews/carbine@snow__side.png")
	p, rep, err := BuildByID(IDOptions{Paths: []string{filepath.Join(dir, "asset.json")}, SkipInvalid: true})
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, it := range p.Items {
		keys = append(keys, it.Key)
	}
	want := []string{"core:weapon.carbine", "core:weapon.carbine@desert", "core:weapon.carbine@snow", "core:weapon.pistol", "core:weapon.pistol@gold"}
	if !slices.Equal(keys, want) {
		t.Fatalf("推送条目：%v，应为 %v\n%s", keys, want, ReportText(rep))
	}
	main, desert, snow := p.Items[0], p.Items[1], p.Items[2]
	if main.Variant != "base" || main.Parent != "" || filepath.Base(main.File.Path) != "carbine.glb" {
		t.Fatalf("素材本身是默认变体：%+v", main)
	}
	if desert.Parent != "core:weapon.carbine" || desert.Variant != "desert" || desert.License != "自有" || desert.Category != "weapon" || !slices.Equal(desert.Batches, []string{"G20"}) || string(desert.Meta) != string(main.Meta) {
		t.Fatalf("变体沿用素材的类别、批次、项目字段，许可可覆盖：%+v", desert)
	}
	if len(desert.Previews) != 1 || desert.Previews[0].View != "front" || len(snow.Previews) != 1 || snow.Previews[0].View != "side" || snow.License != "CC0" {
		t.Fatalf("变体的预览图：desert %+v，snow %+v", desert.Previews, snow.Previews)
	}
	errs := map[string][]string{}
	for _, c := range rep.Items {
		errs[c.ID] = c.Errors
	}
	if len(errs["core:weapon.carbine@ghost"]) == 0 || len(errs["core:weapon.carbine 的第 1 个变体"]) == 0 {
		t.Fatalf("有错的变体要报出来：%v", errs)
	}
	if len(errs["core:weapon.broken"]) == 0 || len(errs["core:weapon.broken@x"]) == 0 {
		t.Fatalf("所属素材有错时变体也不推：%v", errs)
	}
}

func TestParseSupersedeMap(t *testing.T) {
	for _, c := range []struct {
		in   string
		want []SupersedePair
		bad  bool
	}{
		{in: "# 迁移\nG11e1/carbine  core:weapon.carbine\nA-12\tcore:weapon.carbine@desert\nA-13 → core:x\n", want: []SupersedePair{{"G11e1/carbine", "core:weapon.carbine"}, {"A-12", "core:weapon.carbine@desert"}, {"A-13", "core:x"}}},
		{in: `{"b": "core:b", "a": "core:a"}`, want: []SupersedePair{{"b", "core:b"}, {"a", "core:a"}}},
		{in: `[{"old": "A-1", "new": "core:a"}]`, want: []SupersedePair{{"A-1", "core:a"}}},
		{in: "A-1\n", bad: true},
		{in: `[{"old": "A-1"}]`, bad: true},
		{in: "  ", bad: true},
	} {
		got, err := ParseSupersedeMap(c.in)
		if c.bad != (err != nil) || !slices.Equal(got, c.want) {
			t.Errorf("%q：%v %v", c.in, got, err)
		}
	}
}
