package protocol

import (
	"encoding/json"
	"testing"
)

// R-408：事件按时间排好；音效素材去重；label 去掉首尾空白；asset 认「素材 id@变体名」。
func TestParseEvents(t *testing.T) {
	c, errs := ParseCompose(json.RawMessage(`{"events": {
	  "load": [{"t": 2.6, "do": "sound", "asset": "core:sfx.latch"}, {"t": 1.8, "do": "attach", "name": " cargo "}],
	  "unload": [{"t": 0.4, "do": "sound", "asset": "core:sfx.latch"}, {"t": 0.5, "do": "sound", "asset": "core:sfx.thud@soft"},
	             {"t": 0.6, "do": "mark", "socket": "crate", "label": " 尘土 "}]}}`))
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	if l := c.Events["load"]; len(l) != 2 || l[0].Do != EventAttach || l[0].Name != "cargo" || l[1].T != 2.6 {
		t.Fatalf("按时间排：%+v", l)
	}
	if s := c.Sounds(); len(s) != 2 || s[0] != "core:sfx.latch" || s[1] != "core:sfx.thud@soft" {
		t.Fatalf("音效去重：%v", s)
	}
	if c.Events["unload"][2].Label != "尘土" || c.Empty() {
		t.Fatalf("label：%+v", c.Events["unload"])
	}
	if c, _ := ParseCompose(json.RawMessage(`{"events": {}}`)); !c.Empty() {
		t.Fatal("空的 events 不算配置")
	}
}
