package hub

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NekroAI/kitestring/internal/protocol"
	"github.com/NekroAI/kitestring/internal/roles"
	"github.com/NekroAI/kitestring/internal/store"
)

// 停下（R-291，D-1372 / D-1381）：用户在工作台上让会话停在稳妥的点，不强行中止。
// 用于暂时不推进某个项目、额度将尽的时候：会话卡 / 会话详情的「停下」停一个，项目页「全部停下」停这个项目所有没结束的会话。
//
// 两步：
//  1. 停下中（stopped_at）：纸鸢送一条停下提醒（kind = role，标题 store.StopQuestion）。待命的会话马上收到；运行中的在这一轮结束时
//     由 daemon 在 Stop 时拦下结束送达（不论身份）；空闲的在用户下次发消息时收到。身份不再下发给 daemon（Stop hook 不拦）。
//     这期间它自己提的决策的答复、用户直接发给它的话照常送达（为等答复可以挂 ks wait），其他点名消息先排队。
//  2. 已下线（offline_at，复用 R-272）：写完交接（handoff_write），或收到提醒后结束本轮且不再待命时记下。之后点名消息只排队，
//     用户开新会话接任时转过去；受阻的需求由新会话关联后转回进行中（R-269）。
//
// 项目的自动派发设置不动（用户选的）。

type stopOut struct {
	Body struct {
		Stopped []string `json:"stopped" doc:"这次停下的会话键"`
		Message string   `json:"message"`
	}
}

func (h *Hub) registerStopWeb(api huma.API) {
	huma.Register(api, huma.Operation{OperationID: "stop-session", Method: http.MethodPost, Path: "/api/sessions/{key}/stop", Tags: []string{"sessions"},
		Summary: "让会话停下（R-291）：送停下提醒，会话写交接后下线；不强行中止"},
		func(ctx context.Context, in *keyPath) (*stopOut, error) {
			x, err := h.store.GetSession(ctx, in.Key)
			if err != nil {
				return nil, apiErr(err)
			}
			if why := unstoppable(x); why != "" {
				return nil, huma.Error409Conflict(why)
			}
			if err := h.stopSession(ctx, x, true); err != nil {
				return nil, err
			}
			out := &stopOut{}
			out.Body.Stopped, out.Body.Message = []string{x.Key}, stopToast(x)
			return out, nil
		})
	huma.Register(api, huma.Operation{OperationID: "stop-project", Method: http.MethodPost, Path: "/api/projects/{id}/stop", Tags: []string{"projects"},
		Summary: "让项目的全部会话停下（R-291）：逐个送停下提醒；不改项目的自动派发设置"},
		func(ctx context.Context, in *idPath) (*stopOut, error) {
			list, err := h.store.StoppableSessions(ctx, in.ID)
			if err != nil {
				return nil, err
			}
			out := &stopOut{}
			out.Body.Stopped = []string{}
			for _, x := range list {
				if unstoppable(x) != "" {
					continue
				}
				if err := h.stopSession(ctx, x, false); err != nil {
					return nil, err
				}
				out.Body.Stopped = append(out.Body.Stopped, x.Key)
			}
			out.Body.Message = fmt.Sprintf("已向 %d 个会话发出停下提醒。", len(out.Body.Stopped))
			return out, nil
		})
}

// unstoppable：会话不能停下的原因；能停时为空。
func unstoppable(x store.Session) string {
	switch {
	case x.State == store.StateEnded:
		return "会话已结束"
	case x.State == store.StateLost:
		return "会话失联，设备重新连接后可停下"
	case x.RetiredAt != nil:
		return "身份已由新会话接任"
	case x.StoppedAt != nil && x.OfflineAt == nil:
		return "会话正在停下"
	case x.OfflineAt != nil:
		return "会话已下线"
	}
	return ""
}

// stopToast：单个停下后网页上的提示（会话名 + 什么时候收到提醒）。
func stopToast(x store.Session) string {
	switch x.AvailabilityAt(time.Now()) {
	case store.AvailStandby:
		return "已发出停下提醒，会话待命中，马上收到。"
	case store.AvailBusy:
		return "已发出停下提醒，会话运行中，本轮结束时收到。"
	}
	return "已记为下线，停下提醒在下次发消息时送达。"
}

// stopSession 让会话停下：记下停下中、送停下提醒、身份不再下发给 daemon。tellTeam 为真时（单个停下）只告知另一位常驻成员一声。
func (h *Hub) stopSession(ctx context.Context, x store.Session, tellTeam bool) error {
	now := time.Now()
	changed, err := h.store.SetStopped(ctx, x.Key, now)
	if err != nil || !changed {
		return err
	}
	h.log.Info("用户让会话停下", "session", x.Key)
	d, err := h.store.CreateDecision(ctx, store.Decision{
		Kind: store.KindRole, SessionKey: x.Key, ProjectID: x.ProjectID, Question: store.StopQuestion,
		Answer: stopAsk(x.Role, x.ProjectName, len(liveAgents(x))),
	})
	if err != nil {
		return err
	}
	// 既没在运行也没待命（本轮已结束、没挂 ks wait）：没有要停在稳妥点的这一轮，直接记为已下线；提醒在用户下次发消息时送达
	if x.AvailabilityAt(now) == store.AvailOnline {
		_, _ = h.store.SetOffline(ctx, x.Key, &now)
	}
	h.pushRoles(ctx, x.DeviceID)
	h.decisionsChanged(ctx, d)
	h.sessionChanged(x.Key)
	if tellTeam && x.Role != "" && x.ProjectID != "" {
		to := h.liveRoleExcept(ctx, x.ProjectID, roles.PM, x.Key)
		if x.Role == roles.PM {
			to = h.liveRoleExcept(ctx, x.ProjectID, roles.Aide, x.Key)
		}
		if to != "" {
			text := fmt.Sprintf("用户让%s %s 停下，写完交接后下线。点名给这个会话的消息先排队，用户开新会话（%s）接任时转过去；这期间不要再向这个会话指派需求。", roles.Display(x.Role), sessionName(x), takeoverCmd(x.Role))
			if _, err := h.tellQuiet(ctx, to, x.ProjectID, "", text); err != nil {
				h.log.Warn("通知停下失败", "err", err)
			}
		}
	}
	return nil
}

// stopAsk：送给会话的停下提醒（第一句是不含工具名的事实，1.3 节）。
func stopAsk(role, project string, agents int) string {
	var b strings.Builder
	if role == "" {
		b.WriteString("用户让你停下。不开新的工作，把手上这一步做到能停的地方（该提交的提交、该保存的保存），用一两句话说明做到哪，然后结束本轮；不要再运行 ks wait，结束时纸鸢不会拦你。需要用户定的事照常用 decision_raise 提出。")
		return b.String()
	}
	fmt.Fprintf(&b, "用户让你停下：暂时不再推进项目「%s」（可能是额度将尽或暂缓这个项目），请停在稳妥的点，不是强行中止。请按顺序做：", project)
	n := 1
	step := func(f string, a ...any) {
		fmt.Fprintf(&b, "%d）"+f, append([]any{n}, a...)...)
		n++
	}
	step("不开新的工作，不再领新需求；")
	if role == roles.PM {
		if agents > 0 {
			step("先停派新活；你还有 %d 个子任务在运行，等它们交回再往下做（子任务没交完时 handoff_write 会被拒绝）；", agents)
		} else {
			step("先停派新活；")
		}
	}
	step("把手上这一步做到能停的地方：该提交的提交、该保存进度的保存，在需求留言（req_comment）写清做到哪；")
	step("你手上进行中的需求改为受阻（req_update_status blocked），原因写「会话已停下，等新会话接着做」；")
	step("用 handoff_write 写交接（doing 做到一半的事、agreements 口头约定、caveats 注意事项，几行即可）；没有 handoff_write 工具时用命令 ks handoff --doing … --caveats …；")
	step("用一两句话说明已停下，然后结束本轮。不要再运行 ks wait 待命；结束时纸鸢不会拦你。")
	fmt.Fprintf(&b, "停下过程中需要用户定的事照常用 decision_raise 提出，可以为等答复挂 ks wait，答复照常送达；写完交接或结束本轮后记为已下线，之后点名给你的消息排队，用户开新会话（%s）接任时转过去。", takeoverCmd(role))
	return b.String()
}

// stopDoneHint：已停下的会话又运行 ks wait 时的提示（R-291）。
func stopDoneHint(role string, handed bool) string {
	if role == "" {
		return "[Kitestring] 用户已让你停下，不要再挂 ks wait：用一两句话说明做到哪，然后结束本轮。\n"
	}
	if handed {
		return "[Kitestring] 用户已让你停下，交接已写好，不要再挂 ks wait：用一两句话说明后结束本轮。用户回来后会运行 " + takeoverCmd(role) + " 开新会话接任。\n"
	}
	return "[Kitestring] 用户已让你停下，还没写交接：先把进行中的需求改为受阻（原因写「会话已停下，等新会话接着做」），用 handoff_write 写交接，再用一两句话说明后结束本轮；不要再挂 ks wait。用户回来后会运行 " + takeoverCmd(role) + " 开新会话接任。\n"
}

// stoppedPasses：停下中的会话，点名给它的哪些消息照常下发（D-1381）：停下提醒、它自己提的决策的答复、用户直接发给它的话。
// 已下线后只下发停下提醒（还没送达时），其余排队等接任。
func stoppedPasses(x store.Session, d store.Decision) bool {
	if d.Kind == store.KindRole && d.Question == store.StopQuestion {
		return true
	}
	if x.OfflineAt != nil {
		return false
	}
	switch d.Kind {
	case store.KindDecision, "":
		return true
	case store.KindNote:
		return d.RequirementID == "" // 工作台上直接回复会话（R-117），不挂在需求上
	}
	return false
}

// stoppedOffline：停下中的会话写完交接，或收到提醒后结束本轮且不再待命时，记为已下线（D-1381）。
func (h *Hub) stoppedOffline(ctx context.Context, x store.Session) {
	if x.StoppedAt == nil || x.OfflineAt != nil {
		return
	}
	now := time.Now()
	changed, err := h.store.SetOffline(ctx, x.Key, &now)
	if err != nil {
		h.log.Warn("记录停下后下线失败", "err", err, "session", x.Key)
		return
	}
	if !changed {
		return
	}
	h.log.Info("会话停下后下线", "session", x.Key)
	h.pushDecisions(ctx, x.DeviceID) // 之后只排队：撤下还没送达的其他消息
	h.sessionChanged(x.Key)
	h.broker.publish("sessions")
}

// trackStopped 按事件判断停下中的会话是否已结束本轮且不再待命：停下提醒已送达、本轮结束（Stop）、没挂 ks wait、没有在运行的子任务。
func (h *Hub) trackStopped(ctx context.Context, next store.Session, ev protocol.Event) {
	if next.StoppedAt == nil || next.OfflineAt != nil || ev.Name != "Stop" || ev.AgentID != "" || next.Parked || len(liveAgents(next)) > 0 {
		return
	}
	if pending, err := h.store.StopNoticePending(ctx, next.Key); err != nil || pending {
		return
	}
	h.stoppedOffline(ctx, next)
}
