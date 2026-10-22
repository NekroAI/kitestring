package hub

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NekroAI/kitestring/internal/store"
)

// 验收：逐条“通过”或“提修改意见”。修改意见作为 kind=feedback 的决策保存，沿用决策的送达通道：
// 在用户下次于目标会话发消息时送达（D15 / D14：不主动驱动 agent）。

type reviewItem struct {
	Ref     string   `json:"ref"`
	Verdict string   `json:"verdict" enum:"accept,revise"`
	Comment string   `json:"comment,omitempty"`
	Files   []string `json:"files,omitempty"`
}

type reviewIn struct {
	Body struct {
		Items []reviewItem `json:"items" minItems:"1"`
	}
}

type reviewOut struct {
	Body struct {
		Requirements []store.Requirement `json:"requirements"`
		Feedback     []store.Decision    `json:"feedback"`
	}
}

// feedbackTarget 选出修改意见送达的会话：优先最近活跃且未结束的关联会话，否则最近活跃的关联会话。
func (h *Hub) feedbackTarget(ctx context.Context, r store.Requirement) string {
	var best, fallback store.Session
	for _, k := range r.Sessions {
		x, err := h.store.GetSession(ctx, k)
		if err != nil {
			continue
		}
		if fallback.Key == "" || x.LastActiveAt.After(fallback.LastActiveAt) {
			fallback = x
		}
		if x.State != store.StateEnded && x.State != store.StateLost && (best.Key == "" || x.LastActiveAt.After(best.LastActiveAt)) {
			best = x
		}
	}
	if best.Key != "" {
		return best.Key
	}
	return fallback.Key
}

func (h *Hub) review(ctx context.Context, items []reviewItem) (*reviewOut, error) {
	out := &reviewOut{}
	out.Body.Requirements = []store.Requirement{}
	out.Body.Feedback = []store.Decision{}
	devices := map[string]bool{}
	for _, it := range items {
		r, err := h.store.GetRequirement(ctx, it.Ref)
		if err != nil {
			return nil, apiErr(err)
		}
		switch it.Verdict {
		case "accept":
			if r, err = h.store.SetRequirementStatus(ctx, r.ID, store.ReqDone); err != nil {
				return nil, apiErr(err)
			}
		case "revise":
			files, err := h.store.ResolveFiles(ctx, it.Files)
			if err != nil {
				return nil, err
			}
			comment := strings.TrimSpace(it.Comment)
			if comment == "" && len(files) == 0 {
				return nil, huma.Error400BadRequest(r.Code + "：请写下修改意见或附上图片")
			}
			if r, err = h.store.SetRequirementStatus(ctx, r.ID, store.ReqInProgress); err != nil {
				return nil, apiErr(err)
			}
			d, err := h.store.CreateDecision(ctx, store.Decision{
				Kind: store.KindFeedback, SessionKey: h.feedbackTarget(ctx, r), ProjectID: r.ProjectID, RequirementID: r.ID,
				Question: r.Title, Answer: comment, AnswerFiles: files,
			})
			if err != nil {
				return nil, apiErr(err)
			}
			out.Body.Feedback = append(out.Body.Feedback, d)
			if x, err := h.store.GetSession(ctx, d.SessionKey); err == nil {
				devices[x.DeviceID] = true
			}
		default:
			return nil, huma.Error400BadRequest("verdict 只能是 accept 或 revise")
		}
		out.Body.Requirements = append(out.Body.Requirements, r)
	}
	for id := range devices {
		h.pushDecisions(ctx, id)
	}
	h.broker.publish("requirements", "decisions", "sessions")
	return out, nil
}

// note 给需求留言：任何状态都可以，只送达给关联会话，不改需求状态。
func (h *Hub) note(ctx context.Context, ref, comment string, fileIDs []string) (store.Decision, error) {
	r, err := h.store.GetRequirement(ctx, ref)
	if err != nil {
		return store.Decision{}, apiErr(err)
	}
	files, err := h.store.ResolveFiles(ctx, fileIDs)
	if err != nil {
		return store.Decision{}, err
	}
	comment = strings.TrimSpace(comment)
	if comment == "" && len(files) == 0 {
		return store.Decision{}, huma.Error400BadRequest("请写下留言或附上图片")
	}
	d, err := h.store.CreateDecision(ctx, store.Decision{
		Kind: store.KindNote, SessionKey: h.feedbackTarget(ctx, r), ProjectID: r.ProjectID, RequirementID: r.ID,
		Question: r.Title, Answer: comment, AnswerFiles: files,
	})
	if err != nil {
		return store.Decision{}, apiErr(err)
	}
	if x, err := h.store.GetSession(ctx, d.SessionKey); err == nil {
		h.pushDecisions(ctx, x.DeviceID)
	}
	h.broker.publish("requirements", "decisions", "sessions")
	return d, nil
}

func (h *Hub) registerReview(api huma.API) {
	huma.Register(api, huma.Operation{OperationID: "note-requirement", Method: http.MethodPost, Path: "/api/requirements/{ref}/notes", Tags: []string{"requirements"}, Summary: "给需求留言（只送达，不改状态）"},
		func(ctx context.Context, in *struct {
			Ref  string `path:"ref"`
			Body struct {
				Comment string   `json:"comment,omitempty"`
				Files   []string `json:"files,omitempty"`
			}
		}) (*decisionOut, error) {
			d, err := h.note(ctx, in.Ref, in.Body.Comment, in.Body.Files)
			if err != nil {
				return nil, err
			}
			return &decisionOut{Body: d}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "review-requirements", Method: http.MethodPost, Path: "/api/reviews", Tags: []string{"requirements"}, Summary: "验收：逐条通过或提修改意见"},
		func(ctx context.Context, in *reviewIn) (*reviewOut, error) {
			if len(in.Body.Items) == 0 {
				return nil, errors.New("没有要验收的需求")
			}
			return h.review(ctx, in.Body.Items)
		})
}
