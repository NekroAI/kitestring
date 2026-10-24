package store

import (
	"context"
	"strings"
	"time"

	"github.com/NekroAI/kitestring/internal/blocks"
)

// 评审验证（R-232、R-237，D-714）：待验收的需求交给了评审（交验收时纸鸢自动派给在线评审，或用户 / 主调度手动交），
// 评审交意见前它不进用户的「要你处理」，需求上写「评审中」和谁在审；评审交了意见再进验收卡，卡上带评审结论。
// 评审不是固定关卡：没有交给评审的需求照旧；ReviewWaitLimit 内没动静（或评审会话已结束）就照常弹给用户，标「评审未完成」；
// 需求页上随时可以直接验收。不加项目开关，状态全部从记录推出来（审查任务、审查意见、状态变更），不另存。

// ReviewWaitLimit 是交给评审后最多等多久（30 分钟），过了照常弹给用户。测试里可以改短。
var ReviewWaitLimit = 30 * time.Minute

// 评审验证的三种状态。
const (
	ReviewReviewing = "reviewing" // 评审中：评审交意见前不进「要你处理」
	ReviewOverdue   = "overdue"   // 评审未完成：等满 ReviewWaitLimit 或评审会话已结束，照常弹给用户
	ReviewDone      = "done"      // 评审交了意见：进验收卡，带结论
)

// ReviewState 是一条待验收需求这一轮的评审验证情况；没交给评审、也没收到审查意见时为空。
type ReviewState struct {
	State        string     `json:"state" enum:"reviewing,overdue,done" doc:"reviewing 评审中（不进「要你处理」）/ overdue 评审未完成（照常弹给用户）/ done 评审已交意见"`
	SessionKey   string     `json:"session_key" doc:"在审的评审会话"`
	SessionTitle string     `json:"session_title,omitempty" doc:"评审会话的标题"`
	Auto         bool       `json:"auto,omitempty" doc:"交验收时纸鸢自动派给在线评审的（否则是手动交给评审）"`
	AskedAt      *time.Time `json:"asked_at,omitempty" doc:"交给评审的时间"`
	DueAt        *time.Time `json:"due_at,omitempty" doc:"等到什么时候：过了照常弹给用户，标「评审未完成」"`
	ReviewID     string     `json:"review_id,omitempty" doc:"done：评审交的那条意见"`
	ReviewedAt   *time.Time `json:"reviewed_at,omitempty"`
	Verdict      string     `json:"verdict,omitempty" doc:"done：评审的一句结论（没有时取意见开头）"`
	Pass         int        `json:"pass" doc:"done：验证清单通过的条数"`
	Fail         int        `json:"fail" doc:"done：不通过的条数"`
	Total        int        `json:"total" doc:"done：验证清单总条数（没交清单为 0）"`
}

// Holding：评审交意见前，这条需求不进用户的「要你处理」。
func (s *ReviewState) Holding() bool { return s != nil && s.State == ReviewReviewing }

// ReviewAuto 是自动派出的审查任务在 Decision.AssignedBy 上的标记（手动交给评审的为空）。
const ReviewAuto = "auto"

// reviewScore：审查意见的清单统计。
func reviewScore(p *blocks.Payload) (pass, fail, total int) {
	if c := p.Checklist(); c != nil {
		for _, it := range c.Items {
			total++
			switch it.Result {
			case blocks.Pass:
				pass++
			case blocks.Fail:
				fail++
			}
		}
	}
	return
}

// ReviewOf 推出 r 这一轮的评审验证情况。r 要带 StatusChanges、Feedback（含审查任务）、Reviews。
// 「这一轮」从最近一次进入待验收算起；之前的审查任务（例如进行中时交的代码审查）不算。
func (s *Store) ReviewOf(ctx context.Context, r Requirement, now time.Time) *ReviewState {
	if r.Status != ReqReview {
		return nil
	}
	var since time.Time
	for _, c := range r.StatusChanges { // 时间正序
		if c.To == ReqReview {
			since = c.At
		}
	}
	since = since.Add(-time.Second) // 自动派的任务紧跟在状态变更之后写，留一点余量
	var task *Decision
	for i := range r.Feedback { // 时间倒序：取最近的审查任务
		d := &r.Feedback[i]
		if d.Kind == KindReviewTask && !d.CreatedAt.Before(since) {
			task = d
			break
		}
	}
	// 评审交的意见：有审查任务时只认那位评审在任务之后交的；没有任务时这一轮里任何评审交的都算
	var rev *Evidence
	for i := len(r.Reviews) - 1; i >= 0; i-- {
		e := &r.Reviews[i]
		if task != nil && (e.SessionKey != task.SessionKey || e.CreatedAt.Before(task.CreatedAt)) {
			continue
		}
		if task == nil && e.CreatedAt.Before(since) {
			continue
		}
		rev = e
		break
	}
	if task == nil && rev == nil {
		return nil
	}
	out := &ReviewState{}
	if task != nil {
		at, due := task.CreatedAt, task.CreatedAt.Add(ReviewWaitLimit)
		out.SessionKey, out.Auto, out.AskedAt, out.DueAt = task.SessionKey, task.AssignedBy == ReviewAuto, &at, &due
	}
	if rev != nil {
		at := rev.CreatedAt
		out.State, out.ReviewID, out.ReviewedAt = ReviewDone, rev.ID, &at
		out.SessionKey = rev.SessionKey
		out.SessionTitle = rev.SessionTitle
		if rev.Payload != nil {
			out.Verdict = rev.Payload.Verdict
		}
		if out.Verdict == "" {
			out.Verdict = reviewLine(rev.Summary, 60)
		}
		out.Pass, out.Fail, out.Total = reviewScore(rev.Payload)
	} else {
		out.State = ReviewReviewing
		if now.After(*out.DueAt) || task.SessionState == StateEnded {
			out.State = ReviewOverdue
		}
	}
	if out.SessionTitle == "" {
		if x, err := s.GetSession(ctx, out.SessionKey); err == nil {
			out.SessionTitle = x.Title
		}
	}
	return out
}

// reviewLine：意见正文第一行非空文字（去掉 Markdown 标题 / 列表符号），最多 n 个字。
func reviewLine(s string, n int) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "#*- "))
		if line == "" {
			continue
		}
		if r := []rune(line); len(r) > n {
			return string(r[:n]) + "…"
		}
		return line
	}
	return ""
}

// fillReviewState 给列表里的待验收需求补上评审验证情况（列表不带证据和记录，这里按条补查）。
func (s *Store) fillReviewState(ctx context.Context, r *Requirement) error {
	if r.Status != ReqReview {
		return nil
	}
	full := *r
	var err error
	if full.StatusChanges, err = s.ListStatusChanges(ctx, StatusQuery{RequirementID: r.ID}); err != nil {
		return err
	}
	if full.Reviews, err = s.ListReviews(ctx, r.ID); err != nil {
		return err
	}
	if full.Feedback, err = s.queryDecs(ctx, decSelect+` WHERE x.requirement_id = ? AND x.kind = ? ORDER BY x.seq DESC`, r.ID, KindReviewTask); err != nil {
		return err
	}
	r.Review = s.ReviewOf(ctx, full, time.Now())
	return nil
}
