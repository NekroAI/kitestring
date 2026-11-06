package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// 已知值脱敏（R-295）：凭据的值一旦出现在纸鸢自己存、自己展示的内容里（会话的消息与回复、决策、留言、证据、团队消息、取用记录的命令……），
// 展示前一律换成「[已隐去]」。做法是在 /api/ 的响应出口统一替换，而不是逐个接口处理：新加的接口、访客页、实时推送都自动覆盖。
// 只有 ks secret run 取值的那个接口例外（它本来就是把值交给运行命令的 ks 进程）。管不到 Claude Code / Codex 自己的对话记录。
//
// 太短的值（少于 secretRedactMin 个字符）不参与：会把普通文字误隐去；这类值本身也不像需要保密的 Token。

// SecretRedacted 是已知值在展示时换成的文字；网页把它画成「已隐去」小标。
const SecretRedacted = "[已隐去]"

const secretRedactMin = 6

// redactor 把已知值换成 SecretRedacted；nil 表示没有已知值。
type redactor = *strings.Replacer

// newRedactor 按已知值建脱敏器：原文之外，也替换它在 JSON 字符串里的转义写法（含 / 不含 HTML 转义两种）。长的先换。
func newRedactor(values []string) redactor {
	seen := map[string]bool{}
	var forms []string
	add := func(s string) {
		if s != "" && !seen[s] {
			seen[s] = true
			forms = append(forms, s)
		}
	}
	for _, v := range values {
		if len([]rune(v)) < secretRedactMin {
			continue
		}
		add(v)
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		_ = enc.Encode(v)
		add(strings.TrimSuffix(strings.TrimSuffix(strings.TrimPrefix(buf.String(), `"`), "\n"), `"`))
		b, _ := json.Marshal(v)
		add(strings.TrimSuffix(strings.TrimPrefix(string(b), `"`), `"`))
	}
	if len(forms) == 0 {
		return nil
	}
	sort.SliceStable(forms, func(i, j int) bool { return len(forms[i]) > len(forms[j]) })
	pairs := make([]string, 0, len(forms)*2)
	for _, f := range forms {
		pairs = append(pairs, f, SecretRedacted)
	}
	return strings.NewReplacer(pairs...)
}

// redactText 用当前的已知值脱敏一段文字（写取用记录的命令时用）；extra 是这次取用的凭据自己的值（还没进缓存时也能隐去）。
func (h *Hub) redactText(s string, extra map[string]string) string {
	if r := h.currentRedactor(); r != nil {
		s = r.Replace(s)
	}
	if len(extra) > 0 {
		var vals []string
		for _, v := range extra {
			vals = append(vals, v)
		}
		if r := newRedactor(vals); r != nil {
			s = r.Replace(s)
		}
	}
	return s
}

// secretUsePath 是 ks secret run / write 取值的接口：响应里就是值，不脱敏。
const secretUsePath = "/api/agent/secrets/use"

// withRedact 给 /api/ 的 JSON 响应和实时推送脱敏。附件下载、原型、设备长连接不经过（不是 JSON，也不进网页的文字）。
func (h *Hub) withRedact(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.store == nil || !strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == secretUsePath || r.Header.Get("Upgrade") != "" {
			next.ServeHTTP(w, r)
			return
		}
		h.vault.once.Do(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			h.refreshRedactor(ctx)
		})
		rep := h.currentRedactor()
		if rep == nil {
			next.ServeHTTP(w, r)
			return
		}
		rw := &redactWriter{ResponseWriter: w, rep: rep}
		next.ServeHTTP(rw, r)
		rw.finish()
	})
}

// redactWriter：JSON 响应先整段缓存、替换后再写出（值可能跨两次 Write）；事件流逐条替换后立即写出（每条事件一次写完）；
// 其它类型原样透传。
type redactWriter struct {
	http.ResponseWriter
	rep    *strings.Replacer
	mode   int // 0 未定 / 1 缓存 JSON / 2 逐条替换的事件流 / 3 透传
	status int
	buf    bytes.Buffer
}

func (w *redactWriter) decide() {
	if w.mode != 0 {
		return
	}
	ct := w.Header().Get("Content-Type")
	switch {
	case strings.Contains(ct, "json"):
		w.mode = 1
	case strings.HasPrefix(ct, "text/event-stream"):
		w.mode = 2
	default:
		w.mode = 3
	}
}

func (w *redactWriter) WriteHeader(code int) {
	w.decide()
	if w.mode == 1 {
		w.status = code
		return
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *redactWriter) Write(p []byte) (int, error) {
	if w.mode == 0 && w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", http.DetectContentType(p))
	}
	w.decide()
	switch w.mode {
	case 1:
		return w.buf.Write(p)
	case 2:
		if _, err := w.ResponseWriter.Write([]byte(w.rep.Replace(string(p)))); err != nil {
			return 0, err
		}
		return len(p), nil
	}
	return w.ResponseWriter.Write(p)
}

func (w *redactWriter) Flush() {
	if w.mode == 1 {
		return
	}
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// finish 写出缓存的 JSON 响应。
func (w *redactWriter) finish() {
	if w.mode != 1 {
		return
	}
	out := w.rep.Replace(w.buf.String())
	w.Header().Del("Content-Length")
	w.Header().Set("Content-Length", strconv.Itoa(len(out)))
	if w.status == 0 {
		w.status = http.StatusOK
	}
	w.ResponseWriter.WriteHeader(w.status)
	_, _ = w.ResponseWriter.Write([]byte(out))
}
