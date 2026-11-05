package hub

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NekroAI/kitestring/internal/store"
)

// 挂 ks wait 前的自查（R-117）：会话名下有没有用户在面板上能推进的事——它提的未答决策、
// 关联的待验收需求、它负责的计划里待批准的计划或待验收的阶段。一件都没有时，用户在面板上看不到它在等什么，
// ks wait 先输出提示再照常等待（不拒绝：PM / 开发常驻待命本身是正常的）。
// 最后一条回复像在问用户时提示加重：要问的事先用 decision_raise 发上去；只是待命时只轻轻提一句。
// 关键词判断（store.AskTail）只用在这里提醒会话本身，不给用户看（R-239）：「要你处理」和「等你」只认明确记录。

type waitCheck struct {
	Pending int      `json:"pending" doc:"名下等用户处理的事项件数"`
	Items   []string `json:"items" doc:"这些事项，如「D-12 待你答复」「R-3 待验收」"`
	Asking  bool     `json:"asking" doc:"最后一条回复看起来在问用户"`
	Ask     string   `json:"ask,omitempty" doc:"判断依据：最后一条回复的最后一段"`
	Hint    string   `json:"hint,omitempty" doc:"要输出给 Agent 的提示；有待处理事项时为空"`
	Retired bool     `json:"retired,omitempty" doc:"会话已被接任卸任（R-224、R-229），或已空等下线（R-272）：ks wait 输出 hint 后直接返回，不再等待"`
	// 空等下线（R-272）
	MaxWait  string `json:"max_wait,omitempty" doc:"Claude Code 会话这次 ks wait 最长等多久（项目设置，默认 55m，不超过 55m）"`
	IdleLeft string `json:"idle_left,omitempty" doc:"常驻会话离空等下线还剩多久：ks wait 最多等这么久，醒来时正好到点"`
	Offline  bool   `json:"offline,omitempty" doc:"空等累计到了项目设置的时长：已记为下线，hint 是写交接下线的指示，不再等待"`
}

// pendingFor 列出会话 key 名下等用户处理的事项。
func (h *Hub) pendingFor(ctx context.Context, key string) ([]string, error) {
	var items []string
	decs, err := h.store.ListDecisions(ctx, []string{store.DecPending}, -1)
	if err != nil {
		return nil, err
	}
	for _, d := range decs {
		if d.SessionKey == key || d.OriginSession == key {
			items = append(items, d.Code+" 待你答复")
		}
	}
	reqs, err := h.store.ListRequirements(ctx, "", []string{store.ReqReview})
	if err != nil {
		return nil, err
	}
	for _, r := range reqs {
		if slices.Contains(r.Sessions, key) {
			items = append(items, r.Code+" 待验收")
		}
	}
	plans, err := h.store.ListPlans(ctx, "")
	if err != nil {
		return nil, err
	}
	for _, x := range plans {
		if x.OwnerSession != key {
			continue
		}
		if x.Status == store.PlanDraft && x.CreatedBy == "agent" {
			items = append(items, x.Code+" 待批准")
		}
		for _, st := range x.Stages {
			if st.Status == store.StageReview {
				items = append(items, fmt.Sprintf("%s 阶段 %d 待验收", x.Code, st.Seq))
			}
		}
	}
	return items, nil
}

// checkWait 算出 ks wait 开始时要不要提示、提示什么。text 是 daemon 从对话记录里读到的最新一段回复（可能为空），
// 为空时用 Hub 存的上一轮最后回复。
// afterTimeout 为真时是 ks wait 超时醒来后的复查（R-272）：常驻会话空等累计到点就记为下线，返回写交接下线的指示。
func (h *Hub) checkWait(ctx context.Context, key, text string, afterTimeout bool) (waitCheck, error) {
	x, err := h.store.GetSession(ctx, key)
	if err != nil {
		return waitCheck{}, apiErr(err)
	}
	if x.OfflineAt != nil && x.Role != "" {
		return waitCheck{Items: []string{}, Retired: true, Offline: true, Hint: offlineDoneHint(x.Role, h.handedOff(ctx, x))}, nil
	}
	if x.RetiredAt != nil {
		return waitCheck{Items: []string{}, Retired: true, Hint: "[Kitestring] 你已卸任（身份已由新会话接任），不要再挂 ks wait：用一两句话收尾后结束本轮。纸鸢不会再唤醒你。\n"}, nil
	}
	maxWait := store.WaitMaxDefault
	if x.ProjectID != "" {
		if p, err := h.store.GetProject(ctx, x.ProjectID); err == nil {
			maxWait = p.WaitMax()
		}
	}
	now := time.Now()
	idle, limit, idleOK := h.idleState(ctx, x, now)
	if afterTimeout {
		if idleOK && idle+idleSlack >= limit {
			h.goOffline(ctx, x, idle, now)
			return waitCheck{Items: []string{}, Retired: true, Offline: true, Hint: offlineHint(x.Role, idle)}, nil
		}
		return waitCheck{Items: []string{}, MaxWait: maxWait.String()}, nil
	}
	items, err := h.pendingFor(ctx, key)
	if err != nil {
		return waitCheck{}, err
	}
	out := waitCheck{Pending: len(items), Items: items, MaxWait: maxWait.String()}
	if idleOK {
		out.IdleLeft = max(limit-idle, time.Minute).Round(time.Second).String()
	}
	if out.Items == nil {
		out.Items = []string{}
	}
	if strings.TrimSpace(text) != "" {
		out.Ask = store.AskTail(text)
	} else {
		out.Ask = x.ReplyAsk
	}
	out.Asking = out.Ask != ""
	if out.Pending > 0 {
		return out, nil
	}
	if out.Asking {
		out.Hint = "[Kitestring] 注意：你最后的回复像是在问用户，但面板上没有你提的待答决策、待验收需求或待批准的计划 / 待验收的阶段——用户在面板上看不到你在等什么。" +
			"要问用户的事，先用 decision_raise（命令行 ks decision raise）发上去，用户在面板上一答就会唤醒你；对话里的问句不会出现在用户的「要你处理」里，用户看不到。本次照常等待。\n"
	} else {
		out.Hint = "[Kitestring] 提示：你名下没有等用户处理的事项（待答决策、待验收需求、待批准的计划或待验收的阶段）。只是待命接活的话照常等就行；" +
			"如果其实在等用户回答，用户在面板上看不到——先用 decision_raise 发上去。\n"
	}
	return out, nil
}

func (h *Hub) registerAgentWaitCheck(api huma.API) {
	huma.Register(api, huma.Operation{OperationID: "agent-wait-check", Method: http.MethodPost, Path: "/api/agent/sessions/{key}/wait-check", Tags: []string{"agent"},
		Summary: "ks wait 开始时自查：名下有没有用户能在面板上推进的事项，没有时返回提示（R-117）"},
		func(ctx context.Context, in *struct {
			Key  string `path:"key"`
			Body struct {
				Text         string `json:"text,omitempty" doc:"对话记录里最新的一段回复；不传时用上一轮结束时的最后回复"`
				AfterTimeout bool   `json:"after_timeout,omitempty" doc:"ks wait 超时醒来后的复查（R-272）：常驻会话空等累计到点时记为下线、返回写交接下线的指示"`
			}
		}) (*struct{ Body waitCheck }, error) {
			c, err := h.checkWait(ctx, in.Key, in.Body.Text, in.Body.AfterTimeout)
			if err != nil {
				return nil, err
			}
			return &struct{ Body waitCheck }{Body: c}, nil
		})
}

// registerSessionReply：在会话面板上直接给会话发一句话（R-117、R-239）。用留言的送达通道（kind = note，不关联需求）：
// 挂着 ks wait 时立刻唤醒，否则在你下次给它发消息时送达。只是留言，不算待办、不进「要你处理」。
func (h *Hub) registerSessionReply(api huma.API) {
	huma.Register(api, huma.Operation{OperationID: "reply-session", Method: http.MethodPost, Path: "/api/sessions/{key}/reply", Tags: []string{"sessions"},
		Summary: "给会话发一句话（送达方式同留言，挂着 ks wait 时立刻唤醒）"},
		func(ctx context.Context, in *struct {
			Key  string `path:"key"`
			Body struct {
				Comment string   `json:"comment,omitempty"`
				Files   []string `json:"files,omitempty"`
			}
		}) (*decisionOut, error) {
			x, err := h.store.GetSession(ctx, in.Key)
			if err != nil {
				return nil, apiErr(err)
			}
			files, err := h.store.ResolveFiles(ctx, in.Body.Files)
			if err != nil {
				return nil, err
			}
			comment := strings.TrimSpace(in.Body.Comment)
			if comment == "" && len(files) == 0 {
				return nil, huma.Error400BadRequest("请写下回复或附上图片")
			}
			d, err := h.store.CreateDecision(ctx, store.Decision{
				Kind: store.KindNote, SessionKey: x.Key, ProjectID: x.ProjectID,
				Question: "回复你的最后一条消息", Answer: comment, AnswerFiles: files,
			})
			if err != nil {
				return nil, apiErr(err)
			}
			if err := h.store.ClearReplyAsk(ctx, x.Key); err != nil {
				return nil, err
			}
			h.decisionsChanged(ctx, d)
			h.sessionChanged(x.Key)
			return &decisionOut{Body: d}, nil
		})
}
