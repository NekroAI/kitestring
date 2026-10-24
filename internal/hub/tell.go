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

type tellOut struct {
	Body struct {
		Delivered bool   `json:"delivered"`
		To        string `json:"to,omitempty" doc:"收到的 PM 会话键"`
		Message   string `json:"message"`
	}
}

func (h *Hub) registerAgentTell(api huma.API) {
	huma.Register(api, huma.Operation{OperationID: "agent-tell-pm", Method: http.MethodPost, Path: "/api/agent/team/tell", Tags: []string{"agent"}, Summary: "给本项目的 PM 留言（只送给 PM，不进用户收件箱，R-135）"},
		func(ctx context.Context, in *struct {
			Body struct {
				SessionKey  string `json:"session_key" minLength:"1" doc:"调用者"`
				Text        string `json:"text" minLength:"1" doc:"要告诉 PM 的话"`
				Requirement string `json:"requirement,omitempty" doc:"相关需求编号（如 R-12），可省略"`
			}
		}) (*tellOut, error) {
			me, err := h.store.GetSession(ctx, in.Body.SessionKey)
			if err != nil {
				return nil, apiErr(err)
			}
			text := strings.TrimSpace(in.Body.Text)
			if text == "" {
				return nil, huma.Error400BadRequest("请写下要告诉 PM 的话")
			}
			out := &tellOut{}
			if me.ProjectID == "" {
				out.Body.Message = "当前会话不属于任何项目，没有 PM 可以告诉；要用户拍板的事用 decision_raise。"
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
			pm := h.projectPM(ctx, me.ProjectID)
			switch {
			case pm == "":
				out.Body.Message = "本项目现在没有 PM，消息没有送出；要用户拍板的事用 decision_raise。"
				return out, nil
			case pm == me.Key:
				return nil, huma.Error400BadRequest("你就是本项目的 PM")
			}
			who := "会话"
			if l := roles.Label(me.Role); l != "" {
				who = l
			}
			if _, err := h.tell(ctx, pm, me.ProjectID, reqID, fmt.Sprintf("%s %s 留言：%s", who, sessionName(me), text)); err != nil {
				return nil, apiErr(err)
			}
			out.Body.Delivered, out.Body.To = true, pm
			out.Body.Message = "已送给 PM " + pm + "（它挂着 ks wait 时立刻收到，否则下一轮开始时收到）；用户不会收到。"
			return out, nil
		})
}
