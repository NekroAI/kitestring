// Package hub 实现 ks hub：事件接收、状态推导、决策下行、REST + SSE API、通知与摘要。
package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/NekroAI/kitestring/internal/notify"
	"github.com/NekroAI/kitestring/internal/protocol"
	"github.com/NekroAI/kitestring/internal/store"
	"github.com/NekroAI/kitestring/internal/summary"
)

const (
	// lostGrace 是设备断线后把会话标为失联前的宽限期。
	lostGrace = 45 * time.Second
	// redeliverWindow 是恢复 / 压缩时补注入的已接收决策时间窗（D15）。
	redeliverWindow = 24 * time.Hour
	// endedVisible 是已结束会话在实例墙上保留的时长。
	endedVisible = 6 * time.Hour
)

// Settings 是保存在数据库里的 Hub 运行配置。
type Settings struct {
	PublicURL     string             `json:"public_url"`
	SummaryMode   string             `json:"summary_mode"` // off | hub | device
	Summary       summary.Config     `json:"summary"`
	NotifyDefault store.NotifyConfig `json:"notify_default"`
}

const settingsKey = "hub_settings"

// Hub 是服务端核心。
type Hub struct {
	store  *store.Store
	log    *slog.Logger
	broker *broker

	mu       sync.Mutex
	conns    map[string]*deviceConn // deviceID → 连接
	lostTmr  map[string]*time.Timer
	notified map[string]time.Time // 通知去重键 → 上次发送时间
}

// New 创建 Hub。
func New(st *store.Store, log *slog.Logger) *Hub {
	return &Hub{
		store:    st,
		log:      log,
		broker:   newBroker(),
		conns:    map[string]*deviceConn{},
		lostTmr:  map[string]*time.Timer{},
		notified: map[string]time.Time{},
	}
}

// Settings 读取配置。
func (h *Hub) Settings(ctx context.Context) (Settings, error) {
	var s Settings
	raw, err := h.store.GetSetting(ctx, settingsKey)
	if err != nil || raw == "" {
		s.SummaryMode = "off"
		return s, err
	}
	err = json.Unmarshal([]byte(raw), &s)
	if s.SummaryMode == "" {
		s.SummaryMode = "off"
	}
	return s, err
}

// SaveSettings 保存配置并把设备相关部分下发给在线 daemon。
func (h *Hub) SaveSettings(ctx context.Context, s Settings) error {
	b, _ := json.Marshal(s)
	if err := h.store.SetSetting(ctx, settingsKey, string(b)); err != nil {
		return err
	}
	cfg := h.deviceConfig(s)
	h.mu.Lock()
	for _, c := range h.conns {
		c.send(protocol.Envelope{Type: protocol.MsgConfig, Config: &cfg})
	}
	h.mu.Unlock()
	return nil
}

func (h *Hub) deviceConfig(s Settings) protocol.DeviceConfig {
	return protocol.DeviceConfig{
		SendLastReply:   s.SummaryMode == "hub" && s.Summary.Enabled(),
		SummaryOnDevice: s.SummaryMode == "device",
	}
}

// Start 启动 HTTP 服务，阻塞直到 ctx 结束。
func (h *Hub) Start(ctx context.Context, addr string, web http.Handler) error {
	if err := h.store.ResetOnline(ctx); err != nil {
		return err
	}
	// 启动时所有设备视为离线：上次运行遗留的未结束会话先标失联，设备重连后由事件恢复。
	devs, err := h.store.ListDevices(ctx)
	if err != nil {
		return err
	}
	for _, d := range devs {
		if _, err := h.store.MarkDeviceLost(ctx, d.ID); err != nil {
			return err
		}
	}
	srv := &http.Server{Addr: addr, Handler: h.Handler(web), ReadHeaderTimeout: 10 * time.Second}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	h.log.Info("Hub 已启动", "addr", addr)
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// ingest 处理一条设备上报的事件：幂等落库、推导会话状态、触发摘要与通知。
func (h *Hub) ingest(ctx context.Context, dev store.Device, ev protocol.Event) error {
	if ev.ID == "" || ev.SessionID == "" || ev.Tool == "" {
		return fmt.Errorf("事件缺少必要字段")
	}
	inserted, err := h.store.InsertEvent(ctx, dev.ID, ev)
	if err != nil || !inserted {
		return err
	}
	key := protocol.SessionKey(ev.Tool, ev.SessionID)
	var prev *store.Session
	if cur, err := h.store.GetSession(ctx, key); err == nil {
		prev = &cur
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	projectID := ""
	if ev.Cwd != "" {
		// 每次都解析：路径映射有缓存，开销小；remote 后补时可按 D16 重新归并。
		if projectID, err = h.store.ResolveProject(ctx, dev.ID, ev.Cwd, ev.Git); err != nil {
			return err
		}
	}
	next := applyEvent(prev, dev.ID, projectID, ev)
	if err := h.store.SaveSession(ctx, next); err != nil {
		return err
	}
	h.broker.publish("sessions")

	prevState := ""
	if prev != nil {
		prevState = prev.State
	}
	if next.State != prevState && (next.State == store.StateWaitApproval || next.State == store.StateWaitReply) {
		full, err := h.store.GetSession(ctx, key)
		if err != nil {
			return err
		}
		if full.State == store.StateWaitApproval {
			h.notifySession(full, notify.EventWaitingApproval, "等待授权", full.StateDetail)
		} else {
			h.notifySession(full, notify.EventWaitingReply, "等待回复", full.Summary)
		}
	}
	if ev.Name == "Stop" && ev.LastReply != "" {
		go h.summarize(key, next.LastPrompt, ev.LastReply)
	}
	return nil
}

func (h *Hub) summarize(key, prompt, reply string) {
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	s, err := h.Settings(ctx)
	if err != nil || s.SummaryMode != "hub" || !s.Summary.Enabled() {
		return
	}
	text, err := summary.Generate(ctx, s.Summary, prompt, reply)
	if err != nil {
		h.log.Warn("生成摘要失败", "session", key, "err", err)
		return
	}
	if err := h.store.SetSessionSummary(ctx, key, text); err != nil {
		h.log.Warn("保存摘要失败", "err", err)
		return
	}
	h.broker.publish("sessions")
}

// pushDecisions 把设备上待注入的决策全集下发给 daemon。
func (h *Hub) pushDecisions(ctx context.Context, deviceID string) {
	h.mu.Lock()
	c := h.conns[deviceID]
	h.mu.Unlock()
	if c == nil {
		return
	}
	list, err := h.store.DecisionsForDevice(ctx, deviceID, time.Now().Add(-redeliverWindow))
	if err != nil {
		h.log.Warn("查询待注入决策失败", "err", err)
		return
	}
	out := make([]protocol.Decision, 0, len(list))
	for _, d := range list {
		pd := protocol.Decision{ID: d.ID, Code: d.Code, SessionKey: d.SessionKey, Question: d.Question, Answer: d.Answer, Delivered: d.Status == store.DecDelivered}
		if d.AnsweredAt != nil {
			pd.AnsweredAt = *d.AnsweredAt
		}
		out = append(out, pd)
	}
	c.send(protocol.Envelope{Type: protocol.MsgDecisions, Decisions: out})
}

// decisionsChanged 在决策变化后刷新相关设备的下行缓存并通知 Web。
func (h *Hub) decisionsChanged(ctx context.Context, d store.Decision) {
	h.broker.publish("decisions", "sessions", "requirements")
	if d.SessionKey == "" {
		return
	}
	if x, err := h.store.GetSession(ctx, d.SessionKey); err == nil {
		h.pushDecisions(ctx, x.DeviceID)
	}
}

// notifySession 按会话所在项目的工作空间路由通知；同一会话同一事件 60 秒内只发一次。
func (h *Hub) notifySession(x store.Session, event, title, body string) {
	dedup := event + "|" + x.Key
	h.mu.Lock()
	if t, ok := h.notified[dedup]; ok && time.Since(t) < time.Minute {
		h.mu.Unlock()
		return
	}
	h.notified[dedup] = time.Now()
	h.mu.Unlock()
	name := x.ProjectName
	if name == "" {
		name = x.Cwd
	}
	h.sendNotify(x.ProjectID, notify.Message{
		Event: event,
		Title: fmt.Sprintf("%s · %s", title, name),
		Body:  fmt.Sprintf("%s @ %s %s", x.Tool, x.DeviceName, body),
		Group: "kitestring",
	}, "/")
}

// sendNotify 异步发送通知。projectID 为空或项目未归属工作空间时使用默认渠道。
func (h *Hub) sendNotify(projectID string, m notify.Message, path string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		s, err := h.Settings(ctx)
		if err != nil {
			return
		}
		cfg := s.NotifyDefault
		if projectID != "" {
			if w, err := h.store.WorkspaceForProject(ctx, projectID); err == nil && notify.HasChannel(w.Notify) {
				cfg = w.Notify
			}
		}
		if !notify.HasChannel(cfg) || !notify.Wants(cfg, m.Event) {
			return
		}
		if s.PublicURL != "" {
			m.URL = s.PublicURL + path
		}
		if err := notify.Send(ctx, cfg, m); err != nil {
			h.log.Warn("发送通知失败", "event", m.Event, "err", err)
		}
	}()
}
