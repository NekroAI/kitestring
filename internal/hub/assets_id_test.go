package hub

import (
	"strings"
	"testing"

	"github.com/NekroAI/kitestring/internal/protocol"
	"github.com/NekroAI/kitestring/internal/store"
)

// R-376：Hub 收按素材 id 的推送——不要求批次、id 和类别要合格；旧的按批次推送照样要批次、不收 id 推送专有的字段；
// 按 id 查找默认在会话所在项目里；访客上传新版本到 id 素材时仍是同一件素材。
func TestPushAssetsByIDHub(t *testing.T) {
	e := newShareEnv(t)
	x := e.pushFixture()
	glb := e.assetFile("carbine.glb", glbMime, append(append([]byte{}, glbHead...), 'z'))
	item := protocol.AssetItem{Key: "core:weapon.carbine", Kind: protocol.AssetPart, Category: "weapon", File: &glb, Batches: []string{"S1"}, Meta: []byte(`{"lod":2}`)}
	res, err := e.h.pushAssets(e.ctx, protocol.AssetPush{ByID: true, Project: "p1", SessionKey: e.dev1, Items: []protocol.AssetItem{item}})
	if err != nil || len(res.Items) != 1 || res.Items[0].Result != "created" {
		t.Fatalf("按 id 推送不要求批次：%+v %v", res, err)
	}
	for _, c := range []struct {
		p    protocol.AssetPush
		want string
	}{
		{protocol.AssetPush{ByID: true, Project: "p1", Items: []protocol.AssetItem{{Key: "carbine", Kind: protocol.AssetPart, Category: "weapon", File: &glb}}}, "命名空间"},
		{protocol.AssetPush{ByID: true, Project: "p1", Items: []protocol.AssetItem{{Key: "core:a", Kind: protocol.AssetPart, File: &glb}}}, "类别"},
		{protocol.AssetPush{Project: "p1", Items: []protocol.AssetItem{item}}, "批次"},
		{protocol.AssetPush{Project: "p1", Batch: "S9", Items: []protocol.AssetItem{item}}, "只用于按 asset.json 推送"},
	} {
		if _, err := e.h.pushAssets(e.ctx, c.p); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v：%v，应含 %q", c.p, err, c.want)
		}
	}
	d, err := e.st.FindAsset(e.ctx, "p1", "core:weapon.carbine")
	if err != nil || d.UID != "core:weapon.carbine" {
		t.Fatalf("按 id 找：%+v %v", d.Asset, err)
	}
	// 批次 S1 的列表里新旧两种素材都在
	list, _ := e.st.ListAssets(e.ctx, store.AssetFilter{ProjectID: "p1", Batch: "S1"})
	keys := []string{}
	for _, a := range list {
		keys = append(keys, a.Key)
	}
	if len(list) != 3 || !strings.Contains(strings.Join(keys, ","), "core:weapon.carbine") || x.wheel.Batch != "S1" {
		t.Fatalf("批次 S1 应含旧条目和 id 素材：%v", keys)
	}
}
