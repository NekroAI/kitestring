package assetpush

import (
	"path/filepath"
	"strings"
	"testing"
)

// R-400：display.name 可选——写了存成 Name（不进 meta）、变体沿用、报告里带上；不写照旧；写错的逐件报错。
func TestDisplayName(t *testing.T) {
	long := strings.Repeat("长", 65)
	dir := fixture(t, `{"assets": [
	  {"id": "core:weapon.carbine", "category": "weapon", "file": "carbine.glb", "display": {"name": " 卡宾枪 "}, "lod": 2,
	   "variants": {"default": {"file": "carbine.glb"}, "desert": {"file": "carbine.glb"}}},
	  {"id": "core:weapon.pistol", "category": "weapon", "file": "pistol.glb"},
	  {"id": "core:weapon.bad1", "category": "weapon", "file": "pistol.glb", "display": {"name": 3}},
	  {"id": "core:weapon.bad2", "category": "weapon", "file": "pistol.glb", "display": {"name": "两\n行"}},
	  {"id": "core:weapon.bad3", "category": "weapon", "file": "pistol.glb", "display": {"name": "`+long+`"}},
	  {"id": "core:weapon.bad4", "category": "weapon", "file": "pistol.glb", "display": "卡宾枪"}
	]}`, "carbine.glb", "pistol.glb")
	p, rep, err := BuildByID(IDOptions{Paths: []string{filepath.Join(dir, "asset.json")}, SkipInvalid: true})
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]string{}
	for _, it := range p.Items {
		by[it.Key] = it.Name
		if strings.Contains(string(it.Meta), "display") {
			t.Errorf("display 不应进 meta：%s", it.Meta)
		}
	}
	if len(p.Items) != 3 || by["core:weapon.carbine"] != "卡宾枪" || by["core:weapon.carbine@desert"] != "卡宾枪" || by["core:weapon.pistol"] != "" {
		t.Fatalf("中文名：%v", by)
	}
	bad := 0
	for _, c := range rep.Items {
		if strings.Contains(c.ID, "bad") {
			bad++
			if len(c.Errors) == 0 {
				t.Errorf("%s 应报错", c.ID)
			}
		}
	}
	if bad != 4 {
		t.Fatalf("4 件写错的应都报错：%d", bad)
	}
	if text := ReportText(rep); !strings.Contains(text, "「卡宾枪」") {
		t.Errorf("报告里应带中文名：\n%s", text)
	}
}
