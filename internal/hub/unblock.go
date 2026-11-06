package hub

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/NekroAI/kitestring/internal/store"
)

// 受阻自动解除（R-269）：需求设为受阻时写了在等 R-x / D-x；等的需求完成（done）或决策答复后，
// 受阻的需求自动回到受阻前的状态，并告诉设为受阻的会话，免得它挂着受阻没人动。

// waitedText 是「原来在等什么」的一句话，写进时间线。
func waitedText(r store.Requirement) string {
	s := "原来在等"
	if r.BlockedOn != "" {
		s += " " + r.BlockedOn
	}
	if r.BlockedReason != "" {
		if r.BlockedOn != "" {
			s += "："
		}
		s += r.BlockedReason
	}
	if r.BlockedOn == "" && r.BlockedReason == "" {
		s += "别的事"
	}
	return s
}

// onWaitSettled 是 store 的回调：code（R-x / D-x）刚完成 / 被答复。
func (h *Hub) onWaitSettled(code string) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	h.unblockWaiters(ctx, code)
}

// unblockWaiters 解除所有在等 code 的受阻需求。
func (h *Hub) unblockWaiters(ctx context.Context, code string) {
	list, err := h.store.ListRequirements(ctx, "", []string{store.ReqBlocked})
	if err != nil {
		h.log.Warn("查受阻需求失败", "err", err)
		return
	}
	for _, r := range list {
		if r.BlockedOn == code {
			h.unblock(ctx, r, code)
		}
	}
}

// liveSession：会话还在（没结束、没丢失、没标完成）。
func (h *Hub) liveSession(ctx context.Context, key string) bool {
	if key == "" {
		return false
	}
	x, err := h.store.GetSession(ctx, key)
	return err == nil && x.State != store.StateEnded && x.State != store.StateLost && x.DoneAt == nil
}

// unblock 把受阻的 r 回到受阻前的状态。受阻前没有记录（旧数据）时：有关联的在线会话回进行中，否则待开始。
func (h *Hub) unblock(ctx context.Context, r store.Requirement, code string) {
	from, setter := h.store.BlockedFrom(ctx, r.ID)
	to, basis := from, "受阻前是"+reqStatusZh[from]
	if from != store.ReqReady && from != store.ReqInProgress {
		to = store.ReqReady
		basis = "没有受阻前的记录，按默认回待开始"
		for _, k := range r.Sessions {
			if h.liveSession(ctx, k) {
				to = store.ReqInProgress
				basis = "没有受阻前的记录，有关联会话在线，按默认回进行中"
				break
			}
		}
	}
	what := "已完成"
	if code[0] == 'D' {
		what = "已答复"
	}
	reason := fmt.Sprintf("等的 %s %s，自动解除受阻（%s）", code, what, basis)
	sctx := store.WithActor(ctx, store.Actor{Via: "unblock", Ref: code, Reason: reason})
	nr, err := h.store.SetRequirementStatus(sctx, r.ID, to)
	if err != nil {
		h.log.Warn("自动解除受阻失败", "err", err, "req", r.Code)
		return
	}
	h.refreshDelivered(ctx, nr)
	h.broker.publish("requirements", "sessions", "plans", "decisions")
	// 通知设为受阻的会话；它已不在线时告诉关联的在线会话和指派的 PM
	ctx = withTold(ctx)
	msg := fmt.Sprintf("%s「%s」等的 %s %s，已自动解除受阻、回到%s，可以接着做。", nr.Code, brief(nr.Title, 40), code, what, reqStatusZh[to])
	var told []string
	if h.liveSession(ctx, setter) {
		told = append(told, setter)
	} else {
		for _, k := range nr.Sessions {
			if h.liveSession(ctx, k) {
				told = append(told, k)
			}
		}
		if a := h.assigner(ctx, nr); a != "" && !slices.Contains(told, a) {
			told = append(told, a)
		}
	}
	for _, k := range told {
		if _, err := h.tell(ctx, k, nr.ProjectID, nr.ID, msg); err != nil {
			h.log.Warn("通知受阻解除失败", "err", err, "to", k)
		}
	}
	h.reqStatusEvent(ctx, nr, store.ReqBlocked, "")
	if to == store.ReqReady {
		h.kick(ctx, nr.ID)
	}
}
