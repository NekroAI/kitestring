package assetpush

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/NekroAI/kitestring/internal/protocol"
)

// fixture 在临时目录里摆好 asset.json 和文件：files 是相对路径 → 内容（.png 用样例图）。
func fixture(t *testing.T, assetJSON string, files ...string) string {
	t.Helper()
	dir := t.TempDir()
	png, err := os.ReadFile("testdata/sample/previews/wheel_hero.png")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		p := filepath.Join(dir, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		b := []byte("glTF-fake")
		if strings.HasSuffix(f, ".png") {
			b = png
		}
		if err := os.WriteFile(p, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "asset.json"), []byte(assetJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// R-376：asset.json 按 id 推送——通用字段、批次合并、项目字段进 meta、预览图按「<名字>__<机位>」或子目录归属、挂不上的逐个列出。
func TestBuildByID(t *testing.T) {
	dir := fixture(t, `{
	  "namespace": "core",
	  "batches": ["G12"],
	  "assets": [
	    {"id": "core:weapon.carbine", "category": "weapon", "file": "weapon/carbine.glb", "batches": ["G11e1"], "source": "自制", "license": "CC0",
	     "variants": [{"name": "desert"}], "triangles": 1200, "budget": 1500, "size": [0.7, 0.2, 0.1], "lod": 2, "faction": "red"},
	    {"id": "prop/crate", "category": "prop", "file": "prop/crate.glb", "previews": ["renders/crate"]},
	    {"id": "core:ui.icon_carbine", "category": "icon", "file": "ui/icon_carbine.png"},
	    {"id": "core:weapon.pistol", "category": "weapon", "file": "weapon/pistol.glb"}
	  ]}`,
		"weapon/carbine.glb", "prop/crate.glb", "ui/icon_carbine.png", "weapon/pistol.glb",
		"previews/weapon.carbine__front.png", "previews/weapon.carbine__side.png",
		"previews/weapon.pistol/top.png",
		"renders/crate/crate__front.png", "renders/crate/hero.png",
		"previews/carbine_front.png", "previews/rifle__front.png", "previews/ui.icon_carbine__front.png")
	if !IsAssetJSON(filepath.Join(dir, "asset.json")) {
		t.Fatal("asset.json 应认作按 id 推送")
	}
	p, rep, err := BuildByID(IDOptions{Paths: []string{filepath.Join(dir, "asset.json")}, Batch: "G13", Branch: "main", Commit: "abc"})
	if err != nil {
		t.Fatal(err)
	}
	if !p.ByID || rep.Bad() != 0 || len(p.Items) != 4 {
		t.Fatalf("应有 4 件合格素材：%+v\n%s", p, ReportText(rep))
	}
	by := map[string]protocol.AssetItem{}
	for _, it := range p.Items {
		by[it.Key] = it
	}
	c := by["core:weapon.carbine"]
	if c.Kind != protocol.AssetPart || c.Source != "自制" || c.License != "CC0" || string(c.Variants) != `[{"name": "desert"}]` || c.Triangles != 1200 || c.Budget != 1500 || len(c.Size) != 3 {
		t.Fatalf("通用字段：%+v", c)
	}
	if !slices.Equal(c.Batches, []string{"G12", "G11e1", "G13"}) {
		t.Fatalf("批次应合并顶层、--batch、本件：%v", c.Batches)
	}
	if string(c.Meta) != `{"faction":"red","lod":2}` {
		t.Fatalf("meta 只放项目特有字段：%s", c.Meta)
	}
	if len(c.Previews) != 2 || c.Previews[0].View != "front" || c.Previews[1].View != "side" {
		t.Fatalf("按文件名归属的预览图与机位：%+v", c.Previews)
	}
	if cr := by["core:prop/crate"]; len(cr.Previews) != 2 || cr.Previews[0].View != "front" || cr.Previews[1].View != "hero" {
		t.Fatalf("命名空间补上、previews 写目录时目录里的图都归它：%+v", cr)
	}
	if pi := by["core:weapon.pistol"]; len(pi.Previews) != 1 || pi.Previews[0].View != "top" {
		t.Fatalf("以名字命名的子目录：%+v", pi.Previews)
	}
	if ic := by["core:ui.icon_carbine"]; ic.Kind != protocol.AssetImage || len(ic.Pixels) != 2 {
		t.Fatalf("图片素材：%+v", ic)
	}
	strays := map[string]string{}
	for _, s := range rep.Strays {
		strays[s.Path] = s.Reason
	}
	if len(strays) != 3 || !strings.Contains(strays["previews/carbine_front.png"], "<名字>__<机位>") ||
		!strings.Contains(strays["previews/rifle__front.png"], "没有名字是「rifle」") || !strings.Contains(strays["previews/ui.icon_carbine__front.png"], "不挂预览图") {
		t.Fatalf("挂不上的预览图应逐个列出：%v", strays)
	}
	text := ReportText(rep)
	for _, want := range []string{"4 件素材，可推送 4 件", "core:weapon.carbine", "预览图 2 个机位（front、side）", "挂不上素材的预览图 3 张", "只校验非空和格式"} {
		if !strings.Contains(text, want) {
			t.Errorf("报告缺「%s」：\n%s", want, text)
		}
	}
}

// R-376：本地校验逐件报错——id 格式、重复与只差大小写、类别表、文件、Windows 不允许的字符；SkipInvalid 只留合格的。
func TestBuildByIDErrors(t *testing.T) {
	dir := fixture(t, `{"assets": [
	    {"id": "weapon.carbine", "category": "weapon", "file": "a.glb"},
	    {"id": "core:ok", "category": "weapon", "file": "a.glb"},
	    {"id": "core:OK", "category": "weapon", "file": "a.glb"},
	    {"id": "core:ok", "category": "weapon", "file": "a.glb"},
	    {"id": "Core:x", "category": "weapon", "file": "a.glb"},
	    {"id": "core:y", "category": "vehicle", "file": "a.glb"},
	    {"id": "core:z", "category": "", "file": "a.glb"},
	    {"id": "core:w", "category": "weapon", "file": "missing.glb"},
	    {"id": "core:v", "category": "weapon", "file": "x.tscn"},
	    {"id": "core:u", "category": "weapon", "file": "a.glb", "previews": ["bad|name.png"]},
	    {"id": "core:con", "category": "weapon", "file": "a.glb"}
	  ]}`, "a.glb", "x.tscn", ".kitestring/assets.json")
	os.WriteFile(filepath.Join(dir, ".kitestring/assets.json"), []byte(`{"categories": ["weapon", "prop"]}`), 0o600)
	p, rep, err := BuildByID(IDOptions{Paths: []string{filepath.Join(dir, "asset.json")}})
	if err != nil {
		t.Fatal(err)
	}
	if rep.CatSource == "" || !slices.Equal(rep.Categories, []string{"weapon", "prop"}) {
		t.Fatalf("应读到项目的类别表：%+v", rep)
	}
	want := []string{"要带命名空间", "", "只差大小写", "重复", "命名空间只能用小写", "不在项目的类别表里", "没写类别", "文件找不到", "不支持", "不允许的字符", "保留名"}
	for i, w := range want {
		errs := strings.Join(rep.Items[i].Errors, "；")
		if w == "" && errs != "" || w != "" && !strings.Contains(errs, w) {
			t.Errorf("第 %d 件 %s：%q，应含 %q", i+1, rep.Items[i].ID, errs, w)
		}
	}
	if rep.Bad() != 10 || len(p.Items) != 1 || p.Items[0].Key != "core:ok" {
		t.Fatalf("只有 core:ok 合格：%d %+v", rep.Bad(), p.Items)
	}
	if !strings.Contains(ReportText(rep), "有错 10 件") {
		t.Fatal(ReportText(rep))
	}
	// --categories 指定别的类别表
	_, rep, _ = BuildByID(IDOptions{Paths: []string{filepath.Join(dir, "asset.json")}, Categories: writeTemp(t, `["vehicle"]`)})
	if errs := strings.Join(rep.Items[5].Errors, ""); errs != "" {
		t.Fatalf("--categories 应覆盖仓库里的类别表：%s", errs)
	}
	if IsAssetJSON("testdata/sample/assets/manifest.json") {
		t.Fatal("旧的 manifest 不应认作 asset.json")
	}
	if _, _, err := BuildByID(IDOptions{Paths: []string{writeTemp(t, `{"assets": [`)}}); err == nil || !strings.Contains(err.Error(), "JSON") {
		t.Fatalf("无效 JSON：%v", err)
	}
}

// R-376：一件素材一个 asset.json（rollcall-squad 的写法）——推整个目录；主文件取默认变体；预算、尺寸、许可写成对象；
// 预览图路径相对项目配置的 previews_base，没列出的按所在目录挂上并提示；--category / --id 只推一部分。
func TestBuildByIDPerAssetDir(t *testing.T) {
	root := fixture(t, `{}`,
		"game/assets/prop/ammo_crate/ammo_crate.glb", "game/assets/weapon/carbine/carbine.glb", "game/assets/weapon/carbine/carbine_v2.glb",
		"game/assets/audio/body_fall/body_fall_01.ogg",
		"art/prop/ammo_crate/previews/ammo_crate__hero.png", "art/prop/ammo_crate/previews/ammo_crate__tactical.png",
		"art/weapon/carbine/previews/carbine__side.png")
	write := func(rel, s string) {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, rel)), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, rel), []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(".kitestring/assets.json", `{"categories": ["prop", "weapon", "audio"], "previews_base": "art/{category}/{name}"}`)
	write("game/assets/prop/ammo_crate/asset.json", `{"format": 1, "id": "core:prop.ammo_crate", "category": "prop", "title": "弹药箱",
	  "default_variant": "default", "variants": {"default": {"files": {"model": "ammo_crate.glb"}}},
	  "size": {"dimensions_m": [0.7, 0.38, 0.46]}, "budget": {"triangles": 784, "max_triangles": 800, "max_surfaces": 2},
	  "batches": ["G0"], "previews": [{"path": "previews/ammo_crate__hero.png", "view": "hero"}],
	  "sources": ["art/_shared/blend/rollcall_g0.blend"], "license": {"name": "项目自有", "notes": "原创"}}`)
	write("game/assets/weapon/carbine/asset.json", `{"id": "core:weapon.carbine", "category": "weapon", "default_variant": "g3",
	  "variants": {"g3": {"files": {"model": "carbine.glb"}}, "v2": {"files": {"model": "carbine_v2.glb"}}}, "batches": ["G3", "G15"], "previews": []}`)
	write("game/assets/audio/body_fall/asset.json", `{"id": "core:audio.body_fall", "category": "audio", "variants": {"01": {"files": {"sound": "body_fall_01.ogg"}}}}`)
	dir := filepath.Join(root, "game/assets")

	p, rep, err := BuildByID(IDOptions{Paths: []string{dir}})
	if err != nil {
		t.Fatal(err)
	}
	// R-380：carbine 的变体 v2 单独一条（默认变体 g3 就是素材本身）
	if len(rep.Items) != 4 || rep.Bad() != 1 || len(p.Items) != 3 || !strings.HasSuffix(rep.Config, ConfigFile) {
		t.Fatalf("三件里音效不支持，carbine 另有变体 v2：\n%s", ReportText(rep))
	}
	by := map[string]protocol.AssetItem{}
	for _, it := range p.Items {
		by[it.Key] = it
	}
	a := by["core:prop.ammo_crate"]
	if a.Kind != protocol.AssetPart || a.File.Name != "ammo_crate.glb" || a.Triangles != 784 || a.Budget != 800 || len(a.Size) != 3 ||
		a.Source != "art/_shared/blend/rollcall_g0.blend" || a.License != "项目自有：原创" || !slices.Equal(a.Batches, []string{"G0"}) {
		t.Fatalf("通用字段：%+v", a)
	}
	if len(a.Previews) != 2 || a.Previews[0].View != "hero" || a.Previews[1].View != "tactical" {
		t.Fatalf("列出的 + 按所在目录挂上的预览图：%+v", a.Previews)
	}
	if !strings.Contains(string(a.Meta), `"title":"弹药箱"`) || !strings.Contains(string(a.Meta), `"max_surfaces":2`) || strings.Contains(string(a.Meta), "variants") {
		t.Fatalf("meta：%s", a.Meta)
	}
	if v := by["core:weapon.carbine@v2"]; v.File == nil || v.File.Name != "carbine_v2.glb" || v.Parent != "core:weapon.carbine" {
		t.Fatalf("变体 v2：%+v", v)
	}
	if c := by["core:weapon.carbine"]; c.File.Name != "carbine.glb" || len(c.Previews) != 1 || c.Previews[0].View != "side" || len(c.Variants) == 0 || c.Variant != "g3" {
		t.Fatalf("默认变体 g3 的文件、按所在目录挂上的预览图：%+v", c)
	}
	text := ReportText(rep)
	for _, want := range []string{"没在 previews 里列出，按所在目录挂上", "文件类型不支持：body_fall_01.ogg", "有错 1 件"} {
		if !strings.Contains(text, want) {
			t.Errorf("报告缺「%s」：\n%s", want, text)
		}
	}
	// 只推一部分
	if _, rep, _ := BuildByID(IDOptions{Paths: []string{dir}, OnlyCat: "prop"}); len(rep.Items) != 1 || rep.Filtered != 2 || rep.Bad() != 0 {
		t.Fatalf("--category：%+v", rep)
	}
	if p, rep, _ := BuildByID(IDOptions{Paths: []string{dir}, OnlyID: "core:weapon.*"}); len(p.Items) != 2 || rep.Filtered != 2 {
		t.Fatalf("--id 前缀：%+v", rep)
	}
}

func writeTemp(t *testing.T, s string) string {
	p := filepath.Join(t.TempDir(), "x.json")
	if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestWinNames(t *testing.T) {
	for name, bad := range map[string]bool{"weapon.carbine__front.png": false, "core:weapon__front.png": true, "a?.png": true, "NUL.png": true, "x.": true, "front side.png": false} {
		if got := winNameProblem(name) != ""; got != bad {
			t.Errorf("%s：%v", name, got)
		}
	}
	if n, v, ok := splitView("weapon.carbine__front.png"); !ok || n != "weapon.carbine" || v != "front" {
		t.Fatal(n, v)
	}
	if _, _, ok := splitView("carbine_front.png"); ok {
		t.Fatal("单下划线不算")
	}
}
