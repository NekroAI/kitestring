package store

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/NekroAI/kitestring/internal/blocks"
)

// 最近动态（R-296）：工作台「最近动态」和项目页最近动态共用的时间线。直接从各类记录（证据、审查意见、状态变更、
// 决策与留言、阶段验收）按时间取最近 n 条，不靠需求列表拼；每条写明主语（你 / 会话 / 纸鸢）、动作和对象编号，
// 网页按这些字段组句。每类记录各取最近 n 条再合并，查询走时间索引，工作台常开、实时刷新也只是几次带 LIMIT 的小查询。

// 动态的种类。
const (
	FeedEvidence   = "evidence"    // 会话提交了证据
	FeedReview     = "review"      // 评审交了审查意见：Text 是一句结论
	FeedSubmit     = "submit"      // 需求设为待验收（会话提交验收）
	FeedBlocked    = "blocked"     // 设为受阻：Ref 是在等的 R-x / D-x，Text 是原因
	FeedUnblock    = "unblock"     // 纸鸢自动解除受阻：Ref 是等的 R-x / D-x
	FeedShelved    = "shelved"     // 搁置：Text 是原因
	FeedDone       = "done"        // 你验收通过
	FeedRaise      = "raise"       // 会话提了决策（Approval 为真是请求批准）
	FeedAnswer     = "answer"      // 决策有了结果：Verdict 是 answer / approved / rejected / proxy / timeout / voided
	FeedAssign     = "assign"      // 指派：Target* 是接手的会话
	FeedNote       = "note"        // 你留言：Text 是正文
	FeedComment    = "comment"     // 会话留言：Text 是正文
	FeedRevise     = "revise"      // 你退回：Text 是意见
	FeedReviewTask = "review_task" // 提交评审：Target* 是评审会话
	FeedRelease    = "release"     // 放行草稿
	FeedStage      = "stage"       // 阶段验收通过：StageSeq / StageTitle，StageWith 非 0 表示随阶段 n 一起通过
)

// Activity 是最近动态里的一条。
type Activity struct {
	At           time.Time `json:"at"`
	Kind         string    `json:"kind" enum:"evidence,review,submit,blocked,unblock,shelved,done,raise,answer,assign,note,comment,revise,review_task,release,stage" doc:"动作：evidence 提交证据 / review 交审查意见 / submit 提交验收 / blocked 设为受阻 / unblock 纸鸢自动解除受阻 / shelved 搁置 / done 验收通过 / raise 提决策 / answer 决策有了结果 / assign 指派 / note 你留言 / comment 会话留言 / revise 退回 / review_task 提交评审 / release 放行 / stage 阶段验收通过"`
	Actor        string    `json:"actor" enum:"user,agent,system" doc:"主语：user 你 / agent 会话 / system 纸鸢"`
	Session      string    `json:"session,omitempty" doc:"会话键（主语是会话时）"`
	Role         string    `json:"role,omitempty" doc:"会话当时的身份：aide / advisor / reviewer / pm / dev；没记时为空"`
	SessionTitle string    `json:"session_title,omitempty" doc:"会话标题（当时的快照，没有时取现在的）"`
	ProjectID    string    `json:"project_id,omitempty"`
	Code         string    `json:"code" doc:"对象编号：R-x / D-x / P-x"`
	ReqCode      string    `json:"req_code,omitempty" doc:"所属需求编号（对象是决策时），点开用"`
	Evidence     bool      `json:"evidence,omitempty" doc:"submit：同时提交了证据（交证据后 2 分钟内提交验收，合成一条）"`
	Approval     bool      `json:"approval,omitempty" doc:"raise / answer：批准类决策或请求"`
	Verdict      string    `json:"verdict,omitempty" enum:",answer,approved,rejected,proxy,timeout,voided" doc:"answer：answer 你回答了 / approved 你批准了 / rejected 你拒绝了 / proxy 主助理代答 / timeout 到期视为拒绝 / voided 申请作废"`
	Ref          string    `json:"ref,omitempty" doc:"blocked / unblock：在等的 R-x / D-x"`
	Text         string    `json:"text,omitempty" doc:"内容：留言正文、退回意见、受阻 / 搁置原因、审查结论（最多 120 字）"`
	TargetRole   string    `json:"target_role,omitempty" doc:"assign / review_task：接手会话的身份"`
	TargetTitle  string    `json:"target_title,omitempty" doc:"assign / review_task：接手会话的标题"`
	StageSeq     int       `json:"stage_seq,omitempty"`
	StageTitle   string    `json:"stage_title,omitempty"`
	StageWith    int       `json:"stage_with,omitempty" doc:"stage：随第几个阶段一起自动通过；0 = 你通过的"`
}

// ActivityQuery 是最近动态的查询条件。
type ActivityQuery struct {
	ProjectID string // 空 = 全部项目
	Mine      bool   // 只看你做的
	Limit     int    // 默认 20，最多 200
}

// actText：内容最多 120 字，空白压成一个空格。
func actText(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 120 {
		return string(r[:120]) + "…"
	}
	return s
}

// ListActivity 取最近动态，新的在前。
func (s *Store) ListActivity(ctx context.Context, q ActivityQuery) ([]Activity, error) {
	n := q.Limit
	if n <= 0 {
		n = 20
	}
	if n > 200 {
		n = 200
	}
	out := []Activity{}
	for _, f := range []func(context.Context, ActivityQuery, int) ([]Activity, error){s.actEvidence, s.actStatus, s.actRecords, s.actAnswers, s.actStages} {
		xs, err := f(ctx, q, 2*n) // 交证据和提交验收要合并，多取一些
		if err != nil {
			return nil, err
		}
		out = append(out, xs...)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	out = mergeSubmit(out)
	if len(out) > n {
		out = out[:n]
	}
	return out, nil
}

// mergeSubmit：同一个会话为同一条需求交证据后 2 分钟内提交验收，合成一条「提交了证据并提交验收」（Evidence 为真）。
func mergeSubmit(xs []Activity) []Activity {
	drop := map[int]bool{}
	for i := range xs {
		if xs[i].Kind != FeedSubmit || xs[i].Session == "" {
			continue
		}
		for j := i + 1; j < len(xs) && xs[i].At.Sub(xs[j].At) <= 2*time.Minute; j++ { // 时间倒序：j 更早
			if e := xs[j]; e.Kind == FeedEvidence && !drop[j] && e.Session == xs[i].Session && e.Code == xs[i].Code {
				xs[i].Evidence, drop[j] = true, true
				break
			}
		}
	}
	out := xs[:0]
	for i, a := range xs {
		if !drop[i] {
			out = append(out, a)
		}
	}
	return out
}

// actEvidence：证据与审查意见，都是会话交的（你只看自己时没有）。
func (s *Store) actEvidence(ctx context.Context, q ActivityQuery, n int) ([]Activity, error) {
	if q.Mine {
		return nil, nil
	}
	sqlq := `SELECT e.kind, COALESCE(e.session_key, ''), e.role, e.session_title, COALESCE(x.role, ''), COALESCE(x.title, ''), e.summary, e.payload, r.seq, COALESCE(r.project_id, ''), e.created_at
		FROM evidence e JOIN requirements r ON r.id = e.requirement_id LEFT JOIN sessions x ON x.key = e.session_key`
	var args []any
	if q.ProjectID != "" {
		sqlq += ` WHERE r.project_id = ?`
		args = append(args, q.ProjectID)
	}
	sqlq += ` ORDER BY e.created_at DESC LIMIT ?`
	rows, err := s.db.QueryContext(ctx, sqlq, append(args, n)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Activity
	for rows.Next() {
		var a Activity
		var kind, role, title, nowRole, nowTitle, summary, payload, at string
		var seq int
		if err := rows.Scan(&kind, &a.Session, &role, &title, &nowRole, &nowTitle, &summary, &payload, &seq, &a.ProjectID, &at); err != nil {
			return nil, err
		}
		a.At, a.Code, a.Kind = parseTS(at), reqCode(seq), FeedEvidence
		a.Actor, a.Role, a.SessionTitle = ActorAgent, or(role, nowRole), or(title, nowTitle)
		if kind == EvidenceReview {
			a.Kind = FeedReview
			if a.Role == "" {
				a.Role = "reviewer"
			}
			if p := blocks.Decode[blocks.Payload](payload); p != nil && p.Verdict != "" {
				a.Text = actText(p.Verdict)
			} else {
				a.Text = reviewLine(summary, 120)
			}
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func or(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// actStatus：状态变更里要进动态的几种——提交验收、设为受阻、自动解除受阻、搁置、验收通过。
// 退回修改另有退回意见记录（带意见原文），这里不重复；关联会话开工、放行等日常流转不进动态。
func (s *Store) actStatus(ctx context.Context, q ActivityQuery, n int) ([]Activity, error) {
	sqlq := `SELECT c.to_status, c.from_status, c.actor, c.session_key, c.role, c.session_title, COALESCE(x.role, ''), COALESCE(x.title, ''), c.via, c.ref, c.reason, r.seq, COALESCE(r.project_id, ''), c.at
		FROM status_changes c JOIN requirements r ON r.id = c.requirement_id LEFT JOIN sessions x ON x.key = c.session_key
		WHERE (c.to_status IN (?, ?, ?) OR (c.to_status = ? AND c.actor = ?) OR c.via = 'unblock')`
	args := []any{ReqReview, ReqBlocked, ReqShelved, ReqDone, ActorUser}
	if q.ProjectID != "" {
		sqlq += ` AND r.project_id = ?`
		args = append(args, q.ProjectID)
	}
	if q.Mine {
		sqlq += ` AND c.actor = ?`
		args = append(args, ActorUser)
	}
	sqlq += ` ORDER BY c.at DESC LIMIT ?`
	rows, err := s.db.QueryContext(ctx, sqlq, append(args, n)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Activity
	for rows.Next() {
		var a Activity
		var to, from, role, title, nowRole, nowTitle, via, ref, reason, at string
		var seq int
		if err := rows.Scan(&to, &from, &a.Actor, &a.Session, &role, &title, &nowRole, &nowTitle, &via, &ref, &reason, &seq, &a.ProjectID, &at); err != nil {
			return nil, err
		}
		a.At, a.Code = parseTS(at), reqCode(seq)
		if a.Actor == ActorAgent {
			a.Role, a.SessionTitle = or(role, nowRole), or(title, nowTitle)
		} else {
			a.Session = ""
		}
		switch {
		case via == "unblock":
			a.Kind, a.Actor, a.Session, a.Role, a.SessionTitle, a.Ref = FeedUnblock, ActorSystem, "", "", "", ref
		case to == ReqReview:
			a.Kind = FeedSubmit
		case to == ReqBlocked:
			a.Kind, a.Ref, a.Text = FeedBlocked, ref, actText(reason)
		case to == ReqShelved:
			a.Kind, a.Text = FeedShelved, actText(reason)
		case to == ReqDone:
			a.Kind = FeedDone
		default:
			continue
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// actKinds：决策表里按写入时间进动态的记录。
var actKinds = []string{KindDecision, KindAssign, KindNote, KindComment, KindFeedback, KindReviewTask, KindRelease}

const actDecFrom = ` FROM decisions d LEFT JOIN requirements r ON r.id = d.requirement_id`

// actDecFilter：项目过滤（决策自己的项目，或所属需求的项目）。
func actDecFilter(q ActivityQuery, sqlq string, args []any) (string, []any) {
	if q.ProjectID != "" {
		sqlq += ` AND (d.project_id = ? OR r.project_id = ?)`
		args = append(args, q.ProjectID, q.ProjectID)
	}
	return sqlq, args
}

// actRecords：提决策、指派、留言、退回、提交评审、放行。
func (s *Store) actRecords(ctx context.Context, q ActivityQuery, n int) ([]Activity, error) {
	kinds := actKinds
	if q.Mine { // 你做的：留言、退回、你在网页上指派 / 提交评审
		kinds = []string{KindAssign, KindNote, KindFeedback, KindReviewTask}
	}
	sqlq := `SELECT d.kind, d.seq, COALESCE(r.seq, 0), COALESCE(d.project_id, r.project_id, ''), COALESCE(d.session_key, ''), COALESCE(d.origin_session, ''), d.assigned_by, d.from_role, d.context, d.by_title, d.approval, d.answer, d.created_at,
		COALESCE((SELECT role FROM sessions WHERE key = d.assigned_by), ''), COALESCE((SELECT title FROM sessions WHERE key = d.assigned_by), ''),
		COALESCE((SELECT role FROM sessions WHERE key = COALESCE(NULLIF(d.origin_session, ''), d.session_key)), ''), COALESCE((SELECT title FROM sessions WHERE key = COALESCE(NULLIF(d.origin_session, ''), d.session_key)), ''),
		COALESCE((SELECT role FROM sessions WHERE key = d.session_key), ''), COALESCE((SELECT title FROM sessions WHERE key = d.session_key), '')` +
		actDecFrom + ` WHERE d.kind IN (?` + strings.Repeat(", ?", len(kinds)-1) + `)`
	var args []any
	for _, k := range kinds {
		args = append(args, k)
	}
	if q.Mine {
		sqlq += ` AND (d.kind IN ('note', 'feedback') OR d.assigned_by = '')`
	}
	sqlq, args = actDecFilter(q, sqlq, args)
	sqlq += ` ORDER BY d.created_at DESC LIMIT ?`
	rows, err := s.db.QueryContext(ctx, sqlq, append(args, n)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Activity
	for rows.Next() {
		var a Activity
		var kind, target, origin, by, fromRole, ctxRole, byTitle, answer, at, byRole, byNowTitle, askRole, askTitle, tRole, tTitle string
		var seq, rseq int
		if err := rows.Scan(&kind, &seq, &rseq, &a.ProjectID, &target, &origin, &by, &fromRole, &ctxRole, &byTitle, &a.Approval, &answer, &at, &byRole, &byNowTitle, &askRole, &askTitle, &tRole, &tTitle); err != nil {
			return nil, err
		}
		a.At, a.Code = parseTS(at), reqCode(rseq)
		if rseq == 0 && kind != KindDecision {
			continue // 需求已删除
		}
		byAgent := func(role string) {
			if by == "" {
				a.Actor = ActorUser
				return
			}
			a.Actor, a.Session, a.Role, a.SessionTitle = ActorAgent, by, or(role, byRole), or(byTitle, byNowTitle)
		}
		switch kind {
		case KindDecision:
			a.Kind, a.Code = FeedRaise, decCode(seq)
			if rseq > 0 {
				a.ReqCode = reqCode(rseq)
			}
			a.Actor, a.Session, a.Role, a.SessionTitle = ActorAgent, or(origin, target), or(fromRole, askRole), or(byTitle, askTitle)
		case KindAssign:
			a.Kind = FeedAssign
			byAgent("")
		case KindReviewTask:
			a.Kind = FeedReviewTask
			if by == ReviewAuto {
				a.Actor = ActorSystem
			} else {
				byAgent("")
			}
		case KindRelease:
			a.Kind = FeedRelease
			byAgent(ctxRole)
		case KindComment:
			a.Kind, a.Text = FeedComment, actText(answer)
			byAgent(fromRole)
		case KindNote:
			a.Kind, a.Actor, a.Text = FeedNote, ActorUser, actText(answer)
		case KindFeedback:
			a.Kind, a.Actor, a.Text = FeedRevise, ActorUser, actText(answer)
		}
		if kind == KindAssign || kind == KindReviewTask {
			a.TargetRole, a.TargetTitle = tRole, tTitle
			if kind == KindReviewTask && a.TargetRole == "" {
				a.TargetRole = "reviewer"
			}
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// actAnswers：决策有了结果——你回答 / 批准 / 拒绝、主助理代答、到期视为拒绝、申请作废。
func (s *Store) actAnswers(ctx context.Context, q ActivityQuery, n int) ([]Activity, error) {
	sqlq := `SELECT d.seq, COALESCE(r.seq, 0), COALESCE(d.project_id, r.project_id, ''), d.approval, d.verdict, d.proxy_by, d.overridden_at, d.timed_out, d.request_voided, d.answered_at,
		COALESCE((SELECT title FROM sessions WHERE key = d.proxy_by), '')` +
		actDecFrom + ` WHERE d.kind = ? AND d.answered_at IS NOT NULL AND d.answered_at <> ''`
	args := []any{KindDecision}
	if q.Mine {
		sqlq += ` AND d.timed_out = 0 AND d.request_voided = 0 AND (d.proxy_by = '' OR d.overridden_at <> '')`
	}
	sqlq, args = actDecFilter(q, sqlq, args)
	sqlq += ` ORDER BY d.answered_at DESC LIMIT ?`
	rows, err := s.db.QueryContext(ctx, sqlq, append(args, n)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Activity
	for rows.Next() {
		var a Activity
		var seq, rseq int
		var verdict, proxyBy, overAt, at, proxyTitle string
		var timedOut, voided bool
		if err := rows.Scan(&seq, &rseq, &a.ProjectID, &a.Approval, &verdict, &proxyBy, &overAt, &timedOut, &voided, &at, &proxyTitle); err != nil {
			return nil, err
		}
		a.At, a.Kind, a.Code, a.Actor = parseTS(at), FeedAnswer, decCode(seq), ActorUser
		if rseq > 0 {
			a.ReqCode = reqCode(rseq)
		}
		switch {
		case timedOut:
			a.Actor, a.Verdict = ActorSystem, "timeout"
		case voided:
			a.Actor, a.Verdict = ActorSystem, "voided"
		case proxyBy != "" && overAt == "":
			a.Actor, a.Session, a.Role, a.SessionTitle, a.Verdict = ActorAgent, proxyBy, "aide", proxyTitle, "proxy"
		case !a.Approval:
			a.Verdict = "answer"
		case verdict == "rejected":
			a.Verdict = "rejected"
		default:
			a.Verdict = "approved"
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// actStages：阶段验收通过。你通过一个阶段时，后面已逐条验收完的阶段会在 2 秒内一并自动通过（D-60），
// 那几条写「随阶段 n 一起通过」，不算你通过的（R-211）。
func (s *Store) actStages(ctx context.Context, q ActivityQuery, n int) ([]Activity, error) {
	sqlq := `SELECT ps.plan_id, p.seq, COALESCE(p.project_id, ''), ps.seq, ps.title, ps.passed_at FROM plan_stages ps JOIN plans p ON p.id = ps.plan_id WHERE ps.passed_at <> ''`
	var args []any
	if q.ProjectID != "" {
		sqlq += ` AND p.project_id = ?`
		args = append(args, q.ProjectID)
	}
	sqlq += ` ORDER BY ps.passed_at DESC LIMIT ?`
	rows, err := s.db.QueryContext(ctx, sqlq, append(args, n)...)
	if err != nil {
		return nil, err
	}
	var out []Activity
	plans := map[string]bool{}
	var ids []string
	for rows.Next() {
		var a Activity
		var plan, at string
		var pseq int
		if err := rows.Scan(&plan, &pseq, &a.ProjectID, &a.StageSeq, &a.StageTitle, &at); err != nil {
			rows.Close()
			return nil, err
		}
		a.At, a.Kind, a.Code, a.Actor = parseTS(at), FeedStage, planCode(pseq), ActorUser
		out = append(out, a)
		if !plans[plan] {
			plans[plan] = true
			ids = append(ids, plan)
		}
		a.ReqCode = plan // 暂存计划 ID，下面算完「随哪个阶段通过」再清掉
		out[len(out)-1] = a
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	with := map[string]map[int]int{}
	for _, id := range ids {
		m, err := s.stagesPassedWith(ctx, id)
		if err != nil {
			return nil, err
		}
		with[id] = m
	}
	keep := out[:0]
	for _, a := range out {
		a.StageWith = with[a.ReqCode][a.StageSeq]
		a.ReqCode = ""
		if a.StageWith > 0 {
			a.Actor = ActorSystem
		}
		if q.Mine && a.StageWith > 0 {
			continue
		}
		keep = append(keep, a)
	}
	return keep, nil
}

// stagesPassedWith：计划里每个已通过的阶段是随哪个阶段一起通过的（相邻阶段通过时间相差 2 秒内算一起）；自己通过的为 0。
func (s *Store) stagesPassedWith(ctx context.Context, planID string) (map[int]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT seq, passed_at FROM plan_stages WHERE plan_id = ? ORDER BY seq`, planID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int]int{}
	by := 0
	var prev time.Time
	for rows.Next() {
		var seq int
		var passed string
		if err := rows.Scan(&seq, &passed); err != nil {
			return nil, err
		}
		var at time.Time
		if passed != "" {
			at = parseTS(passed)
		}
		if at.IsZero() || prev.IsZero() || absDur(at.Sub(prev)) >= 2*time.Second {
			by = seq
		}
		prev = at
		if !at.IsZero() && by != seq {
			out[seq] = by
		}
	}
	return out, rows.Err()
}

func absDur(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}
