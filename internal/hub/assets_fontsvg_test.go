package hub

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/NekroAI/kitestring/internal/assetfmt"
	"github.com/NekroAI/kitestring/internal/protocol"
	"github.com/NekroAI/kitestring/internal/store"
)

// postAssetFile 直接调 Hub 收资源库文件的处理函数（设备令牌那层另有测试），返回状态码和正文。
func postAssetFile(t *testing.T, h *Hub, name string, body []byte) (int, assetFileOut, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", name)
	_, _ = fw.Write(body)
	mw.Close()
	req := httptest.NewRequest("POST", "/api/agent/assets/files", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	h.handleAssetFile(rec, req)
	var out assetFileOut
	if rec.Code == 200 {
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
	}
	return rec.Code, out, rec.Body.String()
}

// R-387：资源库收字体（TTF / OTF / WOFF2）和 SVG。Hub 按内容认类型、存下后完整校验（扩展名对、内容不对的拒收）；
// 字体是 font 类素材，SVG 是独立的图；同一套版本、变体和审核（通过 / 返工 / 意见）；SVG 返回时带禁止脚本的内容安全策略、
// 按附件下载、nosniff；分享页能看 SVG、取字体文件（不算下载原文件），访客上传的字体可收下为新版本。
func TestPushFontSVGHub(t *testing.T) {
	for m, b := range map[string][]byte{assetfmt.MimeTTF: assetfmt.SynthFont(assetfmt.MimeTTF), assetfmt.MimeOTF: assetfmt.SynthFont(assetfmt.MimeOTF), assetfmt.MimeWOFF2: assetfmt.SynthFont(assetfmt.MimeWOFF2), svgMime: []byte(assetfmt.ScriptSVG)} {
		if got := assetFileMime(b); got != m {
			t.Fatalf("文件头认 %s：得 %q", m, got)
		}
	}
	if assetFileMime([]byte("<html><script>")) != "" || assetFileMime([]byte(`<?xml version="1.0"?><note/>`)) != "" {
		t.Fatal("HTML、别的 XML 不收")
	}
	e := newShareEnv(t)
	e.pushFixture()

	// 收文件：内容校验，扩展名骗不过
	code, ttfOut, raw := postAssetFile(t, e.h, "ui.ttf", assetfmt.SynthFont(assetfmt.MimeTTF))
	if code != 200 || ttfOut.Mime != assetfmt.MimeTTF {
		t.Fatalf("收 TTF：%d %s", code, raw)
	}
	if code, _, raw := postAssetFile(t, e.h, "fake.ttf", []byte("\x00\x01\x00\x00garbage-not-a-font")); code != 400 || !strings.Contains(raw, "校验不过") {
		t.Fatalf("文件头对、结构坏的字体要拒收：%d %s", code, raw)
	}
	if code, _, raw := postAssetFile(t, e.h, "fake.svg", []byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"><g></svg>`)); code != 400 || !strings.Contains(raw, "校验不过") {
		t.Fatalf("坏 SVG 要拒收：%d %s", code, raw)
	}
	if code, _, _ := postAssetFile(t, e.h, "page.svg", []byte("<html><body>hi</body></html>")); code != 400 {
		t.Fatal("扩展名是 .svg 的 HTML 要拒收")
	}
	code, svgOut, raw := postAssetFile(t, e.h, "icon.svg", []byte(assetfmt.ScriptSVG))
	if code != 200 || svgOut.Mime != svgMime {
		t.Fatalf("收 SVG：%d %s", code, raw)
	}

	// 推送：字体是 font 类，SVG 是独立的图；类型对不上的拒绝
	ttf := protocol.AssetFile{ID: ttfOut.ID, Name: ttfOut.Name, Size: ttfOut.Size}
	svg := protocol.AssetFile{ID: svgOut.ID, Name: svgOut.Name, Size: svgOut.Size}
	woff := e.assetFile("ui_bold.woff2", assetfmt.MimeWOFF2, assetfmt.SynthFont(assetfmt.MimeWOFF2))
	res, err := e.h.pushAssets(e.ctx, protocol.AssetPush{ByID: true, Project: "p1", SessionKey: e.dev1, Items: []protocol.AssetItem{
		{Key: "core:ui.font", Kind: protocol.AssetFont, Category: "ui", File: &ttf, Variant: "default"},
		{Key: "core:ui.font@bold", Parent: "core:ui.font", Variant: "bold", Kind: protocol.AssetFont, Category: "ui", File: &woff},
		{Key: "core:ui.icon", Kind: protocol.AssetImage, Category: "ui", File: &svg, Pixels: []int{64, 48}},
	}})
	if err != nil || len(res.Items) != 3 {
		t.Fatalf("推送字体和 SVG：%+v %v", res, err)
	}
	if _, err := e.h.pushAssets(e.ctx, protocol.AssetPush{ByID: true, Project: "p1", Items: []protocol.AssetItem{{Key: "core:ui.x", Kind: protocol.AssetFont, Category: "ui", File: &svg}}}); err == nil || !strings.Contains(err.Error(), "TTF、OTF 或 WOFF2") {
		t.Fatalf("字体的主文件要是字体：%v", err)
	}
	if _, err := e.h.pushAssets(e.ctx, protocol.AssetPush{ByID: true, Project: "p1", Items: []protocol.AssetItem{{Key: "core:ui.y", Kind: protocol.AssetImage, Category: "ui", File: &ttf}}}); err == nil {
		t.Fatal("字体不能当图推")
	}
	font, _ := e.st.FindAsset(e.ctx, "p1", "core:ui.font")
	icon, _ := e.st.FindAsset(e.ctx, "p1", "core:ui.icon")
	if font.Kind != protocol.AssetFont || font.Latest.File.Mime != assetfmt.MimeTTF || icon.Kind != protocol.AssetImage || icon.Latest.File.Mime != svgMime || len(font.Variants) != 1 {
		t.Fatalf("存下的类型：%+v / %+v", font.Asset, icon.Asset)
	}

	// 审核：字体返工带意见、SVG 通过，和别的素材一样
	e.do(e.owner, "POST", "/api/assets/"+font.Code+"/review", map[string]any{"verdict": "rework", "comment": "中文字形缺字", "version": 1}, nil, 200)
	e.do(e.owner, "POST", "/api/assets/"+icon.Code+"/review", map[string]any{"verdict": "approved", "version": 1}, nil, 200)
	font, _ = e.st.FindAsset(e.ctx, "p1", "core:ui.font")
	icon, _ = e.st.FindAsset(e.ctx, "p1", "core:ui.icon")
	if font.Status != store.AssetRework || icon.Status != store.AssetApproved || len(font.Reviews) == 0 || font.Reviews[0].Comment != "中文字形缺字" {
		t.Fatalf("审核：%s %s %+v", font.Status, icon.Status, font.Reviews)
	}

	// SVG 的响应：按附件、禁止脚本的 CSP、nosniff；PNG 照旧在页面里显示
	check := func(id string) http.Header {
		req, _ := http.NewRequest("GET", e.srv.URL+"/api/files/"+id, nil)
		resp, err := e.owner.Do(req)
		if err != nil || resp.StatusCode != 200 {
			t.Fatalf("取文件 %s：%v %v", id, resp, err)
		}
		resp.Body.Close()
		return resp.Header
	}
	hd := check(svg.ID)
	csp := hd.Get("Content-Security-Policy")
	if hd.Get("Content-Type") != svgMime || hd.Get("X-Content-Type-Options") != "nosniff" || !strings.HasPrefix(hd.Get("Content-Disposition"), "attachment") ||
		!strings.Contains(csp, "sandbox") || strings.Contains(csp, "allow-scripts") || !strings.Contains(csp, "default-src 'none'") || strings.Contains(csp, "script-src") {
		t.Fatalf("SVG 的响应头：%v", hd)
	}
	if hd := check(ttf.ID); hd.Get("Content-Type") != assetfmt.MimeTTF || !strings.HasPrefix(hd.Get("Content-Disposition"), "attachment") || hd.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("字体的响应头：%v", hd)
	}

	// 分享：访客看 SVG（<img> 取图）、取字体（fetch）不算下载；直接打开原文件算下载，默认关
	v := e.createShare(map[string]any{"name": "界面", "links": []string{"小李"}, "items": []map[string]any{{"kind": "asset_cat", "ref": "p1/ui", "auto": true}}})
	l := linkByName(v, "小李")
	e.do(e.owner, "PUT", "/api/shares/"+v.ID+"/links/"+l.ID, map[string]any{"perm": "upload"}, nil, 200)
	g := e.guest()
	base := "/api/share/" + l.Token
	var got store.AssetDetail
	e.do(g, "GET", base+"/assets/"+font.Code, nil, &got, 200)
	if got.Kind != protocol.AssetFont || got.Latest == nil || got.Latest.File.Mime != assetfmt.MimeTTF {
		t.Fatalf("访客看字体：%+v", got.Asset)
	}
	e.do(g, "GET", base+"/files/"+svg.ID, nil, nil, 200, "Sec-Fetch-Dest", "image")
	e.do(g, "GET", base+"/files/"+ttf.ID, nil, nil, 200, "Sec-Fetch-Dest", "empty")
	e.do(g, "GET", base+"/files/"+svg.ID, nil, nil, 403, "Sec-Fetch-Dest", "document")

	// 访客上传：字体收下为字体的新版本；SVG 不能收为字体的新版本；坏字体拒收
	e.upload(g, l.Token, font.Code, 1, "broken.otf", []byte("OTTO\x00\x00not-a-font"), 400)
	up := e.upload(g, l.Token, font.Code, 1, "ui_v2.otf", assetfmt.SynthFont(assetfmt.MimeOTF), 200)
	wrong := e.upload(g, l.Token, font.Code, 1, "x.svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="2" height="2"/>`), 200)
	place := func(id string, want int) (store.ShareComment, []byte) {
		var out struct {
			Comment store.ShareComment `json:"comment"`
		}
		_, raw := e.do(e.owner, "POST", "/api/share-comments/"+id+"/place", map[string]any{"as": "version"}, &out, want)
		return out.Comment, raw
	}
	if _, raw := place(wrong.ID, 400); !strings.Contains(string(raw), "字体的新版本只能是 TTF、OTF 或 WOFF2") {
		t.Fatalf("类型不符：%s", raw)
	}
	if c, _ := place(up.ID, 200); c.ResultRef != "v2" {
		t.Fatalf("收下为新版本：%+v", c)
	}
	font, _ = e.st.FindAsset(e.ctx, "p1", "core:ui.font")
	if font.Version != 2 || font.Latest.File.Mime != assetfmt.MimeOTF || font.Status != store.AssetPending {
		t.Fatalf("访客的字体新版本：%+v", font.Asset)
	}
}
