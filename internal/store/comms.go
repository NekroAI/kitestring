package store

import (
	"context"
	"time"
)

// 通讯页（R-249 P5）：网页上只读地看会话之间的团队消息（kind = tell）。

// ProjectTells 列出项目里 [since, until) 之间的团队消息，新的在前；until 为零值表示到现在。
func (s *Store) ProjectTells(ctx context.Context, projectID string, since, until time.Time, limit int) ([]Decision, error) {
	q := decSelect + ` WHERE x.project_id = ? AND x.kind = ? AND x.created_at >= ?`
	args := []any{projectID, KindTell, ts(since)}
	if !until.IsZero() {
		q += ` AND x.created_at < ?`
		args = append(args, ts(until))
	}
	q += ` ORDER BY x.seq DESC LIMIT ?`
	args = append(args, limit)
	return s.queryDecs(ctx, q, args...)
}

// UnsentTells 列出还没送达、也没关闭的团队消息（新的在前）；projectID 为空时列所有项目。
// 是「待送达」还是「无法送达」由调用方按收件会话的记录推。
func (s *Store) UnsentTells(ctx context.Context, projectID string, limit int) ([]Decision, error) {
	q := decSelect + ` WHERE x.kind = ? AND x.status NOT IN (?, ?)`
	args := []any{KindTell, DecDelivered, DecClosed}
	if projectID != "" {
		q += ` AND x.project_id = ?`
		args = append(args, projectID)
	}
	q += ` ORDER BY x.seq DESC LIMIT ?`
	args = append(args, limit)
	return s.queryDecs(ctx, q, args...)
}

// DecisionCodes 把决策 ID 换成编号（D-x），查不到的不出现在结果里。
func (s *Store) DecisionCodes(ctx context.Context, ids []string) (map[string]string, error) {
	out := map[string]string{}
	seen := map[string]bool{}
	var want []string
	for _, id := range ids {
		if id != "" && !seen[id] {
			seen[id] = true
			want = append(want, id)
		}
	}
	// 一次查回来，不逐个查（R-382）
	err := eachChunk(want, func(ph string, args []any) error {
		rows, err := s.db.QueryContext(ctx, `SELECT id, seq FROM decisions WHERE id IN (`+ph+`)`, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			var seq int
			if err := rows.Scan(&id, &seq); err != nil {
				return err
			}
			out[id] = decCode(seq)
		}
		return rows.Err()
	})
	return out, err
}
