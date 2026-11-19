package hub

import (
	"net/http"
	"testing"

	"github.com/NekroAI/kitestring/internal/store"
)

// R-383：网页把只有 GLB 的零件渲染成缩略图回传——记下后详情带上，再交不覆盖；有预览图的零件、不是图片的文件、未登录都不收。
func TestAssetAutoThumbAPI(t *testing.T) {
	e := newShareEnv(t)
	x := e.pushFixture()
	th := e.assetFile("auto.png", "image/png", append(append([]byte{}, pngHead...), 't'))
	path := "/api/assets/" + x.other.Code + "/auto-thumb"
	var out struct {
		Saved bool `json:"saved"`
	}
	e.do(e.owner, "POST", path, map[string]any{"version": 1, "file": th.ID}, &out, 200)
	if !out.Saved {
		t.Fatal("第一次应记下")
	}
	e.do(e.owner, "POST", path, map[string]any{"version": 1, "file": th.ID}, &out, 200)
	if out.Saved {
		t.Fatal("已有时不覆盖")
	}
	var d store.AssetDetail
	e.do(e.owner, "GET", "/api/assets/"+x.other.Code, nil, &d, 200)
	if d.Versions[0].AutoThumb == nil || d.Versions[0].AutoThumb.ID != th.ID {
		t.Fatalf("详情应带自动缩略图：%+v", d.Versions[0])
	}
	e.do(e.owner, "POST", "/api/assets/"+x.wheel.Code+"/auto-thumb", map[string]any{"version": 1, "file": th.ID}, nil, 409)
	e.do(e.owner, "POST", path, map[string]any{"version": 1, "file": x.glb1.ID}, nil, 400)
	e.do(e.owner, "POST", path, map[string]any{"version": 7, "file": th.ID}, nil, 404)
	e.do(&http.Client{}, "POST", path, map[string]any{"version": 1, "file": th.ID}, nil, 401)
}
