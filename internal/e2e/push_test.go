package e2e

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/NekroAI/kitestring/internal/hub"
	"github.com/NekroAI/kitestring/internal/store"
)

// R-49：浏览器订阅系统推送 → 测试推送送到推送服务（加密、带 VAPID 签名）；推送服务说订阅失效时自动删除。
func TestWebPush(t *testing.T) {
	e := setup(t)
	var got atomic.Int32
	var gone atomic.Bool
	var auth atomic.Value
	svc := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth.Store(r.Header.Get("Authorization"))
		if gone.Load() {
			w.WriteHeader(http.StatusGone)
			return
		}
		if r.Header.Get("Content-Encoding") == "aes128gcm" && r.ContentLength > 0 {
			got.Add(1)
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer svc.Close()
	hub.SetPushClient(svc.Client())
	defer hub.SetPushClient(http.DefaultClient)

	var info struct {
		PublicKey string          `json:"public_key"`
		Subs      []store.PushSub `json:"subs"`
	}
	e.call("GET", "/api/push", nil, &info)
	if info.PublicKey == "" || len(info.Subs) != 0 {
		t.Fatalf("应生成 VAPID 公钥、还没有订阅：%+v", info)
	}
	key, _ := ecdh.P256().GenerateKey(rand.Reader)
	secret := make([]byte, 16)
	rand.Read(secret)
	b64 := base64.RawURLEncoding.EncodeToString
	var sub store.PushSub
	e.call("POST", "/api/push/subscribe", map[string]any{"endpoint": svc.URL + "/push/abc", "p256dh": b64(key.PublicKey().Bytes()), "auth": b64(secret), "name": "测试设备"}, &sub)
	if code, body := e.try("POST", "/api/push/subscribe", map[string]any{"endpoint": "http://x", "p256dh": "a", "auth": "b"}); code != 400 {
		t.Log(body)
		t.Fatalf("非 https 推送地址应被拒绝：%d", code)
	}

	e.call("POST", "/api/push/test", map[string]any{"ref": sub.ID}, nil)
	if got.Load() != 1 || !strings.HasPrefix(auth.Load().(string), "vapid t=") {
		t.Fatalf("推送服务应收到 1 条加密推送、带 VAPID 签名：%d %v", got.Load(), auth.Load())
	}
	// 再订阅同一地址不重复
	e.call("POST", "/api/push/subscribe", map[string]any{"endpoint": svc.URL + "/push/abc", "p256dh": b64(key.PublicKey().Bytes()), "auth": b64(secret), "name": "改名"}, nil)
	e.call("GET", "/api/push", nil, &info)
	if len(info.Subs) != 1 || info.Subs[0].Name != "改名" || info.Subs[0].LastOKAt == nil {
		t.Fatalf("同一地址只留一条、记下最近送达：%+v", info.Subs)
	}
	// 订阅失效 → 自动删除
	gone.Store(true)
	e.try("POST", "/api/push/test", map[string]any{"ref": sub.ID})
	e.call("GET", "/api/push", nil, &info)
	if len(info.Subs) != 0 {
		t.Fatalf("推送服务返回 410 后应删除订阅：%+v", info.Subs)
	}
}
