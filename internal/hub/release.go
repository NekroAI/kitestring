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

// 放行（R-166）：PM / Adv 可以把草稿改为待开始，让你不在时新需求也能排期。规则：
// 只有项目设置打开「允许 PM / Adv 放行草稿」才行（默认关闭），Dev 不能放行；只能从草稿放行（已搁置的重新打开仍由你操作）；
// 必须写一句理由，记成一条放行记录进时间线；你可以一键撤回（改回草稿），PM / Adv 自己改回草稿也算撤回。
// req_assign 派一条草稿等于顺带放行，同样受开关管、同样要理由。验收通过（done）仍只由你设置。

const releaseOff = "这条还是草稿，先请用户放行，或在项目设置里打开「允许 PM / Adv 放行草稿」"

// releaseCheck 检查 callerKey 能不能放行 r，返回调用者会话。
func (h *Hub) releaseCheck(ctx context.Context, r store.Requirement, callerKey, reason string) (store.Session, error) {
	if callerKey == "" {
		return store.Session{}, huma.Error400BadRequest("放行要知道是哪个会话：请带上 session_key")
	}
	x, err := h.store.GetSession(ctx, callerKey)
	if err != nil {
		return x, apiErr(err)
	}
	if x.Role != roles.PM && x.Role != roles.Advisor {
		return x, huma.Error403Forbidden("只有 PM 或 Adv 能把草稿放行为待开始；Dev 不能放行（避免自己记、自己放、自己做）。需要排期请告诉 PM（pm_notify），或请用户放行；开始做待开始的需求时用 session_link_req，它会变为进行中")
	}
	if r.ProjectID == "" || x.ProjectID != r.ProjectID {
		return x, huma.Error400BadRequest(fmt.Sprintf("%s 不属于你所在的项目，不能放行", r.Code))
	}
	p, err := h.store.GetProject(ctx, r.ProjectID)
	if err != nil {
		return x, apiErr(err)
	}
	if !p.AgentRelease {
		return x, huma.Error403Forbidden(releaseOff)
	}
	if r.Status != store.ReqDraft {
		return x, huma.Error400BadRequest(fmt.Sprintf("只能放行草稿；%s 现在不是草稿（已搁置的重新打开只由用户操作）", r.Code))
	}
	if strings.TrimSpace(reason) == "" {
		return x, huma.Error400BadRequest("放行必须写一句理由：reason（ks 用 --reason），说明为什么做法明确、可以直接排期")
	}
	return x, nil
}

// release 把草稿放行为待开始并留下放行记录。调用前先 releaseCheck。
func (h *Hub) release(ctx context.Context, r store.Requirement, x store.Session, reason string) (store.Requirement, error) {
	nr, err := h.store.SetRequirementStatus(store.WithActor(ctx, store.Actor{Kind: store.ActorAgent, Session: x.Key, Role: x.Role, Via: "release"}), r.ID, store.ReqReady)
	if err != nil {
		return nr, apiErr(err)
	}
	d, err := h.store.CreateRelease(ctx, nr, x.Key, x.Role, strings.TrimSpace(reason))
	if err != nil {
		return nr, apiErr(err)
	}
	_ = h.store.LinkLastStatusChange(ctx, nr.ID, d.Code) // 状态变化与放行记录互相引用（R-153）
	return h.store.GetRequirement(ctx, nr.ID)
}

// withdrawRelease：需求从非草稿改回草稿时调用。before 是改之前的需求；有没撤回的放行就记为撤回，
// 需求已有会话在做（进行中 / 阻塞，或待开始但已交给某个会话）时，给那些会话送「已被撤回为草稿，先停下」。
// actor 是改回草稿的会话，你在网页上操作时为空。
func (h *Hub) withdrawRelease(ctx context.Context, before store.Requirement, actor, reason string) {
	if before.Release == nil || before.Status == store.ReqDraft {
		return
	}
	who := "用户"
	if actor != "" {
		if x, err := h.store.GetSession(ctx, actor); err == nil {
			who = roles.Label(x.Role)
			if who == "" {
				who = "会话"
			}
			who += " " + actor
		}
	}
	text := who + "撤回"
	if who != "用户" {
		text = "由 " + who + " 改回草稿"
	}
	if r := strings.TrimSpace(reason); r != "" {
		text += "：" + r
	}
	if ok, err := h.store.WithdrawRelease(ctx, before.ID, text); err != nil || !ok {
		if err != nil {
			h.log.Warn("撤回放行失败", "err", err)
		}
		return
	}
	working := before.Status == store.ReqInProgress || before.Status == store.ReqBlocked || before.Status == store.ReqReady && before.OfferedTo != ""
	if !working {
		return
	}
	msg := fmt.Sprintf("%s「%s」已被撤回为草稿（%s），先停下，不要继续做；等重新放行后再接着做，有疑问问 PM。", before.Code, brief(before.Title, 40), text)
	for _, k := range before.Sessions {
		if k == actor {
			continue
		}
		x, err := h.store.GetSession(ctx, k)
		if err != nil || x.State == store.StateEnded || x.State == store.StateLost || x.DoneAt != nil {
			continue
		}
		if _, err := h.tell(ctx, k, before.ProjectID, before.ID, msg); err != nil {
			h.log.Warn("通知撤回放行失败", "err", err)
		}
	}
}

func (h *Hub) registerRelease(api huma.API) {
	huma.Register(api, huma.Operation{OperationID: "withdraw-release", Method: http.MethodPost, Path: "/api/requirements/{ref}/release/withdraw", Tags: []string{"requirements"}, Summary: "撤回 PM / Adv 的放行：改回草稿，时间线标为已撤回（R-166）"},
		func(ctx context.Context, in *struct {
			Ref  string `path:"ref"`
			Body struct {
				Reason string `json:"reason,omitempty" doc:"撤回原因，可不写"`
			}
		}) (*reqOut, error) {
			before, err := h.store.GetRequirement(ctx, in.Ref)
			if err != nil {
				return nil, apiErr(err)
			}
			if before.Release == nil {
				return nil, huma.Error400BadRequest(before.Code + " 没有可撤回的放行")
			}
			if before.Status != store.ReqReady && before.Status != store.ReqInProgress && before.Status != store.ReqBlocked {
				return nil, huma.Error400BadRequest(before.Code + " 已经交验收、完成、搁置或是草稿，不能撤回放行")
			}
			if _, err := h.store.SetRequirementStatus(store.WithActor(ctx, store.Actor{Via: "withdraw", Ref: before.Release.Code}), before.ID, store.ReqDraft); err != nil {
				return nil, apiErr(err)
			}
			h.withdrawRelease(ctx, before, "", in.Body.Reason)
			r, err := h.store.GetRequirement(ctx, before.ID)
			if err != nil {
				return nil, apiErr(err)
			}
			h.refreshDelivered(ctx, r)
			h.broker.publish("requirements", "sessions", "decisions", "plans")
			return &reqOut{Body: r}, nil
		})
}
