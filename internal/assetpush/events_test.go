package assetpush

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 机械狗（带动作时长）：load 2.5 秒、idle 1 秒；空节点 crate。
const dogEventsGLB = `{"asset":{"version":"2.0"},"nodes":[
 {"name":"Dog_Mesh","mesh":0},{"name":"root","children":[2]},{"name":"crate"}],
 "accessors":[{"max":[2.5]},{"max":[1.0]},{"max":[0.5]}],
 "animations":[{"name":"load","samplers":[{"input":0},{"input":2}]},{"name":"idle","samplers":[{"input":1}]}]}`

// R-408：display.events 格式不对算错；动作名、时间、挂件名、挂点对不上只提示。
func TestEventsCheck(t *testing.T) {
	dir := fixture(t, `{"assets": [
	  {"id": "core:vehicle.dog", "category": "vehicle", "file": "dog.glb", "display": {
	   "attach": [{"socket": "crate", "asset": "core:prop.box", "name": "cargo"}],
	   "events": {"load": [{"t": 2.6, "do": "sound", "asset": "core:sfx.latch"}, {"t": 1.8, "do": "attach", "name": "cargo"},
	                       {"t": 2.0, "do": "detach", "name": "nobody"}, {"t": 2.0, "do": "mark", "socket": "muzzle", "label": "火光"}],
	              "carry": [{"t": 0.1, "do": "detach", "name": "cargo"}],
	              "idle": [{"t": 0.5, "do": "mark", "socket": "crate"}]}}},
	  {"id": "core:bad.one", "category": "vehicle", "file": "dog.glb", "display": {"events": [{"t": 1, "do": "attach"}]}},
	  {"id": "core:bad.two", "category": "vehicle", "file": "dog.glb", "display": {"events": {"load": [
	     {"do": "attach", "name": "a"}, {"t": -1, "do": "attach", "name": "a"}, {"t": 1, "do": "explode"}, {"t": 1, "do": "attach"},
	     {"t": 1, "do": "sound", "asset": "nonamespace"}, {"t": 1, "do": "mark"}, {"t": 1, "do": "mark", "socket": "a", "label": "一二三四五六七八九十一二三四五六七八九十一二三四五"},
	     {"t": 1, "do": "sound", "asset": "core:a.b", "volume": 2}, {"t": "1", "do": "mark", "socket": "a"}]}}}
	]}`, "dog.glb")
	writeGLB(t, filepath.Join(dir, "dog.glb"), dogEventsGLB)
	p, rep, err := BuildByID(IDOptions{Paths: []string{filepath.Join(dir, "asset.json")}, SkipInvalid: true})
	if err != nil {
		t.Fatal(err)
	}
	checks := map[string]Check{}
	for _, c := range rep.Items {
		checks[c.ID] = c
	}
	dog := checks["core:vehicle.dog"]
	if len(dog.Errors) != 0 {
		t.Fatalf("机械狗不应报错：%v", dog.Errors)
	}
	warn := strings.Join(dog.Warnings, "\n")
	for _, want := range []string{
		"动作「carry」在 GLB 里没有",
		"「load」2.6 秒的事件超出动作时长（2.50 秒）",
		"挂件「nobody」在 display.attach 里没有",
		"挂点「muzzle」在 GLB 里找不到",
	} {
		if !strings.Contains(warn, want) {
			t.Errorf("应提示 %q：\n%s", want, warn)
		}
	}
	if strings.Contains(warn, "idle") || strings.Contains(warn, "cargo") || strings.Contains(warn, "1.8") {
		t.Errorf("对得上的不提示：\n%s", warn)
	}
	if n := len(checks["core:bad.one"].Errors); n != 1 {
		t.Errorf("events 不是对象：%v", checks["core:bad.one"].Errors)
	}
	if n := len(checks["core:bad.two"].Errors); n != 9 {
		t.Errorf("9 项各报一条：%d %v", n, checks["core:bad.two"].Errors)
	}
	if len(p.Items) != 1 {
		t.Fatalf("只推合格的 1 件：%d", len(p.Items))
	}
	var meta struct {
		Display map[string]json.RawMessage `json:"display"`
	}
	if err := json.Unmarshal(p.Items[0].Meta, &meta); err != nil || meta.Display["events"] == nil {
		t.Fatalf("display.events 留在 meta：%s", p.Items[0].Meta)
	}
}

// R-408：compose 子页里事件的例子能通过校验（挂件 cargo、挂点 crate 与上面的完整例子一致）。
func TestEventsHelpExample(t *testing.T) {
	i := strings.Index(ComposeHelp, `"events": {`)
	if i < 0 {
		t.Fatal("compose 子页里找不到事件例子")
	}
	j := strings.Index(ComposeHelp[i:], "]}\n")
	if j < 0 {
		t.Fatal("事件例子没有结尾")
	}
	snippet := ComposeHelp[i : i+j+2]
	doc := `{"id": "core:vehicle.dog", "category": "vehicle", "file": "dog.glb", "display": {
	  "attach": [{"socket": "crate", "asset": "core:prop.supply_crate", "name": "cargo"}], ` + snippet + `}}`
	dir := t.TempDir()
	writeGLB(t, filepath.Join(dir, "dog.glb"), strings.Replace(dogEventsGLB, `"max":[2.5]`, `"max":[3.0]`, 1))
	if err := os.WriteFile(filepath.Join(dir, "asset.json"), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	_, rep, err := BuildByID(IDOptions{Paths: []string{filepath.Join(dir, "asset.json")}})
	if err != nil || len(rep.Items) != 1 || len(rep.Items[0].Errors) != 0 || strings.Contains(strings.Join(rep.Items[0].Warnings, "\n"), "事件") {
		t.Fatalf("事件例子应能通过校验、没有提示：%v %+v", err, rep.Items)
	}
}
