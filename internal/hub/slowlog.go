package hub

import (
	"bufio"
	"net"
	"net/http"
	"strings"
	"time"
)

// 慢请求日志（R-374）：一个 HTTP 请求从进来到处理完超过阈值，写一条日志：方法、路径、状态码、耗时，
// 以及这段时间里连接池排队等数据库连接的次数和时长（全池合计，含同时进行的其它请求；排队多说明有操作占着连接）。
// 长连接（daemon 的 WebSocket、网页和访客的 SSE 推送）不计。阈值由 ks hub serve --slow / KS_SLOW 设置，0 关闭。

// DefaultSlow 是默认的慢请求 / 慢数据库操作阈值。
const DefaultSlow = 500 * time.Millisecond

// SetSlowLog 设置慢请求阈值，同时用作数据库慢操作的阈值；0 关闭。
func (h *Hub) SetSlowLog(d time.Duration) {
	h.slowReq.Store(int64(d))
	if h.store != nil {
		h.store.SetSlowLog(d, h.log)
	}
}

// streamPath 是长连接的路径：不计入慢请求。
func streamPath(p string) bool {
	return p == "/api/device/ws" || p == "/api/stream" || strings.HasSuffix(p, "/stream")
}

type statusWriter struct {
	http.ResponseWriter
	code int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.code == 0 {
		w.code = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// Flush / Hijack 照常转给底层：里层（压缩、脱敏）按类型断言取用。
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hj, ok := w.ResponseWriter.(http.Hijacker); ok {
		return hj.Hijack()
	}
	return nil, nil, http.ErrNotSupported
}

// slowRequests 包住整个路由，记录超过阈值的请求。
func (h *Hub) slowRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		th := time.Duration(h.slowReq.Load())
		if th <= 0 || streamPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		before := h.store.DBStats()
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)
		d := time.Since(start)
		if d < th {
			return
		}
		after := h.store.DBStats()
		code := sw.code
		if code == 0 {
			code = http.StatusOK
		}
		h.log.Warn("请求慢", "method", r.Method, "path", r.URL.Path, "status", code, "ms", d.Milliseconds(),
			"db_waits", after.WaitCount-before.WaitCount, "db_wait_ms", (after.WaitDuration - before.WaitDuration).Milliseconds())
	})
}
