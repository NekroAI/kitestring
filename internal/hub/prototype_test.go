package hub

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"

	"github.com/NekroAI/kitestring/internal/store"
)

func protoZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for n, c := range files {
		w, err := zw.Create(n)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.WriteString(w, c)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// uploadProto 用设备 token 交一个原型包。
func (e *shareEnv) uploadProto(tok string, zipBytes []byte, fields map[string]string, want int) protoUploadOut {
	e.t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, _ := mw.CreateFormFile("file", "proto.zip")
	_, _ = part.Write(zipBytes)
	for k, v := range fields {
		_ = mw.WriteField(k, v)
	}
	_ = mw.Close()
	req, _ := http.NewRequest("POST", e.srv.URL+"/api/agent/prototypes", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != want {
		e.t.Fatalf("交原型 → %d（应为 %d）：%s", resp.StatusCode, want, raw)
	}
	var out protoUploadOut
	if want == 200 {
		if err := json.Unmarshal(raw, &out); err != nil {
			e.t.Fatal(err)
		}
	}
	return out
}

func (e *shareEnv) agent(tok, method, path string, in, out any, want int) []byte {
	e.t.Helper()
	b, _ := json.Marshal(in)
	req, _ := http.NewRequest(method, e.srv.URL+path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != want {
		e.t.Fatalf("%s %s → %d（应为 %d）：%s", method, path, resp.StatusCode, want, raw)
	}
	if out != nil {
		_ = json.Unmarshal(raw, out)
	}
	return raw
}

func get(t *testing.T, c *http.Client, url string, headers ...string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

// 原型（R-197）：交包、版本、隔离路径的响应头、证据和决策里的快照、访客地址随链接失效。
func TestPrototypes(t *testing.T) {
	e := newShareEnv(t)
	tok := "kst_proto-test"
	if _, err := e.st.CreateDevice(e.ctx, "proto", HashToken(tok)); err != nil {
		t.Fatal(err)
	}
	page := `<!doctype html><html><head><title>x</title></head><body><a href="https://example.com">外链</a><script src="https://cdn.example.com/a.js"></script></body></html>`
	z := protoZip(t, map[string]string{"site/index.html": page, "site/app.css": "body{}"})

	// 校验：没有标题、不安全路径、入口找不到
	e.uploadProto(tok, z, map[string]string{}, 400)
	e.uploadProto(tok, protoZip(t, map[string]string{"../evil.html": "x"}), map[string]string{"title": "坏包"}, 400)
	e.uploadProto(tok, protoZip(t, map[string]string{"a.txt": "x"}), map[string]string{"title": "没入口"}, 400)

	p1 := e.uploadProto(tok, z, map[string]string{"title": "待命面板", "requirement": e.ra.Code}, 200)
	if p1.Version != 1 || p1.Entry != "site/index.html" || p1.Files != 2 || !strings.HasPrefix(p1.URL, "/p/") || len(p1.External) != 2 {
		t.Fatalf("第 1 版不对：%+v", p1)
	}
	p2 := e.uploadProto(tok, z, map[string]string{"title": "待命面板 ", "requirement": e.ra.Code}, 200)
	if p2.Version != 2 || p2.Lineage != p1.ID {
		t.Fatalf("同需求同标题应为第 2 版：%+v", p2)
	}

	// 隔离路径：不登录也能打开，每个响应都带 sandbox CSP（含 404），HTML 注入了外链提示脚本
	anon := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, body := get(t, anon, e.srv.URL+p1.URL)
	if resp.StatusCode != 200 || !strings.Contains(resp.Header.Get("Content-Security-Policy"), "sandbox allow-scripts allow-forms") || strings.Contains(resp.Header.Get("Content-Security-Policy"), "allow-same-origin") {
		t.Fatalf("原型页面响应不对：%d %v", resp.StatusCode, resp.Header)
	}
	if resp.Header.Get("Access-Control-Allow-Origin") != "*" || resp.Header.Get("Access-Control-Allow-Credentials") != "" {
		t.Fatalf("原型路径要允许无来源页面读自己的资源、但不带凭据：%v", resp.Header)
	}
	if !strings.Contains(body, "外部链接已停用") || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("应注入外链提示脚本：%s", body)
	}
	resp, _ = get(t, anon, e.srv.URL+strings.TrimSuffix(p1.URL, "index.html")+"nope.js")
	if resp.StatusCode != 404 || resp.Header.Get("Content-Security-Policy") == "" {
		t.Fatalf("404 也要带 CSP：%d %v", resp.StatusCode, resp.Header)
	}
	resp, _ = get(t, anon, e.srv.URL+"/p/not-a-token/index.html")
	if resp.StatusCode != 404 || resp.Header.Get("Content-Security-Policy") == "" {
		t.Fatalf("令牌不对时 404 并带 CSP：%d", resp.StatusCode)
	}
	resp, _ = get(t, anon, e.srv.URL+p1.DownloadURL)
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "application/zip" || !strings.Contains(resp.Header.Get("Content-Disposition"), "attachment") {
		t.Fatalf("下载整包不对：%d %v", resp.StatusCode, resp.Header)
	}
	resp, _ = get(t, anon, e.srv.URL+strings.TrimSuffix(p1.URL, "site/index.html"))
	if resp.StatusCode != http.StatusFound || !strings.HasSuffix(resp.Header.Get("Location"), "/site/index.html") {
		t.Fatalf("根路径应跳到入口：%d %v", resp.StatusCode, resp.Header)
	}

	// 无来源页面发出的请求带 Origin: null：即使带了登录 cookie 也是未登录
	resp, _ = get(t, e.owner, e.srv.URL+"/api/requirements", "Origin", "null")
	if resp.StatusCode != 401 {
		t.Fatalf("Origin: null 应当未登录：%d", resp.StatusCode)
	}
	if resp, _ = get(t, e.owner, e.srv.URL+"/api/requirements", "Sec-Fetch-Site", "cross-site"); resp.StatusCode != 401 {
		t.Fatalf("跨站请求应当未登录：%d", resp.StatusCode)
	}
	if resp, _ = get(t, e.owner, e.srv.URL+"/api/requirements", "Sec-Fetch-Site", "same-origin"); resp.StatusCode != 200 {
		t.Fatalf("同源请求照常：%d", resp.StatusCode)
	}

	// 证据：存快照，读出时换成最新版并带全部版本
	e.agent(tok, "POST", "/api/agent/requirements/"+e.ra.Code+"/evidence", map[string]any{"kind": "manual", "verdict": "原型能打开", "prototypes": []string{p1.ID}}, nil, 200)
	r, err := e.st.GetRequirement(e.ctx, e.ra.ID)
	if err != nil {
		t.Fatal(err)
	}
	ev := r.Evidence[len(r.Evidence)-1]
	if len(ev.Prototypes) != 1 || ev.Prototypes[0].ID != p2.ID || len(ev.Prototypes[0].Versions) != 2 || ev.Prototypes[0].Versions[1].ID != p1.ID {
		t.Fatalf("证据里的原型应显示最新版并带历史：%+v", ev.Prototypes)
	}

	// 决策：选项各附一个原型，要决策的点的选项也能挂；固定在提出时那一版
	other := e.uploadProto(tok, protoZip(t, map[string]string{"index.html": page}), map[string]string{"title": "上下堆叠"}, 200)
	var dec store.Decision
	e.agent(tok, "POST", "/api/agent/decisions", map[string]any{"question": "待命面板用哪种布局", "requirement": e.ra.Code, "options": []string{"左右分栏", "上下堆叠"},
		"option_prototypes": []string{p1.ID, other.ID}, "points": []map[string]any{{"question": "按钮放哪", "options": []map[string]any{{"label": "顶部", "prototype": p2.ID}, {"label": "底部"}}}}}, &dec, 200)
	d, err := e.st.GetDecision(e.ctx, dec.Code)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.OptionPrototypes) != 2 || d.OptionPrototypes[0][0].ID != p1.ID || d.OptionPrototypes[1][0].ID != other.ID || d.Points[0].Items[0].Prototype == nil {
		t.Fatalf("决策的原型不对：%+v %+v", d.OptionPrototypes, d.Points)
	}
	if o, _ := e.st.GetPrototype(e.ctx, other.ID); o.RequirementID != e.ra.ID {
		t.Fatalf("决策挂上需求后原型应补记需求：%+v", o)
	}

	// 访客：拿到的是由链接派生的地址，不暴露令牌；撤回链接后打不开
	v := e.createShare(map[string]any{"name": "原型", "links": []string{"小王"}, "items": []map[string]any{{"kind": "req", "ref": e.ra.Code}}})
	wang := linkByName(v, "小王")
	var gr struct {
		Requirement store.Requirement `json:"requirement"`
	}
	_, raw := e.do(e.guest(), "GET", "/api/share/"+wang.Token+"/requirements/"+e.ra.Code, nil, nil, 200)
	_ = json.Unmarshal(raw, &gr)
	if strings.Contains(string(raw), p1.URL) || strings.Contains(string(raw), p2.URL) {
		t.Fatalf("访客页不应看到原型令牌")
	}
	if !strings.Contains(string(raw), "/p/g/"+wang.ID+"/") {
		t.Fatalf("访客页应有访客原型地址：%s", raw)
	}
	i := strings.Index(string(raw), "/p/g/"+wang.ID+"/"+p2.ID+"/")
	gurl := string(raw)[i:]
	gurl = gurl[:strings.IndexByte(gurl, '"')]
	resp, body = get(t, anon, e.srv.URL+gurl)
	if resp.StatusCode != 200 || resp.Header.Get("Content-Security-Policy") == "" || !strings.Contains(body, "外链") {
		t.Fatalf("访客打开原型不对：%d %s", resp.StatusCode, gurl)
	}
	// 签名改一位、换成范围外的原型都打不开
	bad := strings.Replace(gurl, "/"+p2.ID+"/", "/"+p1.ID+"/", 1)
	if resp, _ = get(t, anon, e.srv.URL+bad); resp.StatusCode != 404 {
		t.Fatalf("签名对不上应 404：%d", resp.StatusCode)
	}
	e.do(e.owner, "PUT", "/api/shares/"+v.ID+"/links/"+wang.ID, map[string]any{"revoked": true}, nil, 200)
	if resp, _ = get(t, anon, e.srv.URL+gurl); resp.StatusCode == 200 {
		t.Fatalf("撤回链接后原型应打不开：%d", resp.StatusCode)
	}
}
