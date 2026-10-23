package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/NekroAI/kitestring/internal/blocks"
)

// 参谋早报（R-165）：Adv 只写两样——给你的一段总述（每个项目一份）和逐条验收建议（每条需求一份），都是覆盖写入；
// 早报里其余内容由 hub 从已有记录汇总（代答、放行、交了证据的需求、团队消息），见 hub/morning.go。

// 验收建议。
const (
	AdvicePass   = "pass"
	AdviceReturn = "return"
)

// MorningNote 是参谋写给你的早报总述。
type MorningNote struct {
	Text string    `json:"text"`
	By   string    `json:"by" doc:"写总述的 Adv 会话"`
	At   time.Time `json:"at"`
}

// Advice 是参谋对一条待验收需求的建议。
type Advice struct {
	RequirementID   string    `json:"requirement_id"`
	RequirementCode string    `json:"requirement_code"`
	ProjectID       string    `json:"project_id"`
	Verdict         string    `json:"verdict" enum:"pass,return" doc:"建议通过 / 建议退回"`
	Reason          string    `json:"reason"`
	By              string    `json:"by" doc:"写建议的 Adv 会话"`
	At              time.Time `json:"at"`
}

// SetMorning 写入 / 清除（text 为空）项目的早报总述。
func (s *Store) SetMorning(ctx context.Context, projectID, text, by string) error {
	if text == "" {
		_, err := s.db.ExecContext(ctx, `DELETE FROM morning_notes WHERE project_id = ?`, projectID)
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO morning_notes(project_id, text, by_session, at) VALUES(?, ?, ?, ?)
		ON CONFLICT(project_id) DO UPDATE SET text = excluded.text, by_session = excluded.by_session, at = excluded.at`, projectID, text, by, now())
	return err
}

// GetMorning 读取项目的早报总述；没有时返回 ErrNotFound。
func (s *Store) GetMorning(ctx context.Context, projectID string) (MorningNote, error) {
	var n MorningNote
	var at string
	err := s.db.QueryRowContext(ctx, `SELECT text, by_session, at FROM morning_notes WHERE project_id = ?`, projectID).Scan(&n.Text, &n.By, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return n, ErrNotFound
	}
	n.At = parseTS(at)
	return n, err
}

// SetAdvice 写入 / 清除（verdict 为空）一条需求的验收建议。
func (s *Store) SetAdvice(ctx context.Context, reqID, verdict, reason, by string) error {
	if verdict == "" {
		_, err := s.db.ExecContext(ctx, `DELETE FROM review_advice WHERE requirement_id = ?`, reqID)
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO review_advice(requirement_id, verdict, reason, by_session, at) VALUES(?, ?, ?, ?, ?)
		ON CONFLICT(requirement_id) DO UPDATE SET verdict = excluded.verdict, reason = excluded.reason, by_session = excluded.by_session, at = excluded.at`,
		reqID, verdict, strings.TrimSpace(reason), by, now())
	return err
}

// ReviewAdvice 列出待验收需求上的验收建议（只算现在还是待验收的需求；建议写在最近一次交证据之前的视为过时，不返回）。
// projectID 为空表示全部。
func (s *Store) ReviewAdvice(ctx context.Context, projectID string) ([]Advice, error) {
	q := `SELECT a.requirement_id, r.seq, COALESCE(r.project_id, ''), a.verdict, a.reason, a.by_session, a.at FROM review_advice a
		JOIN requirements r ON r.id = a.requirement_id
		WHERE r.status = ? AND a.at >= COALESCE((SELECT MAX(e.created_at) FROM evidence e WHERE e.requirement_id = r.id AND e.kind <> ? AND e.voided = 0), '')`
	args := []any{ReqReview, EvidenceReview}
	if projectID != "" {
		q += ` AND r.project_id = ?`
		args = append(args, projectID)
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY r.seq`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Advice{}
	for rows.Next() {
		var a Advice
		var seq int
		var at string
		if err := rows.Scan(&a.RequirementID, &seq, &a.ProjectID, &a.Verdict, &a.Reason, &a.By, &at); err != nil {
			return nil, err
		}
		a.RequirementCode, a.At = reqCode(seq), parseTS(at)
		out = append(out, a)
	}
	return out, rows.Err()
}

// ProjectDecisionsSince 列出项目里 since 之后创建的某类决策（新的在前）。
func (s *Store) ProjectDecisionsSince(ctx context.Context, projectID, kind string, since time.Time, limit int) ([]Decision, error) {
	return s.queryDecs(ctx, decSelect+` WHERE x.project_id = ? AND x.kind = ? AND x.created_at >= ? ORDER BY x.seq DESC LIMIT ?`, projectID, kind, ts(since), limit)
}

// ProxiedSince 列出项目里 since 之后参谋代答的决策（新的在前）。
func (s *Store) ProxiedSince(ctx context.Context, projectID string, since time.Time) ([]Decision, error) {
	return s.queryDecs(ctx, decSelect+` WHERE x.project_id = ? AND x.proxy_by <> '' AND x.proxy_at >= ? ORDER BY x.proxy_at DESC LIMIT 100`, projectID, ts(since))
}

// PendingDecisions 列出项目里还在等你答复的决策（kind = decision）。
func (s *Store) PendingDecisions(ctx context.Context, projectID string) ([]Decision, error) {
	return s.queryDecs(ctx, decSelect+` WHERE x.project_id = ? AND x.kind = ? AND x.status = ? ORDER BY x.seq`, projectID, KindDecision, DecPending)
}

// Delivery 是一条需求在某段时间里交的证据（结论或摘要、提交链接）。
type Delivery struct {
	RequirementID string    `json:"requirement_id"`
	Kind          string    `json:"kind"`
	Summary       string    `json:"summary"`
	Verdict       string    `json:"verdict,omitempty"`
	Link          string    `json:"link,omitempty"`
	At            time.Time `json:"at"`
}

// EvidenceSince 列出项目里 since 之后交的证据（不含审查意见、作废的），按时间先后。
func (s *Store) EvidenceSince(ctx context.Context, projectID string, since time.Time) ([]Delivery, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT e.requirement_id, e.kind, e.summary, e.link, e.payload, e.created_at FROM evidence e
		JOIN requirements r ON r.id = e.requirement_id
		WHERE r.project_id = ? AND e.kind <> ? AND e.voided = 0 AND e.created_at >= ? ORDER BY e.created_at`, projectID, EvidenceReview, ts(since))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Delivery{}
	for rows.Next() {
		var d Delivery
		var payload, at string
		if err := rows.Scan(&d.RequirementID, &d.Kind, &d.Summary, &d.Link, &payload, &at); err != nil {
			return nil, err
		}
		if p := blocks.Decode[blocks.Payload](payload); p != nil {
			d.Verdict = p.Verdict
		}
		d.At = parseTS(at)
		out = append(out, d)
	}
	return out, rows.Err()
}
