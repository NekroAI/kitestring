package hub

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NekroAI/kitestring/internal/roles"
	"github.com/NekroAI/kitestring/internal/store"
)

// 作废证据（R-189）：Agent 交错的证据（格式不对、字数超标、误交）不能一直留在需求上。
// 只有交这条证据的会话，或本项目的主调度（PM）/ 主助理（Aide）能作废；必须写理由。
// 留痕直接记在证据上（谁、什么身份、什么时候、为什么），网页把它显示成一行「已作废 · 理由」，可展开看原文。

func (h *Hub) registerAgentEvidence(api huma.API) {
	huma.Register(api, huma.Operation{OperationID: "agent-void-evidence", Method: http.MethodPost, Path: "/api/agent/evidence/{id}/void", Tags: []string{"agent"}, Summary: "作废一条证据（R-189）：交这条证据的会话，或本项目的 PM / 主助理；必须写理由"},
		func(ctx context.Context, in *struct {
			ID   string `path:"id" doc:"证据编号（evidence_attach 返回的 evidence_id，或需求里证据的 id）"`
			Body struct {
				SessionKey string `json:"session_key" doc:"调用者会话"`
				Reason     string `json:"reason" doc:"作废理由，一句话（必填）"`
			}
		}) (*reqOut, error) {
			r, err := h.voidEvidence(ctx, in.ID, in.Body.SessionKey, in.Body.Reason)
			if err != nil {
				return nil, err
			}
			return &reqOut{Body: r}, nil
		})
}

func (h *Hub) voidEvidence(ctx context.Context, id, sessionKey, reason string) (store.Requirement, error) {
	reason = strings.Join(strings.Fields(reason), " ")
	if reason == "" {
		return store.Requirement{}, huma.Error400BadRequest("作废证据必须写理由（reason），例如：字数超标，已重交")
	}
	if r := []rune(reason); len(r) > 200 {
		reason = string(r[:200])
	}
	if sessionKey == "" {
		return store.Requirement{}, huma.Error400BadRequest("无法识别当前会话：作废证据必须由 Agent 会话发起")
	}
	x, err := h.store.GetSession(ctx, sessionKey)
	if err != nil {
		return store.Requirement{}, apiErr(err)
	}
	e, err := h.store.GetEvidence(ctx, id)
	if err != nil {
		return store.Requirement{}, apiErr(err)
	}
	if e.Kind == store.EvidenceReview {
		return store.Requirement{}, huma.Error400BadRequest("审查意见不能作废")
	}
	r, err := h.store.GetRequirement(ctx, e.RequirementID)
	if err != nil {
		return r, apiErr(err)
	}
	own := e.SessionKey != "" && e.SessionKey == x.Key
	lead := (x.Role == roles.PM || x.Role == roles.Aide) && r.ProjectID != "" && x.ProjectID == r.ProjectID
	if !own && !lead {
		return r, huma.Error403Forbidden(fmt.Sprintf("只有交这条证据的会话，或本项目的主调度 / 主助理能作废它（%s 的这条证据不是本会话交的）", r.Code))
	}
	if _, err := h.store.VoidEvidence(ctx, id, x.Key, x.Role, reason); err != nil {
		return r, apiErr(err)
	}
	h.broker.publish("requirements")
	r, err = h.store.GetRequirement(ctx, r.ID)
	return r, apiErr(err)
}
