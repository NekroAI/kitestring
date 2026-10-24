package blocks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 改动框（R-183）：比例坐标照存；像素坐标、越界、格式不对的丢掉并说明；右下边出界一点点截到边上。
func TestParseBoxes(t *testing.T) {
	raw := decode(t, `[
	 {"x":0.1,"y":0.2,"w":0.3,"h":0.4,"label":"这里改成标题优先"},
	 [0.5,0.5,0.505,0.2,"数组写法"],
	 {"left":0,"top":0,"width":1,"height":1},
	 {"x":120,"y":80,"w":300,"h":40},
	 {"x":0.8,"y":0.1,"w":0.5,"h":0.1},
	 {"x":0.1,"y":0.1,"w":0,"h":0.1},
	 {"x":"a","y":0.1,"w":0.1,"h":0.1},
	 "不是框"
	]`)
	bs, warns := ParseBoxes(raw, "图")
	if len(bs) != 3 {
		t.Fatalf("应保存 3 个框，得到 %d：%+v", len(bs), bs)
	}
	if bs[0].Label != "这里改成标题优先" || bs[0].X != 0.1 || bs[0].H != 0.4 {
		t.Fatalf("第 1 个框不对：%+v", bs[0])
	}
	if bs[1].Label != "数组写法" || bs[1].W != 0.5 {
		t.Fatalf("出界一点点应截到边上：%+v", bs[1])
	}
	if len(warns) != 5 {
		t.Fatalf("应有 5 条提示：%v", warns)
	}
	all := strings.Join(warns, "\n")
	for _, want := range []string{"像素坐标", "超出图片范围", "不是正数", "格式不对"} {
		if !strings.Contains(all, want) {
			t.Fatalf("提示里应有「%s」：%v", want, warns)
		}
	}
	if _, w := ParseBoxes("x", "图"); len(w) != 1 {
		t.Fatalf("boxes 不是数组应提示：%v", w)
	}
	many := make([]any, MaxBoxes+2)
	for i := range many {
		many[i] = map[string]any{"x": 0.1, "y": 0.1, "w": 0.1, "h": 0.1}
	}
	if bs, w := ParseBoxes(many, "图"); len(bs) != MaxBoxes || len(w) != 1 {
		t.Fatalf("超出上限应截掉并提示：%d %v", len(bs), w)
	}
}

// figure / compare 块的标题和框落到图片上：figure 的框在 image 上，compare 的框在 after 上；写错的框不影响块保存。
func TestBlockShots(t *testing.T) {
	raw := decode(t, `[
	 {"type":"figure","image":"F1","title":"工作台 · 带鱼屏","boxes":[{"x":0.1,"y":0.1,"w":0.2,"h":0.2,"label":"新标题"},{"x":2,"y":2,"w":1,"h":1}]},
	 {"type":"compare","before":"F2","after":"F3","caption":"审批抽屉","boxes":[{"x":0,"y":0,"w":0.5,"h":0.5}]},
	 {"type":"figure","image":"F4"}
	]`)
	bs, warns := Parse(raw, known)
	if len(bs) != 3 || len(warns) != 1 {
		t.Fatalf("应保存 3 个块、1 条框的提示：%d %v", len(bs), warns)
	}
	if f := bs[0].Image; f.Title != "工作台 · 带鱼屏" || len(f.Boxes) != 1 || f.Boxes[0].Label != "新标题" {
		t.Fatalf("figure 的标题和框不对：%+v", f)
	}
	if a := bs[1].After; a.Title != "审批抽屉" || len(a.Boxes) != 1 || len(bs[1].Before.Boxes) != 0 {
		t.Fatalf("compare 的框应画在改后的图上：%+v %+v", a, bs[1].Before)
	}
	if bs[2].Image.Title != "" || bs[2].Image.Boxes != nil {
		t.Fatalf("旧写法不应有标题和框：%+v", bs[2].Image)
	}
	h := TitleHints(Files(bs), bs)
	if !strings.Contains(h, "F4.png") || strings.Contains(h, "F1.png") || strings.Contains(h, "F3.png") {
		t.Fatalf("只有没写标题的图要提示：%s", h)
	}
}

// evidence 的 images：附件 ID 或 {id, title, boxes}；找不到的图、写错的框丢掉并提示；没标题的用文件名兜底并提示。
func TestParseImages(t *testing.T) {
	raw := decode(t, `["F1", {"id":"F2","title":"需求池 · 手机","boxes":[{"x":0.1,"y":0.1,"w":0.2,"h":0.2}, {"x":0.9,"y":0.9,"w":0.5,"h":0.5}]}, {"id":"X9","title":"不存在"}]`)
	fs, warns := ParseImages(raw, known)
	if len(fs) != 2 || fs[0].ID != "F1" || fs[1].Title != "需求池 · 手机" || len(fs[1].Boxes) != 1 {
		t.Fatalf("images 解析不对：%+v", fs)
	}
	if len(warns) != 2 {
		t.Fatalf("越界的框和找不到的图都要提示：%v", warns)
	}
	if h := TitleHints(fs, nil); !strings.Contains(h, "F1.png") || strings.Contains(h, "F2.png") {
		t.Fatalf("没写标题的图要按文件名提示：%s", h)
	}
	if h := TitleHints([]File{{ID: "F2", Name: "a.png", Mime: "image/png", Title: "有标题"}, {ID: "F5", Name: "log.txt", Mime: "text/plain"}}, nil); h != "" {
		t.Fatalf("都有标题（或不是图）时不提示：%s", h)
	}
}

// LocalizeImages：字符串当本机路径上传，对象的 path 上传后换成 id，已有 id 的不动；同一路径只传一次。
func TestLocalizeImages(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.png")
	os.WriteFile(a, []byte("x"), 0o600)
	raw := []any{a, map[string]any{"path": a, "title": "标题"}, map[string]any{"id": "F7"}}
	n := 0
	err := LocalizeImages(raw, map[string]string{}, func(p string) (string, error) {
		n++
		return "UP" + filepath.Base(p), nil
	})
	if err != nil || n != 1 {
		t.Fatalf("同一路径应只上传一次：%v %d", err, n)
	}
	if m := raw[0].(map[string]any); m["id"] != "UPa.png" {
		t.Fatalf("字符串应换成 {id}：%v", raw[0])
	}
	if m := raw[1].(map[string]any); m["id"] != "UPa.png" || m["title"] != "标题" || m["path"] != nil {
		t.Fatalf("对象的 path 应换成 id 并保留标题：%v", m)
	}
	if err := LocalizeImages([]any{map[string]any{"path": filepath.Join(dir, "nope.png")}}, map[string]string{}, nil); err == nil {
		t.Fatal("本机文件不存在应报错")
	}
}

// 框对应的验收点（R-195）：criterion 可选，也接受 point / for 和字符串数字；不是合理编号的忽略并提示，框照常保存；老写法没有这个字段。
func TestBoxCriterion(t *testing.T) {
	raw := decode(t, `[
	 {"x":0.1,"y":0.1,"w":0.2,"h":0.2,"label":"1 简报条变浅","criterion":1},
	 {"x":0.1,"y":0.1,"w":0.2,"h":0.2,"point":"3"},
	 {"x":0.1,"y":0.1,"w":0.2,"h":0.2,"for":2.0},
	 {"x":0.1,"y":0.1,"w":0.2,"h":0.2,"criterion":-1},
	 {"x":0.1,"y":0.1,"w":0.2,"h":0.2,"criterion":120},
	 {"x":0.1,"y":0.1,"w":0.2,"h":0.2,"label":"2 老写法"}
	]`)
	bs, warns := ParseBoxes(raw, "图")
	if len(bs) != 6 {
		t.Fatalf("6 个框都应保存：%+v", bs)
	}
	want := []int{1, 3, 2, 0, 0, 0}
	for i, b := range bs {
		if b.Criterion != want[i] {
			t.Fatalf("第 %d 个框的验收点应是 %d：%+v", i+1, want[i], b)
		}
	}
	if len(warns) != 2 || !strings.Contains(warns[0], "criterion") {
		t.Fatalf("不合理的编号应提示 2 条：%v", warns)
	}
	if out := string(mustJSON(t, bs[5])); strings.Contains(out, "criterion") {
		t.Fatalf("没写验收点的框不输出 criterion：%s", out)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
