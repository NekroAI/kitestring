package hub

import (
	"context"
	"fmt"
	"time"

	"github.com/NekroAI/kitestring/internal/roles"
	"github.com/NekroAI/kitestring/internal/store"
)

// 待命超时的开发会话（R-229）：开发挂着 ks wait 待命超过项目设置的时长（默认 30 分钟）、手上没有排给它的需求
// （待开始 / 进行中 / 受阻）时，纸鸢记下「待命超时」：
//   - 泳道和会话面板标「待命超时 · 建议收尾」，项目主调度收到一条只告知的说明，由主调度 / 用户决定收尾；
//   - 之后不再给它送订阅事件（ks wait --on 的那些，store.WaitSubscribers 直接不列它）——不是降为只告知：
//     再派活时不会一次灌进一堆过期事件；
//   - 点名给它的照常叫醒：用户对它所提决策的答复、指名发给它的团队消息、派给它的活、你在终端给它发消息，这些同时清除待命超时，
//     恢复正常；有排给它的需求时（自动派发、用户关联）每分钟的巡查也会清除。
// 卸任（被 ks role 接任）的会话同样不在订阅者里，见 setRole（与 R-224 的交接卸任同一处，SetRetired）。

// devIdleWatch 每分钟查一次待命超时的开发会话。
func (h *Hub) devIdleWatch(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			h.devIdleSweep(ctx, time.Now())
		}
	}
}

// hasWork：会话手上有没有排给它的需求（待开始 / 进行中 / 受阻）。待验收的不算：开发那边已经交了，退回会由主调度重新派。
func hasWork(x store.Session) bool {
	for _, r := range x.Requirements {
		switch r.Status {
		case store.ReqReady, store.ReqInProgress, store.ReqBlocked:
			return true
		}
	}
	return false
}

// DevIdleSweep 供测试按指定时间跑一次待命超时巡查。
func (h *Hub) DevIdleSweep(ctx context.Context, now time.Time) { h.devIdleSweep(ctx, now) }

// devIdleSweep：给待命超时的开发会话记下 idle_at 并告诉主调度；有活了的清除。
func (h *Hub) devIdleSweep(ctx context.Context, now time.Time) {
	list, err := h.store.ListSessions(ctx, 48*time.Hour, false)
	if err != nil {
		h.log.Warn("查待命开发会话失败", "err", err)
		return
	}
	limits := map[string]time.Duration{}
	for _, x := range list {
		if x.Role != roles.Dev || x.ProjectID == "" || x.DoneAt != nil || x.State == store.StateEnded || x.State == store.StateLost {
			continue
		}
		if hasWork(x) {
			h.clearIdle(ctx, x.Key)
			continue
		}
		if x.IdleAt != nil || x.State != store.StateParked {
			continue
		}
		limit, ok := limits[x.ProjectID]
		if !ok {
			limit = store.DevIdleDefault
			if p, err := h.store.GetProject(ctx, x.ProjectID); err == nil {
				limit = p.DevIdleLimit()
			}
			limits[x.ProjectID] = limit
		}
		if now.Sub(x.StateSince) < limit {
			continue
		}
		if err := h.store.SetIdle(ctx, x.Key, now); err != nil {
			h.log.Warn("记下开发待命超时失败", "err", err)
			continue
		}
		h.sessionChanged(x.Key)
		if pm := h.projectPM(ctx, x.ProjectID); pm != "" {
			text := fmt.Sprintf("Dev %s 已待命 %s、手上没有排给它的需求：纸鸢之后不再给它送订阅事件（点名给它的消息、派活照常送到），泳道上标了「待命超时 · 建议收尾」。"+
				"近期没有要派给它的活，就请用户在它的终端里收尾结束；有新活照常 req_assign，派活后自动恢复。", sessionName(x), humanMinutes(now.Sub(x.StateSince)))
			if _, err := h.tellQuiet(ctx, pm, x.ProjectID, "", text); err != nil {
				h.log.Warn("通知主调度开发待命超时失败", "err", err)
			}
		}
	}
}

// clearIdle 清除会话的待命超时（R-229）：派活、点名给它的消息、你在终端给它发消息、它重新声明身份时。
func (h *Hub) clearIdle(ctx context.Context, key string) {
	if key == "" {
		return
	}
	if was, err := h.store.ClearIdle(ctx, key); err == nil && was {
		h.sessionChanged(key)
	}
}

// humanMinutes：45m → 45 分钟，90m → 1 小时 30 分钟。
func humanMinutes(d time.Duration) string {
	m := int(d.Round(time.Minute).Minutes())
	if m < 60 {
		return fmt.Sprintf("%d 分钟", max(m, 1))
	}
	if m%60 == 0 {
		return fmt.Sprintf("%d 小时", m/60)
	}
	return fmt.Sprintf("%d 小时 %d 分钟", m/60, m%60)
}
