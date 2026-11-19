package hub

import (
	"bytes"
	"encoding/hex"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/store"
)

// 签名算法的固定向量：网页 web/src/lib/accel.test.ts 用同一组数，两边算出来必须一样。
func TestAccelSigVector(t *testing.T) {
	master := []byte("0123456789abcdef0123456789abcdef")
	k := accelDerive(master, "user", 1700000000, "")
	if got := hex.EncodeToString(k); got != accelVectorKey {
		t.Fatalf("派生密钥 = %s", got)
	}
	if got := accelSig(k, "01ABC", "480", false); got != accelVectorSig {
		t.Fatalf("签名 = %s", got)
	}
	if accelSig(k, "01ABC", "480", true) == accelVectorSig || accelSig(k, "01ABC", "960", false) == accelVectorSig || accelSig(k, "01ABD", "480", false) == accelVectorSig {
		t.Fatal("档位、下载、文件 ID 不同，签名应不同")
	}
}

const (
	accelVectorKey = "7066e1dcbb9353abe2a8297d833bf7ec252a62e73797b16bff3f725726cc1bdc"
	accelVectorSig = "44bc731671791a2342f64064fc9fc710"
)

func TestNormalizeAccelURL(t *testing.T) {
	for in, want := range map[string]string{"": "", "  ": "", "https://ks.dir.example.com:8081/": "https://ks.dir.example.com:8081", "http://127.0.0.1:9000": "http://127.0.0.1:9000", "https://a.example.com/ks": "https://a.example.com/ks"} {
		if got, err := normalizeAccelURL(in); err != nil || got != want {
			t.Errorf("normalizeAccelURL(%q) = %q, %v", in, got, err)
		}
	}
	for _, in := range []string{"ks.example.com", "ftp://a.com", "https://", "https://a.com/?x=1", "https://u:p@a.com", "javascript:alert(1)"} {
		if _, err := normalizeAccelURL(in); err == nil {
			t.Errorf("normalizeAccelURL(%q) 应报错", in)
		}
	}
}

// 加速地址：设置、派生密钥、签名链接的校验（篡改、过期、未登录）、访客范围、CORS。
func TestAccelSignedFiles(t *testing.T) {
	e := newShareEnv(t)
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
	fIn, fOut := file("in.png"), file("out.png")
	if _, err := e.st.AddEvidence(e.ctx, store.Evidence{RequirementID: e.ra.ID, SessionKey: e.dev1, Kind: "screenshot", Summary: "截图", Files: []store.FileRef{fIn}}); err != nil {
		t.Fatal(err)
	}

	// 没设加速地址：派生密钥接口只回空地址，不发密钥
	var kv accelKeyView
	e.do(e.owner, "GET", "/api/accel/key", nil, &kv, 200)
	if kv.Base != "" || kv.Key != "" {
		t.Fatalf("没设加速地址时不应发密钥：%+v", kv)
	}
	// 设置：格式不对拒收；能填、能清空；不传 accel_url 不改
	var sv SettingsView
	e.do(e.owner, "PUT", "/api/settings", map[string]any{"accel_url": "ks.example.com", "notify_default": map[string]any{}, "ai": map[string]any{}, "public_url": "", "summary_base_url": "", "summary_model": ""}, nil, 422)
	e.do(e.owner, "PUT", "/api/settings", map[string]any{"accel_url": "https://ks.dir.example.com:8081/", "notify_default": map[string]any{}, "ai": map[string]any{}, "public_url": "", "summary_base_url": "", "summary_model": ""}, &sv, 200)
	if sv.AccelURL != "https://ks.dir.example.com:8081" {
		t.Fatalf("加速地址 = %q", sv.AccelURL)
	}
	e.do(e.owner, "PUT", "/api/settings", map[string]any{"public_url": "https://ks.example.com", "notify_default": map[string]any{}, "ai": map[string]any{}, "summary_base_url": "", "summary_model": ""}, &sv, 200)
	if sv.AccelURL != "https://ks.dir.example.com:8081" {
		t.Fatalf("不传 accel_url 时不应改动：%q", sv.AccelURL)
	}

	// 未登录拿不到派生密钥
	e.do(e.guest(), "GET", "/api/accel/key", nil, nil, 401)
	e.do(e.owner, "GET", "/api/accel/key", nil, &kv, 200)
	if kv.Base != "https://ks.dir.example.com:8081" || kv.Scope != "user" || len(kv.Key) != 64 {
		t.Fatalf("派生密钥不对：%+v", kv)
	}
	if left := time.Until(time.Unix(kv.Exp, 0)); left <= time.Hour || left > 2*time.Hour {
		t.Fatalf("派生密钥应 1～2 小时后过期，实际 %v", left)
	}
	key, _ := hex.DecodeString(kv.Key)

	anon := e.guest() // 不带任何 cookie 的浏览器
	u := accelFileURL(key, "user", kv.Exp, fOut.ID, "", false)
	resp, body := get(t, anon, e.srv.URL+u)
	if resp.StatusCode != 200 || body != "png" || resp.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("签名链接应能取到文件并带 CORS 头：%d %q %v", resp.StatusCode, body, resp.Header)
	}
	// 档位、下载各自签名
	if resp, _ := get(t, anon, e.srv.URL+accelFileURL(key, "user", kv.Exp, fOut.ID, "480", false)); resp.StatusCode != 200 {
		t.Fatalf("缩略图签名链接 → %d", resp.StatusCode)
	}
	if resp, _ := get(t, anon, e.srv.URL+accelFileURL(key, "user", kv.Exp, fOut.ID, "", true)); resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Disposition"), "attachment") {
		t.Fatalf("下载签名链接 → %d %v", resp.StatusCode, resp.Header)
	}

	// 篡改：换文件、换档位、加下载、改过期时刻、改范围、签名改一位、没有签名，一律 403
	tampered := []string{
		strings.Replace(u, fOut.ID, fIn.ID, 1),
		strings.Replace(u, "?", "?w=480&", 1),
		strings.Replace(u, "?", "?download=1&", 1),
		strings.Replace(u, "e=", "e=1", 1),
		strings.Replace(u, "c=user", "c=share%3A"+fIn.ID, 1),
		u[:len(u)-1] + map[bool]string{true: "0", false: "1"}[u[len(u)-1] != '0'],
		accelFilePath + fOut.ID,
		accelFilePath + fOut.ID + "?c=user&e=" + "9999999999" + "&s=" + strings.Repeat("0", 32),
	}
	for _, p := range tampered {
		resp, body := get(t, anon, e.srv.URL+p)
		if resp.StatusCode != http.StatusForbidden || !strings.Contains(body, accelBadLink) && !strings.Contains(body, accelExpired) {
			t.Errorf("篡改的链接应被拒：%s → %d %s", p, resp.StatusCode, body)
		}
		if resp.Header.Get("Access-Control-Allow-Origin") != "*" {
			t.Errorf("拒绝时也要带 CORS 头，网页才能读到原因：%s", p)
		}
	}

	// 过期：签名正确但过期时刻已过，拒绝并写明已过期；过期时刻太远（不是 Hub 发的）也拒
	master, _ := e.h.accelMaster(e.ctx)
	old := time.Now().Add(-time.Minute).Unix()
	resp, body = get(t, anon, e.srv.URL+accelFileURL(accelDerive(master, "user", old, ""), "user", old, fOut.ID, "", false))
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(body, accelExpired) {
		t.Fatalf("过期链接应被拒：%d %s", resp.StatusCode, body)
	}
	far := time.Now().Add(24 * time.Hour).Unix()
	if resp, _ := get(t, anon, e.srv.URL+accelFileURL(accelDerive(master, "user", far, ""), "user", far, fOut.ID, "", false)); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("过期时刻太远的链接应被拒：%d", resp.StatusCode)
	}
	// 主密钥不同（例如换了一台 Hub）签出来的不认
	other := []byte(strings.Repeat("k", 32))
	if resp, _ := get(t, anon, e.srv.URL+accelFileURL(accelDerive(other, "user", kv.Exp, ""), "user", kv.Exp, fOut.ID, "", false)); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("别的主密钥签的链接应被拒：%d", resp.StatusCode)
	}

	// 访客范围：输过密码才拿得到密钥；只能取分享范围里的文件；改密码后旧链接失效
	v := e.createShare(map[string]any{"name": "进展", "password": "kite-0821", "links": []string{"小王"},
		"items": []map[string]any{{"kind": "req", "ref": e.ra.Code}}})
	wang := linkByName(v, "小王")
	g := e.guest()
	e.do(g, "GET", "/api/share/"+wang.Token+"/accel", nil, nil, 401)
	e.do(g, "POST", "/api/share/"+wang.Token+"/unlock", map[string]any{"password": "kite-0821"}, nil, 200)
	var gk accelKeyView
	e.do(g, "GET", "/api/share/"+wang.Token+"/accel", nil, &gk, 200)
	if gk.Scope != "share:"+wang.ID || gk.Key == "" {
		t.Fatalf("访客密钥不对：%+v", gk)
	}
	gkey, _ := hex.DecodeString(gk.Key)
	if resp, body := get(t, anon, e.srv.URL+accelFileURL(gkey, gk.Scope, gk.Exp, fIn.ID, "", false)); resp.StatusCode != 200 || body != "png" {
		t.Fatalf("访客取范围内的文件 → %d %s", resp.StatusCode, body)
	}
	if resp, _ := get(t, anon, e.srv.URL+accelFileURL(gkey, gk.Scope, gk.Exp, fOut.ID, "", false)); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("访客不应取到分享外的文件：%d", resp.StatusCode)
	}
	// 访客的密钥冒充登录范围：签名对不上
	if resp, _ := get(t, anon, e.srv.URL+accelFileURL(gkey, "user", gk.Exp, fOut.ID, "", false)); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("访客密钥不能当登录范围用：%d", resp.StatusCode)
	}
	e.do(e.owner, "PUT", "/api/shares/"+v.ID, map[string]any{"password": "new-pass"}, nil, 200)
	if resp, _ := get(t, anon, e.srv.URL+accelFileURL(gkey, gk.Scope, gk.Exp, fIn.ID, "", false)); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("改密码后旧链接应失效：%d", resp.StatusCode)
	}
	// 撤回链接：用新密码对应的密钥也取不到
	nk := accelDerive(master, gk.Scope, gk.Exp, mustShare(t, e, v.ID).PasswordHash)
	if resp, _ := get(t, anon, e.srv.URL+accelFileURL(nk, gk.Scope, gk.Exp, fIn.ID, "", false)); resp.StatusCode != 200 {
		t.Fatalf("新密码下的链接应可用：%d", resp.StatusCode)
	}
	e.do(e.owner, "PUT", "/api/shares/"+v.ID+"/links/"+wang.ID, map[string]any{"revoked": true}, nil, 200)
	if resp, _ := get(t, anon, e.srv.URL+accelFileURL(nk, gk.Scope, gk.Exp, fIn.ID, "", false)); resp.StatusCode != http.StatusGone {
		t.Fatalf("撤回后应拒绝：%d", resp.StatusCode)
	}

	// 探测接口不认登录、带 CORS 头
	resp, _ = get(t, anon, e.srv.URL+"/api/accel/ping")
	if resp.StatusCode != http.StatusNoContent || resp.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("探测接口 → %d %v", resp.StatusCode, resp.Header)
	}
	// 登录的附件接口不受影响：没有 cookie 仍是 401
	if resp, _ := get(t, anon, e.srv.URL+"/api/files/"+fOut.ID); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("/api/files 仍要登录：%d", resp.StatusCode)
	}
}

func mustShare(t *testing.T, e *shareEnv, id string) store.Share {
	t.Helper()
	x, err := e.st.GetShare(e.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return x
}

const accelTestIndex = `<!doctype html>
<html lang="zh-CN">
  <head>
    <meta charset="UTF-8" />
    <title>纸鸢 Kitestring</title>
    <script type="module" crossorigin src="/assets/index-abc.js"></script>
    <link rel="modulepreload" crossorigin href="/assets/react-def.js">
    <link rel="stylesheet" crossorigin href="/assets/index-123.css">
    <link rel="manifest" href="/manifest.webmanifest" />
  </head>
  <body><div id="root"></div></body>
</html>`

// 网页入口：没设加速地址时逐字节不变；设了时摘出 /assets/ 标签、换成启动脚本，其余标签不动。
func TestAccelPage(t *testing.T) {
	e := newShareEnv(t)
	web := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			w.Header().Set("Content-Type", "text/javascript")
			_, _ = w.Write([]byte("export {}"))
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write([]byte(accelTestIndex))
	})
	h := e.h.accelPage(web)
	serve := func(path string) string {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		return rec.Body.String()
	}
	if got := serve("/req/R-1"); got != accelTestIndex {
		t.Fatalf("没设加速地址时页面应原样返回：\n%s", got)
	}
	s, _ := e.h.Settings(e.ctx)
	s.AccelURL = "https://ks.dir.example.com:8081"
	if err := e.h.SaveSettings(e.ctx, s); err != nil {
		t.Fatal(err)
	}
	got := serve("/req/R-1")
	for _, gone := range []string{`src="/assets/index-abc.js"`, `href="/assets/react-def.js"`, `href="/assets/index-123.css"`} {
		if strings.Contains(got, gone) {
			t.Errorf("应摘掉 %s", gone)
		}
	}
	for _, want := range []string{`<link rel="manifest" href="/manifest.webmanifest" />`, `window.__KS_ACCEL={"assets":[{"t":"script","u":"/assets/index-abc.js"},{"t":"modulepreload","u":"/assets/react-def.js"},{"t":"stylesheet","u":"/assets/index-123.css"}],"base":"https://ks.dir.example.com:8081"}`, "/api/accel/ping", "</head>"} {
		if !strings.Contains(got, want) {
			t.Errorf("改写后的页面缺 %s：\n%s", want, got)
		}
	}
	if serve("/assets/index-abc.js") != "export {}" {
		t.Error("静态文件不应被改写")
	}
	if out, ok := rewriteAccelIndex([]byte("<html><body>没有 head</body></html>"), "https://x"); ok || !bytes.Equal(out, []byte("<html><body>没有 head</body></html>")) {
		t.Error("没有 </head> 时原样返回")
	}
}

// 缩略图不输出 WebP（R-369）：即使浏览器 Accept 带 image/webp，也照旧给 JPEG / PNG。
func TestThumbNeverWebP(t *testing.T) {
	e := newShareEnv(t)
	img := testFlatPNG(1600, 1000)
	f, err := e.st.CreateFile(e.ctx, "shot.png", "image/png", "x", int64(len(img)), "user")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.h.filePath(f.ID), img, 0o600); err != nil {
		t.Fatal(err)
	}
	resp, body := get(t, e.owner, e.srv.URL+"/api/files/"+f.ID+"?w=960", "Accept", "image/avif,image/webp,*/*")
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") == "image/webp" || strings.HasPrefix(body, "RIFF") {
		t.Fatalf("缩略图不应是 WebP：%d %v", resp.StatusCode, resp.Header)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "image/jpeg" && ct != "image/png" {
		t.Fatalf("缩略图应是 JPEG / PNG：%v", ct)
	}
}

func testFlatPNG(w, h int) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := color.NRGBA{250, 248, 240, 255}
			if (x/200+y/120)%3 == 0 {
				c = color.NRGBA{40, 60, 90, 255}
			}
			if y%40 < 2 {
				c = color.NRGBA{200, 200, 200, 255}
			}
			img.SetNRGBA(x, y, c)
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}
