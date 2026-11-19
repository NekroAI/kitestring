package hub

import (
	"strings"
	"testing"

	"github.com/NekroAI/kitestring/internal/protocol"
	"github.com/NekroAI/kitestring/internal/store"
)

// R-380：Hub 收变体（key 要是「素材 id@变体名」）；ks asset supersede 的接口批量标记取代、有错一条不改；
// 分享挂在批次上，取代后访客照常能看旧条目，取代它的素材也在，分享页上带着取代关系；
// 网页删除前的检查列出引用着它的分享，删除后访客看不到。
func TestAssetLinksHub(t *testing.T) {
	e := newShareEnv(t)
	x := e.pushFixture() // 批次 S1：wheel（A-1）、crate（A-2）；S2：bolt（A-3）
	tok := "kst_asset-links"
	if _, err := e.st.CreateDevice(e.ctx, "links", HashToken(tok)); err != nil {
		t.Fatal(err)
	}
	glb := e.assetFile("wheel_v2.glb", glbMime, append(append([]byte{}, glbHead...), 'v'))
	snow := e.assetFile("wheel_snow.glb", glbMime, append(append([]byte{}, glbHead...), 's'))
	main := protocol.AssetItem{Key: "core:part.wheel", Kind: protocol.AssetPart, Category: "车轮", File: &glb, Variant: "default", Batches: []string{"S9"}}
	variant := protocol.AssetItem{Key: "core:part.wheel@snow", Parent: "core:part.wheel", Variant: "snow", Kind: protocol.AssetPart, Category: "车轮", File: &snow, Batches: []string{"S9"}}
	for _, c := range []struct {
		it   protocol.AssetItem
		want string
	}{
		{protocol.AssetItem{Key: "core:part.wheel@x", Parent: "core:part.wheel", Variant: "y", Kind: protocol.AssetPart, Category: "车轮", File: &snow}, "素材 id@变体名"},
		{protocol.AssetItem{Key: "core:part.wheel@a b", Parent: "core:part.wheel", Variant: "a b", Kind: protocol.AssetPart, Category: "车轮", File: &snow}, "变体名"},
		{protocol.AssetItem{Key: "core:nope@a", Parent: "core:nope", Variant: "a", Kind: protocol.AssetPart, Category: "车轮", File: &snow}, "不在资源库里"},
	} {
		if _, err := e.h.pushAssets(e.ctx, protocol.AssetPush{ByID: true, Project: "p1", SessionKey: e.dev1, Items: []protocol.AssetItem{c.it}}); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s：%v，应含 %q", c.it.Key, err, c.want)
		}
	}
	res, err := e.h.pushAssets(e.ctx, protocol.AssetPush{ByID: true, Project: "p1", SessionKey: e.dev1, Items: []protocol.AssetItem{main, variant}})
	if err != nil || len(res.Items) != 2 || res.Items[1].Code != "A-5" {
		t.Fatalf("素材和变体：%+v %v", res, err)
	}

	// 分享挂在批次 S1 上
	v := e.createShare(map[string]any{"name": "给外包", "links": []string{"小王"}, "items": []map[string]any{{"kind": "asset_batch", "ref": "p1/S1", "auto": true}}})
	g := e.guest()
	base := "/api/share/" + linkByName(v, "小王").Token

	// 取代：dry run 不改；有错一条不改；改好后标记
	var out struct {
		Applied bool                    `json:"applied"`
		Results []store.SupersedeResult `json:"results"`
	}
	pairs := []map[string]string{{"old": "S1/wheel", "new": "core:part.wheel"}, {"old": "A-2", "new": "A-2"}}
	e.agent(tok, "POST", "/api/agent/assets/supersede", map[string]any{"project": "p1", "pairs": pairs}, &out, 200)
	if out.Applied || out.Results[0].Result != "set" || out.Results[1].Result != "error" {
		t.Fatalf("有错的一批：%+v", out)
	}
	e.agent(tok, "POST", "/api/agent/assets/supersede", map[string]any{"project": "p1", "pairs": pairs[:1], "dry_run": true}, &out, 200)
	if out.Applied {
		t.Fatal("dry run 不应改")
	}
	e.agent(tok, "POST", "/api/agent/assets/supersede", map[string]any{"project": "p1", "pairs": pairs[:1]}, &out, 200)
	if !out.Applied || out.Results[0].NewRef == nil || out.Results[0].NewRef.Code != "A-4" {
		t.Fatalf("标记取代：%+v", out)
	}

	// 访客：旧条目照常能看，带着取代关系；取代它的素材和变体也在
	var list GuestAssetsView
	e.do(g, "GET", base+"/assets", nil, &list, 200)
	codes := map[string]store.Asset{}
	for _, a := range list.Assets {
		codes[a.Code] = a
	}
	if len(list.Assets) != 4 || codes["A-1"].SupersededBy == nil || codes["A-1"].SupersededBy.Code != "A-4" || codes["A-5"].Parent == nil {
		t.Fatalf("访客看到的素材：%+v", codes)
	}
	var wheel store.AssetDetail
	e.do(g, "GET", base+"/assets/"+x.wheel.Code, nil, &wheel, 200)
	if wheel.SupersededBy == nil || wheel.SupersededBy.UID != "core:part.wheel" {
		t.Fatalf("访客详情的取代关系：%+v", wheel.Asset)
	}
	var nw store.AssetDetail
	e.do(g, "GET", base+"/assets/A-4", nil, &nw, 200)
	if len(nw.Supersedes) != 1 || len(nw.Variants) != 1 {
		t.Fatalf("访客看取代它的素材：%+v", nw)
	}

	// 删除：先检查，列出引用着它的分享；删后访客看不到
	var info store.AssetDeleteInfo
	e.do(e.owner, "GET", "/api/assets/A-4/delete-check", nil, &info, 200)
	if len(info.Variants) != 1 || len(info.Supersedes) != 1 || len(info.Shares) != 0 {
		t.Fatalf("A-4 不在任何分享的批次里，只经取代带进来：%+v", info)
	}
	e.do(e.owner, "GET", "/api/assets/"+x.crate.Code+"/delete-check", nil, &info, 200)
	if len(info.Shares) != 1 || info.Shares[0].Name != "给外包" || info.Shares[0].Via != "asset_batch" || info.Shares[0].Ref != "S1" {
		t.Fatalf("crate 被批次 S1 的分享引用：%+v", info.Shares)
	}
	var del struct {
		Deleted []store.AssetRef `json:"deleted"`
	}
	e.do(e.owner, "DELETE", "/api/assets/"+x.crate.Code, nil, &del, 200)
	if len(del.Deleted) != 1 {
		t.Fatalf("删除：%+v", del)
	}
	e.do(g, "GET", base+"/assets/"+x.crate.Code, nil, nil, 404)
	e.do(e.owner, "DELETE", "/api/assets/"+x.crate.Code, nil, nil, 404)
	// Agent 不能删
	e.agent(tok, "DELETE", "/api/agent/assets/A-1", nil, nil, 405)
}
