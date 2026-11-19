package hub

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
)

// 加速地址（R-369）：设置里可以填同一个 Hub 的另一条入口（例如家里宽带直连的 https://ks.dir.example.com:8081）。
// 页面仍从主地址打开，接口和实时推送仍走主地址；页面探测加速地址可用后，网页静态文件、附件、缩略图、原型和资源库文件改从它加载。
//
// 加速地址是另一个域名，带不了主地址的登录 cookie，所以私有文件走签名链接：
//   - 已登录的页面向 Hub 取一把派生密钥（GET /api/accel/key；访客页取 GET /api/share/{token}/accel），
//     派生密钥 = HMAC(主密钥, 版本 | 范围 | 过期时刻 | 附加值)，1～2 小时后过期；
//   - 页面用它对「文件 ID、档位、是否下载」算出每条链接的签名，地址形如 /api/accel/f/{id}?w=480&c=范围&e=过期时刻&s=签名；
//   - Hub 用主密钥重算派生密钥和签名来校验，过期的拒绝；范围是 user（登录的所有者）或 share:<链接 ID>（访客），
//     访客范围的附加值是分享当前的密码哈希（改密码后旧链接失效），并且仍按分享范围查文件在不在里面。
// 单条链接泄露只能取到那一个文件、那一个档位，到期作废；伪造别的链接需要派生密钥，派生密钥只发给已登录 / 已通过访客校验的页面。
//
// 静态文件本来就公开，/assets/ 和这里的签名链接、探测接口都回 Access-Control-Allow-Origin: *（不带凭据）。

const (
	accelKeySetting = "accel_sign_key"
	accelVersion    = "ks-accel-v1"
	accelStep       = time.Hour     // 过期时刻按整点对齐：同一小时里链接不变，浏览器缓存能命中
	accelMaxAhead   = 3 * time.Hour // 过期时刻离现在超过这个的不认（防止拿到的派生密钥被当成长期密钥用）
	accelScopeUser  = "user"
	accelSharePre   = "share:"
	accelFilePath   = "/api/accel/f/"
	accelBadLink    = "链接无效"
	accelExpired    = "链接已过期，刷新页面后重试"
)

// normalizeAccelURL 规范化设置里填的加速地址：留空表示不用；只认 http / https 加主机（可带端口和路径前缀），去掉末尾的 /。
func normalizeAccelURL(raw string) (string, error) {
	s := strings.TrimSuffix(strings.TrimSpace(raw), "/")
	if s == "" {
		return "", nil
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return "", errors.New("加速地址格式不正确，应写成 https://主机名:端口")
	}
	return s, nil
}

func (h *Hub) accelBase(ctx context.Context) string {
	s, err := h.Settings(ctx)
	if err != nil {
		return ""
	}
	return s.AccelURL
}

// accelMaster 取签名主密钥：首次用到时生成并存进设置，之后缓存在内存里。
func (h *Hub) accelMaster(ctx context.Context) ([]byte, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.accelKey != nil {
		return h.accelKey, nil
	}
	v, err := h.store.GetSetting(ctx, accelKeySetting)
	if err != nil {
		return nil, err
	}
	if v == "" {
		b := make([]byte, 32)
		_, _ = rand.Read(b)
		v = hex.EncodeToString(b)
		if err := h.store.SetSetting(ctx, accelKeySetting, v); err != nil {
			return nil, err
		}
	}
	h.accelKey, err = hex.DecodeString(v)
	return h.accelKey, err
}

// accelDerive 算派生密钥。extra 是访客范围的分享密码哈希（登录范围为空）。
func accelDerive(master []byte, scope string, exp int64, extra string) []byte {
	m := hmac.New(sha256.New, master)
	m.Write([]byte(accelVersion + "\x00" + scope + "\x00" + strconv.FormatInt(exp, 10) + "\x00" + extra))
	return m.Sum(nil)
}

// accelSig 用派生密钥给一条文件链接签名：文件 ID、档位（w 的原样字符串）、是否下载。网页 lib/accel.ts 的 sign 与它一致。
func accelSig(key []byte, id, w string, download bool) string {
	d := ""
	if download {
		d = "1"
	}
	m := hmac.New(sha256.New, key)
	m.Write([]byte(id + "\n" + w + "\n" + d))
	return hex.EncodeToString(m.Sum(nil))[:32]
}

// accelExp 返回新发的派生密钥的过期时刻：下一个整点再加一小时（有效 1～2 小时）。
func accelExp(now time.Time) int64 {
	return now.Truncate(accelStep).Add(2 * accelStep).Unix()
}

type accelKeyView struct {
	Base  string `json:"base"`
	Scope string `json:"scope,omitempty"`
	Key   string `json:"key,omitempty"`
	Exp   int64  `json:"exp,omitempty"`
}

func (h *Hub) writeAccelKey(w http.ResponseWriter, r *http.Request, scope, extra string) {
	v := accelKeyView{Base: h.accelBase(r.Context())}
	if v.Base != "" {
		master, err := h.accelMaster(r.Context())
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		v.Scope, v.Exp = scope, accelExp(time.Now())
		v.Key = hex.EncodeToString(accelDerive(master, scope, v.Exp, extra))
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

// handleAccelKey：已登录的页面取派生密钥（范围 user）。
func (h *Hub) handleAccelKey(w http.ResponseWriter, r *http.Request) {
	h.writeAccelKey(w, r, accelScopeUser, "")
}

// handleGuestAccelKey：访客页取派生密钥（范围 share:<链接 ID>），要先通过访客校验（链接可用、输过密码）。
func (h *Hub) handleGuestAccelKey(w http.ResponseWriter, r *http.Request) {
	g := h.guestAuthHTTP(w, r)
	if g == nil {
		return
	}
	h.writeAccelKey(w, r, accelSharePre+g.link.ID, g.share.PasswordHash)
}

func accelCORS(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Timing-Allow-Origin", "*")
}

// handleAccelPing 是页面探测加速地址用的轻量检查：不认登录、不读数据库。
func (h *Hub) handleAccelPing(w http.ResponseWriter, r *http.Request) {
	accelCORS(w)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

// handleAccelFile 校验签名链接后返回文件；登录范围同 /api/files/{id}，访客范围同 /api/share/{token}/files/{id}。
func (h *Hub) handleAccelFile(w http.ResponseWriter, r *http.Request) {
	accelCORS(w)
	ctx := r.Context()
	q := r.URL.Query()
	scope := q.Get("c")
	exp, err := strconv.ParseInt(q.Get("e"), 10, 64)
	if err != nil || scope == "" || len(q.Get("s")) != 32 {
		writeErr(w, http.StatusForbidden, accelBadLink)
		return
	}
	var g *guestCtx
	extra := ""
	switch {
	case scope == accelScopeUser:
	case strings.HasPrefix(scope, accelSharePre):
		l, err := h.store.ShareLinkByID(ctx, strings.TrimPrefix(scope, accelSharePre))
		if err != nil {
			writeErr(w, http.StatusForbidden, accelBadLink)
			return
		}
		x, l, err := h.linkShare(ctx, l)
		if err != nil {
			var se huma.StatusError
			if errors.As(err, &se) {
				writeErr(w, se.GetStatus(), se.Error())
			} else {
				writeErr(w, http.StatusForbidden, accelBadLink)
			}
			return
		}
		sc, err := h.store.ResolveShare(ctx, x.Items)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		g = &guestCtx{share: x, link: l, scope: sc}
		extra = x.PasswordHash
	default:
		writeErr(w, http.StatusForbidden, accelBadLink)
		return
	}
	master, err := h.accelMaster(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	want := accelSig(accelDerive(master, scope, exp, extra), r.PathValue("id"), q.Get("w"), q.Has("download"))
	if !hmac.Equal([]byte(want), []byte(q.Get("s"))) {
		writeErr(w, http.StatusForbidden, accelBadLink)
		return
	}
	now := time.Now()
	if now.Unix() >= exp || time.Unix(exp, 0).Sub(now) > accelMaxAhead {
		writeErr(w, http.StatusForbidden, accelExpired)
		return
	}
	if g != nil {
		h.guestServeFile(w, r, g)
		return
	}
	h.handleDownload(w, r)
}

// accelFileURL 给测试和服务端用：按派生密钥拼一条签名链接（不含加速地址前缀）。
func accelFileURL(key []byte, scope string, exp int64, id, w string, download bool) string {
	v := url.Values{}
	if w != "" {
		v.Set("w", w)
	}
	if download {
		v.Set("download", "1")
	}
	v.Set("c", scope)
	v.Set("e", strconv.FormatInt(exp, 10))
	v.Set("s", accelSig(key, id, w, download))
	return fmt.Sprintf("%s%s?%s", accelFilePath, url.PathEscape(id), v.Encode())
}
