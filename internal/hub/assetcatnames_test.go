package hub

import (
	"strings"
	"testing"

	"github.com/NekroAI/kitestring/internal/protocol"
	"github.com/NekroAI/kitestring/internal/store"
)

// R-411：推送带上项目配置里的类别中文名，Hub 按项目存最新一份；列表 facets、分享页都带上；
// 格式不对（换行、太长）整批拒收；不带（没找到配置）不改已存的，带空表清掉。
func TestAssetCategoryNamesPush(t *testing.T) {
	e := newShareEnv(t)
	e.pushFixture()
	box := e.assetFile("box.glb", glbMime, append(append([]byte{}, glbHead...), 'b'))
	item := protocol.AssetItem{Key: "core:prop.box", Kind: protocol.AssetPart, Category: "prop", File: &box}
	push := func(names map[string]protocol.AssetCategoryName, has bool) error {
		_, err := e.h.pushAssets(e.ctx, protocol.AssetPush{Project: "p1", SessionKey: e.dev1, ByID: true, Items: []protocol.AssetItem{item}, CategoryNames: names, HasCategoryNames: has})
		return err
	}
	facets := func() store.AssetFacets {
		var out struct {
			Facets store.AssetFacets `json:"facets"`
		}
		e.do(e.owner, "GET", "/api/assets", nil, &out, 200)
		return out.Facets
	}
	names := map[string]protocol.AssetCategoryName{"prop": {Name: "道具", Subtypes: map[string]string{"crate": "箱子"}}}
	if err := push(names, true); err != nil {
		t.Fatal(err)
	}
	if f := facets(); f.CategoryNames["prop"].Name != "道具" || f.CategoryNames["prop"].Subtypes["crate"] != "箱子" {
		t.Fatalf("列表 facets 应带中文名：%+v", f.CategoryNames)
	}
	// 分享页同样有
	box2, _ := e.st.GetAsset(e.ctx, "core:prop.box")
	v := e.createShare(map[string]any{"name": "看箱子", "links": []string{"小李"}, "items": []map[string]any{{"kind": "asset", "ref": box2.Code, "auto": true}}})
	g := e.guest()
	var gv GuestAssetsView
	e.do(g, "GET", "/api/share/"+linkByName(v, "小李").Token+"/assets", nil, &gv, 200)
	if gv.Facets.CategoryNames["prop"].Name != "道具" {
		t.Fatalf("分享页 facets 应带中文名：%+v", gv.Facets.CategoryNames)
	}
	// 不带：不改
	if err := push(nil, false); err != nil {
		t.Fatal(err)
	}
	if facets().CategoryNames["prop"].Name != "道具" {
		t.Fatal("没带中文名的推送不该清掉已存的")
	}
	// 格式不对：整批拒收，已存的不变
	for _, bad := range []string{"道\n具", strings.Repeat("字", 65)} {
		if err := push(map[string]protocol.AssetCategoryName{"prop": {Name: bad}}, true); err == nil || !strings.Contains(err.Error(), "中文名") {
			t.Errorf("格式不对应拒收并说明：%v", err)
		}
	}
	if err := push(map[string]protocol.AssetCategoryName{"prop": {Subtypes: map[string]string{"crate": "箱\n子"}}}, true); err == nil {
		t.Error("细类中文名换行应拒收")
	}
	if facets().CategoryNames["prop"].Name != "道具" {
		t.Fatal("被拒收的推送不该改已存的")
	}
	// 带空表：清掉
	if err := push(nil, true); err != nil {
		t.Fatal(err)
	}
	if n := facets().CategoryNames; len(n) != 0 {
		t.Fatalf("带空表应清掉：%+v", n)
	}
}
