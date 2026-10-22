package hub

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"golang.org/x/crypto/bcrypt"

	"github.com/NekroAI/kitestring/internal/store"
)

// 鉴权方案见 D18：设备用可吊销的独立 token，Web 用单用户密码 + HttpOnly 会话 cookie。
const (
	passwordKey   = "admin_password_bcrypt"
	cookieName    = "ks_session"
	webSessionTTL = 30 * 24 * time.Hour
	devicePrefix  = "kst_"
)

type ctxKey int

const deviceKey ctxKey = 1

func deviceFrom(ctx context.Context) (store.Device, bool) {
	d, ok := ctx.Value(deviceKey).(store.Device)
	return d, ok
}

// HashToken 计算 token 的存储哈希。token 为高熵随机串，SHA-256 即可。
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func randomToken(prefix string) string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return prefix + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b))
}

// NewDeviceToken 生成设备 token。
func NewDeviceToken() string { return randomToken(devicePrefix) }

// SetPassword 设置 Web 登录密码，并注销所有已登录会话。
func (h *Hub) SetPassword(ctx context.Context, password string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	if err := h.store.SetSetting(ctx, passwordKey, string(hash)); err != nil {
		return err
	}
	return h.store.DeleteWebSession(ctx, "", true)
}

func (h *Hub) passwordSet(ctx context.Context) (bool, error) {
	v, err := h.store.GetSetting(ctx, passwordKey)
	return v != "", err
}

func (h *Hub) checkPassword(ctx context.Context, password string) bool {
	v, err := h.store.GetSetting(ctx, passwordKey)
	if err != nil || v == "" {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(v), []byte(password)) == nil
}

func (h *Hub) newWebSession(ctx context.Context) (http.Cookie, error) {
	tok := randomToken("")
	if err := h.store.CreateWebSession(ctx, HashToken(tok), webSessionTTL); err != nil {
		return http.Cookie{}, err
	}
	return http.Cookie{Name: cookieName, Value: tok, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: int(webSessionTTL.Seconds())}, nil
}

func (h *Hub) webAuthed(r *http.Request) bool {
	c, err := r.Cookie(cookieName)
	if err != nil || c.Value == "" {
		return false
	}
	ok, err := h.store.ValidWebSession(r.Context(), HashToken(c.Value))
	return err == nil && ok
}

// verifyPassword 校验密码并按客户端 IP 限流（D18 公网加固）。
func (h *Hub) verifyPassword(ctx context.Context, password string) error {
	ip := clientFrom(ctx).IP
	if d := h.logins.attempt(ip); d > 0 {
		return huma.Error429TooManyRequests(fmt.Sprintf("密码错误次数过多，请 %d 分钟后再试", int(d.Minutes())+1))
	}
	if h.checkPassword(ctx, password) {
		h.logins.success(ip)
		return nil
	}
	time.Sleep(600 * time.Millisecond)
	if h.logins.failed(ip) {
		h.log.Warn("登录失败次数过多，已锁定", "ip", ip, "minutes", int(loginLockFor.Minutes()))
		return huma.Error429TooManyRequests(fmt.Sprintf("密码错误次数过多，请 %d 分钟后再试", int(loginLockFor.Minutes())))
	}
	return huma.Error401Unauthorized("密码错误")
}

// requireWeb 要求有效的 Web 登录会话。
func (h *Hub) requireWeb(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !h.webAuthed(r) {
			writeErr(w, http.StatusUnauthorized, "未登录")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// requireDevice 要求有效的设备 token（Authorization: Bearer kst_...）。
func (h *Hub) requireDevice(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !strings.HasPrefix(tok, devicePrefix) {
			writeErr(w, http.StatusUnauthorized, "缺少设备 token")
			return
		}
		dev, err := h.store.DeviceByToken(r.Context(), HashToken(tok))
		if err != nil {
			writeErr(w, http.StatusUnauthorized, "设备 token 无效或已吊销")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), deviceKey, dev)))
	})
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"status": code, "title": http.StatusText(code), "detail": msg})
}
