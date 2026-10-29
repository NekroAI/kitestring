package hub

import (
	"context"
	"time"

	"github.com/NekroAI/kitestring/internal/store"
)

// 只告知的记录不进「待转交」（R-265）：答复后目标会话已结束的记录，会出现在「要你处理」里等你转交或关闭。
// 其中计划进展有一类只是告知、已经没有人要接着做什么——计划已全部完成（或已搁置）时的进展，
// 以及同一计划后来又有了新进展的旧进展（新的那条才是现状；与 staleDelivered 不补发旧进展同一口径）。
// 这类记录目标会话结束时纸鸢直接关掉，原因写明，不转给现任主调度：计划完成是你刚验收的结果，计划页上看得到，
// 主调度不需要据此做事；转过去只会多唤醒一次、多占一段上下文。仍在进行的计划的最新进展（让负责会话接着做下一阶段）照常进待转交。

// noticeClosed 是这类记录关掉时写的原因（时间线、决策记录里看得到）。
const noticeClosed = "只告知的计划进展，目标会话已结束，不用转交"

// noticeSweep 关掉目标会话已结束的只告知记录；返回关掉了几条。
func (h *Hub) noticeSweep(ctx context.Context) int {
	list, err := h.store.ListDecisions(ctx, []string{store.DecAnswered}, 1000)
	if err != nil {
		h.log.Warn("查只告知的记录失败", "err", err)
		return 0
	}
	latest := map[string]int{} // 计划 → 最新一条进展的编号（含已送达的）
	active := map[string]bool{}
	all, err := h.store.ListDecisions(ctx, []string{store.DecAnswered, store.DecDelivered}, 2000)
	if err != nil {
		return 0
	}
	for _, d := range all {
		if d.Kind == store.KindPlan && d.PlanID != "" && seqOf(d.Code) > latest[d.PlanID] {
			latest[d.PlanID] = seqOf(d.Code)
		}
	}
	n := 0
	for _, d := range list {
		if d.Kind != store.KindPlan || d.PlanID == "" || (d.SessionState != "" && d.SessionState != store.StateEnded && d.SessionState != store.StateLost) {
			continue
		}
		on, ok := active[d.PlanID]
		if !ok {
			p, err := h.store.GetPlan(ctx, d.PlanID)
			on = err == nil && p.Status == store.PlanActive
			active[d.PlanID] = on
		}
		if on && latest[d.PlanID] == seqOf(d.Code) {
			continue // 进行中计划的最新进展：还要有人接着做，留在待转交
		}
		if _, err := h.store.CloseDecision(ctx, d.ID, noticeClosed, ""); err != nil {
			h.log.Warn("关掉只告知的记录失败", "err", err, "decision", d.Code)
			continue
		}
		n++
	}
	if n > 0 {
		h.broker.publish("decisions", "sessions")
	}
	return n
}

// NoticeSweep 供测试跑一次只告知记录的清理。
func (h *Hub) NoticeSweep(ctx context.Context) int { return h.noticeSweep(ctx) }

// noticeWatch 每分钟清一次（目标会话可能在记录产生之后才结束）；启动时先清一次，老数据（如 D-964）随之离开「要你处理」。
func (h *Hub) noticeWatch(ctx context.Context) {
	h.noticeSweep(ctx)
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			h.noticeSweep(ctx)
		}
	}
}

// seqOf：D-12 → 12。
func seqOf(code string) int {
	n := 0
	for _, c := range code {
		if c >= '0' && c <= '9' {
			n = n*10 + int(c-'0')
		}
	}
	return n
}
