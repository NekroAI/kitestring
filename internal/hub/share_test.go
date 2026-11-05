package hub

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NekroAI/kitestring/internal/store"
)

// 分享（R-281）的测试环境：一个计划（两个阶段）、一条计划外的需求，几个会话。
type shareEnv struct {
	t     *testing.T
	h     *Hub
	st    *store.Store
	srv   *httptest.Server
	owner *http.Client
	ctx   context.Context

	plan                 store.Plan
	ra, rb, rc, rout     store.Requirement
	dev1, dev2, dev3, pm string
}

func newShareEnv(t *testing.T) *shareEnv {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	h := New(st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	h.SetFilesDir(filepath.Join(dir, "files"))
	if err := os.MkdirAll(filepath.Join(dir, "files"), 0o700); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h.Handler(nil))
	t.Cleanup(srv.Close)
	jar, _ := cookiejar.New(nil)
	e := &shareEnv{t: t, h: h, st: st, srv: srv, owner: &http.Client{Jar: jar}, ctx: context.Background()}
	e.do(e.owner, "POST", "/api/auth/setup", map[string]any{"password": "password123"}, nil, 200)

	d, err := st.CreateDevice(e.ctx, "mac", "h1")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	e.dev1, e.dev2, e.dev3, e.pm = "claude:dev1", "claude:dev2", "claude:dev3", "claude:pm"
	for _, x := range [][4]string{{e.dev1, "dev", "分享后端", "running"}, {e.dev2, "dev", "别的需求", "idle"}, {e.dev3, "dev", "后来的会话", "idle"}, {e.pm, "pm", "排期", "parked"}} {
		if _, err := st.DB().Exec(`INSERT INTO sessions(key, tool, session_id, device_id, role, title, state, cwd, last_prompt, started_at, last_active_at) VALUES(?, 'claude', ?, ?, ?, ?, ?, '/Users/secret/repo', '做 R-1 的接口', ?, ?)`, x[0], x[0], d.ID, x[1], x[2], x[3], now, now); err != nil {
			t.Fatal(err)
		}
	}
	mk := func(title string) store.Requirement {
		r, err := st.CreateRequirement(e.ctx, store.Requirement{Title: title, Status: store.ReqInProgress, Priority: "P1", Detail: title + "的说明"})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	e.ra, e.rb, e.rc, e.rout = mk("分享后端"), mk("被取消的需求"), mk("第二阶段需求"), mk("不在分享里的需求")
	e.plan, err = st.CreatePlan(e.ctx, store.Plan{Title: "分享功能", Goal: "做分享", Status: "active"}, []store.StageInput{
		{Title: "后端", Requirements: []string{e.ra.ID, e.rb.ID}}, {Title: "网页", Requirements: []string{e.rc.ID}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range [][2]string{{e.ra.ID, e.dev1}, {e.ra.ID, e.pm}, {e.rb.ID, e.dev2}, {e.rout.ID, e.dev2}} {
		if err := st.LinkSession(e.ctx, l[0], l[1]); err != nil {
			t.Fatal(err)
		}
	}
	return e
}

// do 发请求；want 非 0 时核对状态码。返回状态码和正文。
func (e *shareEnv) do(c *http.Client, method, path string, in, out any, want int, headers ...string) (int, []byte) {
	e.t.Helper()
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, body)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := c.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if want != 0 && resp.StatusCode != want {
		e.t.Fatalf("%s %s → %d（应为 %d）：%s", method, path, resp.StatusCode, want, raw)
	}
	if out != nil && resp.StatusCode/100 == 2 {
		if err := json.Unmarshal(raw, out); err != nil {
			e.t.Fatalf("解析 %s 失败：%v", path, err)
		}
	}
	return resp.StatusCode, raw
}

func (e *shareEnv) guest() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar}
}

func (e *shareEnv) createShare(body map[string]any) ShareView {
	var v ShareView
	e.do(e.owner, "POST", "/api/shares", body, &v, 200)
	return v
}

func linkByName(v ShareView, name string) ShareLinkView {
	for _, l := range v.Links {
		if l.Name == name {
			return l
		}
	}
	return ShareLinkView{}
}

func nodeRefs(v ShareScopeView, kind string) []string {
	var out []string
	for _, n := range v.Nodes {
		if n.Kind == kind {
			out = append(out, n.Ref)
		}
	}
	return out
}

// 分层勾选、单独取消、「包含以后新增的」（验收 2）：计划后来新增的需求、需求后来关联的会话自动纳入；单独取消的不会回来。
func TestShareScope(t *testing.T) {
	e := newShareEnv(t)
	v := e.createShare(map[string]any{"name": "分享功能进展", "items": []map[string]any{
		{"kind": "plan", "ref": e.plan.Code, "auto": true},
		{"kind": "req", "ref": e.rb.Code, "excluded": true},
	}})
	if v.Status != store.ShareActive || v.Scope.Plans != 1 || v.Scope.Stages != 2 {
		t.Fatalf("范围不对：%+v", v.Scope)
	}
	reqs := nodeRefs(v.Scope, "req")
	if !slices.Contains(reqs, e.ra.ID) || !slices.Contains(reqs, e.rc.ID) || slices.Contains(reqs, e.rb.ID) || slices.Contains(reqs, e.rout.ID) {
		t.Fatalf("需求范围不对：%v", reqs)
	}
	sess := nodeRefs(v.Scope, "session")
	if !slices.Contains(sess, e.dev1) || !slices.Contains(sess, e.pm) || slices.Contains(sess, e.dev2) {
		t.Fatalf("会话范围不对：%v", sess)
	}
	if !v.HasSessions {
		t.Fatal("范围里有会话时 has_sessions 应为真（创建时提醒对话全文可见）")
	}
	// 后来新增：计划第二阶段放进一条新需求、R-a 后来关联 dev3
	rd, err := e.st.CreateRequirement(e.ctx, store.Requirement{Title: "后来加的", Status: store.ReqReady, Priority: "P2"})
	if err != nil {
		t.Fatal(err)
	}
	p, _ := e.st.GetPlan(e.ctx, e.plan.ID)
	if _, err := e.st.UpdatePlan(e.ctx, p, []store.StageInput{
		{ID: p.Stages[0].ID, Title: "后端", Requirements: []string{e.ra.ID, e.rb.ID}},
		{ID: p.Stages[1].ID, Title: "网页", Requirements: []string{e.rc.ID, rd.ID}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.st.LinkSession(e.ctx, e.ra.ID, e.dev3); err != nil {
		t.Fatal(err)
	}
	e.do(e.owner, "GET", "/api/shares/"+v.ID, nil, &v, 200)
	reqs, sess = nodeRefs(v.Scope, "req"), nodeRefs(v.Scope, "session")
	if !slices.Contains(reqs, rd.ID) || !slices.Contains(sess, e.dev3) {
		t.Fatalf("后来新增的应自动纳入：%v %v", reqs, sess)
	}
	if slices.Contains(reqs, e.rb.ID) || slices.Contains(sess, e.dev2) {
		t.Fatalf("单独取消的不应回来：%v %v", reqs, sess)
	}
	// 没开「包含以后新增的」：只认明确勾上的
	var pv ShareScopeView
	e.do(e.owner, "POST", "/api/shares/preview", map[string]any{"items": []map[string]any{{"kind": "req", "ref": e.ra.Code}}}, &pv, 200)
	if len(pv.Nodes) != 1 || pv.Nodes[0].Ref != e.ra.ID {
		t.Fatalf("只勾需求、不开 auto 时只有它自己：%+v", pv.Nodes)
	}
	// 单独取消一个会话：别的需求下也不再出现（同一会话各处同步）
	e.do(e.owner, "POST", "/api/shares/preview", map[string]any{"items": []map[string]any{
		{"kind": "req", "ref": e.ra.Code, "auto": true}, {"kind": "session", "ref": e.dev1, "excluded": true},
	}}, &pv, 200)
	if s := nodeRefs(pv, "session"); slices.Contains(s, e.dev1) || !slices.Contains(s, e.pm) {
		t.Fatalf("单独取消的会话不应出现：%v", s)
	}
	// 不存在的内容不能挂
	e.do(e.owner, "POST", "/api/shares", map[string]any{"name": "x", "items": []map[string]any{{"kind": "req", "ref": "R-99999"}}}, nil, 400)
}

// 访客链接、密码、撤回、到期（验收 3）。
func TestShareGuestAccess(t *testing.T) {
	e := newShareEnv(t)
	v := e.createShare(map[string]any{"name": "分享功能进展", "password": "kite-0821", "links": []string{"小王", "老陈"},
		"items": []map[string]any{{"kind": "req", "ref": e.ra.Code, "auto": true}}})
	wang, chen := linkByName(v, "小王"), linkByName(v, "老陈")
	if !strings.HasPrefix(wang.Token, sharePrefix) || wang.Path != "/s/"+wang.Token || wang.URL != "" || wang.Token == chen.Token {
		t.Fatalf("链接不对：%+v %+v", wang, chen)
	}
	base := "/api/share/" + wang.Token
	g := e.guest()
	var meta guestMetaOut
	e.do(g, "GET", base, nil, &meta.Body, 200)
	if !meta.Body.NeedsPassword || meta.Body.Unlocked || meta.Body.Guest != "小王" || meta.Body.Name != "分享功能进展" {
		t.Fatalf("访客信息不对：%+v", meta.Body)
	}
	e.do(g, "GET", base+"/overview", nil, nil, 401)
	e.do(g, "POST", base+"/unlock", map[string]any{"password": "wrong"}, nil, 401)
	e.do(g, "POST", base+"/unlock", map[string]any{"password": "kite-0821"}, nil, 200)
	var ov guestOverviewOut
	e.do(g, "GET", base+"/overview", nil, &ov.Body, 200)
	if !slices.Contains(ov.Body.Codes, e.ra.Code) || ov.Body.Counts[store.ReqInProgress] != 1 {
		t.Fatalf("概览不对：%+v", ov.Body)
	}
	// 一条链接的 cookie 不能用在另一条链接上
	_, raw := e.do(g, "GET", "/api/share/"+chen.Token+"/overview", nil, nil, 401)
	if !strings.Contains(string(raw), sharePasswordNeeded) {
		t.Fatalf("另一条链接应要求输密码：%s", raw)
	}
	g2 := e.guest()
	e.do(g2, "POST", "/api/share/"+chen.Token+"/unlock", map[string]any{"password": "kite-0821"}, nil, 200)

	// 撤回小王：下一次访问即拒绝，老陈不受影响；恢复后又能看
	e.do(e.owner, "PUT", "/api/shares/"+v.ID+"/links/"+wang.ID, map[string]any{"revoked": true}, nil, 200)
	_, raw = e.do(g, "GET", base+"/overview", nil, nil, 410)
	if !strings.Contains(string(raw), shareLinkRevoked) {
		t.Fatalf("撤回后应提示链接已撤回：%s", raw)
	}
	e.do(g, "GET", base, nil, nil, 410)
	e.do(g2, "GET", "/api/share/"+chen.Token+"/overview", nil, nil, 200)
	e.do(e.owner, "PUT", "/api/shares/"+v.ID+"/links/"+wang.ID, map[string]any{"revoked": false}, nil, 200)
	e.do(g, "GET", base+"/overview", nil, nil, 200)

	// 改密码：已输过旧密码的访客要重新输
	e.do(e.owner, "PUT", "/api/shares/"+v.ID, map[string]any{"password": "new-pass"}, nil, 200)
	e.do(g, "GET", base+"/overview", nil, nil, 401)
	e.do(g, "POST", base+"/unlock", map[string]any{"password": "new-pass"}, nil, 200)
	e.do(g, "GET", base+"/overview", nil, nil, 200)

	// 到期：全部链接失效；重新启用要给新的有效期
	if _, err := e.st.DB().Exec(`UPDATE shares SET expires_at = ? WHERE id = ?`, time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano), v.ID); err != nil {
		t.Fatal(err)
	}
	_, raw = e.do(g, "GET", base+"/overview", nil, nil, 410)
	if !strings.Contains(string(raw), shareExpiredText) {
		t.Fatalf("到期后应提示分享已到期：%s", raw)
	}
	e.do(g2, "GET", "/api/share/"+chen.Token+"/overview", nil, nil, 410)
	var got ShareView
	e.do(e.owner, "GET", "/api/shares/"+v.ID, nil, &got, 200)
	if got.Status != store.ShareExpired {
		t.Fatalf("状态应为已失效：%s", got.Status)
	}
	e.do(e.owner, "POST", "/api/shares/"+v.ID+"/enable", map[string]any{}, nil, 400)
	got = ShareView{}
	e.do(e.owner, "POST", "/api/shares/"+v.ID+"/enable", map[string]any{"forever": true}, &got, 200)
	if got.Status != store.ShareActive || got.ExpiresAt != nil {
		t.Fatalf("重新启用后应生效中、永久：%+v", got)
	}
	e.do(g, "GET", base+"/overview", nil, nil, 200)
	e.do(g2, "POST", "/api/share/"+chen.Token+"/unlock", map[string]any{"password": "new-pass"}, nil, 200)

	// 撤回整个分享：所有链接拒绝；重新启用恢复
	e.do(e.owner, "POST", "/api/shares/"+v.ID+"/revoke", nil, nil, 200)
	_, raw = e.do(g2, "GET", "/api/share/"+chen.Token+"/overview", nil, nil, 410)
	if !strings.Contains(string(raw), shareRevokedText) {
		t.Fatalf("撤回后应提示分享已撤回：%s", raw)
	}
	var list sharesOut
	e.do(e.owner, "GET", "/api/shares", nil, &list.Body, 200)
	if len(list.Body.Shares) != 1 || list.Body.Shares[0].Status != store.ShareRevoked {
		t.Fatalf("列表里应是已撤回：%+v", list.Body)
	}
	e.do(e.owner, "POST", "/api/shares/"+v.ID+"/enable", nil, nil, 200)
	e.do(g2, "GET", "/api/share/"+chen.Token+"/overview", nil, nil, 200)

	// 乱写的 token
	e.do(g, "GET", "/api/share/kss_nope/overview", nil, nil, 404)
	e.do(g, "GET", "/api/share/"+strings.TrimPrefix(wang.Token, sharePrefix)+"/overview", nil, nil, 404)

	// 填了对外地址时给完整链接
	e.do(e.owner, "PUT", "/api/settings", map[string]any{"public_url": "https://ks.example/", "ai": map[string]any{}, "summary_base_url": "", "summary_model": "", "notify_default": map[string]any{}}, nil, 200)
	e.do(e.owner, "GET", "/api/shares/"+v.ID, nil, &got, 200)
	if l := linkByName(got, "小王"); l.URL != "https://ks.example/s/"+l.Token {
		t.Fatalf("完整链接不对：%s", l.URL)
	}
}

// 免登录访问只读、范围外取不到（验收 6）：写接口全部拒绝；不在分享里的需求、决策、会话、附件猜编号也取不到。
func TestShareGuestBoundary(t *testing.T) {
	e := newShareEnv(t)
	v := e.createShare(map[string]any{"name": "边界", "links": []string{"小王"}, "items": []map[string]any{
		{"kind": "plan", "ref": e.plan.Code, "auto": true}, {"kind": "req", "ref": e.rb.Code, "excluded": true},
	}})
	tok := linkByName(v, "小王").Token
	base := "/api/share/" + tok
	g := e.guest()

	// 附件：范围内证据的、范围外需求证据的、团队消息的、没人引用的
	file := func(name string) store.FileRef {
		f, err := e.st.CreateFile(e.ctx, name, "image/png", "x", 3, "user")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(e.h.filePath(f.ID), []byte("png"), 0o600); err != nil {
			t.Fatal(err)
		}
		return f
	}
	fIn, fOut, fTell, fLoose := file("in.png"), file("out.png"), file("tell.png"), file("loose.png")
	if _, err := e.st.AddEvidence(e.ctx, store.Evidence{RequirementID: e.ra.ID, SessionKey: e.dev1, Kind: "screenshot", Summary: "截图", Files: []store.FileRef{fIn}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.AddEvidence(e.ctx, store.Evidence{RequirementID: e.rb.ID, Kind: "screenshot", Summary: "截图", Files: []store.FileRef{fOut}}); err != nil {
		t.Fatal(err)
	}
	decIn, err := e.st.CreateDecision(e.ctx, store.Decision{Question: "接口用分页吗", RequirementID: e.ra.ID, Kind: store.KindDecision, Options: []string{"用", "不用"}, OriginSession: e.dev2})
	if err != nil {
		t.Fatal(err)
	}
	decOut, err := e.st.CreateDecision(e.ctx, store.Decision{Question: "被取消需求的决策", RequirementID: e.rb.ID, Kind: store.KindDecision})
	if err != nil {
		t.Fatal(err)
	}
	tell, err := e.st.CreateDecision(e.ctx, store.Decision{Question: "团队消息", RequirementID: e.ra.ID, Kind: store.KindTell, ContextFiles: []store.FileRef{fTell}})
	if err != nil {
		t.Fatal(err)
	}

	// 范围内的照常能看
	var rq guestReqOut
	e.do(g, "GET", base+"/requirements/"+e.ra.Code, nil, &rq.Body, 200)
	if rq.Body.Requirement.Title != e.ra.Title || len(rq.Body.Decisions) != 1 || rq.Body.Decisions[0].Code != decIn.Code {
		t.Fatalf("需求详情不对：%+v", rq.Body)
	}
	if rq.Body.Decisions[0].OriginSession != "" {
		t.Fatalf("范围外会话的键不应给访客：%q", rq.Body.Decisions[0].OriginSession)
	}
	if slices.Contains(rq.Body.Requirement.Sessions, e.dev2) || !slices.Contains(rq.Body.Requirement.Sessions, e.dev1) {
		t.Fatalf("关联会话只留范围内的：%v", rq.Body.Requirement.Sessions)
	}
	e.do(g, "GET", base+"/decisions/"+decIn.Code, nil, nil, 200)
	e.do(g, "GET", base+"/files/"+fIn.ID, nil, nil, 200)
	e.do(g, "GET", base+"/plans/"+e.plan.Code, nil, nil, 200)

	// 范围外的、不存在的：同一句 404
	for _, p := range []string{
		"/requirements/" + e.rb.Code, "/requirements/" + e.rout.Code, "/requirements/" + e.rb.ID, "/requirements/R-99999",
		"/decisions/" + decOut.Code, "/decisions/" + tell.Code, "/decisions/D-99999",
		"/sessions/" + e.dev2, "/sessions/claude:nobody",
		"/files/" + fOut.ID, "/files/" + fTell.ID, "/files/" + fLoose.ID, "/files/01NOPE",
	} {
		_, raw := e.do(g, "GET", base+p, nil, nil, 404)
		if !strings.Contains(string(raw), shareNotIncluded) {
			t.Errorf("%s 应提示不在这个分享里：%s", p, raw)
		}
	}
	// 计划视图里范围外的需求只给编号
	var pl store.Plan
	e.do(g, "GET", base+"/plans/"+e.plan.Code, nil, &pl, 200)
	for _, st := range pl.Stages {
		for _, r := range st.Requirements {
			if r.Code == e.rb.Code && (r.Title != "" || r.ID != "") {
				t.Fatalf("范围外需求只给编号：%+v", r)
			}
		}
	}

	// 会话：主调度只给状态和一句话，不给对话；开发会话给轮；不给目录
	var ss guestSessionOut
	_, raw := e.do(g, "GET", base+"/sessions/"+e.pm, nil, &ss.Body, 200)
	if ss.Body.Session.Conversation || len(ss.Body.Turns) != 0 || ss.Body.Session.Line == "" {
		t.Fatalf("主调度不给对话：%+v", ss.Body)
	}
	_, raw = e.do(g, "GET", base+"/sessions/"+e.dev1, nil, &ss.Body, 200)
	if !ss.Body.Session.Conversation || strings.Contains(string(raw), "/Users/secret") {
		t.Fatalf("开发会话应能点进对话、不带本机目录：%s", raw)
	}
	var ov guestOverviewOut
	_, raw = e.do(g, "GET", base+"/overview", nil, &ov.Body, 200)
	if strings.Contains(string(raw), e.rout.Title) || strings.Contains(string(raw), e.rb.Title) || strings.Contains(string(raw), e.dev2) {
		t.Fatalf("概览里不应有范围外的内容：%s", raw)
	}

	// 写接口全部拒绝：带上访客 token 和访客 cookie（各种写法）挨个打一遍所有非 GET 接口
	param := regexp.MustCompile(`\{[^}]+\}`)
	n := 0
	for path, item := range e.h.OpenAPI().Paths {
		for method, op := range map[string]*huma.Operation{"POST": item.Post, "PUT": item.Put, "DELETE": item.Delete, "PATCH": item.Patch} {
			if op == nil {
				continue
			}
			if strings.HasPrefix(path, "/api/auth/") || path == "/api/share/{token}/unlock" {
				continue // 登录、首次设置、输访问密码本来就不用登录
			}
			p := param.ReplaceAllStringFunc(path, func(s string) string {
				if s == "{token}" {
					return tok
				}
				return e.ra.Code
			})
			code, raw := e.do(&http.Client{}, method, p, map[string]any{}, nil, 0,
				"Authorization", "Bearer "+tok, "Cookie", "ks_session="+tok+"; ks_share="+tok)
			n++
			want := http.StatusUnauthorized
			if strings.HasPrefix(path, "/api/share/") {
				want = http.StatusMethodNotAllowed
			}
			if code != want {
				t.Errorf("%s %s 用访客身份应为 %d，得到 %d：%s", method, path, want, code, raw)
			}
		}
	}
	if n < 50 {
		t.Fatalf("只检查了 %d 个写接口，OpenAPI 可能没取全", n)
	}
	// 访客路径上的写方法
	for _, m := range []string{"POST", "PUT", "DELETE", "PATCH"} {
		e.do(g, m, base+"/requirements/"+e.ra.Code, map[string]any{}, nil, 405)
		e.do(g, m, base+"/overview", map[string]any{}, nil, 405)
	}
	// 访客 token 也不能读所有者接口
	for _, p := range []string{"/api/shares", "/api/requirements", "/api/sessions", "/api/files/" + fIn.ID, "/api/stream", "/api/agent/requirements"} {
		e.do(&http.Client{}, "GET", p, nil, nil, 401, "Authorization", "Bearer "+tok, "Cookie", "ks_session="+tok)
	}
}

// 访客的实时通道（验收 4）：内容一变就发 changed；链接撤回时发 gone 并断开。
func TestShareGuestStream(t *testing.T) {
	e := newShareEnv(t)
	v := e.createShare(map[string]any{"name": "实时", "links": []string{"小王"}, "items": []map[string]any{{"kind": "req", "ref": e.ra.Code, "auto": true}}})
	l := linkByName(v, "小王")
	resp, err := http.Get(e.srv.URL + "/api/share/" + l.Token + "/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("实时通道 %d", resp.StatusCode)
	}
	events := make(chan string, 16)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if ev, ok := strings.CutPrefix(sc.Text(), "event: "); ok {
				events <- ev
			}
		}
		close(events)
	}()
	next := func(want string) {
		t.Helper()
		deadline := time.After(5 * time.Second)
		for {
			select {
			case ev, ok := <-events:
				if !ok {
					t.Fatalf("通道断了，没等到 %s", want)
				}
				if ev == want {
					return
				}
			case <-deadline:
				t.Fatalf("没等到 %s", want)
			}
		}
	}
	next("hello")
	if _, err := e.st.SetRequirementStatus(e.ctx, e.ra.ID, store.ReqReview); err != nil {
		t.Fatal(err)
	}
	e.h.broker.publish("requirements")
	next("changed")
	e.do(e.owner, "PUT", "/api/shares/"+v.ID+"/links/"+l.ID, map[string]any{"revoked": true}, nil, 200)
	next("gone")
	// 撤回的链接连不上实时通道
	r2, err := http.Get(e.srv.URL + "/api/share/" + l.Token + "/stream")
	if err != nil {
		t.Fatal(err)
	}
	r2.Body.Close()
	if r2.StatusCode != http.StatusGone {
		t.Fatalf("撤回后实时通道应拒绝：%d", r2.StatusCode)
	}
}
