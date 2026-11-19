package hub

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/NekroAI/kitestring/internal/store"
)

// 意见挂在模型动作的某一帧（R-392）：访客留言带帧和当前画面截图，放入后成为带帧、带附图的意见；
// 你在站内审核也能带帧；送给会话的正文写明「动作 run · 0.80 秒（v1）」，会话不用查库就知道是哪一帧。
func TestAssetFrameComments(t *testing.T) {
	e := newShareEnv(t)
	x := e.pushFixture()
	v := e.createShare(map[string]any{"name": "给动画师", "links": []string{"小王"}, "items": []map[string]any{{"kind": "asset_cat", "ref": "p1/车轮", "auto": true}}})
	l := linkByName(v, "小王")
	g := e.guest()
	base := "/api/share/" + l.Token
	shot := "data:image/png;base64," + base64.StdEncoding.EncodeToString(append(append([]byte{}, pngHead...), 'f'))

	// 截图不是图片：拒绝
	e.do(g, "POST", base+"/comments", map[string]any{"target_kind": "asset", "target_ref": x.wheel.Code, "text": "手穿模", "frame": map[string]any{"anim": "run", "t": 0.8, "v": 1}, "shot": "data:image/png;base64,aGVsbG8="}, nil, 400)

	var c GuestComment
	e.do(g, "POST", base+"/comments", map[string]any{"target_kind": "asset", "target_ref": x.wheel.Code, "version": 1, "text": "手穿模", "frame": map[string]any{"anim": " run ", "t": 0.80049, "v": 1}, "shot": shot}, &c, 200)
	if c.Frame == nil || c.Frame.Anim != "run" || c.Frame.T != 0.8 || c.Frame.V != 1 || c.Shot == nil || c.Shot.Mime != "image/png" {
		t.Fatalf("访客看到自己的留言带帧和截图：%+v %+v", c.Frame, c.Shot)
	}
	// 待收队列里所有者也看得到
	sc, err := e.st.GetShareComment(e.ctx, c.ID)
	if err != nil || sc.Frame == nil || sc.Shot == nil {
		t.Fatalf("待收的留言：%+v %v", sc, err)
	}
	var out struct {
		Comment store.ShareComment `json:"comment"`
	}
	e.do(e.owner, "POST", "/api/share-comments/"+c.ID+"/place", map[string]any{"as": "rework"}, &out, 200)
	d, _ := e.st.GetAsset(e.ctx, x.wheel.ID)
	rv := d.Reviews[0]
	if rv.Frame == nil || rv.Frame.Anim != "run" || len(rv.Files) != 1 || rv.Files[0].ID != c.Shot.ID {
		t.Fatalf("放入后的意见带帧和截图：%+v", rv)
	}
	dec, err := e.st.GetDecision(e.ctx, rv.DecisionCode)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(dec.Answer, "【动作 run · 0.80 秒（v1）】手穿模") {
		t.Fatalf("送达正文应写明帧：%s", dec.Answer)
	}

	// 站内审核带帧：通过时也写在附言里
	e.do(e.owner, "POST", "/api/assets/"+x.crate.Code+"/review", map[string]any{"verdict": "approved", "comment": "", "frame": map[string]any{"anim": "idle", "t": 1.5, "v": 1}}, nil, 200)
	d, _ = e.st.GetAsset(e.ctx, x.crate.ID)
	if d.Reviews[0].Frame == nil || d.Reviews[0].Frame.Anim != "idle" {
		t.Fatalf("站内审核的帧：%+v", d.Reviews[0])
	}
	dec, _ = e.st.GetDecision(e.ctx, d.Reviews[0].DecisionCode)
	if !strings.Contains(dec.Answer, "附言：【动作 idle · 1.50 秒（v1）】") {
		t.Fatalf("通过时的帧：%s", dec.Answer)
	}
	// 没写动作名的帧不记
	if store.CleanFrame(&store.AssetFrame{Anim: "  ", T: 1}) != nil {
		t.Fatal("空动作名的帧应丢掉")
	}
}

// 访客看不到项目字段，只留 display.animations（动作名、循环标记以 asset.json 为准）。
func TestGuestMetaKeepsAnimations(t *testing.T) {
	if m := guestMeta([]byte(`{"generation":{"script":"x.py"},"display":{"animations":{"run":{"loop":true}},"camera":1}}`)); string(m) != `{"display":{"animations":{"run":{"loop":true}}}}` {
		t.Fatalf("只留动作：%s", m)
	}
	if m := guestMeta([]byte(`{"generation":{"script":"x.py"}}`)); m != nil {
		t.Fatalf("没有动作时不给：%s", m)
	}
}
