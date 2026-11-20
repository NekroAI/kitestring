package assetpush

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NekroAI/kitestring/internal/assetfmt"
	"github.com/NekroAI/kitestring/internal/protocol"
)

// R-387：字体（TTF / OTF / WOFF2）和 SVG 按同一套 id、变体推送。字体是 font 类，files 里有多个字重时取 regular；
// SVG 是独立的图，像素尺寸取 width / height，带脚本时提示（网页上不会执行）；内容按文件头、结构校验，PNG 改名 .ttf、坏 SVG 报错。
func TestBuildFontSVG(t *testing.T) {
	dir := fixture(t, `{"namespace": "core", "assets": [
	    {"id": "ui.font_mono", "category": "ui", "license": "OFL",
	     "variants": {"default": {"files": {"font_bold": "mono-bold.ttf", "font_regular": "mono-regular.ttf", "license_text": "ofl.txt"}}, "web": {"file": "mono.woff2"}}},
	    {"id": "ui.font_sc", "category": "ui", "file": "sc.otf"},
	    {"id": "ui.icon_carbine", "category": "ui", "variants": {"default": {"files": {"icon": "carbine.svg"}}}},
	    {"id": "ui.icon_bad", "category": "ui", "file": "bad.svg"},
	    {"id": "ui.font_fake", "category": "ui", "file": "fake.ttf"},
	    {"id": "ui.font_misnamed", "category": "ui", "file": "misnamed.ttf"}
	  ]}`, "ofl.txt", "fake.ttf")
	put := func(name string, b []byte) {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	put("mono-regular.ttf", assetfmt.SynthFont(assetfmt.MimeTTF))
	put("mono-bold.ttf", assetfmt.SynthFont(assetfmt.MimeTTF))
	put("mono.woff2", assetfmt.SynthFont(assetfmt.MimeWOFF2))
	put("sc.otf", assetfmt.SynthFont(assetfmt.MimeOTF))
	put("misnamed.ttf", assetfmt.SynthFont(assetfmt.MimeOTF))
	put("carbine.svg", []byte(assetfmt.ScriptSVG))
	put("bad.svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg"><g></svg>`))
	png, _ := os.ReadFile("testdata/sample/previews/wheel_hero.png")
	put("fake.ttf", png)

	p, rep, err := BuildByID(IDOptions{Paths: []string{filepath.Join(dir, "asset.json")}, SkipInvalid: true, NoFFmpeg: true})
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]protocol.AssetItem{}
	for _, it := range p.Items {
		by[it.Key] = it
	}
	checks := map[string]Check{}
	for _, c := range rep.Items {
		checks[c.ID] = c
	}
	mono := by["core:ui.font_mono"]
	if mono.Kind != protocol.AssetFont || mono.File == nil || mono.File.Name != "mono-regular.ttf" || mono.Variant != "default" {
		t.Fatalf("字体取 regular 当主文件：%+v %+v", mono, mono.File)
	}
	if web := by["core:ui.font_mono@web"]; web.Kind != protocol.AssetFont || web.File.Name != "mono.woff2" {
		t.Fatalf("WOFF2 变体：%+v", web)
	}
	if sc := by["core:ui.font_sc"]; sc.Kind != protocol.AssetFont || len(checks["core:ui.font_sc"].Errors) != 0 {
		t.Fatalf("OTF：%+v %+v", sc, checks["core:ui.font_sc"])
	}
	icon := by["core:ui.icon_carbine"]
	if icon.Kind != protocol.AssetImage || len(icon.Pixels) != 2 || icon.Pixels[0] != 64 || icon.Pixels[1] != 48 {
		t.Fatalf("SVG 是独立的图、像素取 width / height：%+v", icon)
	}
	if w := strings.Join(checks["core:ui.icon_carbine"].Warnings, "；"); !strings.Contains(w, "脚本不会执行") {
		t.Fatalf("带脚本的 SVG 要提示：%s", w)
	}
	if e := strings.Join(checks["core:ui.icon_bad"].Errors, "；"); !strings.Contains(e, "SVG 文件 bad.svg 校验不过") {
		t.Fatalf("坏 SVG 报错：%s", e)
	}
	if e := strings.Join(checks["core:ui.font_fake"].Errors, "；"); !strings.Contains(e, "字体文件 fake.ttf 校验不过") {
		t.Fatalf("PNG 改名 .ttf 报错：%s", e)
	}
	if _, ok := by["core:ui.font_fake"]; ok {
		t.Fatal("校验不过的不推")
	}
	if w := strings.Join(checks["core:ui.font_misnamed"].Warnings, "；"); !strings.Contains(w, "扩展名和内容不符（内容是 OTF）") {
		t.Fatalf("扩展名和内容不符要提示：%s", w)
	}
	txt := ReportText(rep)
	if !strings.Contains(txt, "字体 · TTF") || !strings.Contains(txt, "图片 · SVG") {
		t.Fatalf("报告写出字体、SVG：%s", txt)
	}
}

func TestPickMainFile(t *testing.T) {
	cases := []struct {
		files map[string]string
		want  string
	}{
		{map[string]string{"license_text": "ofl.txt", "font_regular": "r.ttf", "font_bold": "b.ttf"}, "r.ttf"},
		{map[string]string{"license_text": "ofl.txt", "font_bold": "b.ttf", "font_light": "l.ttf"}, "b.ttf"},
		{map[string]string{"a_license": "ofl.txt", "glyphs": "x.otf"}, "x.otf"},
		{map[string]string{"icon": "i.svg", "license": "l.txt"}, "i.svg"},
		{map[string]string{"model": "m.glb", "font": "f.ttf"}, "m.glb"},
		{map[string]string{"readme": "a.txt", "notes": "b.txt"}, "b.txt"},
	}
	for _, c := range cases {
		if got := pickMainFile(c.files); got != c.want {
			t.Fatalf("%v：得 %s，应为 %s", c.files, got, c.want)
		}
	}
}
