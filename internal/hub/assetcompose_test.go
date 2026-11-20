package hub

import (
	"encoding/json"
	"testing"

	"github.com/NekroAI/kitestring/internal/protocol"
	"github.com/NekroAI/kitestring/internal/store"
)

// R-407：素材详情按 display.attach / refs 找到同项目里对应素材的 GLB；找不到的标 missing；
// 分享页同样给出，被挂的素材不在分享范围里时它的 GLB 也能给查看器取（不能下载），没被引用的范围外文件照旧 404。
func TestAssetCompose(t *testing.T) {
	e := newShareEnv(t)
	x := e.pushFixture()
	dogGLB := e.assetFile("dog.glb", glbMime, append(append([]byte{}, glbHead...), 'g'))
	boxGLB := e.assetFile("box.glb", glbMime, append(append([]byte{}, glbHead...), 'h'))
	sub := store.AssetSubmitter{Kind: store.SubmitterSession, Ref: e.dev1}
	meta := json.RawMessage(`{"display":{"attach":[{"socket":"crate","asset":"core:prop.box","clips":["carry"]},{"socket":"hand","asset":"core:prop.nothing"}],"refs":[{"asset":"core:prop.box","offset":[1,0,0]}]}}`)
	push := func(items ...protocol.AssetItem) {
		t.Helper()
		if _, err := e.st.PushAssets(e.ctx, store.NewAssetPush{ProjectID: "p1", Submitter: sub, ByID: true, Items: items}); err != nil {
			t.Fatal(err)
		}
	}
	push(protocol.AssetItem{Key: "core:prop.box", Kind: protocol.AssetPart, Category: "prop", File: &boxGLB, Name: "箱子"},
		protocol.AssetItem{Key: "core:vehicle.dog", Kind: protocol.AssetPart, Category: "vehicle", File: &dogGLB, Meta: meta})
	dog, err := e.st.GetAsset(e.ctx, "core:vehicle.dog")
	if err != nil {
		t.Fatal(err)
	}
	box, _ := e.st.GetAsset(e.ctx, "core:prop.box")

	var d store.AssetDetail
	e.do(e.owner, "GET", "/api/assets/"+dog.Code, nil, &d, 200)
	c := d.Latest.Compose
	if len(c) != 3 || c[0].Kind != "attach" || c[0].Name != "crate" || c[0].Code != box.Code || c[0].Title != "箱子" || c[0].File == nil || c[0].File.ID != boxGLB.ID || len(c[0].Clips) != 1 {
		t.Fatalf("挂件应找到箱子的 GLB：%+v", c)
	}
	if c[1].Missing != "asset" || c[1].File != nil || c[2].Kind != "ref" || c[2].File == nil || len(c[2].Offset) != 3 {
		t.Fatalf("找不到的标 missing，参照带 offset：%+v", c)
	}

	// 只改挂件配置重推：记为新的一版；配置不变的重推不新增
	meta2 := json.RawMessage(`{"display":{"attach":[{"socket":"crate","asset":"core:prop.box"}]}}`)
	push(protocol.AssetItem{Key: "core:vehicle.dog", Kind: protocol.AssetPart, Category: "vehicle", File: &dogGLB, Meta: meta2})
	push(protocol.AssetItem{Key: "core:vehicle.dog", Kind: protocol.AssetPart, Category: "vehicle", File: &dogGLB, Meta: meta2})
	if d2, _ := e.st.GetAsset(e.ctx, dog.ID); d2.Version != 2 {
		t.Fatalf("改挂件记新版本、不变不记：v%d", d2.Version)
	}

	// 分享只挂机械狗：访客详情里同样有组合预览；箱子的 GLB 查看器能取、不能下载；范围外没被引用的文件仍 404
	v := e.createShare(map[string]any{"name": "看机械狗", "links": []string{"小李"}, "items": []map[string]any{{"kind": "asset", "ref": dog.Code, "auto": true}}})
	g := e.guest()
	base := "/api/share/" + linkByName(v, "小李").Token
	var gd store.AssetDetail
	e.do(g, "GET", base+"/assets/"+dog.Code, nil, &gd, 200)
	if len(gd.Latest.Compose) != 1 || gd.Latest.Compose[0].File == nil || gd.Latest.Compose[0].File.ID != boxGLB.ID {
		t.Fatalf("访客详情的组合预览：%+v", gd.Latest.Compose)
	}
	e.do(g, "GET", base+"/assets/"+box.Code, nil, nil, 404) // 箱子本身不在分享里
	e.do(g, "GET", base+"/files/"+boxGLB.ID, nil, nil, 200, "Sec-Fetch-Dest", "empty")
	e.do(g, "GET", base+"/files/"+boxGLB.ID+"?download=1", nil, nil, 404)
	e.do(g, "GET", base+"/files/"+x.glb1.ID, nil, nil, 404, "Sec-Fetch-Dest", "empty")
}
