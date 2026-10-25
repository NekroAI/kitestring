package blocks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func decode(t *testing.T, s string) []any {
	t.Helper()
	var v []any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

var known = func(ref string) (File, bool) {
	if strings.HasPrefix(ref, "F") {
		return File{ID: ref, Name: ref + ".png", Mime: "image/png", Size: 1}, true
	}
	return File{}, false
}

func TestParseAllTypes(t *testing.T) {
	raw := decode(t, `[
	 {"type":"checklist","items":[{"criterion":1,"text":"能声明身份","result":"pass","why":"徽章可见","see":"fig1"},{"criterion":2,"result":"需你确认"}]},
	 {"id":"fig1","type":"figure","image":"F1","caption":"图 1"},
	 {"type":"compare","before":"F2","after":{"id":"F3"}},
	 {"type":"flow","mermaid":"flowchart LR; A-->B"},
	 {"type":"metrics","items":[{"label":"e2e","value":2,"unit":"个","trend":[1,"2",3]},{"label":"go test","value":"全过"}]},
	 {"type":"scope","items":[{"module":"hub","count":4},{"module":"daemon","count":2}]},
	 {"type":"options","items":[{"label":"无身份","recommended":true,"image":"F4"},{"label":"降为开发"}]},
	 {"type":"table","columns":["a","b"],"rows":[["1"],["2","3","extra"]]}
	]`)
	bs, warns := Parse(raw, known)
	if len(warns) != 0 {
		t.Fatalf("不该有提示：%v", warns)
	}
	if len(bs) != 8 {
		t.Fatalf("应保存 8 个块，得到 %d", len(bs))
	}
	if bs[0].ID != "b1" || bs[1].ID != "fig1" {
		t.Fatalf("id 应自动编号并保留自定义：%s %s", bs[0].ID, bs[1].ID)
	}
	if bs[0].Items[1].Result != Ask || bs[0].Items[0].See != "fig1" {
		t.Fatalf("清单解析不对：%+v", bs[0].Items)
	}
	if bs[2].After == nil || bs[2].After.ID != "F3" {
		t.Fatalf("compare 的 {id} 写法应能解析")
	}
	if got := bs[4].Items[0]; got.Value != "2" || len(got.Trend) != 3 {
		t.Fatalf("指标解析不对：%+v", got)
	}
	if !bs[6].Items[0].Recommended || bs[6].Items[0].Image == nil {
		t.Fatalf("选项解析不对：%+v", bs[6].Items[0])
	}
	if r := bs[7].Rows; len(r[0]) != 2 || r[1][1] != "3" {
		t.Fatalf("表格行应按列数对齐：%v", r)
	}
	if fs := Files(bs); len(fs) != 4 {
		t.Fatalf("应收集 4 个附件，得到 %d", len(fs))
	}
}

// 写错的块丢掉并说明原因，其余块照常保存。
func TestParseLenient(t *testing.T) {
	raw := decode(t, `[
	 {"type":"chart","data":[1,2]},
	 "not an object",
	 {"title":"无类型"},
	 {"type":"figure","image":"/no/such.png"},
	 {"type":"compare","before":"F1"},
	 {"type":"metrics","items":[{"label":"a","value":1},{"label":"b","value":2},{"label":"c","value":3},{"label":"d","value":4},{"label":"e","value":5}]},
	 {"type":"checklist","items":[{"text":"x","result":"maybe","see":"nope"}]},
	 {"type":"options","items":[{"label":"only one"}]},
	 {"type":"flow","mermaid":"  "}
	]`)
	bs, warns := Parse(raw, known)
	if len(bs) != 2 || bs[0].Type != Metrics || bs[1].Type != Checklist {
		t.Fatalf("应只保存 metrics 和 checklist，得到 %+v", bs)
	}
	if len(bs[0].Items) != MaxMetrics {
		t.Fatalf("指标应截到 %d 个", MaxMetrics)
	}
	if bs[1].Items[0].Result != Ask || bs[1].Items[0].See != "" {
		t.Fatalf("不认识的 result 按 ask，坏 see 去掉：%+v", bs[1].Items[0])
	}
	all := strings.Join(warns, "\n")
	for _, want := range []string{"「chart」不认识", "可用的类型：checklist", "不是对象", "缺少 type", "/no/such.png", "before 和 after", "最多 4 个指标", "「maybe」不认识", "see「nope」", "至少要 2 个", "需要 mermaid"} {
		if !strings.Contains(all, want) {
			t.Errorf("提示里缺少「%s」：\n%s", want, all)
		}
	}
}

func TestPayloadText(t *testing.T) {
	raw := decode(t, `[{"type":"checklist","items":[{"text":"能声明身份","result":"pass","why":"徽章可见"},{"criterion":4,"result":"ask"}]}]`)
	p, warns := ParsePayload(" 会话可以声明身份 ", []string{"看一眼提示词", " "}, "", []string{"修了 --data"}, raw, known)
	if len(warns) != 0 || p == nil {
		t.Fatalf("解析失败：%v", warns)
	}
	if pass, total := p.Score(); pass != 1 || total != 2 {
		t.Fatalf("达标 %d/%d", pass, total)
	}
	want := "会话可以声明身份\n- 能声明身份：达标，徽章可见\n- 验收标准 4：要你看\n要你做的：看一眼提示词\n顺带改动：修了 --data"
	if got := p.Text(); got != want {
		t.Fatalf("纯文本不对：\n%s", got)
	}
	if p, _ := ParsePayload("", nil, "", nil, nil, known); p != nil {
		t.Fatal("什么都没写时应返回 nil")
	}
}

func TestEncodeDecode(t *testing.T) {
	bs, _ := Parse(decode(t, `[{"type":"flow","mermaid":"graph TD; A-->B"}]`), nil)
	s := Encode(bs)
	got := Decode[[]Block](s)
	if got == nil || len(*got) != 1 || (*got)[0].Mermaid != "graph TD; A-->B" {
		t.Fatalf("往返失败：%s", s)
	}
	if Encode([]Block(nil)) != "" || Decode[[]Block]("") != nil || Decode[[]Block]("{bad") != nil {
		t.Fatal("空值与坏数据应得到空")
	}
}

func TestLocalize(t *testing.T) {
	dir := t.TempDir()
	png := filepath.Join(dir, "a.png")
	if err := os.WriteFile(png, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	raw := decode(t, `[{"type":"compare","before":"`+png+`","after":"`+png+`"},{"type":"figure","image":"F9"},{"type":"options","items":[{"label":"a","image":"`+png+`"},{"label":"b"}]}]`)
	n := 0
	err := Localize(raw, map[string]string{}, func(p string) (string, error) { n++; return "F1", nil })
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("同一路径应只上传一次，实际 %d 次", n)
	}
	m := raw[0].(map[string]any)
	if m["before"] != "F1" || m["after"] != "F1" || raw[1].(map[string]any)["image"] != "F9" {
		t.Fatalf("路径应换成附件 ID，已有 ID 不动：%v %v", m, raw[1])
	}
	if raw[2].(map[string]any)["items"].([]any)[0].(map[string]any)["image"] != "F1" {
		t.Fatal("选项里的图也要上传")
	}
}

// 决策的要决策的点就是 options 块（R-112）：缺问题、选项不足的点丢掉并说明。
func TestParsePoints(t *testing.T) {
	raw := decode(t, `[
	 {"question":"旧 PM 变成什么？","options":[{"label":"无身份","recommended":true,"image":"F1"},{"label":"降为开发"}]},
	 {"options":[{"label":"甲"},{"label":"乙"}]},
	 {"question":"只有一个","options":[{"label":"唯一"}]}
	]`)
	ps, warns := ParsePoints(raw, known)
	if len(ps) != 1 || ps[0].Type != Options || ps[0].Title != "旧 PM 变成什么？" || !ps[0].Items[0].Recommended || ps[0].Items[0].Image == nil {
		t.Fatalf("要决策的点解析不对：%+v", ps)
	}
	all := strings.Join(warns, "\n")
	if !strings.Contains(all, "「甲 / 乙」的要决策的点缺少 question") || !strings.Contains(all, "第 3 个要决策的点至少要 2 个有效选项") {
		t.Fatalf("提示不对：\n%s", all)
	}
}
