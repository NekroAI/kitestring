package assetpush

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NekroAI/kitestring/internal/protocol"
)

// R-351：manifest 的零件挂上同名预览图，其余预览图成为独立的图（目录图、A/B 候选成组），面数和尺寸从 manifest 读。
func TestBuildSample(t *testing.T) {
	p, notes, err := Build(Options{Manifest: "testdata/sample/assets/manifest.json", Previews: []string{"testdata/sample/previews"}, Commit: "abc1234", Branch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Batch != "S1" || len(notes) != 0 {
		t.Fatalf("批次应取 manifest 里的 S1：%s %v", p.Batch, notes)
	}
	by := map[string]protocol.AssetItem{}
	for _, it := range p.Items {
		by[it.Key] = it
	}
	if len(p.Items) != 5 {
		t.Fatalf("应有 2 个零件 + 3 张独立的图：%d", len(p.Items))
	}
	w := by["wheel"]
	if w.Kind != protocol.AssetPart || len(w.Previews) != 2 || w.Triangles != 4 || w.Budget != 1500 || len(w.Size) != 3 || w.Size[0] != 0.76 || !filepath.IsAbs(w.File.Path) {
		t.Fatalf("wheel：%+v", w)
	}
	if c := by["crate"]; c.Category != "cover" || len(c.Previews) != 1 || len(c.Meta) == 0 {
		t.Fatalf("crate：%+v", c)
	}
	if c := by["s1_catalog"]; c.Kind != protocol.AssetImage || c.Category != "目录图" || len(c.Pixels) != 2 || c.Pixels[0] != 96 {
		t.Fatalf("目录图：%+v", c)
	}
	if a, b := by["mule_A"], by["mule_B"]; a.Group != "mule" || b.Group != "mule" {
		t.Fatalf("A/B 候选应成组：%q %q", a.Group, b.Group)
	}

	// --batch 与 manifest 不同时按 --batch，并提示
	p, notes, err = Build(Options{Manifest: "testdata/sample/assets/manifest.json", Batch: "S2"})
	if err != nil || p.Batch != "S2" || len(notes) != 3 || !strings.Contains(notes[0], "--batch S2") {
		t.Fatalf("批次提示：%v %v", notes, err)
	}
	// 没有预览图的零件逐个提示，说明会自动生成缩略图（R-383）
	if !strings.Contains(notes[1], "零件 crate 没有预览图") && !strings.Contains(notes[1], "零件 wheel 没有预览图") || !strings.Contains(notes[1], "自动生成缩略图") || !strings.Contains(notes[2], "自动生成缩略图") {
		t.Fatalf("没有预览图的提示：%v", notes)
	}
	// 只有预览目录、没写批次
	if _, _, err := Build(Options{Previews: []string{"testdata/sample/previews"}}); err == nil {
		t.Fatal("没有批次应报错")
	}
	if p, _, err := Build(Options{Previews: []string{"testdata/sample/previews"}, Batch: "S1", Category: "概念图"}); err != nil || len(p.Items) != 6 {
		t.Fatalf("只推独立的图：%v %d", err, len(p.Items))
	}
}

func TestBuildErrors(t *testing.T) {
	dir := t.TempDir()
	write := func(s string) string {
		p := filepath.Join(dir, "manifest.json")
		if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	os.WriteFile(filepath.Join(dir, "x.tscn"), []byte("x"), 0o600)
	for _, c := range []struct{ json, want string }{
		{`{"batch":"B","assets":[{"id":"a","file":"missing.glb"}]}`, "找不到"},
		{`{"batch":"B","assets":[{"id":"a","file":"x.tscn"}]}`, "不支持"},
		{`{"batch":"B","assets":[{"file":"x.tscn"}]}`, "没有 id"},
		{`{"batch":"B","assets":[`, "JSON"},
	} {
		if _, _, err := Build(Options{Manifest: write(c.json)}); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s：%v", c.json, err)
		}
	}
}
