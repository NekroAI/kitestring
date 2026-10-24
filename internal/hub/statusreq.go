package hub

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NekroAI/kitestring/internal/store"
)

// Agent 申请改需求状态（R-188）：要用户确认某条需求时（主要是草稿 → 待开始，也可以重新打开已搁置的需求），
// Agent 不再在对话里写「请在面板上把 R-x 设为待开始」，而是发一条申请——复用 R-169 的请求类审批（request_kind = status），
// 出现在「要你处理」，点开在审批抽屉里批准 / 拒绝（R-176 请求类用词）。批准后由纸鸢直接改状态，时间线记成「用户批准了 Agent 的申请」
// （status_changes.via = request，ref = 申请的 D-x）。批准前需求状态被任何人改动过，申请自动作废，结果送回申请方。
// 申请不限时、不推送、不能代答（批准类本来就不能代答）；同一条需求只留一条在等的申请。
// 项目开了「允许 PM / Adv 放行草稿」时，做法明确的草稿仍可由 PM / Adv 直接放行（R-166），不用申请。

// statusRequestAllowed：可以申请的状态变化。done / review / in_progress / blocked 不走申请（验收、开工有各自的路）。
var statusRequestAllowed = map[string][]string{
	store.ReqDraft:   {store.ReqReady},
	store.ReqShelved: {store.ReqReady, store.ReqDraft},
}

func statusRequestOK(from, to string) bool {
	for _, x := range statusRequestAllowed[from] {
		if x == to {
			return true
		}
	}
	return false
}

// statusRequestWhat：申请在文字里的称呼，例如「把 R-12 从草稿改为待开始的申请」。
func statusRequestWhat(d store.Decision) string {
	code := d.RequirementCode
	if code == "" {
		code = "需求"
	}
	return fmt.Sprintf("把 %s 从%s改为%s的申请", code, reqStatusZh[d.RequestFrom], reqStatusZh[d.RequestTo])
}

// statusRequestText：改状态申请送给申请方的答复——批准 / 拒绝 / 作废三种写法分明。
func statusRequestText(d store.Decision) string {
	what := statusRequestWhat(d)
	if d.Voided {
		return fmt.Sprintf("已作废（%s 的状态在你申请后、用户批准前已被改动，不再是%s；这条申请没有执行，状态以现在的为准，需要的话按现状重新申请）", d.RequirementCode, reqStatusZh[d.RequestFrom])
	}
	a := strings.TrimSpace(d.Answer)
	if rest, ok := strings.CutPrefix(a, "拒绝"); ok {
		return rejectedText(what, strings.TrimLeft(rest, "；;：:，, \n")) + fmt.Sprintf("。%s 保持%s，不要再催用户改。", d.RequirementCode, reqStatusZh[d.RequestFrom])
	}
	rest, ok := strings.CutPrefix(a, "批准")
	if !ok {
		return d.Answer
	}
	t := fmt.Sprintf("批准（用户批准了%s）。纸鸢已把 %s 改为%s，不用你再改。", what, d.RequirementCode, reqStatusZh[d.RequestTo])
	if note := strings.TrimLeft(rest, "；;：:，, \n"); note != "" {
		t += "附言：" + note
	}
	return t
}

// onStatusRequestVoided：需求状态被改动、改状态申请自动作废后，把结果送回申请方、刷新网页。
func (h *Hub) onStatusRequestVoided(ids []string) {
	ctx := context.Background()
	for _, id := range ids {
		if d, err := h.store.GetDecision(ctx, id); err == nil {
			h.decisionsChanged(ctx, d)
		}
	}
}

// checkStatusRequest：用户答改状态申请前检查。需求状态没变时照常往下答（handled = false）；
// 状态已被改动时这条申请作废（handled = true，返回作废后的决策）。
func (h *Hub) checkStatusRequest(ctx context.Context, d0 store.Decision) (store.Decision, bool, error) {
	if d0.RequestKind != store.RequestStatus {
		return d0, false, nil
	}
	if d0.Status != store.DecPending {
		return d0, false, huma.Error400BadRequest(d0.Code + " 这条申请已经处理过（批准、拒绝或作废），不能再答")
	}
	r, err := h.store.GetRequirement(ctx, d0.RequirementID)
	if err != nil {
		return d0, false, apiErr(err)
	}
	if r.Status == d0.RequestFrom {
		return d0, false, nil
	}
	d, err := h.store.VoidStatusRequest(ctx, d0.ID)
	if err != nil {
		return d0, false, apiErr(err)
	}
	h.decisionsChanged(ctx, d)
	return d, true, nil
}

// applyStatusRequest：用户批准了改状态申请——由纸鸢改状态，时间线记成「用户批准了 Agent 的申请」。
func (h *Hub) applyStatusRequest(ctx context.Context, d store.Decision) {
	if d.RequestKind != store.RequestStatus || !requestApproved(d) || d.Voided {
		return
	}
	before, err := h.store.GetRequirement(ctx, d.RequirementID)
	if err != nil || before.Status != d.RequestFrom {
		return
	}
	r, err := h.store.SetRequirementStatus(store.WithActor(ctx, store.Actor{Kind: store.ActorUser, Via: "request", Ref: d.Code}), before.ID, d.RequestTo)
	if err != nil {
		h.log.Warn("按批准的申请改需求状态失败", "err", err, "decision", d.Code)
		return
	}
	h.reopenStage(ctx, before, r.Status)
	h.kick(ctx, r.ID)
	h.refreshDelivered(ctx, r)
	h.onUserStatusChanged(ctx, before, r, d.SessionKey) // 申请方从答复里知道结果，不重复送
	h.broker.publish("requirements", "sessions", "plans")
}

type statusRequestOut struct {
	Body struct {
		Decision *store.Decision `json:"decision,omitempty"`
		Existing bool            `json:"existing" doc:"这条需求已有在等用户批准的改状态申请，没有新建"`
		Message  string          `json:"message"`
	}
}

func (h *Hub) registerStatusRequest(api huma.API) {
	huma.Register(api, huma.Operation{OperationID: "agent-request-status", Method: http.MethodPost, Path: "/api/agent/requirements/{ref}/status-request", Tags: []string{"agent"},
		Summary: "Agent 申请改需求状态：用户在审批抽屉里批准后由纸鸢改（R-188）"},
		func(ctx context.Context, in *struct {
			Ref  string `path:"ref"`
			Body struct {
				SessionKey string `json:"session_key" minLength:"1" doc:"申请的会话"`
				To         string `json:"to" doc:"目标状态：ready（草稿 / 已搁置 → 待开始）或 draft（已搁置 → 草稿）"`
				Reason     string `json:"reason" doc:"一句理由：为什么现在可以改（用户在审批里看到）"`
			}
		}) (*statusRequestOut, error) {
			me, err := h.store.GetSession(ctx, in.Body.SessionKey)
			if err != nil {
				return nil, apiErr(err)
			}
			r, err := h.store.GetRequirement(ctx, in.Ref)
			if err != nil {
				return nil, apiErr(err)
			}
			if r.ProjectID != "" && me.ProjectID != "" && r.ProjectID != me.ProjectID {
				return nil, huma.Error400BadRequest(fmt.Sprintf("%s 不属于你所在的项目，不能申请改它的状态", r.Code))
			}
			to := strings.TrimSpace(in.Body.To)
			if !store.ValidReqStatus(to) {
				return nil, huma.Error400BadRequest(fmt.Sprintf("目标状态 %q 不认识；可以申请：草稿 → ready（待开始），已搁置 → ready 或 draft", to))
			}
			if to == r.Status {
				return nil, huma.Error400BadRequest(fmt.Sprintf("%s 已经是%s，不用申请", r.Code, reqStatusZh[to]))
			}
			if !statusRequestOK(r.Status, to) {
				return nil, huma.Error400BadRequest(fmt.Sprintf("不能申请把 %s 从%s改为%s。可以申请的只有：草稿 → 待开始、已搁置 → 待开始、已搁置 → 草稿；开工用 session_link_req，做完设为 review，已完成只由用户验收", r.Code, reqStatusZh[r.Status], reqStatusZh[to]))
			}
			reason := strings.TrimSpace(in.Body.Reason)
			if reason == "" {
				return nil, huma.Error400BadRequest("申请要写一句理由：reason（ks 用 --reason），说明为什么现在可以改，用户在审批里看到")
			}
			out := &statusRequestOut{}
			if d, ok, err := h.store.PendingStatusRequest(ctx, r.ID); err != nil {
				return nil, apiErr(err)
			} else if ok {
				out.Body.Decision, out.Body.Existing = &d, true
				out.Body.Message = fmt.Sprintf("%s 已有申请 %s 在等用户批准（%s），没有重复提。", r.Code, d.Code, statusRequestWhat(d))
				return out, nil
			}
			d, err := h.store.CreateDecision(ctx, store.Decision{
				SessionKey: me.Key, ProjectID: r.ProjectID, RequirementID: r.ID, Kind: store.KindDecision,
				Question: fmt.Sprintf("申请把 %s 改为%s", r.Code, reqStatusZh[to]), Context: reason,
				Approval: true, Options: []string{"批准", "拒绝"}, Risk: store.RiskHigh,
				RequestKind: store.RequestStatus, RequestFrom: r.Status, RequestTo: to,
			})
			if err != nil {
				return nil, apiErr(err)
			}
			h.broker.publish("decisions", "sessions", "requirements")
			out.Body.Decision = &d
			out.Body.Message = fmt.Sprintf("已申请（%s）：%s，在用户的「要你处理」里等批准，不推送、不限时。批准后纸鸢直接改状态，不用你再改；拒绝或作废（批准前状态被改动）也会送到你这里。不要再在对话里请用户去面板上改。", d.Code, statusRequestWhat(d))
			return out, nil
		})
}
