package hub

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/NekroAI/kitestring/internal/store"
)

// passkey 登录（R-7）：密码登录之外多一个选项，不强制。
// 用 WebAuthn 标准，只存公钥与计数器。passkey 绑定在注册时的域名上：只有在已登录状态下注册过 passkey 的网页来源，
// 才能用 passkey 登录（通过 IP 或别的域名访问时自动只剩密码登录）。登录同样受按 IP 的失败限流保护。

const settingPasskeyUser = "passkey_user_id"

// passkeyUser 是 WebAuthn 里的“用户”：纸鸢是单用户，用户句柄是一个随机 ID。
type passkeyUser struct {
	id    []byte
	creds []webauthn.Credential
}

func (u passkeyUser) WebAuthnID() []byte                         { return u.id }
func (u passkeyUser) WebAuthnName() string                       { return "kitestring" }
func (u passkeyUser) WebAuthnDisplayName() string                { return "纸鸢 Kitestring" }
func (u passkeyUser) WebAuthnCredentials() []webauthn.Credential { return u.creds }

// ceremony 是一次注册 / 登录过程在服务端的状态（5 分钟内有效，只能用一次）。
type ceremony struct {
	data   webauthn.SessionData
	origin string
	rpID   string
	name   string
	exp    time.Time
}

type ceremonies struct {
	mu sync.Mutex
	m  map[string]ceremony
}

func (c *ceremonies) put(x ceremony) string {
	b := make([]byte, 18)
	_, _ = rand.Read(b)
	tok := base64.RawURLEncoding.EncodeToString(b)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]ceremony{}
	}
	for k, v := range c.m {
		if time.Now().After(v.exp) {
			delete(c.m, k)
		}
	}
	x.exp = time.Now().Add(5 * time.Minute)
	c.m[tok] = x
	return tok
}

func (c *ceremonies) take(tok string) (ceremony, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	x, ok := c.m[tok]
	delete(c.m, tok)
	return x, ok && time.Now().Before(x.exp)
}

var passkeyCeremonies ceremonies

// requestOrigin 取浏览器发来的 Origin，要求是 https（或本机调试用的 localhost / 127.0.0.1）。
func requestOrigin(r *http.Request) (origin, rpID string, err error) {
	o := r.Header.Get("Origin")
	u, err := url.Parse(o)
	if o == "" || err != nil || u.Host == "" {
		return "", "", errors.New("缺少网页来源")
	}
	host := u.Hostname()
	if u.Scheme != "https" && host != "localhost" && host != "127.0.0.1" {
		return "", "", errors.New("passkey 只能在 https 下使用")
	}
	return u.Scheme + "://" + u.Host, host, nil
}

func newWebAuthn(origin, rpID string) (*webauthn.WebAuthn, error) {
	return webauthn.New(&webauthn.Config{RPID: rpID, RPDisplayName: "纸鸢 Kitestring", RPOrigins: []string{origin}})
}

// passkeyUserFor 读出用户句柄（没有就生成）和某个来源下登记的 passkey。
func (h *Hub) passkeyUserFor(ctx context.Context, origin string) (passkeyUser, []store.Passkey, error) {
	idHex, err := h.store.GetSetting(ctx, settingPasskeyUser)
	if err != nil {
		return passkeyUser{}, nil, err
	}
	if idHex == "" {
		b := make([]byte, 32)
		_, _ = rand.Read(b)
		idHex = hex.EncodeToString(b)
		if err := h.store.SetSetting(ctx, settingPasskeyUser, idHex); err != nil {
			return passkeyUser{}, nil, err
		}
	}
	id, _ := hex.DecodeString(idHex)
	all, err := h.store.ListPasskeys(ctx)
	if err != nil {
		return passkeyUser{}, nil, err
	}
	u := passkeyUser{id: id}
	var mine []store.Passkey
	for _, p := range all {
		if p.Origin != origin {
			continue
		}
		var c webauthn.Credential
		if json.Unmarshal([]byte(p.Credential), &c) == nil {
			u.creds = append(u.creds, c)
			mine = append(mine, p)
		}
	}
	return u, mine, nil
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// handlePasskeyRegisterBegin：已登录时开始登记一个 passkey。
func (h *Hub) handlePasskeyRegisterBegin(w http.ResponseWriter, r *http.Request) {
	origin, rpID, err := requestOrigin(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var in struct {
		Name string `json:"name"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&in)
	user, _, err := h.passkeyUserFor(r.Context(), origin)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	wa, err := newWebAuthn(origin, rpID)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var exclude []protocol.CredentialDescriptor
	for _, c := range user.creds {
		exclude = append(exclude, c.Descriptor())
	}
	creation, data, err := wa.BeginRegistration(user, webauthn.WithResidentKeyRequirement(protocol.ResidentKeyRequirementRequired), webauthn.WithExclusions(exclude))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	tok := passkeyCeremonies.put(ceremony{data: *data, origin: origin, rpID: rpID, name: in.Name})
	writeJSON(w, http.StatusOK, map[string]any{"ceremony": tok, "publicKey": creation.Response})
}

// handlePasskeyRegisterFinish：校验浏览器返回的新凭据并保存。
func (h *Hub) handlePasskeyRegisterFinish(w http.ResponseWriter, r *http.Request) {
	x, ok := passkeyCeremonies.take(r.URL.Query().Get("ceremony"))
	if !ok {
		writeErr(w, http.StatusBadRequest, "登记已过期，请重试")
		return
	}
	if origin, _, err := requestOrigin(r); err != nil || origin != x.origin {
		writeErr(w, http.StatusBadRequest, "网页来源不一致")
		return
	}
	user, _, err := h.passkeyUserFor(r.Context(), x.origin)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	wa, _ := newWebAuthn(x.origin, x.rpID)
	cred, err := wa.FinishRegistration(user, x.data, r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "passkey 校验失败："+protocolErr(err))
		return
	}
	raw, _ := json.Marshal(cred)
	name := x.name
	if name == "" {
		name = "passkey"
	}
	p := store.Passkey{ID: base64.RawURLEncoding.EncodeToString(cred.ID), Name: name, Origin: x.origin, RPID: x.rpID, Credential: string(raw)}
	if err := h.store.SavePasskey(r.Context(), p); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.broker.publish("settings")
	writeJSON(w, http.StatusOK, map[string]any{"id": p.ID, "name": p.Name})
}

// handlePasskeyLoginBegin：开始用 passkey 登录（不需要登录）。
func (h *Hub) handlePasskeyLoginBegin(w http.ResponseWriter, r *http.Request) {
	origin, rpID, err := requestOrigin(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if d := h.logins.locked(clientFrom(r.Context()).IP); d > 0 {
		writeErr(w, http.StatusTooManyRequests, fmt.Sprintf("登录失败次数过多，请 %d 分钟后再试", int(d.Minutes())+1))
		return
	}
	if _, mine, err := h.passkeyUserFor(r.Context(), origin); err != nil || len(mine) == 0 {
		writeErr(w, http.StatusBadRequest, "这个地址还没有登记 passkey，请用密码登录")
		return
	}
	wa, err := newWebAuthn(origin, rpID)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	assertion, data, err := wa.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	tok := passkeyCeremonies.put(ceremony{data: *data, origin: origin, rpID: rpID})
	writeJSON(w, http.StatusOK, map[string]any{"ceremony": tok, "publicKey": assertion.Response})
}

// handlePasskeyLoginFinish：校验签名，成功则发登录 cookie；失败计入限流。
func (h *Hub) handlePasskeyLoginFinish(w http.ResponseWriter, r *http.Request) {
	ip := clientFrom(r.Context()).IP
	if d := h.logins.attempt(ip); d > 0 {
		writeErr(w, http.StatusTooManyRequests, fmt.Sprintf("登录失败次数过多，请 %d 分钟后再试", int(d.Minutes())+1))
		return
	}
	fail := func(msg string) {
		time.Sleep(600 * time.Millisecond)
		if h.logins.failed(ip) {
			writeErr(w, http.StatusTooManyRequests, fmt.Sprintf("登录失败次数过多，请 %d 分钟后再试", int(loginLockFor.Minutes())))
			return
		}
		writeErr(w, http.StatusUnauthorized, msg)
	}
	x, ok := passkeyCeremonies.take(r.URL.Query().Get("ceremony"))
	if !ok {
		writeErr(w, http.StatusBadRequest, "登录已过期，请重试")
		return
	}
	if origin, _, err := requestOrigin(r); err != nil || origin != x.origin {
		fail("网页来源不一致")
		return
	}
	user, mine, err := h.passkeyUserFor(r.Context(), x.origin)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	wa, _ := newWebAuthn(x.origin, x.rpID)
	handler := func(rawID, userHandle []byte) (webauthn.User, error) {
		if string(userHandle) != string(user.id) {
			return nil, errors.New("不认识的用户")
		}
		return user, nil
	}
	_, cred, err := wa.FinishPasskeyLogin(handler, x.data, r)
	if err != nil {
		fail("passkey 校验失败：" + protocolErr(err))
		return
	}
	id := base64.RawURLEncoding.EncodeToString(cred.ID)
	found := false
	for _, p := range mine {
		found = found || p.ID == id
	}
	if !found {
		fail("这个 passkey 没有登记")
		return
	}
	raw, _ := json.Marshal(cred)
	_ = h.store.UsePasskey(r.Context(), id, string(raw))
	h.logins.success(ip)
	c, err := h.newWebSession(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	http.SetCookie(w, &c)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func protocolErr(err error) string {
	var pe *protocol.Error
	if errors.As(err, &pe) && pe.DevInfo != "" {
		return pe.Details + "（" + pe.DevInfo + "）"
	}
	return err.Error()
}

// passkeyOrigins 返回登记过 passkey 的网页来源（登录页据此决定是否显示 passkey 登录）。
func (h *Hub) passkeyOrigins(ctx context.Context) []string {
	all, err := h.store.ListPasskeys(ctx)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	out := []string{}
	for _, p := range all {
		if !seen[p.Origin] {
			seen[p.Origin] = true
			out = append(out, p.Origin)
		}
	}
	return out
}
