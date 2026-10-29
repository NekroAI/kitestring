package hub

import (
	"bufio"
	"compress/gzip"
	"errors"
	"mime"
	"net"
	"net/http"
	"strings"
	"sync"
)

// 传输压缩（R-246）：文本类响应（JS / CSS / HTML / JSON / SVG 等）按 Accept-Encoding 返回 gzip。
// 图片、视频等本身已压缩的类型、SSE 流、WebSocket 升级、Range 请求一律原样透传。
// 只做 gzip：标准库自带，所有浏览器都认；brotli 需要第三方依赖，收益有限，先不引入。

var gzipPool = sync.Pool{New: func() any { gz, _ := gzip.NewWriterLevel(nil, 5); return gz }}

// compressible 判断响应类型是否值得压缩。
func compressible(ct string) bool {
	base, _, err := mime.ParseMediaType(ct)
	if err != nil {
		return false
	}
	switch {
	case base == "text/event-stream":
		return false
	case strings.HasPrefix(base, "text/"):
		return true
	case strings.HasSuffix(base, "+json"), strings.HasSuffix(base, "+xml"):
		return true
	}
	switch base {
	case "application/json", "application/javascript", "application/xml", "application/x-ndjson",
		"application/yaml", "application/manifest+json", "image/svg+xml":
		return true
	}
	return false
}

func acceptsGzip(r *http.Request) bool {
	for _, part := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		name, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		if !strings.EqualFold(strings.TrimSpace(name), "gzip") {
			continue
		}
		q := strings.TrimPrefix(strings.ReplaceAll(strings.ToLower(params), " ", ""), "q=")
		return q == "" || strings.Trim(q, "0.") != ""
	}
	return false
}

// withGzip 给整条处理链加上 gzip。
func withGzip(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Vary", "Accept-Encoding")
		if !acceptsGzip(r) || r.Header.Get("Upgrade") != "" || r.Header.Get("Range") != "" || r.Method == http.MethodHead {
			next.ServeHTTP(w, r)
			return
		}
		gw := &gzipWriter{ResponseWriter: w}
		defer gw.close()
		next.ServeHTTP(gw, r)
	})
}

type gzipWriter struct {
	http.ResponseWriter
	gz      *gzip.Writer
	decided bool
}

func (g *gzipWriter) decide(status int) {
	if g.decided {
		return
	}
	g.decided = true
	h := g.Header()
	ct := h.Get("Content-Type")
	if status != http.StatusNoContent && status != http.StatusNotModified && status >= 200 &&
		h.Get("Content-Encoding") == "" && h.Get("Content-Range") == "" && compressible(ct) {
		h.Del("Content-Length")
		h.Set("Content-Encoding", "gzip")
		g.gz = gzipPool.Get().(*gzip.Writer)
		g.gz.Reset(g.ResponseWriter)
	}
}

func (g *gzipWriter) WriteHeader(status int) {
	g.decide(status)
	g.ResponseWriter.WriteHeader(status)
}

func (g *gzipWriter) Write(p []byte) (int, error) {
	if !g.decided {
		if g.Header().Get("Content-Type") == "" {
			g.Header().Set("Content-Type", http.DetectContentType(p))
		}
		g.decide(http.StatusOK)
	}
	if g.gz != nil {
		return g.gz.Write(p)
	}
	return g.ResponseWriter.Write(p)
}

// Flush 先把压缩器里的数据冲出去，再冲底层连接；SSE 不压缩，直接透传。
func (g *gzipWriter) Flush() {
	g.decide(http.StatusOK)
	if g.gz != nil {
		_ = g.gz.Flush()
	}
	if fl, ok := g.ResponseWriter.(http.Flusher); ok {
		fl.Flush()
	}
}

func (g *gzipWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hj, ok := g.ResponseWriter.(http.Hijacker); ok {
		return hj.Hijack()
	}
	return nil, nil, errors.New("不支持 Hijack")
}

func (g *gzipWriter) Unwrap() http.ResponseWriter { return g.ResponseWriter }

func (g *gzipWriter) close() {
	if g.gz != nil {
		_ = g.gz.Close()
		g.gz.Reset(nil)
		gzipPool.Put(g.gz)
		g.gz = nil
	}
}
