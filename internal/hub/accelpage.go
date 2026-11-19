package hub

import (
	"bytes"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
)

// accelPage（R-369）：设了加速地址时改写网页入口 index.html——把 <head> 里引用 /assets/ 的模块脚本、预加载和样式表摘出来，
// 换成一段内联的启动脚本：先探测加速地址（几秒超时，近期探测失败过的直接跳过），可用就从加速地址插入这些标签，不可用从主地址插入。
// 没设加速地址时原样返回，页面与改动前逐字节相同。

var reAccelTag = regexp.MustCompile(`<(script|link)\b[^>]*?\b(?:src|href)="(/assets/[^"]+)"[^>]*>(?:\s*</script>)?\s*`)
var reRel = regexp.MustCompile(`\brel="([^"]+)"`)

type accelAsset struct {
	T string `json:"t"` // script / modulepreload / stylesheet
	U string `json:"u"`
}

// rewriteAccelIndex 返回改写后的页面；页面里没有可改的标签时 ok 为 false。
func rewriteAccelIndex(page []byte, base string) ([]byte, bool) {
	head, rest, found := bytes.Cut(page, []byte("</head>"))
	if !found {
		return page, false
	}
	var assets []accelAsset
	head = reAccelTag.ReplaceAllFunc(head, func(m []byte) []byte {
		sub := reAccelTag.FindSubmatch(m)
		t := "script"
		if string(sub[1]) == "link" {
			rel := reRel.FindSubmatch(m)
			if rel == nil {
				return m
			}
			t = string(rel[1])
			if t != "modulepreload" && t != "stylesheet" {
				return m
			}
		}
		assets = append(assets, accelAsset{T: t, U: string(sub[2])})
		return nil
	})
	if len(assets) == 0 {
		return page, false
	}
	cfg, _ := json.Marshal(map[string]any{"base": base, "assets": assets})
	// </script> 不能出现在内联脚本里；json.Marshal 已把 < > & 转义成 < 等
	var out bytes.Buffer
	out.Write(head)
	out.WriteString("<script>window.__KS_ACCEL=" + string(cfg) + ";" + accelBoot + "</script>\n  </head>")
	out.Write(rest)
	return out.Bytes(), true
}

// accelBoot 是内联启动脚本（不经构建，写成老语法）。约定（网页 lib/accel.ts 读同一份）：
//   - localStorage「ks-accel」：{"down": 毫秒时间戳}，在这之前不再探测、直接走主地址；
//   - window.__KS_ACCEL.on：这次页面是否经加速地址加载；keyP：派生密钥的请求（和探测并行发出）。
const accelBoot = `(function(){var A=window.__KS_ACCEL,S="ks-accel",st={};try{st=JSON.parse(localStorage.getItem(S)||"{}")||{}}catch(e){}
var g=location.pathname.match(/^\/s\/([^\/]+)/);A.keyUrl=g?"/api/share/"+g[1]+"/accel":"/api/accel/key";
A.keyP=fetch(A.keyUrl,{credentials:"same-origin",cache:"no-store"}).then(function(r){return r.ok?r.json():null}).catch(function(){return null});
var put=function(o){A.on=!!o;var hd=document.head,els=[];A.assets.forEach(function(a){var e;if(a.t==="script"){e=document.createElement("script");e.type="module";e.src=o+a.u;
if(o)e.onerror=function(){fall()}}else{e=document.createElement("link");e.rel=a.t;e.href=o+a.u;if(o&&a.t==="stylesheet")e.onerror=function(){var l=document.createElement("link");l.rel="stylesheet";l.crossOrigin="anonymous";l.href=a.u;hd.appendChild(l)}}
e.crossOrigin="anonymous";els.push(e);hd.appendChild(e)});A.els=els};
var down=function(){try{localStorage.setItem(S,JSON.stringify({down:Date.now()+6e5}))}catch(e){}};
var fall=function(){down();(A.els||[]).forEach(function(e){e.remove()});put("")};
if(st.down&&st.down>Date.now()){put("");return}
var done=false,t=setTimeout(function(){if(!done){done=true;down();put("")}},3000);
fetch(A.base+"/api/accel/ping",{mode:"cors",credentials:"omit",cache:"no-store"}).then(function(r){if(done)return;done=true;clearTimeout(t);if(r.ok)put(A.base);else{down();put("")}}).catch(function(){if(done)return;done=true;clearTimeout(t);down();put("")})})();`

// accelPage 包住网页处理器，设了加速地址时改写 index.html。
func (h *Hub) accelPage(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if (r.Method != http.MethodGet && r.Method != http.MethodHead) || strings.HasPrefix(r.URL.Path, "/assets/") {
			next.ServeHTTP(w, r)
			return
		}
		base := h.accelBase(r.Context())
		if base == "" {
			next.ServeHTTP(w, r)
			return
		}
		rec := &pageRec{h: http.Header{}}
		next.ServeHTTP(rec, r)
		body := rec.buf.Bytes()
		ok := false
		if (rec.code == 0 || rec.code == http.StatusOK) && strings.HasPrefix(rec.h.Get("Content-Type"), "text/html") {
			body, ok = rewriteAccelIndex(body, base)
		}
		for k, v := range rec.h {
			w.Header()[k] = v
		}
		if ok {
			w.Header().Del("Content-Length")
			w.Header().Del("Etag")
			w.Header().Del("Last-Modified")
		}
		if rec.code != 0 {
			w.WriteHeader(rec.code)
		}
		if r.Method != http.MethodHead {
			_, _ = w.Write(body)
		}
	})
}
