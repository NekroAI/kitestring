package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/NekroAI/kitestring/internal/protocol"
)

// 会话交接（R-224）：旧会话写结构化交接，新会话声明同一身份时自动注入，旧会话随之卸任。

const (
	HandoffRequested = "requested" // 用户点了「交接」，等旧会话写
	HandoffWritten   = "written"   // 已写好，等新会话接任
	HandoffTaken     = "taken"     // 已被接任
)

// Handoff 是一份交接。
type Handoff struct {
	ID          string `json:"id"`
	ProjectID   string `json:"project_id"`
	Role        string `json:"role"`
	FromSession string `json:"from_session"`
	FromTitle   string `json:"from_title"`
	Status      string `json:"status" enum:"requested,written,taken"`
	// 三栏（R-245）：只写纸鸢里没有的——做到一半的事、口头约定、注意事项
	Doing      string `json:"doing" doc:"做到一半的事：需求编号、做到哪、分支 / worktree"`
	Agreements string `json:"agreements" doc:"口头约定：用户在对话里交代、还没落成记录的（临时安排用 ks arrange add 记成当前安排）"`
	Caveats    string `json:"caveats" doc:"注意事项：坑、在跑的进程、别碰的东西"`
	// 旧栏目（R-224）：仍接受、照样显示，但这些纸鸢里已有，新交接不必写
	Waiting     string     `json:"waiting" doc:"旧栏目：等用户的（纸鸢里已有）"`
	Rules       string     `json:"rules" doc:"旧栏目：约定和规矩（纸鸢里已有）"`
	Next        string     `json:"next" doc:"旧栏目：下一步"`
	Notes       string     `json:"notes" doc:"旧栏目：注意事项（新交接写 caveats）"`
	RequestedAt *time.Time `json:"requested_at,omitempty"`
	WrittenAt   *time.Time `json:"written_at,omitempty"`
	TakenBy     string     `json:"taken_by,omitempty"`
	TakenTitle  string     `json:"taken_title,omitempty"`
	TakenAt     *time.Time `json:"taken_at,omitempty"`
}

const handoffCols = `id, project_id, role, from_session, from_title, status, doing, agreements, caveats, waiting, rules, next, notes, requested_at, written_at, taken_by, taken_title, taken_at`

func scanHandoff(row interface{ Scan(...any) error }) (Handoff, error) {
	var h Handoff
	var req, wr, tk string
	err := row.Scan(&h.ID, &h.ProjectID, &h.Role, &h.FromSession, &h.FromTitle, &h.Status, &h.Doing, &h.Agreements, &h.Caveats, &h.Waiting, &h.Rules, &h.Next, &h.Notes, &req, &wr, &h.TakenBy, &h.TakenTitle, &tk)
	opt := func(s string) *time.Time {
		if s == "" {
			return nil
		}
		t := parseTS(s)
		return &t
	}
	h.RequestedAt, h.WrittenAt, h.TakenAt = opt(req), opt(wr), opt(tk)
	return h, err
}

// openHandoff 找会话还没被接任的交接（requested / written）。
func (s *Store) openHandoff(ctx context.Context, from, role string) (Handoff, error) {
	h, err := scanHandoff(s.db.QueryRowContext(ctx, `SELECT `+handoffCols+` FROM handoffs WHERE from_session = ? AND role = ? AND status <> ? ORDER BY requested_at DESC, written_at DESC LIMIT 1`, from, role, HandoffTaken))
	if errors.Is(err, sql.ErrNoRows) {
		return h, ErrNotFound
	}
	return h, err
}

// RequestHandoff 记下用户要这个会话交接；已有还没被接任的交接时沿用它（重复点不重复建）。
func (s *Store) RequestHandoff(ctx context.Context, projectID, role, from string) (Handoff, error) {
	if h, err := s.openHandoff(ctx, from, role); err == nil {
		return h, nil
	} else if !errors.Is(err, ErrNotFound) {
		return h, err
	}
	title, _ := sessionSnap(ctx, s.db, from)
	now := time.Now().UTC()
	h := Handoff{ID: protocol.NewID(), ProjectID: projectID, Role: role, FromSession: from, FromTitle: title, Status: HandoffRequested, RequestedAt: &now}
	_, err := s.db.ExecContext(ctx, `INSERT INTO handoffs(id, project_id, role, from_session, from_title, status, requested_at) VALUES(?, ?, ?, ?, ?, ?, ?)`,
		h.ID, h.ProjectID, h.Role, h.FromSession, h.FromTitle, h.Status, ts(now))
	return h, err
}

// WriteHandoff 写入交接内容：有还没被接任的交接就覆盖它（可以改写），没有就新建（会话自己主动写）。
func (s *Store) WriteHandoff(ctx context.Context, projectID, role, from string, c Handoff) (Handoff, error) {
	h, err := s.openHandoff(ctx, from, role)
	if errors.Is(err, ErrNotFound) {
		h = Handoff{ID: protocol.NewID(), ProjectID: projectID, Role: role, FromSession: from}
		if _, err := s.db.ExecContext(ctx, `INSERT INTO handoffs(id, project_id, role, from_session, status) VALUES(?, ?, ?, ?, ?)`, h.ID, projectID, role, from, HandoffWritten); err != nil {
			return h, err
		}
	} else if err != nil {
		return h, err
	}
	title, _ := sessionSnap(ctx, s.db, from)
	now := time.Now().UTC()
	if _, err := s.db.ExecContext(ctx, `UPDATE handoffs SET status = ?, from_title = ?, doing = ?, agreements = ?, caveats = ?, waiting = ?, rules = ?, next = ?, notes = ?, written_at = ? WHERE id = ?`,
		HandoffWritten, title, c.Doing, c.Agreements, c.Caveats, c.Waiting, c.Rules, c.Next, c.Notes, ts(now), h.ID); err != nil {
		return h, err
	}
	return s.GetHandoff(ctx, h.ID)
}

// GetHandoff 按 ID 读一份交接。
func (s *Store) GetHandoff(ctx context.Context, id string) (Handoff, error) {
	h, err := scanHandoff(s.db.QueryRowContext(ctx, `SELECT `+handoffCols+` FROM handoffs WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return h, ErrNotFound
	}
	return h, err
}

// TakeHandoff 新会话声明身份时接任：取同项目同身份、别的会话写好的最新一份交接，标为已接任。没有时返回 ErrNotFound。
func (s *Store) TakeHandoff(ctx context.Context, projectID, role, by string) (Handoff, error) {
	h, err := scanHandoff(s.db.QueryRowContext(ctx, `SELECT `+handoffCols+` FROM handoffs WHERE project_id = ? AND role = ? AND status = ? AND from_session <> ? ORDER BY written_at DESC LIMIT 1`,
		projectID, role, HandoffWritten, by))
	if errors.Is(err, sql.ErrNoRows) {
		return h, ErrNotFound
	}
	if err != nil {
		return h, err
	}
	title, _ := sessionSnap(ctx, s.db, by)
	now := time.Now().UTC()
	if _, err := s.db.ExecContext(ctx, `UPDATE handoffs SET status = ?, taken_by = ?, taken_title = ?, taken_at = ? WHERE id = ?`, HandoffTaken, by, title, ts(now), h.ID); err != nil {
		return h, err
	}
	h.Status, h.TakenBy, h.TakenTitle, h.TakenAt = HandoffTaken, by, title, &now
	return h, nil
}

// HandoffsOf 列出会话交出去的和接手的交接（新的在前，最多 10 份）。
func (s *Store) HandoffsOf(ctx context.Context, key string) ([]Handoff, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+handoffCols+` FROM handoffs WHERE from_session = ? OR taken_by = ? ORDER BY COALESCE(NULLIF(taken_at, ''), NULLIF(written_at, ''), requested_at) DESC LIMIT 10`, key, key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Handoff{}
	for rows.Next() {
		h, err := scanHandoff(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// SetRetired 标记会话已交接卸任（R-224）；retired = false 时清除（重新声明身份）。
func (s *Store) SetRetired(ctx context.Context, key string, retired bool) error {
	v := ""
	if retired {
		v = ts(time.Now().UTC())
	}
	_, err := s.db.ExecContext(ctx, `UPDATE sessions SET retired_at = ? WHERE key = ?`, v, key)
	return err
}

// QuietTellQuestion 是只告知的团队消息的标题（R-198、R-227），只作显示；是否只告知看 decisions.fyi 列（R-249，老数据按这个标题回填）。
const QuietTellQuestion = "团队消息（不唤醒）"

// DevIdleDefault 是开发会话待命超时的默认时长（R-229），项目设置里可调。
const DevIdleDefault = 30 * time.Minute

// DevIdleLimit 是项目设置的开发待命超时时长；没设（0）时为默认 30 分钟。
func (p Project) DevIdleLimit() time.Duration {
	if p.DevIdleMinutes > 0 {
		return time.Duration(p.DevIdleMinutes) * time.Minute
	}
	return DevIdleDefault
}

// StallDefault 是进行中需求「停滞」的默认时长（R-243），项目设置里可调。
const StallDefault = 120 * time.Minute

// StallLimit：进行中的需求多久没动静、又没有会话在跑，就进「要你处理」（R-243）。
func (p Project) StallLimit() time.Duration {
	if p.StallMinutes > 0 {
		return time.Duration(p.StallMinutes) * time.Minute
	}
	return StallDefault
}

// StartNudgeDefault 是指派的需求送达后、多久没开始处理就再提醒一次的默认时长（R-278），项目设置里可调。
const StartNudgeDefault = 10 * time.Minute

// StartNudgeLimit：指派的需求送达后这么久没开始处理，再提醒接收方一次；提醒后再过同样时长仍没开始，进「待处理」（R-278）。
func (p Project) StartNudgeLimit() time.Duration {
	if p.StartNudgeMinutes > 0 {
		return time.Duration(p.StartNudgeMinutes) * time.Minute
	}
	return StartNudgeDefault
}

// SetIdle 记下开发会话待命超时（R-229）。
func (s *Store) SetIdle(ctx context.Context, key string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sessions SET idle_at = ? WHERE key = ?`, ts(at.UTC()), key)
	return err
}

// ClearIdle 清除待命超时（派活、点名给它的消息、你给它发消息时）；返回原来是不是待命超时。
func (s *Store) ClearIdle(ctx context.Context, key string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE sessions SET idle_at = '' WHERE key = ? AND idle_at <> ''`, key)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}
