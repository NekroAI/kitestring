package hub

import (
	"net/http"
	"strings"
	"testing"

	"github.com/NekroAI/kitestring/internal/store"
)

// 访客留言（R-281 验收 6、7）：访客留言接口只能新建一条待收留言，写不了别的；对象必须在分享里；
// 所有者原样 / 改写放入（都可加附言，改写保留原话）后成为需求留言并写明来源，或忽略。

// counts 数一下访客可能借留言接口改动的东西：决策（含留言）条数、需求状态、待收留言条数。
func (e *shareEnv) counts() (decs int, status string, pending int) {
	e.t.Helper()
	if err := e.st.DB().QueryRow(`SELECT COUNT(*) FROM decisions`).Scan(&decs); err != nil {
		e.t.Fatal(err)
	}
	r, err := e.st.GetRequirement(e.ctx, e.ra.ID)
	if err != nil {
		e.t.Fatal(err)
	}
	if err := e.st.DB().QueryRow(`SELECT COUNT(*) FROM share_comments WHERE status = 'pending'`).Scan(&pending); err != nil {
		e.t.Fatal(err)
	}
	return decs, r.Status + "|" + r.Title + "|" + r.Detail, pending
}

func TestShareGuestCommentWriteOnly(t *testing.T) {
	e := newShareEnv(t)
	v := e.createShare(map[string]any{"name": "边界", "links": []string{"小王"}, "items": []map[string]any{
		{"kind": "plan", "ref": e.plan.Code, "auto": true}, {"kind": "req", "ref": e.rb.Code, "excluded": true},
	}})
	tok := linkByName(v, "小王").Token
	base := "/api/share/" + tok
	g := e.guest()
	evIn, err := e.st.AddEvidence(e.ctx, store.Evidence{RequirementID: e.ra.ID, SessionKey: e.dev1, Kind: "test", Summary: "测试全过"})
	if err != nil {
		t.Fatal(err)
	}
	evOut, err := e.st.AddEvidence(e.ctx, store.Evidence{RequirementID: e.rb.ID, Kind: "test", Summary: "范围外的证据"})
	if err != nil {
		t.Fatal(err)
	}
	decIn, err := e.st.CreateDecision(e.ctx, store.Decision{Question: "接口用分页吗", RequirementID: e.ra.ID, Kind: store.KindDecision, Options: []string{"用", "不用"}})
	if err != nil {
		t.Fatal(err)
	}
	decOut, err := e.st.CreateDecision(e.ctx, store.Decision{Question: "范围外的决策", RequirementID: e.rout.ID, Kind: store.KindDecision})
	if err != nil {
		t.Fatal(err)
	}
	tell, err := e.st.CreateDecision(e.ctx, store.Decision{Question: "团队消息", RequirementID: e.ra.ID, Kind: store.KindTell})
	if err != nil {
		t.Fatal(err)
	}
	other := e.createShare(map[string]any{"name": "别的分享", "links": []string{"老陈"}, "items": []map[string]any{{"kind": "req", "ref": e.rout.Code}}})

	d0, s0, p0 := e.counts()
	// 在需求、决策、证据、阶段上留言：只进待收队列
	ok := []map[string]any{
		{"target_kind": "req", "target_ref": e.ra.Code, "text": "接口要不要分页？"},
		{"target_kind": "dec", "target_ref": decIn.Code, "text": "我倾向分页"},
		{"target_kind": "evidence", "target_ref": evIn.ID, "text": "测试覆盖了撤回吗"},
		{"target_kind": "stage", "target_ref": e.plan.Stages[1].ID, "text": "第二阶段什么时候开始"},
	}
	for _, b := range ok {
		var c GuestComment
		e.do(g, "POST", base+"/comments", b, &c, 200)
		if c.Status != store.GuestPending || c.Text != b["text"] {
			t.Fatalf("留言应进待收队列：%+v", c)
		}
	}
	d1, s1, p1 := e.counts()
	if d1 != d0 || s1 != s0 || p1 != p0+4 {
		t.Fatalf("访客留言只能新建待收留言：决策 %d → %d，需求 %q → %q，待收 %d → %d", d0, d1, s0, s1, p0, p1)
	}

	// 借留言接口写别的：多余字段（状态、需求、放入）一律拒绝，什么都不改
	for _, b := range []map[string]any{
		{"target_kind": "req", "target_ref": e.ra.Code, "text": "x", "status": "placed"},
		{"target_kind": "req", "target_ref": e.ra.Code, "text": "x", "requirement_id": e.rout.ID},
		{"target_kind": "req", "target_ref": e.ra.Code, "text": "x", "decision_id": decIn.ID, "answer": "批准"},
		{"target_kind": "session", "target_ref": e.dev1, "text": "x"},
		{"target_kind": "plan", "target_ref": e.plan.Code, "text": "x"},
	} {
		code, raw := e.do(g, "POST", base+"/comments", b, nil, 0)
		if code/100 == 2 {
			t.Errorf("%v 不应被接受：%s", b, raw)
		}
	}
	// 对象不在分享里、不存在、别的分享的、团队消息：同一句 404
	for _, b := range [][2]string{
		{"req", e.rb.Code}, {"req", e.rout.Code}, {"req", "R-99999"}, {"req", e.rb.ID},
		{"dec", decOut.Code}, {"dec", tell.Code}, {"dec", "D-99999"},
		{"evidence", evOut.ID}, {"evidence", "01NOPE"},
		{"stage", "01NOPE"},
	} {
		_, raw := e.do(g, "POST", base+"/comments", map[string]any{"target_kind": b[0], "target_ref": b[1], "text": "x"}, nil, 404)
		if !strings.Contains(string(raw), shareNotIncluded) {
			t.Errorf("%v 应提示不在这个分享里：%s", b, raw)
		}
	}
	// 空的、太长的
	e.do(g, "POST", base+"/comments", map[string]any{"target_kind": "req", "target_ref": e.ra.Code, "text": "  "}, nil, 400)
	e.do(g, "POST", base+"/comments", map[string]any{"target_kind": "req", "target_ref": e.ra.Code, "text": strings.Repeat("长", store.GuestTextMax+1)}, nil, 400)
	// 别的路径、别的方法：一律 405
	for _, p := range []string{"/requirements/" + e.ra.Code + "/comments", "/comments/x", "/decisions/" + decIn.Code, "/comments/../unlock/x"} {
		e.do(g, "POST", base+p, map[string]any{"target_kind": "req", "target_ref": e.ra.Code, "text": "x"}, nil, 405)
	}
	for _, m := range []string{"PUT", "DELETE", "PATCH"} {
		e.do(g, m, base+"/comments", map[string]any{"target_kind": "req", "target_ref": e.ra.Code, "text": "x"}, nil, 405)
	}
	// 访客 token 不能调所有者的放入 / 忽略
	var list shareCommentsOut
	e.do(e.owner, "GET", "/api/share-comments", nil, &list.Body, 200)
	if len(list.Body.Comments) != 4 {
		t.Fatalf("所有者应看到 4 条待收留言：%+v", list.Body.Comments)
	}
	id := list.Body.Comments[0].ID
	for _, p := range []string{"/api/share-comments/" + id + "/place", "/api/share-comments/" + id + "/ignore"} {
		e.do(&http.Client{}, "POST", p, map[string]any{}, nil, 401, "Authorization", "Bearer "+tok, "Cookie", "ks_session="+tok+"; ks_share="+tok)
	}
	// 别的分享的链接不能在这个分享的内容上留言
	e.do(e.guest(), "POST", "/api/share/"+linkByName(other, "老陈").Token+"/comments", map[string]any{"target_kind": "req", "target_ref": e.ra.Code, "text": "x"}, nil, 404)
	d2, s2, p2 := e.counts()
	if d2 != d0 || s2 != s0 || p2 != p1 {
		t.Fatalf("被拒绝的请求不应改动任何东西：决策 %d → %d，需求 %q → %q，待收 %d → %d", d0, d2, s0, s2, p1, p2)
	}

	// 访客看到自己的留言
	var mine guestCommentsOut
	e.do(g, "GET", base+"/comments", nil, &mine.Body, 200)
	if len(mine.Body.Comments) != 4 || mine.Body.Comments[0].TargetKind != "stage" || mine.Body.Comments[0].TargetCode != e.plan.Code || mine.Body.Comments[0].TargetSeq != 2 {
		t.Fatalf("访客自己的留言不对：%+v", mine.Body.Comments)
	}

	// 链接撤回后不能再留言；限流
	e.do(e.owner, "PUT", "/api/shares/"+v.ID+"/links/"+linkByName(v, "小王").ID, map[string]any{"revoked": true}, nil, 200)
	e.do(g, "POST", base+"/comments", ok[0], nil, 410)
	e.do(e.owner, "PUT", "/api/shares/"+v.ID+"/links/"+linkByName(v, "小王").ID, map[string]any{"revoked": false}, nil, 200)
	for i := 4; i < guestRateMax; i++ {
		e.do(g, "POST", base+"/comments", ok[0], nil, 200)
	}
	e.do(g, "POST", base+"/comments", ok[0], nil, http.StatusTooManyRequests)
}

func TestShareGuestCommentPassword(t *testing.T) {
	e := newShareEnv(t)
	v := e.createShare(map[string]any{"name": "有密码", "password": "kite-0821", "links": []string{"小王"}, "items": []map[string]any{{"kind": "req", "ref": e.ra.Code}}})
	base := "/api/share/" + linkByName(v, "小王").Token
	g := e.guest()
	b := map[string]any{"target_kind": "req", "target_ref": e.ra.Code, "text": "x"}
	e.do(g, "POST", base+"/comments", b, nil, 401)
	e.do(g, "POST", base+"/unlock", map[string]any{"password": "kite-0821"}, nil, 200)
	e.do(g, "POST", base+"/comments", b, nil, 200)
}

func TestShareCommentPlace(t *testing.T) {
	e := newShareEnv(t)
	v := e.createShare(map[string]any{"name": "分享功能进展", "links": []string{"小王"}, "items": []map[string]any{{"kind": "plan", "ref": e.plan.Code, "auto": true}}})
	base := "/api/share/" + linkByName(v, "小王").Token
	g := e.guest()
	decIn, err := e.st.CreateDecision(e.ctx, store.Decision{Question: "接口用分页吗", RequirementID: e.ra.ID, Kind: store.KindDecision, Options: []string{"用", "不用"}})
	if err != nil {
		t.Fatal(err)
	}
	post := func(kind, ref, text string) string {
		var c GuestComment
		e.do(g, "POST", base+"/comments", map[string]any{"target_kind": kind, "target_ref": ref, "text": text}, &c, 200)
		return c.ID
	}
	raw := post("dec", decIn.Code, "我倾向分页，列表会很长")
	rw := post("req", e.ra.Code, "这个好像不太对吧，撤回以后还能看到？")
	ign := post("req", e.ra.Code, "随便说说")
	stg := post("stage", e.plan.Stages[1].ID, "第二阶段什么时候开始")

	var sv ShareView
	e.do(e.owner, "GET", "/api/shares/"+v.ID, nil, &sv, 200)
	if sv.Pending != 4 {
		t.Fatalf("分享上待收留言应为 4：%d", sv.Pending)
	}
	var list shareCommentsOut
	e.do(e.owner, "GET", "/api/share-comments", nil, &list.Body, 200)
	if len(list.Body.Comments) != 4 || list.Body.Comments[0].TargetCode != decIn.Code || list.Body.Comments[0].RequirementCode != e.ra.Code || list.Body.Comments[0].GuestName != "小王" || list.Body.Comments[0].ShareName != "分享功能进展" {
		t.Fatalf("待收留言不对：%+v", list.Body.Comments[0])
	}
	if c := list.Body.Comments[3]; len(c.Choices) != 1 || c.Choices[0].Code != e.rc.Code {
		t.Fatalf("阶段上的留言应能选这个阶段的需求：%+v", c.Choices)
	}

	// 原样放入 + 附言
	var p placeOut
	e.do(e.owner, "POST", "/api/share-comments/"+raw+"/place", map[string]any{"addend": "分页先不做"}, &p.Body, 200)
	d := p.Body.Decision
	if d.Kind != store.KindNote || d.RequirementID != e.ra.ID || d.Answer != "我倾向分页，列表会很长" || d.Guest == nil ||
		d.Guest.Name != "小王" || d.Guest.Share != "分享功能进展" || d.Guest.Original != "" || d.Guest.Addend != "分页先不做" || d.Guest.TargetCode != decIn.Code {
		t.Fatalf("原样放入不对：%+v %+v", d, d.Guest)
	}
	if d.SessionKey != e.pm && d.SessionKey != e.dev1 {
		t.Fatalf("放入的留言应送达关联会话：%q", d.SessionKey)
	}
	if p.Body.Comment.Status != store.GuestPlaced || p.Body.Comment.DecisionCode != d.Code {
		t.Fatalf("留言应已放入：%+v", p.Body.Comment)
	}
	e.do(e.owner, "POST", "/api/share-comments/"+raw+"/place", map[string]any{}, nil, http.StatusConflict)
	// 送达文字带来源
	if s := guestNoteText(d); !strings.Contains(s, "来自「小王」（经分享链接「分享功能进展」），由用户放入，针对 "+decIn.Code+"：我倾向分页") || !strings.Contains(s, "用户附言：分页先不做") {
		t.Fatalf("送达文字不对：%s", s)
	}

	// 改写放入：保留原话
	e.do(e.owner, "POST", "/api/share-comments/"+rw+"/place", map[string]any{"text": "撤回链接后访客页还能看到内容吗？"}, &p.Body, 200)
	if p.Body.Decision.Answer != "撤回链接后访客页还能看到内容吗？" || p.Body.Decision.Guest.Original != "这个好像不太对吧，撤回以后还能看到？" {
		t.Fatalf("改写放入应保留原话：%+v", p.Body.Decision.Guest)
	}
	// 需求上读得到来源（req_get 用的同一份数据）
	r, err := e.st.GetRequirement(e.ctx, e.ra.ID)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, f := range r.Feedback {
		if f.Guest != nil {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("需求留言里应有 2 条访客留言：%+v", r.Feedback)
	}
	// 访客页：看得到放入的正文和来源，看不到改写前的原话
	var rq guestReqOut
	_, body := e.do(g, "GET", base+"/requirements/"+e.ra.Code, nil, &rq.Body, 200)
	if strings.Contains(string(body), "这个好像不太对吧") || !strings.Contains(string(body), "撤回链接后访客页还能看到内容吗") {
		t.Fatalf("访客页不应看到改写前的原话：%s", body)
	}

	// 阶段上的留言：要选这个阶段里的需求
	e.do(e.owner, "POST", "/api/share-comments/"+stg+"/place", map[string]any{}, nil, 400)
	e.do(e.owner, "POST", "/api/share-comments/"+stg+"/place", map[string]any{"requirement": e.rout.Code}, nil, 400)
	e.do(e.owner, "POST", "/api/share-comments/"+stg+"/place", map[string]any{"requirement": e.rc.Code}, &p.Body, 200)
	if p.Body.Decision.RequirementID != e.rc.ID || p.Body.Decision.Guest.TargetKind != store.GuestOnStage || p.Body.Decision.Guest.TargetSeq != 2 {
		t.Fatalf("阶段留言放入不对：%+v", p.Body.Decision.Guest)
	}

	// 忽略：不写进需求
	d0, _, _ := e.counts()
	e.do(e.owner, "POST", "/api/share-comments/"+ign+"/ignore", nil, nil, 200)
	e.do(e.owner, "POST", "/api/share-comments/"+ign+"/ignore", nil, nil, http.StatusConflict)
	e.do(e.owner, "POST", "/api/share-comments/"+ign+"/place", map[string]any{}, nil, http.StatusConflict)
	if d1, _, _ := e.counts(); d1 != d0 {
		t.Fatalf("忽略不应写进需求")
	}
	e.do(e.owner, "GET", "/api/share-comments", nil, &list.Body, 200)
	if len(list.Body.Comments) != 0 {
		t.Fatalf("处理完应没有待收留言：%+v", list.Body.Comments)
	}
	// 访客看到处理结果
	var mine guestCommentsOut
	e.do(g, "GET", base+"/comments", nil, &mine.Body, 200)
	st := map[string]string{}
	for _, c := range mine.Body.Comments {
		st[c.ID] = c.Status
	}
	if st[raw] != store.GuestPlaced || st[ign] != store.GuestIgnored {
		t.Fatalf("访客看到的处理结果不对：%v", st)
	}
}
