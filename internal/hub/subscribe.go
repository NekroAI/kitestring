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
// 从没给过时按身份默认（主助理：decision、review、settings；主调度：review、blocked、settings；顾问（R-190 起）、开发等其它身份不订阅，和以前一样不被打扰）。
// 发生时给在线的订阅者送一条团队消息（kind = tell，R-135 的通道）：挂着 ks wait 的立刻被唤醒，正在跑的下次挂上时收到；
// 只是告诉，不是派活（R-150）；不进用户收件箱、不推送。事件是自己引起的不送；onReview 已经告诉过的 PM 不重复送。

// defaultWaitOn 是各身份的默认订阅。
func defaultWaitOn(role string) []string {
	switch role {
	case roles.Aide:
		return []string{protocol.OnDecision, protocol.OnReview, protocol.OnSettings}
	case roles.PM:
		return []string{protocol.OnReview, protocol.OnBlocked, protocol.OnSettings}
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

var waitOnZh = map[string]string{protocol.OnDecision: "有人提决策", protocol.OnReview: "交验收", protocol.OnBlocked: "需求受阻", protocol.OnStage: "阶段交小结", protocol.OnSettings: "用户改项目设置", protocol.OnStatus: "用户改需求状态"}

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
		if _, err := h.tellFor(kind)(ctx, s.Key, projectID, reqID, msg); err != nil {
			h.log.Warn("送订阅事件失败", "err", err, "to", s.Key)
		}
	}
}

// notifyAssigner（R-160）：派活关系本身保证通知——事件 kind 发生在需求 r 上，派活的 PM（没有派活记录时为项目 PM）
// 即使没订阅 kind 也要收到一条团队消息。订阅了的由 broadcast 照常送，这里不重复送。返回已处理的 PM 键（供 broadcast 跳过，没有为空）。
func (h *Hub) notifyAssigner(ctx context.Context, kind string, r store.Requirement, by, text string) string {
	to := h.assigner(ctx, r)
	if to == "" || to == by {
		return ""
	}
	if subs, err := h.store.WaitSubscribers(ctx, r.ProjectID); err == nil {
		for _, s := range subs {
			if s.Key == to && slices.Contains(effectiveWaitOn(s.Role, s.WaitOn), kind) {
				return "" // 订阅了，broadcast 会送
			}
		}
	}
	msg := fmt.Sprintf("这条需求是你派的（%s · %s）：%s 只是告诉你，不是派给你的活；不必回复。", kind, waitOnZh[kind], text)
	if _, err := h.tellFor(kind)(ctx, to, r.ProjectID, r.ID, msg); err != nil {
		h.log.Warn("通知派活的 PM 失败", "err", err, "to", to)
	}
	return to
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
	text += "，等用户在 Kitestring 上拍板。"
	if h.proxyQuiet(ctx, d) {
		text += "用户开着参谋授权窗口，这条在范围内：被授权的主助理可以用 decision_answer 代答（写理由和自己的风险判断），拿不准就留给用户。"
	}
	skip := []string{d.SessionKey}
	if d.RequirementID != "" {
		// 派活关系本身保证通知（R-160）：针对这条需求的决策一定告诉派活的 PM，不看它订阅了什么；之后不再按订阅重复送。
		if r, err := h.store.GetRequirement(ctx, d.RequirementID); err == nil {
			skip = append(skip, h.notifyAssigner(ctx, protocol.OnDecision, r, d.SessionKey, text))
		}
	}
	h.broadcast(ctx, protocol.OnDecision, d.ProjectID, d.RequirementID, text, skip...)
}

// onStatusEvent：需求刚进入待验收或受阻。told 是已经收到交验收通知的 PM（R-135），不重复送。
func (h *Hub) onStatusEvent(ctx context.Context, r store.Requirement, by, told string) {
	switch r.Status {
	case store.ReqReview:
		h.broadcast(ctx, protocol.OnReview, r.ProjectID, r.ID, reviewNote(r, by), by, told)
	case store.ReqBlocked:
		text := fmt.Sprintf("%s把 %s「%s」设为受阻。", h.actorName(ctx, by), r.Code, brief(r.Title, 40))
		text += "卡在哪见需求时间线 / 证据（req_get）。"
		skip := []string{by}
		// 派活关系本身保证通知（R-160）：受阻一定告诉派活的 PM，不看它订阅了什么；之后不再按订阅重复送。
		skip = append(skip, h.notifyAssigner(ctx, protocol.OnBlocked, r, by, text))
		h.broadcast(ctx, protocol.OnBlocked, r.ProjectID, r.ID, text, skip...)
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

// 用户在网页上做的事（R-171）：R-164 的订阅只覆盖会话引起的事件，用户改设置 / 改需求状态也要让相关会话知道，不必再回终端手动叫醒。
// 同样是团队消息：不进用户收件箱、不推送；已经被别的通知告诉过的会话不重复送。

// onSettingsChanged：用户改了项目 p 的设置，text 写清楚改了什么。送给订阅了 settings 的在线会话（主调度、主助理默认订阅）；skip 里是已被告知的。
func (h *Hub) onSettingsChanged(ctx context.Context, projectID, text string, skip ...string) {
	h.broadcast(ctx, protocol.OnSettings, projectID, "", text, skip...)
}

// projectSettingsChanges 比较项目设置改前改后，逐条写出「用户打开了项目 X 的……」。
func projectSettingsChanges(before, after store.Project) []string {
	var out []string
	flip := func(was, now bool, name string) {
		if was != now {
			act := "打开"
			if !now {
				act = "关闭"
			}
			out = append(out, fmt.Sprintf("用户%s了项目 %s 的%s（%s → %s）。", act, before.Name, name, onOff(was), onOff(now)))
		}
	}
	flip(before.AgentRelease, after.AgentRelease, "「允许主调度 / 主助理放行草稿」放行开关")
	flip(before.CommitOnAccept, after.CommitOnAccept, "「验收通过后自动提交」（需求验收通过时提示主调度提交属于它的改动，不推送）")
	flip(before.AutoDispatch, after.AutoDispatch, "「自动派发」（会话空闲时自动送达排给它的待开始需求）")
	if roles.NormPolicy(before.ModelPolicy) != roles.NormPolicy(after.ModelPolicy) {
		out = append(out, fmt.Sprintf("用户把项目 %s 的「子任务模型策略」从「%s」改为「%s」：之后派子任务按新的倾向选模型（身份提示词已更新，ks role 重新读到的就是新的）。", before.Name, roles.PolicyName(before.ModelPolicy), roles.PolicyName(after.ModelPolicy)))
	}
	return out
}

func onOff(b bool) string {
	if b {
		return "开"
	}
	return "关"
}

// onUserStatusChanged：用户在网页上把需求 before 改成了 after.Status。送给派活的 PM、关联的在线会话和订阅了 status 的会话；
// 关联的主助理 / 顾问只是记下需求的（R-198）：订阅了 status 的由 broadcast 照常送，没订阅的只静默入队、不唤醒 ks wait；
// skip 里是别的通知已经告诉过的会话（比如撤回放行时已经通知了在做的会话）。
func (h *Hub) onUserStatusChanged(ctx context.Context, before, after store.Requirement, skip ...string) {
	if before.Status == after.Status || after.ProjectID == "" {
		return
	}
	text := fmt.Sprintf("用户把 %s「%s」从%s改为%s。", after.Code, brief(after.Title, 40), reqStatusZh[before.Status], reqStatusZh[after.Status])
	told := slices.Clone(skip)
	// 验收通过且项目开了「验收通过后自动提交」（R-192）：给负责的主调度送一条合成消息（状态变化 + 提交提示），不再另送 status 通知。
	if to := h.commitHint(ctx, after, text); to != "" {
		told = append(told, to)
	}
	// 放行为待开始且项目自动派发开着：会话空闲时排给它的需求会整批送达，那就是通知，这里不再重复送；订阅了 status 的照常收到。
	if after.Status == store.ReqReady {
		if p, err := h.store.GetProject(ctx, after.ProjectID); err == nil && p.AutoDispatch {
			h.broadcast(ctx, protocol.OnStatus, after.ProjectID, after.ID, text, told...)
			return
		}
	}
	if a := h.assigner(ctx, after); a != "" && !slices.Contains(told, a) {
		if to := h.notifyAssigner(ctx, protocol.OnStatus, after, "", text); to != "" {
			told = append(told, to)
		}
	}
	msg := fmt.Sprintf("你关联的需求有变化（status · %s）：%s 只是告诉你，不是派给你的活；需要时自己看，不必回复。", waitOnZh[protocol.OnStatus], text)
	statusSubs := map[string]bool{} // 会被下面 broadcast 送到 status 的会话
	if subs, err := h.store.WaitSubscribers(ctx, after.ProjectID); err == nil {
		for _, s := range subs {
			statusSubs[s.Key] = slices.Contains(effectiveWaitOn(s.Role, s.WaitOn), protocol.OnStatus)
		}
	}
	for _, k := range after.Sessions {
		if slices.Contains(told, k) {
			continue
		}
		x, err := h.store.GetSession(ctx, k)
		if err != nil || x.State == store.StateEnded || x.State == store.StateLost || x.DoneAt != nil {
			continue
		}
		send := h.tellQuiet // 只告知（R-227）：状态变化不唤醒，下次唤醒或下一轮时一并带上
		if recorderOnly(x.Role) && statusSubs[k] {
			continue // 订阅了 status：下面的 broadcast 送（同样不唤醒）
		}
		if !recorderOnly(x.Role) && (after.Status == store.ReqDraft || after.Status == store.ReqShelved) {
			send = h.tell // 执行方手上的需求被用户撤回 / 搁置：要停下，照常叫醒
		}
		if _, err := send(ctx, k, after.ProjectID, after.ID, msg); err != nil {
			h.log.Warn("通知关联会话需求状态变化失败", "err", err, "to", k)
			continue
		}
		told = append(told, k)
	}
	h.broadcast(ctx, protocol.OnStatus, after.ProjectID, after.ID, text, told...)
}

// recorderOnly（R-198）：这种身份关联需求只因为需求是它记下的（记需求时自动关联），不负责做——主助理、顾问。
// 用户改这类需求的状态时，没订阅 status 的它们不该被 ks wait 唤醒；执行方（主调度、开发、子任务等其它身份）照旧收到。
func recorderOnly(role string) bool { return role == roles.Aide || role == roles.Advisor }

// onUserPlanEvent：用户在网页上批准 / 退回计划、通过 / 退回阶段（R-171）。负责会话已经收到计划进展（planMsg），不重复送；
// 订阅了 status 的其它会话收到一句说明。
func (h *Hub) onUserPlanEvent(ctx context.Context, x store.Plan, text string) {
	h.broadcast(ctx, protocol.OnStatus, x.ProjectID, "", text, x.OwnerSession)
}

// commitHint（R-192）：需求 r 刚被验收通过，项目开了「验收通过后自动提交」时，给负责的主调度（派活的 PM，没有时为项目 PM）送一条团队消息，
// 请它按项目设置提交属于这条需求的改动、不推送。纸鸢自己不执行 git，只送消息；head 是同一事件已有的状态说明，合成一条送出，调用方不用再给它送 status 通知。
// 返回已送到的会话键（没送为空）。
func (h *Hub) commitHint(ctx context.Context, r store.Requirement, head string) string {
	if r.Status != store.ReqDone || r.ProjectID == "" {
		return ""
	}
	p, err := h.store.GetProject(ctx, r.ProjectID)
	if err != nil || !p.CommitOnAccept {
		return ""
	}
	to := h.assigner(ctx, r)
	if to == "" {
		return ""
	}
	if head != "" {
		head += " "
	}
	msg := fmt.Sprintf("团队消息（status · %s）：%s%s 已验收，按项目设置提交属于它的改动（不推送）。只提交这条需求的文件，提交说明用中文并带需求编号 %s；提交由你来做，纸鸢不动仓库。", waitOnZh[protocol.OnStatus], head, r.Code, r.Code)
	if _, err := h.tell(ctx, to, r.ProjectID, r.ID, msg); err != nil {
		h.log.Warn("送验收后提交提示失败", "err", err, "to", to)
		return ""
	}
	return to
}
