package hub

import (
	"context"
	"fmt"
	"time"

	"github.com/NekroAI/kitestring/internal/protocol"
	"github.com/NekroAI/kitestring/internal/roles"
	"github.com/NekroAI/kitestring/internal/store"
)

// 空等下线（R-272）：Claude 的提示缓存 1 小时有效，ks wait 单次等待不超过 55 分钟（项目设置可调），醒来时缓存还在。
// 常驻会话（主调度 / 主助理 / 开发）连续空等累计超过项目设置的时长（默认 4 小时）后，在那次 ks wait 超时醒来时
// 纸鸢叫它写交接（handoff_write）、下线：不再挂 ks wait，Stop hook 不拦（身份不再下发给 daemon），订阅事件不再送。
// 身份保留：点名给它的消息（团队消息、答复、指派）照常入队；你回来后在项目目录运行 ks takeover <身份> 开新会话，
// 新会话接任时拿到交接和接手概览，积压的消息转给新会话。
//
// 「连续空等」的起点记在 sessions.idle_wait_since：
//   - 挂上 ks wait 时没有起点就记下（一段空等开始）；
//   - ks wait 被要动手的事唤醒（delivered）、你在终端发消息时清除；
//   - 上一个 ks wait 还挂着就又挂了一个（被子任务交回等唤醒、干了活再挂）时从这次重新算；
//   - 手上有在跑的子任务时不算空等（主调度在等子任务交回）。
// 只告知的消息（fyi、settings 等）不唤醒，也就不打断空等。

// idleSlack：判断累计空等是否到点时的余量。最后一次等待按剩余时长截短，醒来时差几秒也算到点。
const idleSlack = time.Minute

// trackIdleWait 按事件更新常驻会话的空等起点。prev 是事件前的会话（可能为空），next 是事件后的。
func (h *Hub) trackIdleWait(ctx context.Context, prev *store.Session, next store.Session, ev protocol.Event) {
	if !roles.Resident(next.Role) || next.ProjectID == "" || next.Tool == protocol.ToolDSH || next.OfflineAt != nil {
		return
	}
	var err error
	switch {
	case ev.Name == protocol.EventWait && prev != nil && prev.Parked:
		err = h.store.ResetIdleWait(ctx, next.Key, ev.Time) // 上一个等待没结束就又挂上：中间被别的事唤醒干过活
	case ev.Name == protocol.EventWait:
		err = h.store.MarkIdleWait(ctx, next.Key, ev.Time)
	case ev.Name == protocol.EventWaitEnd && ev.Reason == "delivered",
		ev.Name == "UserPromptSubmit" && ev.Source != protocol.SourceAuto:
		err = h.store.ResetIdleWait(ctx, next.Key, time.Time{})
	}
	if err != nil {
		h.log.Warn("记录空等起点失败", "err", err, "session", next.Key)
	}
}

// idleState 算出会话现在的累计空等时长和下线时长；不适用（非常驻、没项目、DSH、有在跑的子任务）时 ok 为假。
func (h *Hub) idleState(ctx context.Context, x store.Session, now time.Time) (idle, limit time.Duration, ok bool) {
	if !roles.Resident(x.Role) || x.ProjectID == "" || x.Tool == protocol.ToolDSH {
		return 0, 0, false
	}
	limit = store.IdleOfflineDefault
	if p, err := h.store.GetProject(ctx, x.ProjectID); err == nil {
		limit = p.IdleOfflineLimit()
	}
	if len(liveAgents(x)) > 0 {
		return 0, limit, false
	}
	if x.IdleWaitSince != nil {
		idle = max(now.Sub(*x.IdleWaitSince), 0)
	}
	return idle, limit, true
}

// goOffline 记下会话空等下线：身份不再下发给 daemon（Stop hook 不拦），告诉另一位常驻成员一声（只告知）。
func (h *Hub) goOffline(ctx context.Context, x store.Session, idle time.Duration, now time.Time) {
	changed, err := h.store.SetOffline(ctx, x.Key, &now)
	if err != nil {
		h.log.Warn("记录空等下线失败", "err", err, "session", x.Key)
		return
	}
	if !changed {
		return
	}
	h.log.Info("常驻会话空等到时，交接下线", "session", x.Key, "idle", idle.String())
	h.pushRoles(ctx, x.DeviceID)
	h.sessionChanged(x.Key)
	h.broker.publish("sessions")
	// 只告知另一位常驻成员：主助理 / 开发下线告诉主调度，主调度下线告诉主助理
	to := h.liveRoleExcept(ctx, x.ProjectID, roles.PM, x.Key)
	if x.Role == roles.PM {
		to = h.liveRoleExcept(ctx, x.ProjectID, roles.Aide, x.Key)
	}
	if to != "" {
		text := fmt.Sprintf("%s %s 已连续待命 %s，按项目设置写交接后下线。点名给这个会话的消息照常排队，用户开新会话（%s）接任时转给新会话。", roles.Label(x.Role), sessionName(x), humanMinutes(idle), takeoverCmd(x.Role))
		if _, err := h.tellQuiet(ctx, to, x.ProjectID, "", text); err != nil {
			h.log.Warn("通知空等下线失败", "err", err)
		}
	}
}

// offlineHint：叫会话写交接下线的指示（ks wait 超时醒来时输出）。
func offlineHint(role string, idle time.Duration) string {
	pm := ""
	if role == roles.PM {
		pm = "（子任务都交完了才能写；还有在跑的就等它们交回）"
	}
	return fmt.Sprintf("[Kitestring] 你已连续空等 %s（期间没有要动手的事，用户也没发消息），按项目设置交接下线——再续等，每次醒来都要重写一遍上下文缓存。请按顺序做：\n"+
		"1）用 handoff_write 写交接%s，只写纸鸢里没有的三栏，几行即可：doing 做到一半的事、agreements 口头约定、caveats 注意事项；没有 handoff_write 工具时用命令 ks handoff --doing … --caveats …；\n"+
		"2）用一两句话说明已交接下线，然后结束本轮。不要再运行 ks wait；结束时纸鸢不会再拦你。\n"+
		"下线后点名给你的消息照常入队；用户回来后在项目目录运行 %s 开新会话接任，交接、接手概览和积压的消息都会交给新会话。\n", humanMinutes(idle), pm, takeoverCmd(role))
}

// offlineDoneHint：已经下线的会话又运行 ks wait 时的提示。
func offlineDoneHint(role string, handed bool) string {
	if handed {
		return "[Kitestring] 你已交接下线，不要再挂 ks wait：用一两句话收尾后结束本轮。用户回来后会运行 " + takeoverCmd(role) + " 开新会话接任。\n"
	}
	return "[Kitestring] 你已空等下线，还没写交接：先用 handoff_write 写交接（doing / agreements / caveats 三栏），再用一两句话收尾、结束本轮；不要再挂 ks wait。用户回来后会运行 " + takeoverCmd(role) + " 开新会话接任。\n"
}

// handedOff：会话有没有写好、还没被接任的交接。
func (h *Hub) handedOff(ctx context.Context, x store.Session) bool {
	list, err := h.store.HandoffsOf(ctx, x.Key)
	if err != nil {
		return false
	}
	for _, hd := range list {
		if hd.FromSession == x.Key && hd.Role == x.Role && hd.Status == store.HandoffWritten {
			return true
		}
	}
	return false
}

// takeOverOffline：新会话 to 接任时，把下线会话 from 积压的点名消息和它提的未答决策转给 to（R-272）。返回转了几条。
func (h *Hub) takeOverOffline(ctx context.Context, from store.Session, to store.Session) int {
	if from.OfflineAt == nil {
		return 0
	}
	n, err := h.store.MoveUndelivered(ctx, from.Key, to.Key)
	if err != nil {
		h.log.Warn("转交下线会话积压的消息失败", "err", err)
	}
	if _, err := h.store.SetOffline(ctx, from.Key, nil); err != nil {
		h.log.Warn("清除下线标记失败", "err", err)
	}
	if n > 0 {
		h.pushDecisions(ctx, to.DeviceID)
		h.pushDecisions(ctx, from.DeviceID)
		h.broker.publish("decisions")
	}
	return n
}

// offlineBacklogNote：接任时附在身份提示词后的一句——积压的消息已转过来。
func offlineBacklogNote(n int) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf("\n[Kitestring] 上一任下线期间积压的 %d 条点名消息（团队消息、答复、指派等）和它提的未答决策已转给你，稍后随下一条消息或 ks wait 送达。\n", n)
}
