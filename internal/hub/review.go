package hub

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NekroAI/kitestring/internal/roles"
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
// 项目有 PM（没结束）时，退回意见一律交给 PM，由它判断自己处理还是转派（R-85）。
// 没有 PM 时送给最近在做的开发 / 无身份会话；顾问、审查员一律跳过（和 R-150 一致），都没有就返回空，
// 意见留在需求上，看板提示没人接手，由你手动交出（R-167）。
func (h *Hub) feedbackTarget(ctx context.Context, r store.Requirement) string {
	if pm := h.projectPM(ctx, r.ProjectID); pm != "" {
		return pm
	}
	var best, fallback store.Session
	for _, k := range r.Sessions {
		x, err := h.store.GetSession(ctx, k)
		if err != nil || !queueRole(x.Role) {
			continue // 顾问 / 审查员不接退回意见（R-167）
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

// adviceText：主助理的验收建议写成一句话，随退回送给接手的会话；开头标明是主助理的建议、不是用户原话（R-242）。
func adviceText(a store.Advice) string {
	v := "建议退回"
	if a.Verdict == "pass" {
		v = "建议通过"
	}
	s := "主助理的验收建议（不是用户原话）：" + v
	if r := strings.TrimSpace(a.Reason); r != "" {
		s += "，理由：" + r
	}
	return s
}

func (h *Hub) review(ctx context.Context, items []reviewItem) (*reviewOut, error) {
	out := &reviewOut{}
	out.Body.Requirements = []store.Requirement{}
	out.Body.Feedback = []store.Decision{}
	devices := map[string]bool{}
	type changed struct {
		before, after store.Requirement
		told          []string
	}
	var changes []changed
	for _, it := range items {
		r, err := h.store.GetRequirement(ctx, it.Ref)
		if err != nil {
			return nil, apiErr(err)
		}
		was, told := r, []string(nil)
		switch it.Verdict {
		case "accept":
			if r, err = h.store.SetRequirementStatus(store.WithActor(ctx, store.Actor{Via: "accept"}), r.ID, store.ReqDone); err != nil {
				return nil, apiErr(err)
			}
			for _, k := range r.Sessions {
				if x, err := h.store.GetSession(ctx, k); err == nil {
					devices[x.DeviceID] = true
				}
			}
			// 带意见通过：意见作为留言送达 Agent（需求仍是已完成）
			comment := strings.TrimSpace(it.Comment)
			if comment != "" || len(it.Files) > 0 {
				files, err := h.store.ResolveFiles(ctx, it.Files)
				if err != nil {
					return nil, err
				}
				text := "验收通过。"
				if comment != "" {
					text += "附言：" + comment
				}
				d, err := h.store.CreateDecision(ctx, store.Decision{
					Kind: store.KindNote, SessionKey: h.feedbackTarget(ctx, r), ProjectID: r.ProjectID, RequirementID: r.ID,
					Question: r.Title, Answer: text, AnswerFiles: files,
				})
				if err != nil {
					return nil, apiErr(err)
				}
				out.Body.Feedback = append(out.Body.Feedback, d)
				told = append(told, d.SessionKey)
				if x, err := h.store.GetSession(ctx, d.SessionKey); err == nil {
					devices[x.DeviceID] = true
				}
			}
		case "revise":
			files, err := h.store.ResolveFiles(ctx, it.Files)
			if err != nil {
				return nil, err
			}
			comment := strings.TrimSpace(it.Comment)
			// 主助理对这条需求有「建议退回」时，用户同意可以不写附言（R-242）；其余情况照旧必填
			adv, hasAdv := h.store.AdviceOf(ctx, r.ID)
			if comment == "" && len(files) == 0 && !(hasAdv && adv.Verdict == "return") {
				return nil, huma.Error400BadRequest(r.Code + "：请写下修改意见或附上图片")
			}
			advText := ""
			if hasAdv {
				advText = adviceText(adv)
				if comment == "" && len(files) == 0 {
					comment = "（用户没写附言，按主助理建议退回）"
				}
			}
			before := r
			if r, err = h.store.SetRequirementStatus(store.WithActor(ctx, store.Actor{Via: "revise"}), r.ID, store.ReqInProgress); err != nil {
				return nil, apiErr(err)
			}
			h.reopenStage(ctx, before, r.Status)
			d, err := h.store.CreateDecision(ctx, store.Decision{
				Kind: store.KindFeedback, SessionKey: h.feedbackTarget(ctx, r), ProjectID: r.ProjectID, RequirementID: r.ID,
				Question: r.Title, Answer: comment, AnswerFiles: files, Context: advText,
			})
			if err != nil {
				return nil, apiErr(err)
			}
			if r.Status != before.Status {
				_ = h.store.LinkLastStatusChange(ctx, r.ID, d.Code) // 状态变化与这条修改意见互相引用（R-153）
			}
			out.Body.Feedback = append(out.Body.Feedback, d)
			told = append(told, d.SessionKey)
			if x, err := h.store.GetSession(ctx, d.SessionKey); err == nil {
				devices[x.DeviceID] = true
			}
		default:
			return nil, huma.Error400BadRequest("verdict 只能是 accept 或 revise")
		}
		out.Body.Requirements = append(out.Body.Requirements, r)
		changes = append(changes, changed{was, r, told})
	}
	for id := range devices {
		h.pushDecisions(ctx, id)
	}
	for _, c := range changes {
		h.onUserStatusChanged(ctx, c.before, c.after, c.told...) // 验收通过 / 退回也是用户改状态；收到验收意见的会话不重复送（R-171）
	}
	h.broker.publish("requirements", "decisions", "sessions", "plans")
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

// assign 把需求交给某个会话：关联会话、放到待开始，并把需求内容作为一条送达交给它（D-14）。
// 不直接改为进行中——Agent 真正开始做时调用 session_link_req，才进入进行中，看板反映的是实际进度。
// 会话正在待命（ks wait）时会立刻被唤醒。
// by 是派活的会话（PM 派活时），你在网页上交出时为空。
func (h *Hub) assign(ctx context.Context, ref, sessionKey, comment, by string) (store.Decision, error) {
	d, err := h.assignQuiet(ctx, ref, sessionKey, comment, by)
	if err != nil {
		return d, err
	}
	if x, err := h.store.GetSession(ctx, d.SessionKey); err == nil {
		h.pushDecisions(ctx, x.DeviceID)
	}
	h.broker.publish("requirements", "decisions", "sessions")
	return d, nil
}

// assignQuiet 做 assign 的全部记录但不下发：批量交出时（计划阶段解锁）最后统一下发一次，
// 挂起等待的会话一次收到整批内容。
func (h *Hub) assignQuiet(ctx context.Context, ref, sessionKey, comment, by string) (store.Decision, error) {
	r, err := h.store.GetRequirement(ctx, ref)
	if err != nil {
		return store.Decision{}, apiErr(err)
	}
	x, err := h.store.GetSession(ctx, sessionKey)
	if err != nil {
		return store.Decision{}, apiErr(err)
	}
	if r.Status == store.ReqShelved { // 搁置的需求不会被自动拉回待开始，重新打开只能由用户明确操作（R-166 / R-175）
		return store.Decision{}, huma.Error400BadRequest(r.Code + " 已搁置，先重新打开再交出")
	}
	if err := h.store.LinkSession(ctx, r.ID, x.Key); err != nil {
		return store.Decision{}, apiErr(err)
	}
	if r.Status == store.ReqDraft || r.Status == store.ReqDone {
		actx := store.WithActor(ctx, store.Actor{Via: "assign"})
		if by != "" {
			actx = store.WithActor(ctx, store.Actor{Kind: store.ActorAgent, Session: by, Via: "assign"}) // PM 派活
		}
		if r, err = h.store.SetRequirementStatus(actx, r.ID, store.ReqReady); err != nil {
			return store.Decision{}, apiErr(err)
		}
	}
	if r.Status == store.ReqReady {
		_ = h.store.SetOffered(ctx, r.ID, x.Key) // 已交给它，不再重复排队送达
	}
	var body []string
	if r.Kind == "design" {
		body = append(body, "这是设计类需求（标注）：界面 / 交互有几种做法、需要用户决策时，用 decision_raise 交 2～3 套方案并在 option_images 附效果图；做法明确时可以直接做。")
	}
	if c := strings.TrimSpace(comment); c != "" {
		body = append(body, c)
	}
	if d := strings.TrimSpace(r.Detail); d != "" {
		body = append(body, "说明："+d)
	}
	if a := strings.TrimSpace(r.Acceptance); a != "" {
		body = append(body, "验收标准："+a)
	}
	if v := strings.TrimSpace(h.verifyNote(ctx, r.ProjectID, x.Role)); v != "" && x.Role == roles.Dev {
		body = append(body, strings.TrimPrefix(v, "[Kitestring] ")) // 验证分工（R-232）：此刻有没有在线评审
	}
	d, err := h.store.CreateDecision(ctx, store.Decision{
		Kind: store.KindAssign, SessionKey: x.Key, ProjectID: r.ProjectID, RequirementID: r.ID,
		Question: r.Title, Answer: strings.Join(body, "\n"), AnswerFiles: r.Files, AssignedBy: by,
	})
	if err != nil {
		return store.Decision{}, apiErr(err)
	}
	return d, nil
}

func (h *Hub) registerReview(api huma.API) {
	huma.Register(api, huma.Operation{OperationID: "assign-requirement", Method: http.MethodPost, Path: "/api/requirements/{ref}/assign", Tags: []string{"requirements"}, Summary: "把需求交给某个会话（关联、放到待开始并送达；Agent 开始做时才进入进行中）"},
		func(ctx context.Context, in *struct {
			Ref  string `path:"ref"`
			Body struct {
				SessionKey string `json:"session_key" minLength:"1"`
				Comment    string `json:"comment,omitempty"`
			}
		}) (*decisionOut, error) {
			d, err := h.assign(ctx, in.Ref, in.Body.SessionKey, in.Body.Comment, "")
			if err != nil {
				return nil, err
			}
			return &decisionOut{Body: d}, nil
		})
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
