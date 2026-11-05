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
	"github.com/NekroAI/kitestring/internal/writing"
)

// 团队消息（R-135 / R-116）：会话之间的话只送给 Agent，不进你的收件箱。
// 用决策的送达通道（kind = tell）：对方挂着 ks wait 时立刻被唤醒，否则下一轮开始时注入；送达一次就不再补发。
// 三种来源：开发把需求设为待验收（送给派活的 PM，没有就送给项目 PM）；开发主动给 PM 留言（pm_notify / ks team tell）；
// 开发上线待命、批准开 Dev 会话后的横幅因开发够数被清除（送给项目 PM）。项目没有 PM 时安静跳过。
// 按身份寻址（R-191）：ks team tell / pm_notify 的 to 选 pm / aide / reviewer，发给本项目现在没结束的那个会话，不认会话名；
// 没有主助理时退回主调度并注明。消息上记下发送方（assigned_by、from_role）和子任务（from_agent），
// reply_to 回复时据此送回提问方：子任务的回复送到它所属的会话，并注明转给哪个子任务。

// livePM：key 是不是 projectID 里没结束的 PM 会话。
func (h *Hub) livePM(ctx context.Context, key, projectID string) bool {
	if key == "" {
		return false
	}
	x, err := h.store.GetSession(ctx, key)
	return err == nil && x.Role == roles.PM && x.ProjectID == projectID && seatHolder(x)
}

// seatHolder：会话还占着它的身份——没结束、没失联、没被你标为已完成；
// 主调度 / 主助理空等下线后（R-272）即使终端关了也还占着：点名给它的消息照常入队，接任时转给新会话。
// 顾问 / 评审 / 开发可能有多个，下线的不再选它。
func seatHolder(x store.Session) bool {
	if x.DoneAt != nil {
		return false
	}
	if x.OfflineAt != nil {
		return roles.Unique(x.Role)
	}
	return x.State != store.StateEnded && x.State != store.StateLost
}

// tell 给会话 to 送一条团队消息；to 为空时不送。
func (h *Hub) tell(ctx context.Context, to, projectID, reqID, text string) (store.Decision, error) {
	if to == "" {
		return store.Decision{}, nil
	}
	d, err := h.store.CreateDecision(ctx, store.Decision{
		Kind: store.KindTell, SessionKey: to, ProjectID: projectID, RequirementID: reqID, Question: "团队消息", Answer: text,
	})
	if err != nil {
		return d, err
	}
	markTold(ctx, reqID, to) // 需求订阅提醒不重复送（R-249 P4）
	h.decisionsChanged(ctx, d)
	return d, nil
}

// quietTellQ 是静默团队消息的标题（R-198）：照旧入队，会话下一轮开始或 ks wait 因别的消息被唤醒时一并注入，但它本身不唤醒 ks wait。
// 用标题区分，不加新列：下行时 pushDecisions 据此置 Quiet。
const quietTellQ = store.QuietTellQuestion

// 团队消息和订阅事件分两档（R-227），档位由发送方类型 / 事件类型定，不靠会话自己判断：
//   - 要对方动手的，立刻叫醒（tell）：提问、求裁定、派活、用户答复、新决策要查冲突、review 交验收、需求受阻、
//     撤回放行 / 需求被改（要按新的做）、验收后提交提示、开发上线待命、会话主动留言（默认）。
//   - 只是告知的，进待送队列（tellQuiet）：用户改需求状态、改项目设置、授权窗口变化、代答被推翻、阶段交小结，
//     以及会话用 ks team tell --fyi 标明「只告知」的留言（部署 / 重启通知等）。
//     它们不结束 ks wait，等下次因要动手的事被叫醒、或用户发消息（下一轮开始）时一起附上，不丢。
//
// quietEvents 是订阅事件里只告知的几类；其余（decision、review、blocked）要动手，照常叫醒。
var quietEvents = map[string]bool{protocol.OnSettings: true, protocol.OnStage: true, protocol.OnStatus: true}

// tellFor 按订阅事件类型选送法：只告知的走静默队列，要动手的立刻叫醒。
func (h *Hub) tellFor(kind string) func(context.Context, string, string, string, string) (store.Decision, error) {
	if quietEvents[kind] {
		return h.tellQuiet
	}
	return h.tell
}

// tellQuiet 和 tell 一样送一条团队消息，但不唤醒挂着的 ks wait（R-198、R-227）。
func (h *Hub) tellQuiet(ctx context.Context, to, projectID, reqID, text string) (store.Decision, error) {
	if to == "" {
		return store.Decision{}, nil
	}
	d, err := h.store.CreateDecision(ctx, store.Decision{
		Kind: store.KindTell, SessionKey: to, ProjectID: projectID, RequirementID: reqID, Question: quietTellQ, Answer: text, FYI: true,
	})
	if err != nil {
		return d, err
	}
	markTold(ctx, reqID, to)
	h.decisionsChanged(ctx, d)
	return d, nil
}

// assigner 返回派这条需求的 PM（最近一次 PM 派活记录，且它仍是本项目没结束的 PM），没有时为项目 PM。
func (h *Hub) assigner(ctx context.Context, r store.Requirement) string {
	for _, d := range r.Feedback { // 时间倒序
		if d.Kind == store.KindAssign && d.AssignedBy != "" {
			if h.livePM(ctx, d.AssignedBy, r.ProjectID) {
				return d.AssignedBy
			}
			break
		}
	}
	return h.projectPM(ctx, r.ProjectID)
}

// reviewNote 拼“R-x 已交验收”：最近一份证据的结论（没有结论时取摘要开头）和清单达标数。
func reviewNote(r store.Requirement, by string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s「%s」已交验收", r.Code, brief(r.Title, 40))
	if by != "" {
		fmt.Fprintf(&b, "（%s）", by)
	}
	for i := len(r.Evidence) - 1; i >= 0; i-- {
		e := r.Evidence[i]
		if e.Voided {
			continue
		}
		verdict := ""
		pass, total := 0, 0
		if e.Payload != nil {
			verdict = e.Payload.Verdict
			pass, total = e.Payload.Score()
		}
		if verdict == "" {
			verdict = brief(e.Summary, 60)
		}
		if verdict != "" {
			b.WriteString("：" + verdict)
		}
		if total > 0 {
			fmt.Fprintf(&b, "；达标 %d/%d", pass, total)
		}
		b.WriteString("。")
		if w := reviewWaitNote(r); w != "" {
			return b.String() + w + "不必回复；需要细节用 req_get。"
		}
		return b.String() + "等用户验收，不必回复；需要细节用 req_get。"
	}
	return b.String() + "（没有附证据）。" + reviewWaitNote(r) + "等用户验收；需要细节用 req_get。"
}

// onReview：需求刚进入待验收，告诉派活的 PM。PM 自己交的不送。返回送到的 PM（没送时为空）。
func (h *Hub) onReview(ctx context.Context, ref, by string) string {
	r, err := h.store.GetRequirement(ctx, ref)
	if err != nil || r.ProjectID == "" {
		return ""
	}
	to := h.assigner(ctx, r)
	if to == "" || to == by {
		return ""
	}
	if _, err := h.tell(ctx, to, r.ProjectID, r.ID, reviewNote(r, by)); err != nil {
		h.log.Warn("通知 PM 交验收失败", "err", err)
		return ""
	}
	return to
}

// sessionName 是消息里称呼会话的方式：标题（没有就用设备与工具）加会话键。
func sessionName(x store.Session) string {
	name := x.Title
	if name == "" {
		name = x.DeviceName + " 上的 " + x.Tool + " 会话"
	}
	return fmt.Sprintf("「%s」（%s）", brief(name, 30), x.Key)
}

// devUp：开发在本项目上线时告诉项目 PM「开发 X 已上线待命，可以 req_assign」（R-116）：
// 声明开发身份，或这次在线以来第一次挂上 ks wait（announce = true，每次在线只报一次，离线后重新计）；
// 在线开发够数、用户批准开 Dev 会话后的接入语横幅被清除时也报（announce = false 时只在清除时送）。项目没有 PM 时不送。
func (h *Hub) devUp(ctx context.Context, key string, announce bool) {
	x, err := h.store.GetSession(ctx, key)
	if err != nil || x.ProjectID == "" || x.Role != roles.Dev {
		return
	}
	need, cleared := h.checkNeed(ctx, x.ProjectID)
	if announce {
		h.mu.Lock()
		if _, seen := h.notified["devup|"+key]; seen {
			announce = false
		} else {
			h.notified["devup|"+key] = time.Now()
		}
		h.mu.Unlock()
	}
	if !announce && !cleared {
		return
	}
	pm := h.projectPM(ctx, x.ProjectID)
	if pm == "" || pm == key {
		return
	}
	text := fmt.Sprintf("Dev %s 已上线待命，可以 req_assign 派活。", sessionName(x))
	if cleared {
		text += fmt.Sprintf("在线 Dev 已够用户批准的 %d 个，接入语横幅已撤下。", need)
	}
	if _, err := h.tell(ctx, pm, x.ProjectID, "", text); err != nil {
		h.log.Warn("通知 PM 开发上线失败", "err", err)
	}
}

// devDown：开发离线了，下次上线重新报。
func (h *Hub) devDown(key string) {
	h.mu.Lock()
	delete(h.notified, "devup|"+key)
	h.mu.Unlock()
}

// liveRole 返回项目里身份为 role、没结束的会话中最近活跃的那个，没有时为空。
// 主调度、主助理每项目唯一；评审可能有多个，取最近活跃的（R-191）。
func (h *Hub) liveRole(ctx context.Context, projectID, role string) string {
	return h.liveRoleExcept(ctx, projectID, role, "")
}

// liveRoleExcept 同 liveRole，但跳过会话 except（发消息的一方自己，R-248：顾问可能有多个，不能把话选回给自己）。
func (h *Hub) liveRoleExcept(ctx context.Context, projectID, role, except string) string {
	if projectID == "" {
		return ""
	}
	list, err := h.store.RoleSessions(ctx, projectID) // 按最近活跃倒序
	if err != nil {
		return ""
	}
	for _, x := range list {
		if x.Key != except && x.Role == role && seatHolder(x) {
			return x.Key
		}
	}
	return ""
}

// liveIn：key 是不是 projectID 里没结束的会话。
func (h *Hub) liveIn(ctx context.Context, key, projectID string) bool {
	x, err := h.store.GetSession(ctx, key)
	return err == nil && x.ProjectID == projectID && x.State != store.StateEnded && x.State != store.StateLost && x.DoneAt == nil
}

// tellRoles 是团队消息可以按身份寻址的收件身份（R-191；R-249 加上开发）。
var tellRoles = map[string]bool{roles.PM: true, roles.Aide: true, roles.Reviewer: true, roles.Advisor: true, roles.Dev: true}

// looksLikeSessionKey：--to 填的是会话键（tool:id）而不是身份（R-249）。
func looksLikeSessionKey(s string) bool { return strings.Contains(s, ":") }

// senderName 是团队消息里称呼发消息一方的方式：「身份 会话」，子任务再加「的子任务「名字」（agent_id）」。
func (h *Hub) senderName(ctx context.Context, me store.Session, agent string) string {
	who := "会话"
	if l := roles.Label(me.Role); l != "" {
		who = l
	}
	name := who + " " + sessionName(me)
	if agent != "" {
		name += " 的子任务" + h.agentName(ctx, me.Key, agent)
	}
	return name
}

// agentName 是子任务的称呼：「名字」（agent_id）；没记下名字时只写 agent_id。
func (h *Hub) agentName(ctx context.Context, key, agent string) string {
	if label := h.store.AgentLabel(ctx, key, agent); label != "" {
		return fmt.Sprintf("「%s」（%s）", brief(label, 30), agent)
	}
	return "（" + agent + "）"
}

// noPMNote 是项目没有主调度时发给主调度的团队消息的返回：照旧不送，但明说没送到，不当成错误（R-191）。
const noPMNote = "本项目当前没有主调度，消息未送达"

type tellOut struct {
	Body struct {
		Delivered bool   `json:"delivered"`
		To        string `json:"to,omitempty" doc:"收到的会话键"`
		Role      string `json:"role,omitempty" doc:"收到的会话的身份：pm / aide / reviewer；回复时是提问方的身份（可能为空）"`
		Code      string `json:"code,omitempty" doc:"这条团队消息的编号（D-x）"`
		Message   string `json:"message"`
		// 送达状态（R-249）：逐个收件人列出，点对点只有一个
		Recipients []Delivery `json:"recipients" doc:"逐个收件人的送达状态：delivered 已送达 / pending_wake 等对方醒来（带原因）/ offline 对方不在线"`
	}
}

func (h *Hub) registerAgentTell(api huma.API) {
	h.registerAgentBroadcast(api)
	huma.Register(api, huma.Operation{OperationID: "agent-tell-pm", Method: http.MethodPost, Path: "/api/agent/team/tell", Tags: []string{"agent"},
		Summary:     "给本项目按身份发团队消息，或回复收到的团队消息（只送给 Agent，不进用户收件箱，R-135 / R-191）",
		Description: "to 选收件身份：pm（主调度，默认）、aide（主助理）、reviewer（评审）、advisor（顾问）、dev（开发）；也可直接填会话键（tool:id），点名本项目任何没结束的会话，没有身份的也行（R-249）。按身份发给本项目现在没结束的那个会话；评审、顾问、开发有多个时发给最近活跃的那个（不含你自己），也可用 to_session 点名某个会话（R-248）；没有在线顾问时报错退回给你；点名的会话已结束或失联时报错退回。返回的 recipients 是送达状态：delivered 已送达、pending_wake 等对方醒来（写明原因，如 Codex 要等用户下次发消息）、offline 对方不在线；之后用 /api/agent/team/sent 查。没有主助理时退回给主调度，并注明原本要发给主助理；没有在线评审时报错；没有主调度时不送出、不报错，返回「本项目当前没有主调度，消息未送达」（delivered = false）。fyi = true 时是「只告知」（R-227）：不唤醒对方挂着的 ks wait，下次它因别的事被叫醒或用户发消息时一并带上；默认要对方动手，立刻叫醒。reply_to 填收到的团队消息编号（D-x）时是回复：送回发那条消息的会话；那条是子任务发的，就送到它所属的会话，并注明转给哪个子任务。消息只做通知（R-249）：放编号加一句话，超过 120 字照常送出、message 里提示把细节写进需求留言（/api/agent/requirements/{ref}/comments）。"},
		func(ctx context.Context, in *struct {
			Body struct {
				SessionKey     string `json:"session_key" minLength:"1" doc:"调用者"`
				Text           string `json:"text" minLength:"1" doc:"要说的话"`
				Requirement    string `json:"requirement,omitempty" doc:"相关需求编号（如 R-12），可省略"`
				To             string `json:"to,omitempty" doc:"收件身份：pm（默认）、aide、reviewer、advisor、dev；也认中文名（主调度 / 主助理 / 评审 / 顾问 / 开发）；也可填会话键（tool:id），等同 to_session。回复时不填"`
				ToSession      string `json:"to_session,omitempty" doc:"指定收件会话键（tool:id）：本项目没结束的会话，没有身份的也行（R-249）；与 to（身份）同时填时身份要对得上。本项目有多个顾问 / 评审 / 开发时用它点名；不填则发给该身份最近活跃的一个（R-248）"`
				ReplyTo        string `json:"reply_to,omitempty" doc:"回复收到的团队消息：填它的编号（D-x）"`
				FYI            bool   `json:"fyi,omitempty" doc:"只告知（R-227）：对方不必动手，不唤醒挂着的 ks wait，等它下次因别的事被叫醒或用户发消息时一并带上；默认 false（要对方动手的话：提问、求裁定、派活、回复，立刻叫醒）。部署 / 重启、进展通报这类用 true"`
				SessionClaimed bool   `json:"session_claimed,omitempty" doc:"session_key 是调用方用 --session 自报的（自动识别不到当前会话时，R-248）：hub 校验它是没结束、属于某个项目的会话，并在返回里注明按它认定发送方"`
				AgentID        string `json:"agent_id,omitempty" doc:"发消息的子任务（一次性子代理）的 agent_id；会话主线程发时为空，ks 命令和 MCP 自动带上"`
			}
		}) (*tellOut, error) {
			me, err := h.store.GetSession(ctx, in.Body.SessionKey)
			if err != nil {
				return nil, apiErr(err)
			}
			if in.Body.SessionClaimed && (me.State == store.StateEnded || me.State == store.StateLost || me.DoneAt != nil || me.ProjectID == "") {
				return nil, huma.Error409Conflict("--session " + me.Key + " 不是一个没结束、属于某个项目的会话，不能据此认定发送方，消息没有送出")
			}
			text := strings.TrimSpace(in.Body.Text)
			if text == "" {
				return nil, huma.Error400BadRequest("请写下要说的话")
			}
			agent := strings.TrimSpace(in.Body.AgentID)
			out := &tellOut{}
			if me.ProjectID == "" {
				out.Body.Message = "当前会话不属于任何项目，没有团队成员可以发；要用户决策的事用 decision_raise。"
				return out, nil
			}
			reqID := ""
			if ref := strings.TrimSpace(in.Body.Requirement); ref != "" {
				r, err := h.store.GetRequirement(ctx, ref)
				if err != nil {
					return nil, apiErr(err)
				}
				reqID = r.ID
				text = fmt.Sprintf("%s「%s」%s", r.Code, brief(r.Title, 40), text)
			}
			question := "团队消息"
			if in.Body.FYI {
				question = quietTellQ
			}
			msg := store.Decision{Kind: store.KindTell, ProjectID: me.ProjectID, RequirementID: reqID, Question: question, AssignedBy: me.Key, FromRole: me.Role, FromAgent: agent, FYI: in.Body.FYI}
			from := h.senderName(ctx, me, agent)
			var toRole, note string
			if ref := strings.TrimSpace(in.Body.ReplyTo); ref != "" {
				if strings.TrimSpace(in.Body.To) != "" {
					return nil, huma.Error400BadRequest("回复不用填收件身份：回复会送回发那条消息的一方；要另发给别人请去掉 reply_to")
				}
				orig, err := h.store.GetDecision(ctx, ref)
				if err != nil {
					return nil, apiErr(err)
				}
				switch {
				case orig.Kind != store.KindTell || orig.SessionKey != me.Key:
					return nil, huma.Error400BadRequest(orig.Code + " 不是发给你的团队消息，不能回复")
				case orig.AssignedBy == "":
					return nil, huma.Error400BadRequest(orig.Code + " 是纸鸢自动发的通知，没有可以回复的一方")
				}
				msg.ReplyTo = orig.ID
				msg.SessionKey = orig.AssignedBy
				toRole = orig.FromRole
				if !h.liveIn(ctx, orig.AssignedBy, me.ProjectID) {
					// 提问的会话已结束：它当时是主调度 / 主助理的，送给现在接任的那个
					msg.SessionKey = ""
					if roles.Unique(orig.FromRole) {
						msg.SessionKey = h.liveRole(ctx, me.ProjectID, orig.FromRole)
					}
					if msg.SessionKey == "" {
						return nil, huma.Error409Conflict(fmt.Sprintf("发 %s 的会话 %s 已结束，回复没有送出；有需要可以按身份另发（不填 reply_to）。", orig.Code, orig.AssignedBy))
					}
					note = fmt.Sprintf("（原本回复给 %s，它已结束，转给现在的%s）", orig.AssignedBy, roles.Name(orig.FromRole))
				}
				head := fmt.Sprintf("%s 回复 %s", from, orig.Code)
				if orig.FromAgent != "" {
					head += "，请转给子任务" + h.agentName(ctx, orig.AssignedBy, orig.FromAgent)
				}
				msg.Answer = head + note + "：" + text
			} else {
				role := roles.PM
				toText, ts := strings.TrimSpace(in.Body.To), strings.TrimSpace(in.Body.ToSession)
				if looksLikeSessionKey(toText) {
					// --to 直接填会话键（R-249）：等同 to_session
					if ts != "" && ts != toText {
						return nil, huma.Error400BadRequest("to 填的会话 " + toText + " 和 to_session " + ts + " 不一致，只填一个")
					}
					ts, toText = toText, ""
				}
				if toText != "" {
					r, ok := roles.Resolve(toText)
					if !ok || !tellRoles[r] {
						return nil, huma.Error400BadRequest("收件身份只能是 pm（主调度）、aide（主助理）、reviewer（评审）、advisor（顾问）或 dev（开发），也可以直接填会话键（tool:id）；收到的是「" + toText + "」")
					}
					role = r
				}
				if ts != "" {
					x, err := h.store.GetSession(ctx, ts)
					switch {
					case err != nil:
						return nil, huma.Error404NotFound("没有会话 " + ts + "，消息没有送出；用 ks team sessions 看本项目的会话")
					case x.ProjectID != me.ProjectID:
						return nil, huma.Error409Conflict("会话 " + ts + " 不属于本项目，消息没有送出")
					case sessionOffline(x):
						return nil, huma.Error409Conflict("会话 " + ts + " 不在线（" + offlineWhy(x) + "），消息没有送出、已退回给你；用 ks team sessions 看现在在线的会话")
					}
					if toText != "" && x.Role != role {
						return nil, huma.Error400BadRequest(fmt.Sprintf("会话 %s 的身份是%s，不是你填的%s", ts, roleOrNone(x.Role), roles.Name(role)))
					}
					role = x.Role
				}
				toRole = role
				if ts != "" {
					msg.SessionKey = ts
				} else if roles.Unique(role) {
					msg.SessionKey = h.liveRole(ctx, me.ProjectID, role) // 主调度 / 主助理每项目唯一；自己发给自己由下面统一处理（子任务可以发给所属会话）
				} else {
					msg.SessionKey = h.liveRoleExcept(ctx, me.ProjectID, role, me.Key) // 顾问 / 评审 / 开发可能有多个，不选回自己
				}
				if msg.SessionKey == "" {
					switch role {
					case roles.Aide:
						if msg.SessionKey = h.liveRole(ctx, me.ProjectID, roles.PM); msg.SessionKey == "" {
							return nil, huma.Error409Conflict("本项目现在没有主助理，也没有主调度，消息没有送出；要用户决策的事用 decision_raise。")
						}
						toRole = roles.PM
						note = "（原本发给主助理；本项目现在没有主助理，请你处理或问用户）"
					case roles.Advisor:
						return nil, huma.Error409Conflict("本项目现在没有在线的顾问会话，消息没有送出（已退回给你）；请让用户拉起顾问，或把要交接的内容写进需求再请用户转告；要用户决策的事用 decision_raise。")
					case roles.Reviewer:
						return nil, huma.Error409Conflict("本项目现在没有在线的评审会话，消息没有送出；要审查请让主调度派审查任务，要用户决策的事用 decision_raise。")
					case roles.Dev:
						return nil, huma.Error409Conflict("本项目现在没有在线的开发会话（不含你自己），消息没有送出；派活请告诉主调度。")
					default:
						out.Body.Message = noPMNote
						return out, nil
					}
				}
				msg.Answer = from + " 留言" + note + "：" + text
			}
			if msg.SessionKey == me.Key && agent == "" {
				if note != "" && msg.ReplyTo == "" {
					return nil, huma.Error409Conflict("本项目现在没有主助理，消息没有送出；你就是主调度，自己定或用 decision_raise 问用户。")
				}
				if strings.TrimSpace(in.Body.ToSession) != "" || looksLikeSessionKey(strings.TrimSpace(in.Body.To)) || toRole == "" {
					return nil, huma.Error400BadRequest("不用发给自己")
				}
				return nil, huma.Error400BadRequest("你就是本项目的" + roles.Name(toRole) + "，不用发给自己")
			}
			d, err := h.store.CreateDecision(ctx, msg)
			if err != nil {
				return nil, apiErr(err)
			}
			h.decisionsChanged(ctx, d)
			out.Body.Delivered, out.Body.To, out.Body.Role, out.Body.Code = true, d.SessionKey, toRole, d.Code
			out.Body.Recipients = h.deliveries(ctx, []store.Decision{d})
			st := out.Body.Recipients[0]
			who := roles.Name(toRole)
			if who == "" {
				who = "会话"
			}
			out.Body.Message = fmt.Sprintf("%s 已送给%s %s（%s）：%s——%s；用户不会收到。", d.Code, who, d.SessionKey, st.Tool, st.StatusLabel, st.Reason)
			if in.Body.FYI {
				out.Body.Message = fmt.Sprintf("%s 已按「只告知」送给%s %s（%s）：不唤醒它，%s——%s；用户不会收到。", d.Code, who, d.SessionKey, st.Tool, st.StatusLabel, st.Reason)
			}
			if note != "" && msg.ReplyTo == "" {
				out.Body.Message = fmt.Sprintf("本项目现在没有主助理，%s 已退回给主调度 %s，并注明原本要发给主助理；用户不会收到。", d.Code, d.SessionKey)
			}
			if agent != "" {
				out.Body.Message += "对方的回复会送到你所属的会话（" + me.Key + "），由它转给你；不用 ks wait 等。"
			} else if msg.ReplyTo == "" {
				out.Body.Message += "对方回复时会自动送回你。"
			}
			out.Body.Message += "之后用 ks team sent " + d.Code + "（MCP team_sent）查送达。"
			if in.Body.SessionClaimed {
				out.Body.Message += "（自动识别不到当前会话，已按 --session " + me.Key + " 认定发送方）"
			}
			if hint := writing.MessageHint(in.Body.Text); hint != "" {
				out.Body.Message += "\n" + hint // 消息只做通知（R-249）：照常送出，提示把细节挪到需求留言
			}
			return out, nil
		})
}
