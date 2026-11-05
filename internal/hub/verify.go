package hub

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/NekroAI/kitestring/internal/blocks"
	"github.com/NekroAI/kitestring/internal/notify"
	"github.com/NekroAI/kitestring/internal/protocol"
	"github.com/NekroAI/kitestring/internal/roles"
	"github.com/NekroAI/kitestring/internal/store"
)

// 评审验证（R-232、R-237，D-679、D-714）：
//   - 需求交验收时，本项目有在线评审就自动派一条验证任务（审查任务，Context = auto），不用用户点「交给评审」；没有评审时照旧。
//   - 交给了评审的需求，评审交意见前不进用户的「要你处理」（store.ReviewState，推导得出，不是关卡、不加项目开关）；
//     评审交了意见进验收卡，卡上带结论；有不通过的，用「要动手」那一档通知主调度退回给开发修。
//   - 30 分钟（store.ReviewWaitLimit）没动静、或评审会话结束了，照常弹给用户，标「评审未完成」。需求页上随时可以直接验收。
//   - 评审只验证：不改代码、不改需求状态（hub 拒绝评审改状态、交验收证据）。
//   - 开发减负：有在线评审时，开发交验收前只保证编译、单测通过，附改动说明和一张主截图；没有时开发照旧全做（verifyNote）。

// autoReview：r 刚交验收，本项目有在线评审（不是交验收的会话自己）就自动派验证任务。返回派给的评审，没派时为空。
func (h *Hub) autoReview(ctx context.Context, r store.Requirement, by string) string {
	if r.ProjectID == "" {
		return ""
	}
	key := h.liveRole(ctx, r.ProjectID, roles.Reviewer)
	if key == "" || key == by {
		return ""
	}
	note := fmt.Sprintf("%s 刚交验收，纸鸢自动派给你验证（本项目有在线评审时交验收即自动派）。", r.Code)
	if by != "" {
		note = fmt.Sprintf("%s 刚由 %s 交验收，纸鸢自动派给你验证（本项目有在线评审时交验收即自动派）。", r.Code, h.actorName(ctx, by))
	}
	if _, err := h.askReview(ctx, r.ID, key, note, true); err != nil {
		h.log.Warn("自动派验证任务失败", "err", err, "req", r.Code, "to", key)
		return ""
	}
	return key
}

// reviewWaitNote：交验收通知里补一句「已交给评审，评审交意见前不进用户的要你处理」；没交给评审时为空。
func reviewWaitNote(r store.Requirement) string {
	if !r.Review.Holding() {
		return ""
	}
	how := "已交给"
	if r.Review.Auto {
		how = "已自动交给"
	}
	return fmt.Sprintf("%s评审「%s」验证，评审交意见前不进用户的「要你处理」（最多等 %d 分钟，过了照常弹给用户）；评审有不通过的会再通知。", how, reviewerName(r.Review), int(store.ReviewWaitLimit/time.Minute))
}

// onReviewSubmitted：评审交了这一轮的验证意见（r 是交完之后重新读的）。有不通过的，用「要动手」那一档告诉派活的主调度退回给开发修；
// 全部通过 / 没交清单时只告知主调度；订阅了交验收的（主助理写建议要参考）照常收到；先前压着没推的验收通知这时推给用户。
func (h *Hub) onReviewSubmitted(ctx context.Context, r store.Requirement, x store.Session, reviewID string) {
	st := r.Review
	if r.Status != store.ReqReview || st == nil || st.State != store.ReviewDone || st.ReviewID != reviewID {
		return
	}
	text := fmt.Sprintf("评审 %s 交了 %s「%s」的验证意见", sessionName(x), r.Code, brief(r.Title, 40))
	if st.Verdict != "" {
		text += "：" + st.Verdict
	}
	if st.Total > 0 {
		text += fmt.Sprintf("；通过 %d/%d", st.Pass, st.Total)
		if st.Fail > 0 {
			text += fmt.Sprintf("，不通过 %d 条（%s）", st.Fail, failItems(r, reviewID))
		}
	}
	text += "。"
	pm := h.assigner(ctx, r)
	if pm != "" && pm != x.Key {
		var err error
		if st.Fail > 0 {
			_, err = h.tell(ctx, pm, r.ProjectID, r.ID, text+"请退回给开发修：转派原来做的开发（req_assign，comment 里写上不通过的条目）或派子任务，改完重新交验收。需求已进用户的验收卡，用户也可能直接退回；细节用 req_get。")
		} else {
			_, err = h.tellQuiet(ctx, pm, r.ProjectID, r.ID, text+"需求已进用户的验收卡，不必动手。")
		}
		if err != nil {
			h.log.Warn("通知主调度评审结论失败", "err", err, "to", pm)
		}
	} else {
		pm = ""
	}
	h.broadcast(ctx, protocol.OnReview, r.ProjectID, r.ID, text+"需求已进用户的验收卡；写验收建议时参考评审结论（req_get 看清单）。", x.Key, pm)
	if st.AskedAt != nil { // 交给评审时压着没推的验收通知
		body := r.Title
		if st.Total > 0 {
			body = fmt.Sprintf("评审通过 %d/%d · %s", st.Pass, st.Total, r.Title)
		}
		h.sendNotify(r.ProjectID, notify.Message{Event: notify.EventRequirementReview, Title: "待验收 " + r.Code, Body: body, Group: "kitestring"}, "/requirements?r="+r.Code)
	}
}

// failItems：这条意见里不通过的条目（最多三条，每条截短）。
func failItems(r store.Requirement, reviewID string) string {
	for _, e := range r.Reviews {
		if e.ID != reviewID {
			continue
		}
		var out []string
		if c := e.Payload.Checklist(); c != nil {
			for _, it := range c.Items {
				if it.Result == blocks.Fail && len(out) < 3 {
					out = append(out, brief(it.Text, 30))
				}
			}
		}
		return strings.Join(out, "；")
	}
	return ""
}

// reviewWatch：每分钟看一遍交给评审还没交意见的待验收需求，等满 30 分钟（或评审会话结束了）就照常弹给用户：
// 刷新网页（需求回到「要你处理」，标「评审未完成」）、推送待验收，并只告知派活的主调度。每条每轮只报一次；
// 只报刚过期 10 分钟以内的，Hub 重启后不把早就过期的再推一遍。
func (h *Hub) reviewWatch(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			h.reviewOverdue(ctx, time.Now())
		}
	}
}

func (h *Hub) reviewOverdue(ctx context.Context, now time.Time) {
	reqs, err := h.store.ListRequirements(ctx, "", []string{store.ReqReview})
	if err != nil {
		return
	}
	changed := false
	for _, r := range reqs {
		st := r.Review
		if st == nil || st.State != store.ReviewOverdue || st.AskedAt == nil {
			continue
		}
		key := "reviewdue|" + r.ID + "|" + st.AskedAt.Format(time.RFC3339Nano)
		h.mu.Lock()
		_, seen := h.notified[key]
		if !seen {
			h.notified[key] = now
		}
		h.mu.Unlock()
		if seen || (st.DueAt != nil && now.Sub(*st.DueAt) > 10*time.Minute) {
			continue
		}
		changed = true
		why := fmt.Sprintf("%d 分钟没交意见", int(store.ReviewWaitLimit/time.Minute))
		if st.DueAt != nil && now.Before(*st.DueAt) {
			why = "会话已结束、没交意见"
		}
		if pm := h.assigner(ctx, r); pm != "" {
			if _, err := h.tellQuiet(ctx, pm, r.ProjectID, r.ID, fmt.Sprintf("评审「%s」%s，%s「%s」已照常弹给用户验收，标「评审未完成」。", reviewerName(st), why, r.Code, brief(r.Title, 40))); err != nil {
				h.log.Warn("通知主调度评审未完成失败", "err", err)
			}
		}
		h.sendNotify(r.ProjectID, notify.Message{Event: notify.EventRequirementReview, Title: "待验收 " + r.Code, Body: "评审未完成 · " + r.Title, Group: "kitestring"}, "/requirements?r="+r.Code)
	}
	if changed {
		h.broker.publish("requirements")
	}
}

// verifyNote：写给开发、主调度的「验证分工」（R-232）：本项目此刻有没有在线评审，决定开发交验收前要做到哪一步。
// 写进身份提示词（ks role、恢复 / 压缩后补发）和派活内容；其它身份、没有项目时为空。
func (h *Hub) verifyNote(ctx context.Context, projectID, role string) string {
	if projectID == "" || (role != roles.Dev && role != roles.PM) {
		return ""
	}
	rev := ""
	if key := h.liveRole(ctx, projectID, roles.Reviewer); key != "" {
		if x, err := h.store.GetSession(ctx, key); err == nil {
			rev = sessionName(x)
		}
	}
	if role == roles.PM {
		if rev != "" {
			return "[Kitestring] 验证分工：本项目现在有在线评审 " + rev + "，需求交验收后纸鸢自动派给它逐条实测。派活时交代开发 / 子任务：交验收前只保证编译、单测通过，证据附改动说明（verdict + checklist，按验收标准写自测到哪一步）和一张主截图（界面类）；逐条实测和完整验收截图由评审做。评审有不通过的会以团队消息通知你，按意见退回给开发修。评审下线后交验收的，回到开发全做。\n"
		}
		return "[Kitestring] 验证分工：本项目现在没有在线评审，派活时照旧交代开发 / 子任务全做：逐条实测、真实数据截图、checklist 逐条判定。\n"
	}
	if rev != "" {
		return "[Kitestring] 验证分工：本项目现在有在线评审 " + rev + "，交验收后纸鸢自动派给它逐条实测。你交验收前只保证编译、单测通过，证据附改动说明（verdict + checklist，按验收标准写自测到哪一步）和一张主截图（界面类）；逐条实测和完整验收截图由评审做。\n"
	}
	return "[Kitestring] 验证分工：本项目现在没有在线评审，交验收前照旧全做：逐条实测、真实数据截图、checklist 逐条判定。\n"
}

// roleNotes：身份提示词末尾跟着的项目相关提示——子任务模型策略（R-228）和验证分工（R-232）。
func (h *Hub) roleNotes(ctx context.Context, key string) string {
	x, err := h.store.GetSession(ctx, key)
	if err != nil {
		return ""
	}
	return h.projectPolicyNote(ctx, x.ProjectID, x.Role) + h.verifyNote(ctx, x.ProjectID, x.Role)
}

// isReviewer：key 是评审会话。评审只审查和验证，不改需求状态、不交验收证据（R-232）。
func (h *Hub) isReviewer(ctx context.Context, key string) bool {
	if key == "" {
		return false
	}
	x, err := h.store.GetSession(ctx, key)
	return err == nil && x.Role == roles.Reviewer
}

// reviewerName：消息里称呼在审的评审：会话标题，没有时用会话键。
func reviewerName(st *store.ReviewState) string {
	if t := strings.TrimSpace(st.SessionTitle); t != "" {
		return brief(t, 30)
	}
	return st.SessionKey
}
