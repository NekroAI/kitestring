package store

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"
)

// 订阅需求（R-249 P4，D-1027）：会话可以 watch 一条需求，需求有变动时收到一句话提醒。
// 关联这条需求、或正在做它的会话默认订阅（隐式订阅，不写行）；显式订阅 / 退订写进 req_watches，退订优先于隐式订阅。

const (
	WatchAll    = "all"    // 两档都收
	WatchAction = "action" // 只收「要动手」那一档
)

// Watch 是一条实际生效（或已退订）的订阅。
type Watch struct {
	RequirementID string    `json:"requirement_id"`
	Code          string    `json:"code" doc:"需求编号 R-x"`
	Title         string    `json:"title"`
	Status        string    `json:"status" doc:"需求现在的状态"`
	SessionKey    string    `json:"session_key"`
	Mode          string    `json:"mode" enum:"all,action" doc:"all 两档都收；action 只收「要动手」的"`
	Off           bool      `json:"off,omitempty" doc:"已退订"`
	Implicit      bool      `json:"implicit" doc:"因关联这条需求而订阅（没有显式订阅过）"`
	Linked        bool      `json:"linked" doc:"会话关联了这条需求"`
	CreatedAt     time.Time `json:"created_at,omitempty"`
}

// NormWatchMode 规整订阅档位：action 以外都按 all。
func NormWatchMode(m string) string {
	if m == WatchAction {
		return WatchAction
	}
	return WatchAll
}

// SetWatch 写下会话 key 对需求 reqID 的显式订阅（off = false）或退订（off = true）；mode 为空时保留原档位（没有时为 all）。
func (s *Store) SetWatch(ctx context.Context, reqID, key, mode string, off bool) (Watch, error) {
	if reqID == "" || key == "" {
		return Watch{}, errors.New("订阅要指明需求和会话")
	}
	if mode != "" {
		mode = NormWatchMode(mode)
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO req_watches(requirement_id, session_key, mode, off, created_at) VALUES(?, ?, ?, ?, ?)
		ON CONFLICT(requirement_id, session_key) DO UPDATE SET off = excluded.off, mode = CASE WHEN ? = '' THEN req_watches.mode ELSE excluded.mode END`,
		reqID, key, NormWatchMode(mode), off, now(), mode)
	if err != nil {
		return Watch{}, err
	}
	w, _, err := s.WatchOf(ctx, reqID, key)
	return w, err
}

// WatchOf 返回会话 key 对需求 reqID 的订阅情况：ok 为真表示订阅生效（显式或因关联）；退订了的 ok 为假、Off 为真。
func (s *Store) WatchOf(ctx context.Context, reqID, key string) (Watch, bool, error) {
	w := Watch{RequirementID: reqID, SessionKey: key, Mode: WatchAll}
	var seq int
	if err := s.db.QueryRowContext(ctx, `SELECT seq, title, status FROM requirements WHERE id = ?`, reqID).Scan(&seq, &w.Title, &w.Status); err != nil {
		return w, false, err
	}
	w.Code = "R-" + strconv.Itoa(seq)
	var n int
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM requirement_sessions WHERE requirement_id = ? AND session_key = ?`, reqID, key).Scan(&n)
	w.Linked = n > 0
	var mode, created string
	var off bool
	err := s.db.QueryRowContext(ctx, `SELECT mode, off, created_at FROM req_watches WHERE requirement_id = ? AND session_key = ?`, reqID, key).Scan(&mode, &off, &created)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		w.Implicit = w.Linked
		return w, w.Linked, nil
	case err != nil:
		return w, false, err
	}
	w.Mode, w.Off, w.CreatedAt = NormWatchMode(mode), off, parseTS(created)
	return w, !off, nil
}

// Watchers 返回需求 reqID 现在的订阅者：显式订阅 ∪ 关联且没结束的会话，去掉退订的；只含没结束（未结束、未失联、未标完成）的会话。
func (s *Store) Watchers(ctx context.Context, reqID string) ([]Watch, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT k, COALESCE(w.mode, 'all'), w.session_key IS NULL, EXISTS(SELECT 1 FROM requirement_sessions rs WHERE rs.requirement_id = ? AND rs.session_key = k)
		FROM (SELECT session_key AS k FROM req_watches WHERE requirement_id = ? AND off = 0
		      UNION SELECT session_key FROM requirement_sessions WHERE requirement_id = ?) u
		LEFT JOIN req_watches w ON w.requirement_id = ? AND w.session_key = u.k
		JOIN sessions x ON x.key = u.k
		WHERE COALESCE(w.off, 0) = 0 AND x.state NOT IN (?, ?) AND x.done_at = '' AND COALESCE(x.retired_at, '') = ''
		ORDER BY k`, reqID, reqID, reqID, reqID, StateEnded, StateLost)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Watch
	for rows.Next() {
		w := Watch{RequirementID: reqID}
		if err := rows.Scan(&w.SessionKey, &w.Mode, &w.Implicit, &w.Linked); err != nil {
			return nil, err
		}
		w.Mode = NormWatchMode(w.Mode)
		out = append(out, w)
	}
	return out, rows.Err()
}

// Watching 返回会话 key 订阅的需求（显式订阅在前，再是因关联而订阅的），以及它退订掉的需求；按需求编号倒序。
func (s *Store) Watching(ctx context.Context, key string) (on, off []Watch, err error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT r.id, r.seq, r.title, r.status, COALESCE(w.mode, 'all'), COALESCE(w.off, 0), w.session_key IS NULL,
		       EXISTS(SELECT 1 FROM requirement_sessions rs WHERE rs.requirement_id = r.id AND rs.session_key = ?), COALESCE(w.created_at, '')
		FROM (SELECT requirement_id AS id FROM req_watches WHERE session_key = ?
		      UNION SELECT requirement_id FROM requirement_sessions WHERE session_key = ?) u
		JOIN requirements r ON r.id = u.id
		LEFT JOIN req_watches w ON w.requirement_id = r.id AND w.session_key = ?
		ORDER BY w.session_key IS NULL, r.seq DESC`, key, key, key, key)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		w := Watch{SessionKey: key}
		var seq int
		var created string
		if err := rows.Scan(&w.RequirementID, &seq, &w.Title, &w.Status, &w.Mode, &w.Off, &w.Implicit, &w.Linked, &created); err != nil {
			return nil, nil, err
		}
		w.Code, w.Mode, w.CreatedAt = "R-"+strconv.Itoa(seq), NormWatchMode(w.Mode), parseTS(created)
		if w.Off {
			off = append(off, w)
		} else {
			on = append(on, w)
		}
	}
	return on, off, rows.Err()
}

// MergeTell 把 text 换成同一条团队消息（还没送达的）的新正文：订阅提醒 60 秒内的「只告知」合并成一条（R-249 P4）。
// 已送达或已关闭的不动，返回 false。
func (s *Store) MergeTell(ctx context.Context, id, text string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE decisions SET answer = ? WHERE id = ? AND kind = ? AND status = ?`, text, id, KindTell, DecAnswered)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}
