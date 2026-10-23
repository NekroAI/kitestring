package hub

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/NekroAI/kitestring/internal/protocol"
	"github.com/NekroAI/kitestring/internal/roles"
	"github.com/NekroAI/kitestring/internal/store"
)

// 事件订阅（R-164）：ks wait --on decision,review,blocked,stage 订阅本项目别的会话发生的事；不给 --on 时沿用上次，
// 从没给过时按身份默认（Adv：decision、review；PM：review、blocked；Dev 等其它身份不订阅，和以前一样不被打扰）。
// 发生时给在线的订阅者送一条团队消息（kind = tell，R-135 的通道）：挂着 ks wait 的立刻被唤醒，正在跑的下次挂上时收到；
// 只是告诉，不是派活（R-150）；不进用户收件箱、不推送。事件是自己引起的不送；onReview 已经告诉过的 PM 不重复送。

// defaultWaitOn 是各身份的默认订阅。
func defaultWaitOn(role string) []string {
	switch role {
	case roles.Advisor:
		return []string{protocol.OnDecision, protocol.OnReview}
	case roles.PM:
		return []string{protocol.OnReview, protocol.OnBlocked}
	}
	return nil
}

// effectiveWaitOn：存下的订阅（空为按身份默认，none 为不订阅）实际订阅了哪些事件。
func effectiveWaitOn(role, stored string) []string {
	switch stored {
	case "", protocol.OnDefault:
		return defaultWaitOn(role)
	case protocol.OnNone:
		return nil
	}
	return strings.Split(stored, ",")
}

var waitOnZh = map[string]string{protocol.OnDecision: "有人提决策", protocol.OnReview: "交验收", protocol.OnBlocked: "需求受阻", protocol.OnStage: "阶段交小结"}

// broadcast 把事件 kind 告诉项目里订阅了它的在线会话；skip 里的会话（引起事件的、已经通知过的）不送。
func (h *Hub) broadcast(ctx context.Context, kind, projectID, reqID, text string, skip ...string) {
	if projectID == "" {
		return
	}
	subs, err := h.store.WaitSubscribers(ctx, projectID)
	if err != nil {
		h.log.Warn("查事件订阅者失败", "err", err)
		return
	}
	msg := fmt.Sprintf("你订阅的事件（%s · %s）：%s 只是告诉你，不是派给你的活；需要时自己看，不必回复。", kind, waitOnZh[kind], text)
	for _, s := range subs {
		if s.Key == "" || slices.Contains(skip, s.Key) || !slices.Contains(effectiveWaitOn(s.Role, s.WaitOn), kind) {
			continue
		}
		if _, err := h.tell(ctx, s.Key, projectID, reqID, msg); err != nil {
			h.log.Warn("送订阅事件失败", "err", err, "to", s.Key)
		}
	}
}

// actorName 是消息里称呼引起事件的会话；没有会话键时说“有会话”。
func (h *Hub) actorName(ctx context.Context, key string) string {
	if key == "" {
		return "有会话"
	}
	x, err := h.store.GetSession(ctx, key)
	if err != nil {
		return "会话（" + key + "）"
	}
	who := "会话"
	if l := roles.Label(x.Role); l != "" {
		who = l
	}
	return who + " " + sessionName(x)
}

// onDecisionRaised：有会话向用户提了决策。
func (h *Hub) onDecisionRaised(ctx context.Context, d store.Decision) {
	text := fmt.Sprintf("%s向用户提了决策 %s「%s」", h.actorName(ctx, d.SessionKey), d.Code, brief(d.Question, 60))
	if d.RequirementID != "" {
		if r, err := h.store.GetRequirement(ctx, d.RequirementID); err == nil {
			text += "（" + r.Code + "）"
		}
	}
	h.broadcast(ctx, protocol.OnDecision, d.ProjectID, d.RequirementID, text+"，等用户在 Kitestring 上拍板。", d.SessionKey)
}

// onStatusEvent：需求刚进入待验收或受阻。told 是已经收到交验收通知的 PM（R-135），不重复送。
func (h *Hub) onStatusEvent(ctx context.Context, r store.Requirement, by, told string) {
	switch r.Status {
	case store.ReqReview:
		h.broadcast(ctx, protocol.OnReview, r.ProjectID, r.ID, reviewNote(r, by), by, told)
	case store.ReqBlocked:
		text := fmt.Sprintf("%s把 %s「%s」设为受阻。", h.actorName(ctx, by), r.Code, brief(r.Title, 40))
		h.broadcast(ctx, protocol.OnBlocked, r.ProjectID, r.ID, text+"卡在哪见需求时间线 / 证据（req_get）。", by)
	}
}

// onStageSubmitted：计划阶段交了小结。
func (h *Hub) onStageSubmitted(ctx context.Context, x store.Plan, seq int, by string) {
	title := ""
	for _, st := range x.Stages {
		if st.Seq == seq {
			title = "「" + brief(st.Title, 30) + "」"
		}
	}
	text := fmt.Sprintf("%s交了计划 %s「%s」第 %d 阶段%s的阶段小结，等用户验收（plan_get 查看）。", h.actorName(ctx, by), x.Code, brief(x.Title, 30), seq, title)
	h.broadcast(ctx, protocol.OnStage, x.ProjectID, "", text, by)
}
