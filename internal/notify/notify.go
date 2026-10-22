// Package notify 发送 Bark 与企业微信群机器人通知（D8）。
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/NekroAI/kitestring/internal/store"
)

// 通知事件类型。
const (
	EventDecisionRaised    = "decision_raised"
	EventWaitingApproval   = "waiting_approval"
	EventWaitingReply      = "waiting_reply"
	EventRequirementReview = "requirement_review"
	EventDeviceLost        = "device_lost"
)

// AllEvents 是可配置的通知事件，用于设置页展示。
var AllEvents = []string{EventDecisionRaised, EventWaitingApproval, EventRequirementReview, EventWaitingReply, EventDeviceLost}

// DefaultEvents 是未显式配置时启用的事件。
var DefaultEvents = []string{EventDecisionRaised, EventWaitingApproval, EventRequirementReview}

// Message 是一条通知。
type Message struct {
	Event string
	Title string
	Body  string
	URL   string // 点击跳转的 Web 地址，可为空
	Group string // Bark 分组
}

// Wants 判断配置是否订阅了该事件。
func Wants(cfg store.NotifyConfig, event string) bool {
	events := cfg.Events
	if len(events) == 0 {
		events = DefaultEvents
	}
	for _, e := range events {
		if e == event {
			return true
		}
	}
	return false
}

// Send 向配置中的所有渠道发送通知，返回各渠道错误的合并。
func Send(ctx context.Context, cfg store.NotifyConfig, m Message) error {
	var errs []string
	if cfg.Bark != nil && cfg.Bark.Key != "" {
		if err := sendBark(ctx, *cfg.Bark, m); err != nil {
			errs = append(errs, "Bark："+err.Error())
		}
	}
	if cfg.WeCom != nil && cfg.WeCom.Webhook != "" {
		if err := sendWeCom(ctx, *cfg.WeCom, m); err != nil {
			errs = append(errs, "企业微信："+err.Error())
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "；"))
	}
	return nil
}

// HasChannel 判断是否配置了至少一个渠道。
func HasChannel(cfg store.NotifyConfig) bool {
	return (cfg.Bark != nil && cfg.Bark.Key != "") || (cfg.WeCom != nil && cfg.WeCom.Webhook != "")
}

func sendBark(ctx context.Context, c store.BarkConfig, m Message) error {
	server := strings.TrimSuffix(c.Server, "/")
	if server == "" {
		server = "https://api.day.app"
	}
	body := map[string]any{"title": m.Title, "body": m.Body, "group": m.Group}
	if m.URL != "" {
		body["url"] = m.URL
	}
	return postJSON(ctx, server+"/"+c.Key, body)
}

func sendWeCom(ctx context.Context, c store.WeComConfig, m Message) error {
	content := m.Title + "\n" + m.Body
	if m.URL != "" {
		content += "\n" + m.URL
	}
	return postJSON(ctx, c.Webhook, map[string]any{"msgtype": "text", "text": map[string]string{"content": content}})
}

func postJSON(ctx context.Context, url string, body any) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("HTTP %d：%s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	// 企业微信在 HTTP 200 中用 errcode 表示失败。
	var wc struct {
		ErrCode *int   `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	if json.Unmarshal(raw, &wc) == nil && wc.ErrCode != nil && *wc.ErrCode != 0 {
		return fmt.Errorf("errcode %d：%s", *wc.ErrCode, wc.ErrMsg)
	}
	return nil
}
