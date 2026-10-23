package hub

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"

	webpush "github.com/SherClockHolmes/webpush-go"

	"github.com/NekroAI/kitestring/internal/notify"
	"github.com/NekroAI/kitestring/internal/store"
)

// 系统推送（R-49）：装到主屏的 PWA / 桌面浏览器直接收系统通知，和 Bark、企业微信并存。
// Hub 首次需要时生成一对 VAPID 密钥存在本机设置里；推送内容只有标题、一句话和要打开的页面。

const (
	settingVAPIDPublic  = "vapid_public"
	settingVAPIDPrivate = "vapid_private"
)

var vapidMu sync.Mutex

// pushClient 发推送用的 HTTP 客户端（测试时换成信任本地证书的）。
var pushClient webpush.HTTPClient = http.DefaultClient

// SetPushClient 替换发推送用的 HTTP 客户端（测试用）。
func SetPushClient(c webpush.HTTPClient) { pushClient = c }

// vapidKeys 返回 VAPID 密钥对，没有就生成。
func (h *Hub) vapidKeys(ctx context.Context) (pub, priv string, err error) {
	vapidMu.Lock()
	defer vapidMu.Unlock()
	if pub, err = h.store.GetSetting(ctx, settingVAPIDPublic); err != nil {
		return
	}
	if priv, err = h.store.GetSetting(ctx, settingVAPIDPrivate); err != nil {
		return
	}
	if pub != "" && priv != "" {
		return
	}
	if priv, pub, err = webpush.GenerateVAPIDKeys(); err != nil {
		return
	}
	if err = h.store.SetSetting(ctx, settingVAPIDPrivate, priv); err != nil {
		return
	}
	err = h.store.SetSetting(ctx, settingVAPIDPublic, pub)
	return
}

type pushPayload struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	URL   string `json:"url"` // 站内路径，点通知打开
	Tag   string `json:"tag,omitempty"`
}

// sendPush 把通知推给所有订阅了的浏览器；推送服务说订阅已失效（404 / 410）时删掉它。
func (h *Hub) sendPush(ctx context.Context, m notify.Message, path string, only string) (sent int, firstErr error) {
	subs, err := h.store.ListPushSubs(ctx)
	if err != nil || len(subs) == 0 {
		return 0, err
	}
	pub, priv, err := h.vapidKeys(ctx)
	if err != nil {
		return 0, err
	}
	body, _ := json.Marshal(pushPayload{Title: m.Title, Body: m.Body, URL: path, Tag: m.Event})
	for _, s := range subs {
		if only != "" && s.ID != only && s.Endpoint != only {
			continue
		}
		contact := s.Origin
		if !strings.HasPrefix(contact, "https://") {
			contact = "mailto:noreply@kitestring.invalid"
		}
		resp, err := webpush.SendNotificationWithContext(ctx, body, &webpush.Subscription{Endpoint: s.Endpoint, Keys: webpush.Keys{P256dh: s.P256dh, Auth: s.Auth}}, &webpush.Options{
			HTTPClient: pushClient, Subscriber: contact, VAPIDPublicKey: pub, VAPIDPrivateKey: priv, TTL: 3600, Urgency: webpush.UrgencyHigh,
		})
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		resp.Body.Close()
		switch {
		case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
			_ = h.store.DeletePushSub(ctx, s.ID)
			h.broker.publish("settings")
		case resp.StatusCode/100 == 2:
			sent++
			_ = h.store.TouchPushSub(ctx, s.ID)
		default:
			h.log.Warn("系统推送失败", "status", resp.StatusCode, "body", string(msg))
		}
	}
	return sent, firstErr
}

// pushWants：推送沿用通知的事件规则（工作空间配了渠道时用它的，否则用默认的）。
func pushWants(cfg store.NotifyConfig, event string) bool { return notify.Wants(cfg, event) }
