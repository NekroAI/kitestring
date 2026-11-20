package assetpush

import (
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeGLB 写一个只有 JSON 块的 GLB（节点、蒙皮、动作名），够推送端读挂点。
func writeGLB(t *testing.T, path, doc string) {
	t.Helper()
	js := []byte(doc)
	for len(js)%4 != 0 {
		js = append(js, ' ')
	}
	b := make([]byte, 20, 20+len(js))
	copy(b, "glTF")
	binary.LittleEndian.PutUint32(b[4:], 2)
	binary.LittleEndian.PutUint32(b[8:], uint32(20+len(js)))
	binary.LittleEndian.PutUint32(b[12:], uint32(len(js)))
	copy(b[16:], "JSON")
	if err := os.WriteFile(path, append(b, js...), 0o600); err != nil {
		t.Fatal(err)
	}
}

// 机械狗：网格、骨架（root → chassis → arm_main）、货叉下的空节点 crate、名字带 hand 的骨骼；动作 idle、carry_walk。
const dogGLB = `{"asset":{"version":"2.0"},"nodes":[
 {"name":"Dog_Mesh","mesh":0},{"name":"root","children":[2]},{"name":"chassis","children":[3]},{"name":"arm_main","children":[4]},
 {"name":"crate"},{"name":"hand_r"},{"name":"Dog_Rig","children":[0,1]}],
 "skins":[{"joints":[1,2,3,5]}],"animations":[{"name":"idle"},{"name":"carry_walk"}]}`

// R-407：display.attach / refs 格式不对算错；挂点、动作在 GLB 里找不到只提示；推送报告一行列出 GLB 里的挂点；display 除 name 外留在 meta。
func TestComposeCheck(t *testing.T) {
	dir := fixture(t, `{"assets": [
	  {"id": "core:vehicle.dog", "category": "vehicle", "file": "dog.glb", "display": {"name": "机械狗", "animations": ["idle"],
	   "attach": [{"socket": "crate", "asset": "core:prop.box", "variant": "empty", "clips": ["carry_walk"]},
	              {"socket": "crate_typo", "asset": "core:prop.box@junk", "name": "extra", "clips": ["carry_idle"]}],
	   "refs": [{"asset": "core:char.soldier"}, {"asset": "core:prop.box", "offset": [1, 0, 0]}]}},
	  {"id": "core:vehicle.plain", "category": "vehicle", "file": "plain.glb"},
	  {"id": "core:bad.one", "category": "vehicle", "file": "plain.glb", "display": {"attach": {"socket": "x"}}},
	  {"id": "core:bad.two", "category": "vehicle", "file": "plain.glb", "display": {"attach": [{"asset": "core:prop.box"}, {"socket": "a", "asset": "nonamespace"}]}},
	  {"id": "core:bad.three", "category": "vehicle", "file": "plain.glb", "display": {"attach": [{"socket": "a", "asset": "core:p.x"}, {"socket": "a", "asset": "core:p.y"}]}},
	  {"id": "core:bad.four", "category": "vehicle", "file": "plain.glb", "display": {"refs": [{"asset": "core:p.x", "offset": [1, 2]}, {"asset": "core:p.x", "scale": 2}]}}
	]}`, "dog.glb", "plain.glb")
	writeGLB(t, filepath.Join(dir, "dog.glb"), dogGLB)
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
	// 默认挂点：空节点 crate、带通用词的 hand_r，以及引用到的 crate_typo（GLB 里没有，不列）；骨骼 chassis、容器 Dog_Rig 不列
	if strings.Join(dog.Sockets, ",") != "crate,hand_r" {
		t.Fatalf("挂点：%v", dog.Sockets)
	}
	warn := strings.Join(dog.Warnings, "\n")
	if !strings.Contains(warn, "「crate_typo」在 GLB 里找不到") || !strings.Contains(warn, "「carry_idle」在 GLB 里没有") || strings.Contains(warn, "carry_walk") {
		t.Fatalf("挂点、动作对不上只提示：%s", warn)
	}
	text := ReportText(rep)
	if !strings.Contains(text, "说明：GLB 里的挂点：crate、hand_r（挂件写法见 ks help asset compose）") || strings.Count(text, "GLB 里的挂点") != 1 {
		t.Fatalf("报告里一行列出挂点（只给有挂点的素材）：\n%s", text)
	}
	for _, id := range []string{"core:bad.one", "core:bad.two", "core:bad.three", "core:bad.four"} {
		if len(checks[id].Errors) == 0 {
			t.Errorf("%s 格式不对应报错", id)
		}
	}
	if n := len(checks["core:bad.two"].Errors); n != 2 {
		t.Errorf("缺 socket、素材 id 不合格各一条：%v", checks["core:bad.two"].Errors)
	}
	if len(p.Items) != 2 {
		t.Fatalf("只推合格的 2 件：%d", len(p.Items))
	}
	var meta struct {
		Display map[string]json.RawMessage `json:"display"`
	}
	if err := json.Unmarshal(p.Items[0].Meta, &meta); err != nil || meta.Display["name"] != nil || meta.Display["attach"] == nil || meta.Display["animations"] == nil {
		t.Fatalf("display 除 name 外留在 meta：%s", p.Items[0].Meta)
	}
}

// R-407：ks help asset compose 子页在约 60 行内，带完整例子且例子能通过校验。
func TestComposeHelp(t *testing.T) {
	if n := strings.Count(ComposeHelp, "\n"); n > 60 {
		t.Fatalf("compose 子页 %d 行，超过 60", n)
	}
	for _, want := range []string{"display.attach", "display.refs", "socket", "asset", "variant", "clips", "name", "offset", "可选"} {
		if !strings.Contains(ComposeHelp, want) {
			t.Errorf("compose 子页缺 %q", want)
		}
	}
	start, end := strings.Index(ComposeHelp, "\n{\n"), strings.Index(ComposeHelp, "\n}\n")
	var ex struct {
		Display json.RawMessage `json:"display"`
	}
	if start < 0 || end < 0 || json.Unmarshal([]byte(ComposeHelp[start:end+2]), &ex) != nil {
		t.Fatal("例子要是能解析的 asset.json")
	}
	dir := t.TempDir()
	writeGLB(t, filepath.Join(dir, "robodog_loader.glb"), dogGLB)
	if err := os.WriteFile(filepath.Join(dir, "asset.json"), []byte(ComposeHelp[start:end+2]), 0o600); err != nil {
		t.Fatal(err)
	}
	_, rep, err := BuildByID(IDOptions{Paths: []string{filepath.Join(dir, "asset.json")}})
	if err != nil || len(rep.Items) != 1 || len(rep.Items[0].Errors) != 0 {
		t.Fatalf("例子应能通过校验：%v %+v", err, rep.Items)
	}
}
