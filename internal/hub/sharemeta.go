package hub

import (
	"bytes"
	"context"
	"html"
	"net/http"
	"regexp"
	"strings"

	"github.com/NekroAI/kitestring/internal/store"
)

// 分享链接的预览摘要（R-286）：访客页是前端渲染，IM 抓取方只读服务端返回的 HTML，
// 所以 GET /s/<token> 时在 index.html 的 head 里填 og:* 和 twitter:card。
// 没设密码的有效分享显示分享名称和一句概况；设了密码、失效、撤回、到期、链接无效一律显示通用宣传摘要，不露名称和内容。

var (
	reTitle = regexp.MustCompile(`(?s)<title>.*?</title>`)
	reDesc  = regexp.MustCompile(`<meta name="description"[^>]*>`)
)

// shareToken 取出 /s/<token> 的 token；多余的路径段、空 token 都不算。
func shareToken(path string) (string, bool) {
	t, ok := strings.CutPrefix(path, "/s/")
	if !ok || t == "" || strings.Contains(t, "/") {
		return "", false
	}
	return t, true
}

func (h *Hub) shareMeta(web http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := shareToken(r.URL.Path)
		if !ok || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
			web.ServeHTTP(w, r)
			return
		}
		rec := &pageRec{h: http.Header{}}
		web.ServeHTTP(rec, r)
		body := rec.buf.Bytes()
		if (rec.code != 0 && rec.code != http.StatusOK) || !bytes.Contains(body, []byte("</head>")) {
			for k, v := range rec.h {
				w.Header()[k] = v
			}
			if rec.code != 0 {
				w.WriteHeader(rec.code)
			}
			_, _ = w.Write(body)
			return
		}
		title, desc := h.shareSummary(r.Context(), token)
		base := h.baseURL(r)
		page := string(body)
		page = reTitle.ReplaceAllLiteralString(page, "<title>"+html.EscapeString(title)+"</title>")
		page = reDesc.ReplaceAllLiteralString(page, `<meta name="description" content="`+html.EscapeString(desc)+`" />`)
		tags := []string{
			`<meta property="og:type" content="website" />`,
			`<meta property="og:site_name" content="` + html.EscapeString(ogSiteName) + `" />`,
			`<meta property="og:title" content="` + html.EscapeString(title) + `" />`,
			`<meta property="og:description" content="` + html.EscapeString(desc) + `" />`,
			`<meta property="og:image" content="` + html.EscapeString(base+"/apple-touch-icon.png") + `" />`,
			`<meta property="og:url" content="` + html.EscapeString(base+r.URL.Path) + `" />`,
			`<meta name="twitter:card" content="summary" />`,
		}
		page = strings.Replace(page, "</head>", "    "+strings.Join(tags, "\n    ")+"\n  </head>", 1)
		for k, v := range rec.h {
			w.Header()[k] = v
		}
		w.Header().Del("Content-Length")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.Method == http.MethodHead {
			return
		}
		_, _ = w.Write([]byte(page))
	})
}

// baseURL 是 og:image 等绝对地址的前缀：优先用设置里的对外地址，没填时按请求推算。
func (h *Hub) baseURL(r *http.Request) string {
	if s, err := h.Settings(r.Context()); err == nil && s.PublicURL != "" {
		return s.PublicURL
	}
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// shareSummary 返回链接预览的标题和简介；只有没设密码的有效分享才露分享的内容。
func (h *Hub) shareSummary(ctx context.Context, token string) (string, string) {
	x, _, err := h.guestLink(ctx, token)
	if err != nil || x.HasPassword || x.PasswordHash != "" {
		return ogPromoTitle, ogPromoDesc
	}
	sc, err := h.store.ResolveShare(ctx, x.Items)
	if err != nil {
		return ogPromoTitle, ogPromoDesc
	}
	done, latest := 0, ""
	var at int64
	for id := range sc.Reqs {
		rq, err := h.store.GetRequirement(ctx, id)
		if err != nil {
			continue
		}
		if rq.Status == store.ReqDone {
			done++
		}
		if u := rq.UpdatedAt.UnixNano(); u > at {
			at, latest = u, rq.UpdatedAt.Local().Format("01-02 15:04")
		}
	}
	return x.Name, ogShareDesc(len(sc.Reqs), done, latest)
}

// pageRec 把下游处理器的输出缓存下来，改写后再写给客户端。
type pageRec struct {
	h    http.Header
	code int
	buf  bytes.Buffer
}

func (p *pageRec) Header() http.Header         { return p.h }
func (p *pageRec) WriteHeader(c int)           { p.code = c }
func (p *pageRec) Write(b []byte) (int, error) { return p.buf.Write(b) }
