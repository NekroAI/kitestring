package assetpush

import (
	"strings"
	"testing"

	"github.com/NekroAI/kitestring/internal/protocol"
)

// R-415：挂件覆盖不全的提示——A-155 的真实配置提示 idle、walk、load、unload；clips 不写、全覆盖、有 attach / detach 事件都不提示。
func TestAttachCoverageHint(t *testing.T) {
	clips := []string{"idle", "walk", "idle_loaded", "walk_loaded", "load", "unload"}
	mk := func(a protocol.ComposeAttach, ev map[string][]protocol.ComposeEvent) Check {
		return Check{clips: clips, animsText: true, compose: protocol.Compose{Attach: []protocol.ComposeAttach{a}, Events: ev}}
	}
	a := protocol.ComposeAttach{Socket: "crate", Asset: "core:prop.standard_crate", Name: "cargo", Clips: []string{"idle_loaded", "walk_loaded"}}
	h := hintsOf(mk(a, nil))
	want := "挂件 cargo 在 idle_loaded、walk_loaded 里显示；idle、walk、load、unload 里没有，也没有动作事件"
	if len(h) != 1 || !strings.Contains(h[0], want) || !strings.Contains(h[0], "display.events") {
		t.Errorf("A-155 配置应提示：%v", h)
	}
	t.Log(h)
	if h := hintsOf(mk(protocol.ComposeAttach{Socket: "crate", Asset: "x:y.z", Name: "cargo"}, nil)); len(h) != 0 {
		t.Errorf("clips 不写不该提示：%v", h)
	}
	a.Clips = clips
	if h := hintsOf(mk(a, nil)); len(h) != 0 {
		t.Errorf("clips 全覆盖不该提示：%v", h)
	}
	a.Clips = []string{"idle_loaded", "walk_loaded"}
	ev := map[string][]protocol.ComposeEvent{}
	for _, c := range []string{"idle", "walk", "load"} {
		ev[c] = []protocol.ComposeEvent{{T: 1, Do: protocol.EventAttach, Name: "cargo"}}
	}
	ev["unload"] = []protocol.ComposeEvent{{T: 1, Do: protocol.EventDetach, Name: "cargo"}}
	if h := hintsOf(mk(a, ev)); len(h) != 0 {
		t.Errorf("未覆盖的动作都有 attach / detach 事件不该提示：%v", h)
	}
	ev["unload"] = []protocol.ComposeEvent{{T: 1, Do: protocol.EventSound, Asset: "core:sfx.a"}}
	if h := hintsOf(mk(a, ev)); len(h) != 1 || !strings.Contains(h[0], "unload 里没有") {
		t.Errorf("只有 sound 事件的动作仍算没覆盖：%v", h)
	}
	long := Check{clips: []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"}, animsText: true,
		compose: protocol.Compose{Attach: []protocol.ComposeAttach{{Socket: "s", Asset: "x:y.z", Clips: []string{"a"}}}}}
	if h := hintsOf(long); len(h) != 1 || !strings.Contains(h[0], "b、c、d、e、f、g 等 8 个") {
		t.Errorf("动作多时应截断：%v", h)
	}
}
