package hub

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/NekroAI/kitestring/internal/store"
)

// 指派后未开始处理（R-278）：已指派（交出或排队送达）、已送达的待开始需求，接收方迟迟没开始处理时纸鸢兜底。
//   - 送达后过了项目设置的时长（默认 10 分钟）仍在待开始：用「要动手」那一档再提醒接收方一次（唤醒它挂着的等待）；
//   - 提醒后再过同样时长仍没开始：需求上记下 offer_overdue_at，进用户的「待处理」，写明哪条需求、指派给了谁、等了多久；
//   - 开始处理（关联需求、状态改为进行中）或状态有任何变化、重新指派：记录随之清空；还没送达的提醒直接关掉，不再送。
// 接收方会话已结束 / 失联、没法唤醒的，不发提醒，直接在同一时刻进「待处理」。
// 只认明确记录（设计原则第 11 条）：送达时间取那条指派 / 排队送达的 delivered_at，提醒、进「待处理」都记在需求上。

// startNudgeQ 是提醒的标题（只用于显示）。
const startNudgeQ = "指派未开始处理"

// startNudgeClosed 是需求开始处理后、还没送达的提醒被关掉时写的原因。
const startNudgeClosed = "需求已开始处理或状态已变化，提醒不再送"

// startWatch 每分钟查一次指派后未开始处理的需求。
func (h *Hub) startWatch(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			h.startSweep(ctx, time.Now())
		}
	}
}

// StartSweep 供测试按指定时间跑一次巡查。
func (h *Hub) StartSweep(ctx context.Context, now time.Time) { h.startSweep(ctx, now) }

func (h *Hub) startSweep(ctx context.Context, now time.Time) {
	reqs, err := h.store.ListRequirements(ctx, "", []string{store.ReqReady})
	if err != nil {
		h.log.Warn("查待开始需求失败", "err", err)
		return
	}
	limits := map[string]time.Duration{}
	limitOf := func(pid string) time.Duration {
		l, ok := limits[pid]
		if !ok {
			l = store.StartNudgeDefault
			if p, err := h.store.GetProject(ctx, pid); err == nil {
				l = p.StartNudgeLimit()
			}
			limits[pid] = l
		}
		return l
	}
	changed := false
	for _, r := range reqs {
		if r.OfferedTo == "" || r.OfferedAt == nil || r.OfferOverdueAt != nil || r.StageStatus == store.StageLocked {
			continue
		}
		key := r.OfferedTo
		delivered := r.OfferDeliveredAt
		if delivered == nil {
			at, err := h.store.OfferDelivery(ctx, r.ID, key, *r.OfferedAt)
			if err != nil || at == nil {
				continue // 还没送达（例如 Codex 会话要等用户下次发消息）：不算等
			}
			if _, err := h.store.SetOfferStage(ctx, r.ID, key, "offer_delivered_at", *at); err != nil {
				continue
			}
			delivered = at
		}
		limit := limitOf(r.ProjectID)
		x, err := h.store.GetSession(ctx, key)
		gone := err != nil || x.State == store.StateEnded || x.State == store.StateLost || x.DoneAt != nil
		switch {
		case r.OfferNudgedAt == nil && now.Sub(*delivered) >= limit:
			if gone {
				// 没法唤醒：不发提醒，直接进「待处理」
				if ok, _ := h.store.SetOfferStage(ctx, r.ID, key, "offer_nudged_at", now); ok {
					_, _ = h.store.SetOfferStage(ctx, r.ID, key, "offer_overdue_at", now)
					changed = true
				}
				continue
			}
			ok, err := h.store.SetOfferStage(ctx, r.ID, key, "offer_nudged_at", now)
			if err != nil || !ok {
				continue
			}
			changed = true
			if _, err := h.startNudge(ctx, r, key, now.Sub(*delivered)); err != nil {
				h.log.Warn("提醒接收方开始处理失败", "err", err, "req", r.Code)
			}
		case r.OfferNudgedAt != nil && now.Sub(*r.OfferNudgedAt) >= limit:
			if ok, _ := h.store.SetOfferStage(ctx, r.ID, key, "offer_overdue_at", now); ok {
				changed = true
			}
		}
	}
	if h.closeStaleNudges(ctx) > 0 {
		changed = true
	}
	if changed {
		h.broker.publish("requirements", "decisions")
	}
}

// startNudge 用「要动手」那一档再提醒接收方一次。
func (h *Hub) startNudge(ctx context.Context, r store.Requirement, to string, waited time.Duration) (store.Decision, error) {
	text := fmt.Sprintf("纸鸢提醒（start · 未开始处理）：%s「%s」已指派给你，送达后 %s仍未开始处理。"+
		"先用 session_link_req %s 关联需求再开始；手上有别的事、暂时没法开始的，在回复里说明原因和打算何时开始。",
		r.Code, brief(r.Title, 40), humanMinutes(waited), r.Code)
	d, err := h.store.CreateDecision(ctx, store.Decision{
		Kind: store.KindTell, SessionKey: to, ProjectID: r.ProjectID, RequirementID: r.ID, Question: startNudgeQ, Answer: text,
	})
	if err != nil {
		return d, err
	}
	h.decisionsChanged(ctx, d)
	return d, nil
}

// closeStaleNudges 关掉还没送达、但需求已开始处理（不在待开始，或已交给别的会话）的提醒；返回关掉了几条。
func (h *Hub) closeStaleNudges(ctx context.Context) int {
	list, err := h.store.ListDecisions(ctx, []string{store.DecAnswered}, 1000)
	if err != nil {
		return 0
	}
	n := 0
	for _, d := range list {
		if d.Kind != store.KindTell || d.Question != startNudgeQ || d.RequirementID == "" {
			continue
		}
		r, err := h.store.GetRequirement(ctx, d.RequirementID)
		if err == nil && r.Status == store.ReqReady && r.OfferedTo == d.SessionKey {
			continue
		}
		if c, err := h.store.CloseDecision(ctx, d.ID, startNudgeClosed, ""); err == nil {
			n++
			h.decisionsChanged(ctx, c)
		}
	}
	return n
}

// clearStartNudge：需求离开待开始（开始处理、改状态）时立刻关掉还没送达的提醒，不等下一次巡查。
func (h *Hub) clearStartNudge(ctx context.Context, r store.Requirement, before string) {
	if before != store.ReqReady || r.Status == store.ReqReady {
		return
	}
	if h.closeStaleNudges(ctx) > 0 {
		h.broker.publish("decisions")
	}
}

// 紧接着重挂等待时重列（R-278，D-1201）：同一会话里旧的 ks wait 送来指派等要动手的内容后，会话紧接着又挂了新的 ks wait，
// 旧等待的输出可能没被读（D-1192：以为它是被新等待接替的）。新等待开头把那些内容再列一次——只列编号和一句话，
// 只列对应需求仍待开始、仍交给这个会话的；每条只重列一次。
// 「紧接着」= 上一次等待开始以来经 ks wait 送达的；Hub 重启后不知道上一次等待何时开始，按最近 replayWindow 算。

// replayWindow：不知道上一次等待何时开始时，往回看多久的送达。
const replayWindow = time.Hour

// waitReplay 记下这次等待开始，返回要在开头重列的内容（没有时为空）。
func (h *Hub) waitReplay(ctx context.Context, key string, now time.Time) string {
	h.mu.Lock()
	since, ok := h.notified["waitstart|"+key]
	h.notified["waitstart|"+key] = now
	h.mu.Unlock()
	if !ok {
		since = now.Add(-replayWindow)
	}
	list, err := h.store.WaitDeliveredSince(ctx, key, since)
	if err != nil || len(list) == 0 {
		return ""
	}
	var lines []string
	seen := map[string]bool{}
	for _, d := range list {
		if seen[d.RequirementID] {
			continue
		}
		r, err := h.store.GetRequirement(ctx, d.RequirementID)
		if err != nil || r.Status != store.ReqReady || r.OfferedTo != key {
			continue
		}
		h.mu.Lock()
		_, done := h.notified["replay|"+d.ID]
		h.notified["replay|"+d.ID] = now
		h.mu.Unlock()
		if done {
			continue
		}
		seen[d.RequirementID] = true
		lines = append(lines, fmt.Sprintf("- %s %s %s「%s」，需求仍待开始", d.Code, replayKind(d), r.Code, brief(r.Title, 30)))
	}
	if len(lines) == 0 {
		return ""
	}
	return "[Kitestring] 上一次等待送来、对应需求还没开始处理的内容（再列一次，详情用 req_get 看）：\n" + strings.Join(lines, "\n") +
		"\n先用 session_link_req 关联需求再开始；暂时没法开始的，在回复里说明原因和打算何时开始。\n"
}

func replayKind(d store.Decision) string {
	switch d.Kind {
	case store.KindAssign:
		return "指派"
	case store.KindQueue:
		return "排队送达"
	case store.KindTell:
		if d.Question == startNudgeQ {
			return "提醒开始处理"
		}
		return "团队消息"
	}
	return "送达"
}
