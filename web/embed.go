// Package web 把前端构建产物嵌入二进制（D12）。构建前端：cd web && pnpm build。
package web

import (
	"bytes"
	"embed"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
	"time"
)

//go:embed all:dist
var dist embed.FS

const placeholder = `<!doctype html><meta charset="utf-8"><title>Kitestring</title><p>前端尚未构建：在 web/ 目录执行 pnpm install && pnpm build 后重新编译 ks。</p>`

// Handler 返回前端静态资源处理器：存在的文件直接返回，其余路径回退到 index.html（单页应用路由）。
func Handler() http.Handler {
	sub, _ := fs.Sub(dist, "dist")
	files := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if p != "" && p != "." {
			if f, err := sub.Open(p); err == nil {
				st, _ := f.Stat()
				f.Close()
				if st != nil && !st.IsDir() {
					if strings.HasPrefix(p, "assets/") {
						w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
						// 加速地址（R-369）：页面从别的域名加载这些文件（模块脚本、字体都按 CORS 取），静态文件本来公开
						w.Header().Set("Access-Control-Allow-Origin", "*")
						w.Header().Set("Timing-Allow-Origin", "*")
						if serveBrotli(w, r, sub, p) {
							return
						}
					}
					files.ServeHTTP(w, r)
					return
				}
			}
		}
		index, err := fs.ReadFile(sub, "index.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		if err != nil {
			_, _ = w.Write([]byte(placeholder))
			return
		}
		_, _ = w.Write(index)
	})
}

// serveBrotli：构建时给 assets/ 下的文本文件预压缩了 .br（vite.config.ts），浏览器接受 br 时直接返回它（R-369）。
// 预压缩省掉运行时开销，也不用给 Go 引入 brotli 依赖；Range 请求、找不到 .br 时交回原处理。
func serveBrotli(w http.ResponseWriter, r *http.Request, sub fs.FS, p string) bool {
	if r.Header.Get("Range") != "" || !acceptsBrotli(r.Header.Get("Accept-Encoding")) {
		return false
	}
	b, err := fs.ReadFile(sub, p+".br")
	if err != nil {
		return false
	}
	ct := mime.TypeByExtension(path.Ext(p))
	if ct == "" {
		return false
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Encoding", "br")
	w.Header().Add("Vary", "Accept-Encoding")
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(b))
	return true
}

func acceptsBrotli(ae string) bool {
	for _, part := range strings.Split(ae, ",") {
		name, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		if !strings.EqualFold(strings.TrimSpace(name), "br") {
			continue
		}
		q := strings.TrimPrefix(strings.ReplaceAll(strings.ToLower(params), " ", ""), "q=")
		return q == "" || strings.Trim(q, "0.") != ""
	}
	return false
}
