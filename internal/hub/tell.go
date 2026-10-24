package hub

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NekroAI/kitestring/internal/roles"
	"github.com/NekroAI/kitestring/internal/store"
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
	return err == nil && x.Role == roles.PM && x.ProjectID == projectID && x.State != store.StateEnded && x.State != store.StateLost && x.DoneAt == nil
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
	h.decisionsChanged(ctx, d)
	return d, nil
}

// quietTellQ 是静默团队消息的标题（R-198）：照旧入队，会话下一轮开始或 ks wait 因别的消息被唤醒时一并注入，但它本身不唤醒 ks wait。
// 用标题区分，不加新列：下行时 pushDecisions 据此置 Quiet。
const quietTellQ = "团队消息（不唤醒）"

// tellQuiet 和 tell 一样送一条团队消息，但不唤醒挂着的 ks wait（R-198）。
func (h *Hub) tellQuiet(ctx context.Context, to, projectID, reqID, text string) (store.Decision, error) {
	if to == "" {
		return store.Decision{}, nil
	}
	d, err := h.store.CreateDecision(ctx, store.Decision{
		Kind: store.KindTell, SessionKey: to, ProjectID: projectID, RequirementID: reqID, Question: quietTellQ, Answer: text,
	})
	if err != nil {
		return d, err
	}
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
		return b.String() + "等用户验收，不必回复；需要细节用 req_get。"
	}
	return b.String() + "（没有附证据）。等用户验收；需要细节用 req_get。"
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
	if projectID == "" {
		return ""
	}
	list, err := h.store.RoleSessions(ctx, projectID) // 按最近活跃倒序
	if err != nil {
		return ""
	}
	for _, x := range list {
		if x.Role == role && x.State != store.StateEnded && x.State != store.StateLost && x.DoneAt == nil {
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

// tellRoles 是团队消息可以按身份寻址的收件身份（R-191）。
var tellRoles = map[string]bool{roles.PM: true, roles.Aide: true, roles.Reviewer: true}

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

type tellOut struct {
	Body struct {
		Delivered bool   `json:"delivered"`
		To        string `json:"to,omitempty" doc:"收到的会话键"`
		Role      string `json:"role,omitempty" doc:"收到的会话的身份：pm / aide / reviewer；回复时是提问方的身份（可能为空）"`
		Code      string `json:"code,omitempty" doc:"这条团队消息的编号（D-x）"`
		Message   string `json:"message"`
	}
}

func (h *Hub) registerAgentTell(api huma.API) {
	huma.Register(api, huma.Operation{OperationID: "agent-tell-pm", Method: http.MethodPost, Path: "/api/agent/team/tell", Tags: []string{"agent"},
		Summary:     "给本项目按身份发团队消息，或回复收到的团队消息（只送给 Agent，不进用户收件箱，R-135 / R-191）",
		Description: "to 选收件身份：pm（主调度，默认）、aide（主助理）、reviewer（评审）。按身份发给本项目现在没结束的那个会话，不认会话名；评审有多个时发给最近活跃的那个。没有主助理时退回给主调度，并注明原本要发给主助理；没有在线评审时报错；没有主调度时不送出、不报错。reply_to 填收到的团队消息编号（D-x）时是回复：送回发那条消息的会话；那条是子任务发的，就送到它所属的会话，并注明转给哪个子任务。"},
		func(ctx context.Context, in *struct {
			Body struct {
				SessionKey  string `json:"session_key" minLength:"1" doc:"调用者"`
				Text        string `json:"text" minLength:"1" doc:"要说的话"`
				Requirement string `json:"requirement,omitempty" doc:"相关需求编号（如 R-12），可省略"`
				To          string `json:"to,omitempty" doc:"收件身份：pm（默认）、aide、reviewer；也认中文名（主调度 / 主助理 / 评审）。回复时不填"`
				ReplyTo     string `json:"reply_to,omitempty" doc:"回复收到的团队消息：填它的编号（D-x）"`
				AgentID     string `json:"agent_id,omitempty" doc:"发消息的子任务（一次性子代理）的 agent_id；会话主线程发时为空，ks 命令和 MCP 自动带上"`
			}
		}) (*tellOut, error) {
			me, err := h.store.GetSession(ctx, in.Body.SessionKey)
			if err != nil {
				return nil, apiErr(err)
			}
			text := strings.TrimSpace(in.Body.Text)
			if text == "" {
				return nil, huma.Error400BadRequest("请写下要说的话")
			}
			agent := strings.TrimSpace(in.Body.AgentID)
			out := &tellOut{}
			if me.ProjectID == "" {
				out.Body.Message = "当前会话不属于任何项目，没有团队成员可以发；要用户拍板的事用 decision_raise。"
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
			msg := store.Decision{Kind: store.KindTell, ProjectID: me.ProjectID, RequirementID: reqID, Question: "团队消息", AssignedBy: me.Key, FromRole: me.Role, FromAgent: agent}
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
				if t := strings.TrimSpace(in.Body.To); t != "" {
					r, ok := roles.Resolve(t)
					if !ok || !tellRoles[r] {
						return nil, huma.Error400BadRequest("收件身份只能是 pm（主调度）、aide（主助理）或 reviewer（评审），收到的是「" + t + "」")
					}
					role = r
				}
				toRole = role
				msg.SessionKey = h.liveRole(ctx, me.ProjectID, role)
				if msg.SessionKey == "" {
					switch role {
					case roles.Aide:
						if msg.SessionKey = h.liveRole(ctx, me.ProjectID, roles.PM); msg.SessionKey == "" {
							return nil, huma.Error409Conflict("本项目现在没有主助理，也没有主调度，消息没有送出；要用户拍板的事用 decision_raise。")
						}
						toRole = roles.PM
						note = "（原本发给主助理；本项目现在没有主助理，请你处理或问用户）"
					case roles.Reviewer:
						return nil, huma.Error409Conflict("本项目现在没有在线的评审会话，消息没有送出；要审查请让主调度派审查任务，要用户拍板的事用 decision_raise。")
					default:
						out.Body.Message = "本项目现在没有 PM，消息没有送出；要用户拍板的事用 decision_raise。"
						return out, nil
					}
				}
				msg.Answer = from + " 留言" + note + "：" + text
			}
			if msg.SessionKey == me.Key && agent == "" {
				if note != "" && msg.ReplyTo == "" {
					return nil, huma.Error409Conflict("本项目现在没有主助理，消息没有送出；你就是主调度，自己定或用 decision_raise 问用户。")
				}
				return nil, huma.Error400BadRequest("你就是本项目的" + roles.Name(toRole) + "，不用发给自己")
			}
			d, err := h.store.CreateDecision(ctx, msg)
			if err != nil {
				return nil, apiErr(err)
			}
			h.decisionsChanged(ctx, d)
			out.Body.Delivered, out.Body.To, out.Body.Role, out.Body.Code = true, d.SessionKey, toRole, d.Code
			who := roles.Name(toRole)
			if who == "" {
				who = "会话"
			}
			out.Body.Message = fmt.Sprintf("%s 已送给%s %s（它挂着 ks wait 时立刻收到，否则下一轮开始时收到）；用户不会收到。", d.Code, who, d.SessionKey)
			if note != "" && msg.ReplyTo == "" {
				out.Body.Message = fmt.Sprintf("本项目现在没有主助理，%s 已退回给主调度 %s，并注明原本要发给主助理；用户不会收到。", d.Code, d.SessionKey)
			}
			if agent != "" {
				out.Body.Message += "对方的回复会送到你所属的会话（" + me.Key + "），由它转给你；不用 ks wait 等。"
			} else if msg.ReplyTo == "" {
				out.Body.Message += "对方回复时会自动送回你。"
			}
			return out, nil
		})
}
