package store

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
	"time"

	"github.com/NekroAI/kitestring/internal/protocol"
)

// 放行（R-166）：PM / Adv 把草稿改为待开始时留下的记录。存为 kind = release 的决策（状态直接是已关闭，不送达任何会话），
// 进需求时间线：assigned_by 是放行的会话，context 是它当时的身份，answer 是理由；撤回时写 close_reason。

// Release 是需求上最近一次没撤回的放行。
type Release struct {
	Code   string    `json:"code" doc:"放行记录的编号（D-x）"`
	By     string    `json:"by" doc:"放行的会话键"`
	Role   string    `json:"role" doc:"放行时的身份：pm / aide（R-190 前的旧记录可能是 advisor）"`
	Reason string    `json:"reason"`
	At     time.Time `json:"at"`
}

func parseRelease(s string) *Release {
	if s == "" {
		return nil
	}
	p := strings.SplitN(s, "\x1f", 5)
	if len(p) != 5 {
		return nil
	}
	seq, _ := strconv.Atoi(p[0])
	return &Release{Code: decCode(seq), By: p[1], Role: p[2], Reason: p[3], At: parseTS(p[4])}
}

// CreateRelease 记一次放行。
func (s *Store) CreateRelease(ctx context.Context, r Requirement, by, role, reason string) (Decision, error) {
	id := protocol.NewID()
	err := s.tx(ctx, func(t *sql.Tx) error {
		var seq int
		if err := t.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) + 1 FROM decisions`).Scan(&seq); err != nil {
			return err
		}
		n := now()
		_, err := t.ExecContext(ctx, `INSERT INTO decisions(id, seq, session_key, origin_session, project_id, requirement_id, question, context, options, recommendation, kind, status, answer, answer_files, answered_at, created_at, assigned_by) VALUES(?, ?, ?, ?, ?, ?, ?, ?, '[]', '', ?, ?, ?, '[]', ?, ?, ?)`,
			id, seq, nullStr(by), nullStr(by), nullStr(r.ProjectID), r.ID, r.Title, role, KindRelease, DecClosed, reason, n, n, by)
		if err != nil {
			return err
		}
		return snapDecision(ctx, t, id, by)
	})
	if err != nil {
		return Decision{}, err
	}
	return s.GetDecision(ctx, id)
}

// WithdrawRelease 撤回需求上没撤回的放行，写下原因；没有可撤回的放行时返回 false。
func (s *Store) WithdrawRelease(ctx context.Context, reqID, reason string) (bool, error) {
	if strings.TrimSpace(reason) == "" {
		reason = "已撤回"
	}
	res, err := s.db.ExecContext(ctx, `UPDATE decisions SET close_reason = ? WHERE requirement_id = ? AND kind = ? AND close_reason = ''`, reason, reqID, KindRelease)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}
