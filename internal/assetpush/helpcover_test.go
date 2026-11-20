package assetpush

import (
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/NekroAI/kitestring/internal/protocol"
)

// 一致性（R-409）：Hub / 网页读取的 asset.json display 字段，都得写进 ks asset help（含 compose 子页）。
// 字段清单的来源：protocol.DisplayFields（顶层）、挂件 / 参照的结构体 json 标签、网页 JsonAnim 类型（动作表的项）；
// 另外扫描代码里写到的 display.<字段>，没登记的也失败，所以新增读取处漏登记、漏写说明都会被拦下。

func jsonTags(v any) []string {
	var out []string
	t := reflect.TypeOf(v)
	for i := 0; i < t.NumField(); i++ {
		if n, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ","); n != "" {
			out = append(out, n)
		}
	}
	return out
}

func repoPath(rel string) string {
	return filepath.Join("..", "..", filepath.FromSlash(rel))
}

// jsAnimFields 从网页的 JsonAnim 类型里取动作表每项的字段。
func jsAnimFields(t *testing.T) []string {
	b, err := os.ReadFile(repoPath("web/src/lib/anim.ts"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`type JsonAnim = \{([^}]*)\}`).FindSubmatch(b)
	if m == nil {
		t.Fatal("web/src/lib/anim.ts 里找不到 JsonAnim 类型，请更新这个测试的取法")
	}
	var out []string
	for _, f := range regexp.MustCompile(`(\w+)\??:`).FindAllSubmatch(m[1], -1) {
		out = append(out, string(f[1]))
	}
	if len(out) == 0 {
		t.Fatal("JsonAnim 里没取到字段")
	}
	return out
}

func TestDisplayFieldsInHelp(t *testing.T) {
	help := FormatHelp + ComposeHelp
	for _, k := range protocol.DisplayFields {
		if !strings.Contains(help, "display."+k) {
			t.Errorf("display.%s 是 Hub / 网页读取的字段，但 ks asset help（含 compose 子页）里没有写 display.%s 的说明", k, k)
		}
	}
	// 子字段：compose 子页里每项缩进写「名字  必填 / 可选：……」；动作表的项写在 FormatHelp 里（name 动作名、loop、label）
	sub := map[string][]string{
		"attach":     jsonTags(protocol.ComposeAttach{}),
		"refs":       jsonTags(protocol.ComposeRef{}),
		"animations": jsAnimFields(t),
	}
	for k, fields := range sub {
		for _, f := range fields {
			if !regexp.MustCompile(`(?m)(^\s+` + f + `\s+(必填|可选)|\b` + f + ` [^\n]{0,6}[：:]|"` + f + `")`).MatchString(help) {
				t.Errorf("display.%s 的子字段 %s 在 ks asset help 里没有说明", k, f)
			}
		}
	}
	// 登记表要跟代码一致：代码里写到的 display.<字段> 都得登记
	known := map[string]bool{}
	for _, k := range protocol.DisplayFields {
		known[k] = true
	}
	re := regexp.MustCompile(`\bdisplay\.([a-z_]+)`)
	for _, root := range []string{"internal", "cmd", "web/src"} {
		_ = filepath.WalkDir(repoPath(root), func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			ext := filepath.Ext(p)
			if (ext != ".go" && ext != ".ts" && ext != ".tsx") || strings.HasSuffix(p, "_test.go") || strings.Contains(p, ".test.") ||
				strings.HasSuffix(p, "schema.d.ts") || strings.Contains(filepath.ToSlash(p), "/i18n/") {
				return nil
			}
			b, _ := os.ReadFile(p)
			for _, m := range re.FindAllStringSubmatch(string(b), -1) {
				if !known[m[1]] {
					t.Errorf("%s 里写到 display.%s，但没登记在 protocol.DisplayFields，也就没人核对 ks asset help 里有没有它的说明", p, m[1])
				}
			}
			return nil
		})
	}
}

func TestAnimHint(t *testing.T) {
	c := Check{clips: []string{"idle", "run"}}
	if h := hintsOf(c); len(h) != 1 || !strings.Contains(h[0], "这 2 个动作") || !strings.Contains(h[0], "display.animations") {
		t.Errorf("有动作没写 display.animations 应提示：%v", h)
	}
	c.animsText = true
	if h := hintsOf(c); len(h) != 0 {
		t.Errorf("写了 display.animations 不该提示：%v", h)
	}
	if h := hintsOf(Check{}); len(h) != 0 {
		t.Errorf("没有动作不该提示：%v", h)
	}
}
