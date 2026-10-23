package store

import (
	"context"
	"database/sql"

	"github.com/NekroAI/kitestring/internal/blocks"
	"github.com/NekroAI/kitestring/internal/protocol"
)

// 修改记录（R-134）：Agent 改需求内容时留下的痕迹。存为 kind = edit 的决策（状态直接是已关闭，不送达任何会话），
// 进需求时间线：assigned_by 是改的会话，context 是它当时的身份，answer 是一句说明（改了哪些、理由、凭哪条批准），
// blocks 里放一个 table 块：列为「字段 / 改前 / 改后」，每改一个字段一行。

// FieldChange 是一个字段的改前改后。
type FieldChange struct {
	Field  string // title / detail / acceptance / priority / kind
	Label  string // 中文名：标题、描述……
	Before string
	After  string
}

// CreateEdit 记一次修改。
func (s *Store) CreateEdit(ctx context.Context, r Requirement, by, role, note string, changes []FieldChange) (Decision, error) {
	rows := make([][]string, 0, len(changes))
	for _, c := range changes {
		rows = append(rows, []string{c.Field, c.Label, c.Before, c.After})
	}
	table := []blocks.Block{{ID: "edit", Type: blocks.Table, Title: "修改", Columns: []string{"field", "字段", "改前", "改后"}, Rows: rows}}
	id := protocol.NewID()
	err := s.tx(ctx, func(t *sql.Tx) error {
		var seq int
		if err := t.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) + 1 FROM decisions`).Scan(&seq); err != nil {
			return err
		}
		n := now()
		_, err := t.ExecContext(ctx, `INSERT INTO decisions(id, seq, session_key, origin_session, project_id, requirement_id, question, context, blocks, options, recommendation, kind, status, answer, answer_files, answered_at, created_at, assigned_by) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, '[]', '', ?, ?, ?, '[]', ?, ?, ?)`,
			id, seq, nullStr(by), nullStr(by), nullStr(r.ProjectID), r.ID, r.Title, role, blocks.Encode(table), KindEdit, DecClosed, note, n, n, by)
		return err
	})
	if err != nil {
		return Decision{}, err
	}
	return s.GetDecision(ctx, id)
}
