package hub

import (
	"context"
	"fmt"
	"net/http"
	"sort"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NekroAI/kitestring/internal/roles"
	"github.com/NekroAI/kitestring/internal/store"
)

// 工人池（R-86）：PM 查看本项目的开发会话，把需求交给指定开发，或交给 hub 按规则挑人（auto）。
// 派活复用“交给会话”（assign）：开发待命时立刻被唤醒，在忙就等这一轮结束、挂上 ks wait 时收到。

// Worker 是工人池里的一个会话。
type Worker struct {
	Key           string                 `json:"key"`
	Role          string                 `json:"role"`
	Title         string                 `json:"title"`
	Tool          string                 `json:"tool"`
	Device        string                 `json:"device"`
	Presence      string                 `json:"presence" enum:"standby,working,offline" doc:"standby 待命；working 运行中；offline 空闲（没挂 ks wait）"`
	ContextTokens int                    `json:"context_tokens"`
	ContextMax    int                    `json:"context_max"`
	Open          []store.RequirementRef `json:"open" doc:"手上的需求：待开始、进行中、受阻"`
	LastActiveAt  string                 `json:"last_active_at"`
}

type teamOut struct {
	Body struct {
		ProjectID   string   `json:"project_id"`
		ProjectName string   `json:"project_name"`
		PM          *Worker  `json:"pm"`
		Devs        []Worker `json:"devs" doc:"本项目的 Dev 会话，在线的在前；已结束的不列"`
	}
}

func toWorker(x store.Session) Worker {
	w := Worker{Key: x.Key, Role: x.Role, Title: x.Title, Tool: x.Tool, Device: x.DeviceName, Presence: x.Presence(),
		ContextTokens: x.ContextTokens, ContextMax: x.ContextMax, Open: []store.RequirementRef{}, LastActiveAt: x.LastActiveAt.UTC().Format("2006-01-02T15:04:05Z")}
	if w.Title == "" {
		w.Title = x.LastPrompt
	}
	for _, r := range x.Requirements {
		if r.Status == store.ReqReady || r.Status == store.ReqInProgress || r.Status == store.ReqBlocked {
			w.Open = append(w.Open, r)
		}
	}
	return w
}

var presenceRank = map[string]int{"standby": 0, "working": 1, "offline": 2}

// team 返回会话所在项目的 PM 与开发；开发按 在线待命 → 工作中 → 离线、手上需求少、上下文轻 排序，排在最前的就是 auto 会选的人。
func (h *Hub) team(ctx context.Context, sessionKey string) (store.Session, *Worker, []Worker, error) {
	me, err := h.store.GetSession(ctx, sessionKey)
	if err != nil {
		return me, nil, nil, err
	}
	if me.ProjectID == "" {
		return me, nil, nil, huma.Error400BadRequest("当前会话不属于任何项目")
	}
	list, err := h.store.RoleSessions(ctx, me.ProjectID)
	if err != nil {
		return me, nil, nil, err
	}
	var pm *Worker
	devs := []Worker{}
	for _, x := range list {
		switch {
		case x.Role == roles.PM && pm == nil:
			w := toWorker(x)
			pm = &w
		case x.Role == roles.Dev && x.State != store.StateEnded:
			devs = append(devs, toWorker(x))
		}
	}
	sort.SliceStable(devs, func(i, j int) bool {
		a, b := devs[i], devs[j]
		if presenceRank[a.Presence] != presenceRank[b.Presence] {
			return presenceRank[a.Presence] < presenceRank[b.Presence]
		}
		if len(a.Open) != len(b.Open) {
			return len(a.Open) < len(b.Open)
		}
		return a.ContextTokens < b.ContextTokens
	})
	return me, pm, devs, nil
}

func (h *Hub) registerAgentTeam(api huma.API) {
	h.registerAgentRoster(api)
	tag := []string{"agent"}
	huma.Register(api, huma.Operation{OperationID: "agent-team", Method: http.MethodGet, Path: "/api/agent/team", Tags: tag, Summary: "工人池：本项目的 PM 与 Dev 会话（R-86）"},
		func(ctx context.Context, in *struct {
			SessionKey string `query:"session_key" required:"true"`
		}) (*teamOut, error) {
			me, pm, devs, err := h.team(ctx, in.SessionKey)
			if err != nil {
				return nil, apiErr(err)
			}
			out := &teamOut{}
			out.Body.ProjectID, out.Body.ProjectName, out.Body.PM, out.Body.Devs = me.ProjectID, me.ProjectName, pm, devs
			return out, nil
		})
	huma.Register(api, huma.Operation{OperationID: "agent-assign", Method: http.MethodPost, Path: "/api/agent/requirements/{ref}/assign", Tags: tag, Summary: "PM 把需求交给开发（R-86）"},
		func(ctx context.Context, in *struct {
			Ref  string `path:"ref"`
			Body struct {
				SessionKey string `json:"session_key" minLength:"1" doc:"调用者（必须是本项目的 PM）"`
				To         string `json:"to" minLength:"1" doc:"Dev 会话键；auto 表示由 hub 挑：待命优先，其次手上需求少、上下文轻"`
				Comment    string `json:"comment,omitempty" doc:"交代给开发的话，例如为什么交给它、和哪条需求相关"`
				Reason     string `json:"reason,omitempty" doc:"派的是草稿时必填：放行理由（派活会顺带把草稿放行为待开始，R-166）"`
			}
		}) (*assignOut, error) {
			return h.pmAssign(ctx, in.Ref, in.Body.SessionKey, in.Body.To, in.Body.Comment, in.Body.Reason)
		})
}

type assignOut struct {
	Body struct {
		Assigned bool   `json:"assigned"`
		To       string `json:"to,omitempty" doc:"接手的 Dev 会话键"`
		Title    string `json:"title,omitempty" doc:"接手的 Dev 会话标题"`
		Presence string `json:"presence,omitempty"`
		Message  string `json:"message"`
	}
}

// pmAssign：PM 派活。只有本项目的 PM 能派；只能派给本项目、未结束的开发会话。
func (h *Hub) pmAssign(ctx context.Context, ref, caller, to, comment, reason string) (*assignOut, error) {
	me, _, devs, err := h.team(ctx, caller)
	if err != nil {
		return nil, apiErr(err)
	}
	if me.Role != roles.PM {
		return nil, huma.Error403Forbidden("只有 PM 能派活；当前会话不是本项目的 PM（用 ks role pm 接管）")
	}
	r, err := h.store.GetRequirement(ctx, ref)
	if err != nil {
		return nil, apiErr(err)
	}
	if r.ProjectID != me.ProjectID {
		return nil, huma.Error400BadRequest(fmt.Sprintf("%s 不属于本项目", r.Code))
	}
	if r.Status == store.ReqDone || r.Status == store.ReqShelved {
		return nil, huma.Error400BadRequest(fmt.Sprintf("%s 已完成或已搁置，不能派", r.Code))
	}
	// 派草稿 = 顺带放行（R-166）：同样受项目开关管、同样要理由，记一条放行
	draft := r.Status == store.ReqDraft
	if draft {
		if _, err := h.releaseCheck(ctx, r, me.Key, reason); err != nil {
			return nil, err
		}
	}
	out := &assignOut{}
	var w *Worker
	if to == "auto" {
		if len(devs) > 0 && devs[0].Presence != "offline" {
			w = &devs[0]
		}
		if w == nil {
			// 不再自动提醒用户开 Dev 会话（R-169）：默认交给一次性子代理，确实要并行再 dev_recommend
			out.Body.Message = fmt.Sprintf("没有在线的 Dev，%s 未派出。默认交给后台一次性子代理做；确实适合并行、需要多轮沟通时，再用 dev_recommend 推荐开 Dev 会话，由用户限时批准。", r.Code)
			return out, nil
		}
	} else {
		for i := range devs {
			if devs[i].Key == to {
				w = &devs[i]
			}
		}
		if w == nil {
			return nil, huma.Error400BadRequest("找不到这个 Dev 会话：只能派给本项目、未结束、身份为 Dev 的会话（用 team 查看）")
		}
	}
	if draft {
		if r, err = h.release(ctx, r, me, reason); err != nil {
			return nil, err
		}
	}
	if _, err := h.assign(ctx, r.ID, w.Key, comment, me.Key); err != nil {
		return nil, err
	}
	out.Body.Assigned, out.Body.To, out.Body.Title, out.Body.Presence = true, w.Key, w.Title, w.Presence
	switch w.Presence {
	case "standby":
		out.Body.Message = fmt.Sprintf("%s 已交给 %s，它正在待命，马上开工。", r.Code, w.Key)
	case "working":
		out.Body.Message = fmt.Sprintf("%s 已交给 %s，它手上在忙，这一轮结束挂上 ks wait 时收到。", r.Code, w.Key)
	default:
		out.Body.Message = fmt.Sprintf("%s 已交给 %s，但它现在离线，用户下次和它对话时才会收到。", r.Code, w.Key)
	}
	return out, nil
}
