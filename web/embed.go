// Package web 把前端构建产物嵌入二进制（D12）。构建前端：cd web && pnpm build。
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
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
