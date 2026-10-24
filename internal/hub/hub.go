// Package hub 实现 ks hub：事件接收、状态推导、决策下行、REST + SSE API、通知与摘要。
package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/NekroAI/kitestring/internal/notify"
	"github.com/NekroAI/kitestring/internal/protocol"
	"github.com/NekroAI/kitestring/internal/roles"
	"github.com/NekroAI/kitestring/internal/store"
	"github.com/NekroAI/kitestring/internal/summary"
)

const (
	// lostGrace 是设备断线后把会话标为失联前的宽限期。
	lostGrace = 45 * time.Second
	// redeliverWindow 是恢复 / 压缩时补注入的已接收决策时间窗（D15）。
	redeliverWindow = 24 * time.Hour
	// recentWindow 是工作台展示的会话活跃时间窗。
	recentWindow = 24 * time.Hour
)

// Settings 是保存在数据库里的 Hub 运行配置。
type Settings struct {
	PublicURL     string             `json:"public_url"`
	SummaryMode   string             `json:"summary_mode,omitempty"` // 旧版字段，仅用于迁移
	Summary       summary.Config     `json:"summary"`                // Hub 侧模型（OpenAI 兼容接口）
	AI            AIFeatures         `json:"ai"`
	NotifyDefault store.NotifyConfig `json:"notify_default"`
	Upload        UploadLimits       `json:"upload"`
	// DevRequestMinutes：PM 推荐开 Dev 会话的批准时限（分钟），到点没批视为拒绝（R-169）；0 = 默认 30
	DevRequestMinutes int `json:"dev_request_minutes,omitempty"`
	// CompactTokens：上下文超过它就提示「建议压缩」（R-224），也是会话圆环变红的线、一半处变黄（R-225）；0 = 默认 30 万
	CompactTokens int `json:"compact_tokens,omitempty"`
}

const (
	defaultCompactTokens = 300_000
	minCompactTokens     = 50_000
	maxCompactTokens     = 2_000_000
)

// compactTokens 返回生效的建议压缩阈值。
func (s Settings) compactTokens() int {
	if s.CompactTokens <= 0 {
		return defaultCompactTokens
	}
	return min(max(s.CompactTokens, minCompactTokens), maxCompactTokens)
}

// UploadLimits 附件大小上限（MB），0 表示用默认值（R-72）；FileMB 是图片、录屏以外的文件（R-221）。
type UploadLimits struct {
	ImageMB int `json:"image_mb,omitempty"`
	VideoMB int `json:"video_mb,omitempty"`
	FileMB  int `json:"file_mb,omitempty"`
}

const (
	defaultImageMB = 10
	defaultVideoMB = 50
	defaultFileMB  = 20
	maxUploadMB    = 4096
)

// bytes 返回生效的上限（字节）。
func (u UploadLimits) bytes() (image, video, file int64) {
	pick := func(v, def int) int64 {
		if v <= 0 {
			v = def
		}
		return int64(min(v, maxUploadMB)) << 20
	}
	return pick(u.ImageMB, defaultImageMB), pick(u.VideoMB, defaultVideoMB), pick(u.FileMB, defaultFileMB)
}

const settingsKey = "hub_settings"

// Hub 是服务端核心。
type Hub struct {
	store    *store.Store
	log      *slog.Logger
	broker   *broker
	logins   *loginLimiter
	ai       aiGuard
	filesDir string

	mu       sync.Mutex
	conns    map[string]*deviceConn // deviceID → 连接
	lostTmr  map[string]*time.Timer
	notified map[string]time.Time   // 通知去重键 → 上次发送时间
	kicks    map[string]*time.Timer // 会话 → 待送达的整批需求（攒几秒再送，R-66）
	dirty    map[string]bool        // 待推送最新数据的会话（R-56，300ms 内合并）
	dirtyTmr *time.Timer
	orphans  map[string]deviceOrphans            // deviceID → daemon 上报的孤儿 / 已脱离进程（R-209，只在内存里，daemon 连上就重报）
	stops    map[string]chan protocol.StopResult // 停止请求 → 等 daemon 回结果
	lanes    *laneState                          // 泳道时间线（R-205）
}

// New 创建 Hub。
func New(st *store.Store, log *slog.Logger) *Hub {
	h := &Hub{
		store:    st,
		log:      log,
		broker:   newBroker(),
		logins:   newLoginLimiter(),
		conns:    map[string]*deviceConn{},
		lostTmr:  map[string]*time.Timer{},
		notified: map[string]time.Time{},
		kicks:    map[string]*time.Timer{},
		dirty:    map[string]bool{},
		orphans:  map[string]deviceOrphans{},
		stops:    map[string]chan protocol.StopResult{},
		lanes:    newLaneState(),
	}
	if st != nil {
		st.OnRequestVoided = h.onStatusRequestVoided // 改状态申请因状态被改动而作废，送回申请方（R-188）
	}
	return h
}

// Settings 读取配置。
func (h *Hub) Settings(ctx context.Context) (Settings, error) {
	var s Settings
	raw, err := h.store.GetSetting(ctx, settingsKey)
	if err != nil || raw == "" {
		return s, err
	}
	err = json.Unmarshal([]byte(raw), &s)
	// 迁移：旧版“在 Hub 上生成摘要”等价于只开启会话摘要；“在各设备上生成”已移除。
	if s.SummaryMode == "hub" && !s.AI.any() {
		s.AI.Summary = true
	}
	s.SummaryMode = ""
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
	return protocol.DeviceConfig{SendLastReply: s.Summary.Enabled() && s.AI.needsReply()}
}

// Start 启动 HTTP 服务，阻塞直到 ctx 结束。
func (h *Hub) Start(ctx context.Context, addr string, web http.Handler) error {
	if err := h.store.ResetOnline(ctx); err != nil {
		return err
	}
	if n, err := h.store.RepairPrompts(ctx, protocol.CleanPrompt); err != nil {
		h.log.Warn("修复会话最后消息失败", "err", err)
	} else if n > 0 {
		h.log.Info("已修复会话最后消息", "count", n)
	}
	if n, err := h.store.BackfillBackground(ctx, protocol.IsBackgroundPrompt); err != nil {
		h.log.Warn("补标后台任务失败", "err", err)
	} else if n > 0 {
		h.log.Info("已把 Codex 后台任务标为后台任务", "count", n)
	}
	// 启动时所有设备视为离线，但给与运行中断线相同的宽限期：daemon 很快重连时会话状态保持不变。
	devs, err := h.store.ListDevices(ctx)
	if err != nil {
		return err
	}
	h.mu.Lock()
	for _, d := range devs {
		if !d.Revoked {
			h.scheduleLostLocked(d)
		}
	}
	h.mu.Unlock()
	h.migrateAide(ctx) // 身份调整（R-190）：先清掉绑在非主助理上的窗口，再排到期通知
	h.resumeGrantExpiry(ctx)
	h.resumeRequestExpiry(ctx)
	go h.laneMaintenance(ctx)
	go h.reviewWatch(ctx) // 交给评审 30 分钟没动静的，照常弹给用户（R-237）
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
	if ev.Name == "UserPromptSubmit" {
		// 兼容旧版 daemon：Hub 侧再整理一次。
		text, auto := protocol.CleanPrompt(ev.Prompt)
		ev.Prompt = text
		if auto {
			ev.Source = protocol.SourceAuto
		}
	}
	// 最后回复单独存在会话上，不写进事件记录（每轮一条，最长 2 万字，R-109）
	reply := ev.Reply
	ev.Reply = ""
	if r := []rune(reply); len(r) > protocol.ReplyMax+1 { // 兼容不截断的设备
		reply = string(r[:protocol.ReplyMax]) + "…"
	}
	// 新提交（R-156）单独存进提交表，不写进事件记录
	commits := ev.Commits
	ev.Commits = nil
	inserted, err := h.store.InsertEvent(ctx, dev.ID, ev)
	if err != nil || !inserted {
		return err
	}
	key := protocol.SessionKey(ev.Tool, ev.SessionID)
	h.laneEvent(ctx, key, ev) // 泳道时间线（R-205）：只更新这一分钟
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
	if len(commits) > 0 && projectID != "" {
		if n, err := h.store.RecordCommits(ctx, projectID, key, commits); err != nil {
			h.log.Warn("记录提交失败", "err", err)
		} else if n > 0 {
			h.broker.publish("requirements")
		}
	}
	next := applyEvent(prev, dev.ID, projectID, ev)
	// ks wait --on 改订阅（R-164）；default 恢复按身份默认。先于“挂起”存下，看到待命时订阅已生效
	setWaitOn := func() error {
		if ev.Name != protocol.EventWait || ev.WaitOn == "" {
			return nil
		}
		v := ev.WaitOn
		if v == protocol.OnDefault {
			v = ""
		}
		return h.store.SetWaitOn(ctx, key, v)
	}
	if prev != nil {
		if err := setWaitOn(); err != nil {
			return err
		}
	}
	if err := h.store.SaveSession(ctx, next); err != nil {
		return err
	}
	if prev == nil {
		if err := setWaitOn(); err != nil {
			return err
		}
	}
	defer h.sessionChanged(key) // 存完工具版本、上下文占用等再推
	if ev.Name == "Stop" && reply != "" {
		if err := h.store.SetLastReply(ctx, key, reply, ev.Time); err != nil {
			return err
		}
	}
	if ev.Context > 0 { // 上下文占用（R-60）
		if err := h.store.SetContext(ctx, key, ev.Context, ev.ContextMax); err != nil {
			return err
		}
	}
	if ev.Tools != "" { // 工具版本有变化（R-59）
		v := ev.Tools
		if v == "ok" {
			v = ""
		}
		if err := h.store.SetToolsStale(ctx, key, v); err != nil {
			return err
		}
	}
	prevState := ""
	if prev != nil {
		prevState = prev.State
	}
	if !next.Background && next.State != prevState && (next.State == store.StateWaitApproval || next.State == store.StateWaitReply) {
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
	switch {
	case next.Background:
		// 后台任务不生成标题 / 摘要，不占模型额度（R-179）
	case ev.Name == "UserPromptSubmit" && ev.Source != protocol.SourceAuto:
		if next.DigestDone != "" || next.DigestAsk != "" {
			_ = h.store.ClearDigest(ctx, key)
		}
		if next.ReplyAsk != "" { // 你在终端里回了它，不再算在等你回复（R-117）
			_ = h.store.ClearReplyAsk(ctx, key)
		}
		go h.aiOnPrompt(key, ev.Prompt)
	case ev.Name == "Stop" && ev.LastReply != "":
		go h.aiOnStop(key, next.LastPrompt, ev.LastReply)
	}
	if next.Role == roles.Dev {
		online, wasOnline := next.Presence() != "offline", prev != nil && prev.Presence() != "offline"
		switch {
		case online && (!wasOnline || ev.Name == protocol.EventWait):
			// 开发上线了：够数就清除“需要开发”的提醒（R-87）；这次在线第一次挂上 ks wait 时告诉 PM（R-116）
			h.devUp(ctx, key, ev.Name == protocol.EventWait)
		case !online && wasOnline:
			h.devDown(key)
		}
	}
	if next.Role == roles.Advisor && ev.Name == protocol.EventWait {
		h.aideHint(ctx, next, false) // 顾问常驻但项目没有主助理：提示一次（R-190）
	}
	// 一轮做完或挂起待命：把排给它的待开始需求整批送过去（R-66）
	if (ev.Name == "Stop" || ev.Name == protocol.EventWait) && next.State != store.StateEnded {
		h.dispatch(ctx, key)
	}
	return nil
}

// sessionChanged 把会话的最新数据推给网页（R-56）：Agent 事件很密，同一批 300ms 内的变化合并成一次，
// 每个会话推一条完整数据，网页就地替换，不用整表重拉。
func (h *Hub) sessionChanged(key string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.dirty[key] = true
	if h.dirtyTmr != nil {
		return
	}
	h.dirtyTmr = time.AfterFunc(sessionPushDelay, func() {
		h.mu.Lock()
		keys := h.dirty
		h.dirty = map[string]bool{}
		h.dirtyTmr = nil
		h.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		for k := range keys {
			// 先读数据、后分配序号：列表若在两者之间查询，拿到的数据只会更新，下一次推送会再纠正
			if x, err := h.store.GetSession(ctx, k); err == nil {
				h.broker.publishSession(x)
			} else {
				h.broker.publish("sessions")
			}
		}
	})
}

var sessionPushDelay = 300 * time.Millisecond

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
	stale := h.staleDelivered(ctx, list)
	out := make([]protocol.Decision, 0, len(list))
	for _, d := range list {
		if stale[d.ID] {
			continue
		}
		pd := protocol.Decision{ID: d.ID, Code: d.Code, SessionKey: d.SessionKey, Question: d.Question, Answer: proxyText(d), Delivered: d.Status == store.DecDelivered,
			Kind: d.Kind, ReqCode: d.RequirementCode, ReqTitle: d.RequirementTitle, Quiet: d.Kind == store.KindTell && d.Question == quietTellQ,
			Replyable: d.Kind == store.KindTell && d.AssignedBy != ""}
		if d.Kind == store.KindPlan {
			pd.ReqCode, pd.ReqTitle = d.PlanCode, d.Question // 计划进展：编号与标题是计划的
		}
		for _, f := range d.AnswerFiles {
			pd.Files = append(pd.Files, protocol.File{ID: f.ID, Name: f.Name, Mime: f.Mime})
		}
		if d.AnsweredAt != nil {
			pd.AnsweredAt = *d.AnsweredAt
		}
		out = append(out, pd)
	}
	c.send(protocol.Envelope{Type: protocol.MsgDecisions, Decisions: out})
}

// refreshDelivered 在需求状态变化后刷新关联会话所在设备的下行决策：已完成 / 重新交验收后，
// 恢复 / 压缩时就不再补发它上面的旧意见（R-58）。
func (h *Hub) refreshDelivered(ctx context.Context, r store.Requirement) {
	seen := map[string]bool{}
	for _, k := range r.Sessions {
		if x, err := h.store.GetSession(ctx, k); err == nil && !seen[x.DeviceID] {
			seen[x.DeviceID] = true
			h.pushDecisions(ctx, x.DeviceID)
		}
	}
}

// staleDelivered 挑出已送达、但会话恢复 / 压缩时不必再补发的决策（R-58）：
// 需求已完成或搁置；修改意见对应的需求已重新交验收；交出 / 排队的需求已不在待开始或进行中；
// 同一计划只留最新一条进展，计划不在进行中时一条都不留。
func (h *Hub) staleDelivered(ctx context.Context, list []store.Decision) map[string]bool {
	stale := map[string]bool{}
	reqs := map[string]string{}
	status := func(id string) string {
		st, ok := reqs[id]
		if !ok {
			if r, err := h.store.GetRequirement(ctx, id); err == nil {
				st = r.Status
			}
			reqs[id] = st
		}
		return st
	}
	plans := map[string]bool{}
	latest := map[string]string{} // 计划 → 最新一条进展
	for _, d := range list {
		if d.Kind == store.KindPlan && d.PlanID != "" {
			latest[d.PlanID] = d.ID // list 按编号升序，后来的覆盖前面的
			if _, ok := plans[d.PlanID]; !ok {
				p, err := h.store.GetPlan(ctx, d.PlanID)
				plans[d.PlanID] = err == nil && p.Status == store.PlanActive
			}
		}
	}
	for _, d := range list {
		if d.Status != store.DecDelivered {
			continue
		}
		if d.Kind == store.KindPlan {
			stale[d.ID] = d.PlanID != "" && (latest[d.PlanID] != d.ID || !plans[d.PlanID])
			continue
		}
		if d.Kind == store.KindRole || d.Kind == store.KindTell {
			stale[d.ID] = true // 身份变更只送达一次；压缩后补发的是当前身份的提示词
			continue
		}
		if d.RequirementID == "" {
			continue
		}
		if d.Kind == store.KindReviewTask {
			// 审查任务：需求搁置 / 删除，或审查员已经交过意见，就不再补发（R-88）
			r, err := h.store.GetRequirement(ctx, d.RequirementID)
			stale[d.ID] = err != nil || r.Status == store.ReqShelved || reviewDone(r, d)
			continue
		}
		switch st := status(d.RequirementID); {
		case st == "" || st == store.ReqDone || st == store.ReqShelved:
			stale[d.ID] = true
		case d.Kind == store.KindFeedback && st == store.ReqReview:
			stale[d.ID] = true
		case (d.Kind == store.KindAssign || d.Kind == store.KindQueue) && st != store.ReqReady && st != store.ReqInProgress && st != store.ReqBlocked:
			stale[d.ID] = true
		}
	}
	return stale
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
	}, "/?s="+url.QueryEscape(x.Key))
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
		// 系统推送（R-49）不依赖 Bark / 企业微信渠道，事件规则相同
		if pushWants(cfg, m.Event) {
			if _, err := h.sendPush(ctx, m, path, ""); err != nil {
				h.log.Warn("系统推送失败", "event", m.Event, "err", err)
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
