package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/NekroAI/kitestring/internal/blocks"
	"github.com/NekroAI/kitestring/internal/protocol"
)

// 会话留言（R-249）：会话在需求下写清细节，消息只做一句话通知。存为 kind = comment 的决策（状态直接是已关闭，
// 留言本身不送达任何会话），和用户留言（kind = note）进同一条时间线、署名区分：assigned_by 是留言的会话，
// from_role 是它当时的身份，from_agent 是发留言的子任务；answer 是正文，answer_files 是附图，blocks 是表达块。

// Comment 是一条会话留言的内容。
type Comment struct {
	By     string // 留言的会话
	Role   string // 它当时的身份
	Agent  string // 子任务的 agent_id，会话主线程为空
	Text   string
	Files  []FileRef
	Blocks []blocks.Block
}

// CreateComment 在需求 r 下记一条会话留言。
func (s *Store) CreateComment(ctx context.Context, r Requirement, c Comment) (Decision, error) {
	c.Text = strings.TrimSpace(c.Text)
	if c.Text == "" && len(c.Files) == 0 && len(c.Blocks) == 0 {
		return Decision{}, errors.New("留言不能为空")
	}
	if c.By == "" {
		return Decision{}, errors.New("留言要有署名的会话")
	}
	id := protocol.NewID()
	err := s.tx(ctx, func(t *sql.Tx) error {
		var seq int
		if err := t.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) + 1 FROM decisions`).Scan(&seq); err != nil {
			return err
		}
		n := now()
		_, err := t.ExecContext(ctx, `INSERT INTO decisions(id, seq, session_key, origin_session, project_id, requirement_id, question, context, blocks, options, recommendation, kind, status, answer, answer_files, answered_at, created_at, assigned_by, from_role, from_agent) VALUES(?, ?, ?, ?, ?, ?, ?, '', ?, '[]', '', ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, seq, nullStr(c.By), nullStr(c.By), nullStr(r.ProjectID), r.ID, r.Title, blocks.Encode(c.Blocks), KindComment, DecClosed, c.Text, filesJSON(c.Files), n, n, c.By, c.Role, c.Agent)
		if err != nil {
			return err
		}
		return snapDecision(ctx, t, id, c.By)
	})
	if err != nil {
		return Decision{}, err
	}
	return s.GetDecision(ctx, id)
}
