package hub

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NekroAI/kitestring/internal/notify"
	"github.com/NekroAI/kitestring/internal/roles"
	"github.com/NekroAI/kitestring/internal/store"
)

// 开独立开发会话改为「PM 推荐、你限时批准」（R-169，D-221）：默认 PM 把需求交给一次性子任务，
// 只有特别适合并行、需要多轮沟通时，PM 才推荐开 Dev 会话。推荐是一条请求类审批（approval = true，request_kind = devs），
// 出现在「待处理」和审批抽屉里，不推送打扰；写明为什么要开、开几个、做什么。到点（默认 30 分钟，设置里可改）没人作答，
// 按规则视为拒绝，送给 PM 的文字写明是超时（verdict.go）。你批准后才推送接入语、项目页出现横幅（沿用 R-87），
// 在线 Dev 够数后横幅自动清除并告诉 PM（R-116）。need_devs 保留为兼容入口，走同一流程。

const (
	needRenotify             = 10 * time.Minute // 同一项目 10 分钟内不重复推送接入语
	defaultDevRequestMinutes = 30
	maxDevRequestMinutes     = 24 * 60
)

// requestUnit 是批准时限的单位（一分钟）；测试里调短。
var requestUnit = time.Minute

// SetRequestUnit 只供测试：把批准时限的「一分钟」换成 d。
func SetRequestUnit(d time.Duration) { requestUnit = d }

// JoinPhrase 是开开发会话时对 Agent 说的那句话。
func JoinPhrase(project string) string {
	return fmt.Sprintf("连接到纸鸢，做「%s」的开发（运行 ks role dev）", project)
}

// devRequestMinutes：推荐开 Dev 会话的批准时限（分钟）；minutes > 0 时按这条覆盖，否则用设置里的，都没有用默认 30。
func (h *Hub) devRequestMinutes(ctx context.Context, minutes int) int {
	if minutes <= 0 {
		if s, err := h.Settings(ctx); err == nil {
			minutes = s.DevRequestMinutes
		}
	}
	if minutes <= 0 {
		minutes = defaultDevRequestMinutes
	}
	return min(minutes, maxDevRequestMinutes)
}

type devRecommend struct {
	Count    int
	Reason   string // 为什么要开：为什么要并行、为什么子任务不合适
	Work     string // 开了做什么：哪些需求
	Duration string // 预计用多久
	Minutes  int    // 批准时限，0 = 用设置
}

// recommendDevs：PM 推荐开 Dev 会话，建一条限时请求。同项目已有在等你批准的推荐时不重复建，返回那一条（existing = true）。
func (h *Hub) recommendDevs(ctx context.Context, me store.Session, in devRecommend) (d store.Decision, existing bool, err error) {
	if list, err := h.store.PendingRequests(ctx, me.ProjectID, store.RequestDevs); err == nil && len(list) > 0 {
		return list[0], true, nil
	}
	p, err := h.store.GetProject(ctx, me.ProjectID)
	if err != nil {
		return d, false, err
	}
	mins := h.devRequestMinutes(ctx, in.Minutes)
	exp := time.Now().Add(time.Duration(mins) * requestUnit)
	var b strings.Builder
	fmt.Fprintf(&b, "**为什么要开**：%s", strings.TrimSpace(in.Reason))
	if w := strings.TrimSpace(in.Work); w != "" {
		fmt.Fprintf(&b, "\n\n**开了做什么**：%s", w)
	}
	if du := strings.TrimSpace(in.Duration); du != "" {
		fmt.Fprintf(&b, "\n\n**预计用多久**：%s", du)
	}
	fmt.Fprintf(&b, "\n\n%d 分钟内未批准按规则视为不开，主调度继续用一次性子任务。批准后推送接入语：在项目目录打开 Claude Code，输入「%s」。", mins, JoinPhrase(p.Name))
	d, err = h.store.CreateDecision(ctx, store.Decision{
		SessionKey: me.Key, ProjectID: me.ProjectID, Kind: store.KindDecision,
		Question: fmt.Sprintf("推荐开启 %d 个开发会话", in.Count), Context: b.String(),
		Approval: true, Options: []string{"批准", "拒绝"}, Risk: store.RiskHigh,
		RequestKind: store.RequestDevs, RequestCount: in.Count, ExpiresAt: &exp,
	})
	if err != nil {
		return d, false, err
	}
	h.broker.publish("decisions", "sessions", "requirements")
	h.scheduleRequestExpiry(d)
	return d, false, nil
}

// scheduleRequestExpiry：限时请求到点仍没作答，按规则视为拒绝并送给提问方；Hub 重启后由 resumeRequestExpiry 重新排。
func (h *Hub) scheduleRequestExpiry(d store.Decision) {
	if d.ExpiresAt == nil {
		return
	}
	time.AfterFunc(time.Until(*d.ExpiresAt)+200*time.Millisecond, func() { h.expireRequest(context.Background(), d.ID) })
}

func (h *Hub) expireRequest(ctx context.Context, id string) {
	d, err := h.store.GetDecision(ctx, id)
	if err != nil {
		return
	}
	d, ok, err := h.store.ExpireRequest(ctx, id, timeoutRejectText(requestWhat(d)))
	if err != nil || !ok {
		return
	}
	h.decisionsChanged(ctx, d)
}

// ResumeRequestExpiry 供测试模拟 Hub 启动时给限时请求重新排期。
func (h *Hub) ResumeRequestExpiry(ctx context.Context) { h.resumeRequestExpiry(ctx) }

// resumeRequestExpiry：Hub 启动时给还在等的限时请求重新排期（已过期的马上处理）。
func (h *Hub) resumeRequestExpiry(ctx context.Context) {
	list, err := h.store.PendingRequests(ctx, "", "")
	if err != nil {
		return
	}
	for _, d := range list {
		h.scheduleRequestExpiry(d)
	}
}

// onRequestAnswered：你在网页上答了限时请求。批准开 Dev 会话 → 推送接入语、项目页出横幅；改答为拒绝 → 撤掉横幅。
func (h *Hub) onRequestAnswered(ctx context.Context, d store.Decision) {
	if d.RequestKind != store.RequestDevs || d.ProjectID == "" || d.TimedOut {
		return
	}
	if requestApproved(d) {
		if err := h.requestDevs(ctx, d.ProjectID, max(d.RequestCount, 1), ""); err != nil {
			h.log.Warn("推送接入语失败", "err", err)
		}
		return
	}
	_ = h.dismissNeed(ctx, d.ProjectID)
}

// requestDevs：你批准了开 n 个 Dev 会话——记在项目上（项目页横幅）并推送接入语。
func (h *Hub) requestDevs(ctx context.Context, projectID string, n int, reason string) error {
	p, err := h.store.GetProject(ctx, projectID)
	if err != nil {
		return err
	}
	if err := h.store.SetNeedDevs(ctx, projectID, n, reason); err != nil {
		return err
	}
	h.broker.publish("projects")
	dedup := "need|" + projectID
	h.mu.Lock()
	t, seen := h.notified[dedup]
	if seen && time.Since(t) < needRenotify && p.NeedDevs == n {
		h.mu.Unlock()
		return nil
	}
	h.notified[dedup] = time.Now()
	h.mu.Unlock()
	h.sendNotify(projectID, notify.Message{
		Event: notify.EventNeedDevs,
		Title: pushNeedDevsTitle(n, p.Name),
		Body:  pushNeedDevsBody(JoinPhrase(p.Name)),
		Group: "kitestring",
	}, "/projects/"+projectID)
	return nil
}

// checkNeed：项目的在线开发够数了，清除横幅；cleared 为 true 时 need 是清除前要的个数。
func (h *Hub) checkNeed(ctx context.Context, projectID string) (need int, cleared bool) {
	if projectID == "" {
		return 0, false
	}
	p, err := h.store.GetProject(ctx, projectID)
	if err != nil || p.NeedDevs == 0 {
		return 0, false
	}
	list, err := h.store.RoleSessions(ctx, projectID)
	if err != nil {
		return 0, false
	}
	online := 0
	for _, x := range list {
		if x.Role == roles.Dev && x.Presence() != "offline" {
			online++
		}
	}
	if online >= p.NeedDevs {
		if err := h.store.SetNeedDevs(ctx, projectID, 0, ""); err == nil {
			h.broker.publish("projects")
			return p.NeedDevs, true
		}
	}
	return 0, false
}

type recommendOut struct {
	Body struct {
		Decision *store.Decision `json:"decision,omitempty"`
		Existing bool            `json:"existing" doc:"同项目已有在等用户批准的推荐，没有新建"`
		Message  string          `json:"message"`
	}
}

func (h *Hub) registerAgentNeed(api huma.API) {
	pmOnly := func(ctx context.Context, key string) (store.Session, error) {
		me, err := h.store.GetSession(ctx, key)
		if err != nil {
			return me, apiErr(err)
		}
		if me.Role != roles.PM || me.ProjectID == "" {
			return me, huma.Error403Forbidden("只有项目的 PM 能推荐开 Dev 会话")
		}
		return me, nil
	}
	recommend := func(ctx context.Context, me store.Session, in devRecommend) (*recommendOut, error) {
		if strings.TrimSpace(in.Reason) == "" {
			return nil, huma.Error400BadRequest("请写明为什么要开 Dev 会话：为什么要并行、为什么一次性子任务不合适；用户在审批里看到")
		}
		d, existing, err := h.recommendDevs(ctx, me, in)
		if err != nil {
			return nil, apiErr(err)
		}
		out := &recommendOut{}
		out.Body.Decision, out.Body.Existing = &d, existing
		left := ""
		if d.ExpiresAt != nil {
			left = fmt.Sprintf("，%s 前没批按规则视为拒绝", d.ExpiresAt.Local().Format("15:04"))
		}
		if existing {
			out.Body.Message = fmt.Sprintf("已有 %s 在等用户批准（%s%s），没有重复提。", d.Code, d.Question, left)
		} else {
			out.Body.Message = fmt.Sprintf("已推荐（%s）：在用户的审批里等批准，不推送%s。批准后用户才会拿到接入语，Dev 上线时你会收到通知；结果（批准 / 拒绝 / 超时）会送到你这里。等待期间照常用一次性子任务。", d.Code, left)
		}
		return out, nil
	}
	huma.Register(api, huma.Operation{OperationID: "agent-recommend-devs", Method: http.MethodPost, Path: "/api/agent/team/recommend", Tags: []string{"agent"},
		Summary: "PM 推荐开独立 Dev 会话：用户限时批准，超时视为拒绝（R-169）"},
		func(ctx context.Context, in *struct {
			Body struct {
				SessionKey string `json:"session_key" minLength:"1" doc:"调用者（必须是本项目的 PM）"`
				Count      int    `json:"count" minimum:"1" maximum:"5" doc:"推荐开几个 Dev 会话"`
				Reason     string `json:"reason" doc:"为什么要开：为什么要并行、为什么一次性子任务不合适"`
				Work       string `json:"work,omitempty" doc:"开了做什么：哪些需求（写编号）"`
				Duration   string `json:"duration,omitempty" doc:"预计用多久，例如「两小时」"`
				Minutes    int    `json:"minutes,omitempty" minimum:"0" maximum:"1440" doc:"批准时限（分钟）；不填用设置里的（默认 30）"`
			}
		}) (*recommendOut, error) {
			me, err := pmOnly(ctx, in.Body.SessionKey)
			if err != nil {
				return nil, err
			}
			b := in.Body
			return recommend(ctx, me, devRecommend{Count: b.Count, Reason: b.Reason, Work: b.Work, Duration: b.Duration, Minutes: b.Minutes})
		})
	huma.Register(api, huma.Operation{OperationID: "agent-need-devs", Method: http.MethodPost, Path: "/api/agent/team/need", Tags: []string{"agent"},
		Summary: "（兼容入口）PM 请用户开 Dev 会话：现在等同推荐开 Dev 会话，用户限时批准（R-87 → R-169）"},
		func(ctx context.Context, in *struct {
			Body struct {
				SessionKey string `json:"session_key" minLength:"1" doc:"调用者（必须是本项目的 PM）"`
				Count      int    `json:"count" minimum:"0" maximum:"5" doc:"推荐开几个 Dev 会话；0 表示撤回在等批准的推荐、撤掉横幅"`
				Reason     string `json:"reason,omitempty" doc:"为什么要开：为什么要并行、为什么一次性子任务不合适"`
			}
		}) (*recommendOut, error) {
			me, err := pmOnly(ctx, in.Body.SessionKey)
			if err != nil {
				return nil, err
			}
			if in.Body.Count == 0 {
				if list, err := h.store.PendingRequests(ctx, me.ProjectID, store.RequestDevs); err == nil {
					for _, d := range list {
						if d, err := h.store.CloseDecision(ctx, d.ID, "PM 不再需要", ""); err == nil {
							h.decisionsChanged(ctx, d)
						}
					}
				}
				out := &recommendOut{}
				out.Body.Message = "已撤回推荐。"
				return out, apiErr(h.dismissNeed(ctx, me.ProjectID))
			}
			return recommend(ctx, me, devRecommend{Count: in.Body.Count, Reason: in.Body.Reason})
		})
}

// dismissNeed 撤掉横幅（PM 不再需要，或你在项目页点“不需要了”）。
func (h *Hub) dismissNeed(ctx context.Context, projectID string) error {
	if err := h.store.SetNeedDevs(ctx, projectID, 0, ""); err != nil {
		return err
	}
	h.broker.publish("projects")
	return nil
}

func (h *Hub) registerNeedWeb(api huma.API) {
	huma.Register(api, huma.Operation{OperationID: "dismiss-need-devs", Method: http.MethodDelete, Path: "/api/projects/{id}/need", Tags: []string{"projects"}, Summary: "撤掉“已批准开 Dev 会话”的接入语横幅（R-87 / R-169）"},
		func(ctx context.Context, in *struct {
			ID string `path:"id"`
		}) (*okBody, error) {
			return ok(), apiErr(h.dismissNeed(ctx, in.ID))
		})
}
