package store

import (
	"context"
	"database/sql"
)

// SessionsOnceRole 列出项目里有明确记录表明当过 role 的会话（R-245）：现在是这个身份的、以这个身份写过交接的、
// 以这个身份改过需求状态的、写过验收建议的、以这个身份发过团队消息或提过决策的。接任概览据此找「上一任提的、还没答的决策」。
func (s *Store) SessionsOnceRole(ctx context.Context, projectID, role string) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT key FROM sessions WHERE project_id = ? AND role = ?
		UNION SELECT from_session FROM handoffs WHERE project_id = ? AND role = ?
		UNION SELECT c.session_key FROM status_changes c JOIN requirements r ON r.id = c.requirement_id WHERE r.project_id = ? AND c.role = ? AND c.session_key <> ''
		UNION SELECT a.by_session FROM review_advice a JOIN requirements r ON r.id = a.requirement_id WHERE r.project_id = ? AND a.by_role = ?
		UNION SELECT COALESCE(NULLIF(assigned_by, ''), session_key) FROM decisions WHERE project_id = ? AND from_role = ?`,
		projectID, role, projectID, role, projectID, role, projectID, role, projectID, role)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var k sql.NullString
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		if k.String != "" {
			out[k.String] = true
		}
	}
	return out, rows.Err()
}
