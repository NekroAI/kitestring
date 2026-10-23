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

// 审查员（R-88）：你在网页上把一条需求交给本项目的审查员会话，它做审查分析，结果作为“审查意见”挂在需求上。
// 审查是旁路：不改需求状态、不关联会话（不会因此成为需求的负责会话或排队对象）、不拦截验收；审查员也不是必须参与。
// 审查意见存在 evidence 表里（kind = review），读出时和证据分开（store.ListReviews）。

// reviewTaskNote 是送达给审查员的说明，提醒它这是审查任务。
const reviewTaskNote = "这是审查任务：只做审查分析（正确性、边界情况、和验收标准是否吻合、潜在风险），不要改代码，不要改需求状态，也不要 session_link_req。审查完用 review_submit（命令行 ks req review）把意见交到这条需求上；验收仍由用户决定。"

// askReview 把需求交给审查员：只送达一条审查任务，需求状态、关联会话都不动。
func (h *Hub) askReview(ctx context.Context, ref, sessionKey, comment string) (store.Decision, error) {
	r, err := h.store.GetRequirement(ctx, ref)
	if err != nil {
		return store.Decision{}, apiErr(err)
	}
	x, err := h.store.GetSession(ctx, sessionKey)
	if err != nil {
		return store.Decision{}, apiErr(err)
	}
	if x.Role != roles.Reviewer {
		return store.Decision{}, huma.Error400BadRequest("这个会话不是审查员：先在它的对话里运行 ks role reviewer")
	}
	if x.State == store.StateEnded || x.State == store.StateLost {
		return store.Decision{}, huma.Error400BadRequest("这个审查员会话已结束")
	}
	if r.ProjectID != "" && x.ProjectID != r.ProjectID {
		return store.Decision{}, huma.Error400BadRequest(fmt.Sprintf("这个审查员会话不属于 %s 所在的项目", r.Code))
	}
	if r.Status == store.ReqShelved {
		return store.Decision{}, huma.Error400BadRequest(fmt.Sprintf("%s 已搁置，不用审查", r.Code))
	}
	body := []string{reviewTaskNote, "需求当前状态：" + reqStatusZh[r.Status] + "（保持不变）。"}
	if c := strings.TrimSpace(comment); c != "" {
		body = append(body, "用户交代："+c)
	}
	if d := strings.TrimSpace(r.Detail); d != "" {
		body = append(body, "说明："+d)
	}
	if a := strings.TrimSpace(r.Acceptance); a != "" {
		body = append(body, "验收标准："+a)
	}
	body = append(body, "证据、修改意见等完整内容用 req_get 查看。")
	d, err := h.store.CreateDecision(ctx, store.Decision{
		Kind: store.KindReviewTask, SessionKey: x.Key, ProjectID: r.ProjectID, RequirementID: r.ID,
		Question: r.Title, Answer: strings.Join(body, "\n"), AnswerFiles: r.Files,
	})
	if err != nil {
		return d, apiErr(err)
	}
	h.pushDecisions(ctx, x.DeviceID)
	h.broker.publish("requirements", "decisions", "sessions")
	return d, nil
}

var reqStatusZh = map[string]string{store.ReqDraft: "草稿", store.ReqReady: "待开始", store.ReqInProgress: "进行中", store.ReqBlocked: "受阻", store.ReqReview: "待验收", store.ReqDone: "已完成", store.ReqShelved: "已搁置"}

// submitReview 审查员交审查意见：只记录，不改需求状态。只有审查员身份的会话能交。
func (h *Hub) submitReview(ctx context.Context, ref, sessionKey, summary string, fileIDs []string) (store.Requirement, error) {
	if sessionKey == "" {
		return store.Requirement{}, huma.Error400BadRequest("无法识别当前会话：审查意见必须由审查员会话提交")
	}
	x, err := h.store.GetSession(ctx, sessionKey)
	if err != nil {
		return store.Requirement{}, apiErr(err)
	}
	if x.Role != roles.Reviewer {
		return store.Requirement{}, huma.Error403Forbidden("只有审查员会话能交审查意见（ks role reviewer）；验收证据请用 evidence_attach")
	}
	r, err := h.store.GetRequirement(ctx, ref)
	if err != nil {
		return r, apiErr(err)
	}
	if r.ProjectID != "" && x.ProjectID != r.ProjectID {
		return r, huma.Error400BadRequest(fmt.Sprintf("%s 不属于本会话所在的项目", r.Code))
	}
	files, err := h.store.ResolveFiles(ctx, fileIDs)
	if err != nil {
		return r, err
	}
	if _, err := h.store.AddEvidence(ctx, store.Evidence{RequirementID: r.ID, SessionKey: x.Key, Kind: store.EvidenceReview, Summary: summary, Files: files}); err != nil {
		return r, apiErr(err)
	}
	h.refreshDelivered(ctx, r)
	h.pushDecisions(ctx, x.DeviceID) // 审查任务交完意见后不再在压缩 / 恢复时补发
	h.broker.publish("requirements", "decisions")
	r, err = h.store.GetRequirement(ctx, r.ID)
	return r, apiErr(err)
}

// reviewDone 判断审查员在收到这条审查任务之后是否已经交过意见。
func reviewDone(r store.Requirement, d store.Decision) bool {
	for _, e := range r.Reviews {
		if e.SessionKey == d.SessionKey && !e.CreatedAt.Before(d.CreatedAt) {
			return true
		}
	}
	return false
}

func (h *Hub) registerReviewerWeb(api huma.API) {
	huma.Register(api, huma.Operation{OperationID: "ask-review", Method: http.MethodPost, Path: "/api/requirements/{ref}/reviewer", Tags: []string{"requirements"}, Summary: "把需求交给审查员审查（R-88）：只送达审查任务，不改状态、不关联会话"},
		func(ctx context.Context, in *struct {
			Ref  string `path:"ref"`
			Body struct {
				SessionKey string `json:"session_key" minLength:"1" doc:"审查员会话键（身份必须是 reviewer）"`
				Comment    string `json:"comment,omitempty" doc:"交代给审查员的话，例如重点看哪里"`
			}
		}) (*decisionOut, error) {
			d, err := h.askReview(ctx, in.Ref, in.Body.SessionKey, in.Body.Comment)
			if err != nil {
				return nil, err
			}
			return &decisionOut{Body: d}, nil
		})
}

func (h *Hub) registerReviewerAgent(api huma.API) {
	huma.Register(api, huma.Operation{OperationID: "agent-submit-review", Method: http.MethodPost, Path: "/api/agent/requirements/{ref}/reviews", Tags: []string{"agent"}, Summary: "审查员交审查意见（R-88）：不改需求状态，不拦截验收"},
		func(ctx context.Context, in *struct {
			Ref  string `path:"ref"`
			Body struct {
				SessionKey string   `json:"session_key" doc:"提交意见的会话（身份必须是 reviewer）"`
				Summary    string   `json:"summary" minLength:"1" doc:"审查意见（可用 Markdown）"`
				Files      []string `json:"files,omitempty" doc:"附图 ID（先上传到 /api/agent/files）"`
			}
		}) (*reqOut, error) {
			r, err := h.submitReview(ctx, in.Ref, in.Body.SessionKey, in.Body.Summary, in.Body.Files)
			if err != nil {
				return nil, err
			}
			return &reqOut{Body: r}, nil
		})
}
