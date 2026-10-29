package hub

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func gzGet(t *testing.T, h http.Handler, path string) *http.Response {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", path, nil)
	req.Header.Set("Accept-Encoding", "gzip, br")
	h.ServeHTTP(rec, req)
	return rec.Result()
}

// R-246：文本压缩、图片不压缩、不带 Accept-Encoding 不压缩，且都带 Vary。
func TestGzipByType(t *testing.T) {
	text := strings.Repeat("hello kitestring ", 200)
	h := withGzip(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/js":
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		case "/json":
			w.Header().Set("Content-Type", "application/json")
		case "/svg":
			w.Header().Set("Content-Type", "image/svg+xml")
		case "/png":
			w.Header().Set("Content-Type", "image/png")
		}
		_, _ = io.WriteString(w, text)
	}))
	for _, p := range []string{"/js", "/json", "/svg"} {
		res := gzGet(t, h, p)
		if res.Header.Get("Content-Encoding") != "gzip" || res.Header.Get("Vary") != "Accept-Encoding" {
			t.Fatalf("%s 应压缩并带 Vary: %v", p, res.Header)
		}
		zr, err := gzip.NewReader(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(zr)
		if string(b) != text {
			t.Fatalf("%s 解压后内容不一致", p)
		}
	}
	res := gzGet(t, h, "/png")
	if res.Header.Get("Content-Encoding") != "" || res.Header.Get("Vary") != "Accept-Encoding" {
		t.Fatalf("图片不应压缩: %v", res.Header)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/js", nil))
	if rec.Header().Get("Content-Encoding") != "" {
		t.Fatal("没带 Accept-Encoding 不应压缩")
	}
}

// R-246：SSE 流不被压缩缓冲，hello 事件在连接刚建立时就能读到。
func TestGzipDoesNotBufferSSE(t *testing.T) {
	b := newBroker()
	srv := httptest.NewServer(withGzip(http.HandlerFunc(b.serve)))
	defer srv.Close()
	req, _ := http.NewRequest("GET", srv.URL, nil)
	req.Header.Set("Accept-Encoding", "gzip")
	res, err := (&http.Client{Transport: &http.Transport{DisableCompression: true}}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.Header.Get("Content-Encoding") != "" {
		t.Fatal("SSE 不应被压缩")
	}
	done := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(res.Body).ReadString('\n')
		done <- line
	}()
	select {
	case line := <-done:
		if !strings.HasPrefix(line, "retry:") {
			t.Fatalf("读到 %q", line)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("SSE 首包被缓冲")
	}
}

func testPNG(w, h int, alpha uint8) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	rnd := rand.New(rand.NewSource(1))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			n := uint8(rnd.Intn(23)) // 带点噪声，像真实截图 / 照片，PNG 不会小得离谱
			img.SetNRGBA(x, y, color.NRGBA{uint8(x) + n, uint8(y) + n, 128 + n, alpha})
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

func TestMakeThumb(t *testing.T) {
	out, ext := makeThumb(testPNG(1200, 800, 255), 480)
	if ext != ".jpg" {
		t.Fatalf("不透明图应出 JPEG，得到 %q", ext)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(out))
	if err != nil || cfg.Width != 480 || cfg.Height != 320 {
		t.Fatalf("尺寸不对: %v %+v", err, cfg)
	}
	if _, ext := makeThumb(testPNG(1200, 800, 100), 480); ext != ".png" {
		t.Fatalf("带透明应出 PNG，得到 %q", ext)
	}
	// 原图不宽于档位：不放大，按原宽度转码（比原图小才用）
	src := testPNG(300, 200, 255)
	out, ext = makeThumb(src, 480)
	cfg, _, err = image.DecodeConfig(bytes.NewReader(out))
	if ext != ".jpg" || err != nil || cfg.Width != 300 || len(out) >= len(src) {
		t.Fatalf("窄图应按原宽度转成更小的 JPEG: %q %v %+v %d/%d", ext, err, cfg, len(out), len(src))
	}
	// 纯色图 PNG 本来就极小，转码不更小就回落原图
	flat := image.NewNRGBA(image.Rect(0, 0, 300, 200))
	var fb bytes.Buffer
	_ = png.Encode(&fb, flat)
	if out, _ := makeThumb(fb.Bytes(), 480); out != nil {
		t.Fatal("转码后不比原图小应回落原图")
	}
	if out, _ := makeThumb([]byte("not an image"), 480); out != nil {
		t.Fatal("解码失败应回落原图")
	}
}

// 1920 档（R-246 退回）：大预览用它而不是原图；按实际宽度不放大。
func TestMakeThumb1920(t *testing.T) {
	out, ext := makeThumb(testPNG(2400, 600, 255), 1920)
	if ext != ".jpg" {
		t.Fatalf("得到 %q", ext)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(out))
	if err != nil || cfg.Width != 1920 || cfg.Height != 480 {
		t.Fatalf("尺寸不对: %v %+v", err, cfg)
	}
	src := testPNG(1500, 500, 255)
	out, _ = makeThumb(src, 1920)
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(out)); err != nil || cfg.Width != 1500 || len(out) >= len(src) {
		t.Fatalf("原图不宽于 1920 时不放大，按原宽度转成更小的图: %v %+v", err, cfg)
	}
	h := &Hub{filesDir: t.TempDir()}
	if err := writeFile(h.filePath("b"), testPNG(2400, 600, 255)); err != nil {
		t.Fatal(err)
	}
	if p, _, ok := h.thumbFile("b", "image/png", 1920); !ok || !strings.HasSuffix(p, "b-1920.jpg") {
		t.Fatalf("1920 档应落盘: %q %v", p, ok)
	}
}

func TestThumbWidth(t *testing.T) {
	for in, want := range map[string]int{"": 0, "x": 0, "-1": 0, "100": 480, "480": 480, "481": 960, "960": 960, "961": 1920, "1920": 1920, "5000": 1920} {
		if got := thumbWidth(in); got != want {
			t.Errorf("thumbWidth(%q)=%d, want %d", in, got, want)
		}
	}
}

func TestThumbFileCaches(t *testing.T) {
	h := &Hub{filesDir: t.TempDir()}
	if err := writeFile(h.filePath("a"), testPNG(1200, 800, 255)); err != nil {
		t.Fatal(err)
	}
	p, ct, ok := h.thumbFile("a", "image/png", 480)
	if !ok || ct != "image/jpeg" || !strings.HasSuffix(p, ".jpg") {
		t.Fatalf("%q %q %v", p, ct, ok)
	}
	if p2, _, ok := h.thumbFile("a", "image/png", 480); !ok || p2 != p {
		t.Fatal("第二次应命中缓存")
	}
	if _, _, ok := h.thumbFile("a", "image/gif", 480); ok {
		t.Fatal("GIF 应回落原图")
	}
}

func writeFile(p string, b []byte) error { return os.WriteFile(p, b, 0o600) }

// 同一张图并发首次请求只生成一次，所有请求拿到同一个完整文件（不会读到半截）。
func TestThumbFileConcurrentOnce(t *testing.T) {
	h := &Hub{filesDir: t.TempDir()}
	if err := writeFile(h.filePath("c"), testPNG(2000, 1500, 255)); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	paths := make([]string, 16)
	for i := range paths {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, _, ok := h.thumbFile("c", "image/png", 480)
			if !ok {
				t.Error("应生成缩略图")
				return
			}
			b, err := os.ReadFile(p)
			if err != nil {
				t.Error(err)
				return
			}
			if _, err := jpeg.Decode(bytes.NewReader(b)); err != nil {
				t.Errorf("读到不完整的文件: %v", err)
			}
			paths[i] = p
		}()
	}
	wg.Wait()
	for _, p := range paths {
		if p != paths[0] {
			t.Fatalf("路径不一致: %q %q", p, paths[0])
		}
	}
	ents, _ := os.ReadDir(h.thumbDir())
	if len(ents) != 1 {
		t.Fatalf("thumbs/ 里应只有 1 个文件，得到 %d", len(ents))
	}
}
