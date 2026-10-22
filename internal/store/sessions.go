package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/NekroAI/kitestring/internal/protocol"
)

// 会话状态（requirements.md 4.1，推导规则见 hook-matrix.md 第 5 节）。
const (
	StateRunning      = "running"
	StateWaitApproval = "waiting_approval"
	StateWaitReply    = "waiting_reply"
	StateIdle         = "idle"
	StateEnded        = "ended"
	StateLost         = "lost"
)

// Session 是一个 agent 实例会话。
type Session struct {
	Key              string           `json:"key"`
	Tool             string           `json:"tool"`
	SessionID        string           `json:"session_id"`
	DeviceID         string           `json:"device_id"`
	DeviceName       string           `json:"device_name"`
	ProjectID        string           `json:"project_id,omitempty"`
	ProjectName      string           `json:"project_name,omitempty"`
	Client           string           `json:"client"`
	Model            string           `json:"model"`
	Cwd              string           `json:"cwd"`
	Branch           string           `json:"branch"`
	Dirty            int              `json:"dirty"`
	State            string           `json:"state"`
	StateDetail      string           `json:"state_detail"`
	LastPrompt       string           `json:"last_prompt"`
	Summary          string           `json:"summary"`
	StateSince       time.Time        `json:"state_since"` // 当前状态开始的时间
	Turns            int              `json:"turns"`       // 用户提交过的轮数
	TurnTools        int              `json:"turn_tools"`  // 本轮工具调用次数
	LastTool         string           `json:"last_tool"`   // 最近一次工具动作简述
	LastToolAt       *time.Time       `json:"last_tool_at,omitempty"`
	Subagents        int              `json:"subagents"` // 正在运行的子 agent 数
	PermMode         string           `json:"permission_mode"`
	Compactions      int              `json:"compactions"`
	Worktree         bool             `json:"worktree"`
	Activity         []int            `json:"activity"` // 最近 60 分钟每 5 分钟的事件数，旧 → 新
	StartedAt        time.Time        `json:"started_at"`
	LastActiveAt     time.Time        `json:"last_active_at"`
	EndedAt          *time.Time       `json:"ended_at,omitempty"`
	Requirements     []RequirementRef `json:"requirements"`
	PendingDecisions int              `json:"pending_decisions"`
}

// RequirementRef 是会话卡片上显示的需求引用。
type RequirementRef struct {
	ID     string `json:"id"`
	Code   string `json:"code"`
	Title  string `json:"title"`
	Status string `json:"status"`
}

const sessionSelect = `SELECT s.key, s.tool, s.session_id, s.device_id, d.name, COALESCE(s.project_id, ''), COALESCE(p.name, ''),
	s.client, s.model, s.cwd, s.branch, s.dirty, s.state, s.state_detail, s.last_prompt, s.summary,
	s.state_since, s.turns, s.turn_tools, s.last_tool, s.last_tool_at, s.subagents, s.perm_mode, s.compactions, s.worktree,
	s.started_at, s.last_active_at, s.ended_at
	FROM sessions s JOIN devices d ON d.id = s.device_id LEFT JOIN projects p ON p.id = s.project_id`

func scanSession(row interface{ Scan(...any) error }) (Session, error) {
	var x Session
	var started, active, since, toolAt string
	var ended sql.NullString
	err := row.Scan(&x.Key, &x.Tool, &x.SessionID, &x.DeviceID, &x.DeviceName, &x.ProjectID, &x.ProjectName,
		&x.Client, &x.Model, &x.Cwd, &x.Branch, &x.Dirty, &x.State, &x.StateDetail, &x.LastPrompt, &x.Summary,
		&since, &x.Turns, &x.TurnTools, &x.LastTool, &toolAt, &x.Subagents, &x.PermMode, &x.Compactions, &x.Worktree,
		&started, &active, &ended)
	x.StartedAt = parseTS(started)
	x.StateSince = parseTS(since)
	if toolAt != "" {
		t := parseTS(toolAt)
		x.LastToolAt = &t
	}
	x.Activity = []int{}
	x.LastActiveAt = parseTS(active)
	x.EndedAt = parseNullTS(ended)
	x.Requirements = []RequirementRef{}
	return x, err
}

// GetSession 按会话键读取会话。
func (s *Store) GetSession(ctx context.Context, key string) (Session, error) {
	x, err := scanSession(s.db.QueryRowContext(ctx, sessionSelect+` WHERE s.key = ?`, key))
	if errors.Is(err, sql.ErrNoRows) {
		return x, ErrNotFound
	}
	if err != nil {
		return x, err
	}
	err = s.fillSessionLinks(ctx, []*Session{&x})
	return x, err
}

// ListSessions 列出会话：未结束的全部返回，已结束的只返回 endedWithin 内的。
func (s *Store) ListSessions(ctx context.Context, endedWithin time.Duration) ([]Session, error) {
	rows, err := s.db.QueryContext(ctx, sessionSelect+` WHERE s.ended_at IS NULL OR s.ended_at >= ? ORDER BY s.last_active_at DESC`, ts(time.Now().Add(-endedWithin)))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Session{}
	for rows.Next() {
		x, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	ptrs := make([]*Session, len(out))
	for i := range out {
		ptrs[i] = &out[i]
	}
	return out, s.fillSessionLinks(ctx, ptrs)
}

func (s *Store) fillSessionLinks(ctx context.Context, list []*Session) error {
	for _, x := range list {
		rows, err := s.db.QueryContext(ctx, `SELECT r.id, r.seq, r.title, r.status FROM requirement_sessions rs JOIN requirements r ON r.id = rs.requirement_id WHERE rs.session_key = ? ORDER BY rs.linked_at`, x.Key)
		if err != nil {
			return err
		}
		for rows.Next() {
			var r RequirementRef
			var seq int
			if err := rows.Scan(&r.ID, &seq, &r.Title, &r.Status); err != nil {
				rows.Close()
				return err
			}
			r.Code = reqCode(seq)
			x.Requirements = append(x.Requirements, r)
		}
		rows.Close()
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM decisions WHERE session_key = ? AND status = 'pending'`, x.Key).Scan(&x.PendingDecisions); err != nil {
			return err
		}
		if err := s.fillActivity(ctx, x); err != nil {
			return err
		}
	}
	return nil
}

// SaveSession 插入或整体更新会话行（不含关联字段）。
func (s *Store) SaveSession(ctx context.Context, x Session) error {
	var ended any
	if x.EndedAt != nil {
		ended = ts(*x.EndedAt)
	}
	toolAt := ""
	if x.LastToolAt != nil {
		toolAt = ts(*x.LastToolAt)
	}
	since := ""
	if !x.StateSince.IsZero() {
		since = ts(x.StateSince)
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO sessions(key, tool, session_id, device_id, project_id, client, model, cwd, branch, dirty, state, state_detail, last_prompt, summary,
			state_since, turns, turn_tools, last_tool, last_tool_at, subagents, perm_mode, compactions, worktree, started_at, last_active_at, ended_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET device_id = excluded.device_id, project_id = excluded.project_id, client = excluded.client, model = excluded.model,
			cwd = excluded.cwd, branch = excluded.branch, dirty = excluded.dirty, state = excluded.state, state_detail = excluded.state_detail,
			last_prompt = excluded.last_prompt, summary = excluded.summary, state_since = excluded.state_since, turns = excluded.turns,
			turn_tools = excluded.turn_tools, last_tool = excluded.last_tool, last_tool_at = excluded.last_tool_at, subagents = excluded.subagents,
			perm_mode = excluded.perm_mode, compactions = excluded.compactions, worktree = excluded.worktree,
			last_active_at = excluded.last_active_at, ended_at = excluded.ended_at`,
		x.Key, x.Tool, x.SessionID, x.DeviceID, nullStr(x.ProjectID), x.Client, x.Model, x.Cwd, x.Branch, x.Dirty, x.State, x.StateDetail,
		x.LastPrompt, x.Summary, since, x.Turns, x.TurnTools, x.LastTool, toolAt, x.Subagents, x.PermMode, x.Compactions, x.Worktree,
		ts(x.StartedAt), ts(x.LastActiveAt), ended)
	return err
}

// SetSessionSummary 只更新摘要（异步摘要生成完成后调用）。
func (s *Store) SetSessionSummary(ctx context.Context, key, summary string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sessions SET summary = ? WHERE key = ?`, summary, key)
	return err
}

// MarkDeviceLost 把设备下未结束的会话标为失联，返回受影响的会话键。
func (s *Store) MarkDeviceLost(ctx context.Context, deviceID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key FROM sessions WHERE device_id = ? AND ended_at IS NULL AND state <> ?`, deviceID, StateLost)
	if err != nil {
		return nil, err
	}
	var keys []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			rows.Close()
			return nil, err
		}
		keys = append(keys, k)
	}
	rows.Close()
	if len(keys) == 0 {
		return nil, nil
	}
	_, err = s.db.ExecContext(ctx, `UPDATE sessions SET state = ?, state_detail = '' WHERE device_id = ? AND ended_at IS NULL`, StateLost, deviceID)
	return keys, err
}

// InsertEvent 幂等写入事件；已存在时返回 false。
func (s *Store) InsertEvent(ctx context.Context, deviceID string, ev protocol.Event) (bool, error) {
	payload, _ := json.Marshal(ev)
	res, err := s.db.ExecContext(ctx, `INSERT INTO events(id, device_id, session_key, name, time, payload) VALUES(?, ?, ?, ?, ?, ?) ON CONFLICT(id) DO NOTHING`,
		ev.ID, deviceID, protocol.SessionKey(ev.Tool, ev.SessionID), ev.Name, ts(ev.Time), string(payload))
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// SessionEvents 返回会话最近的事件（时间倒序）。
func (s *Store) SessionEvents(ctx context.Context, key string, limit int) ([]protocol.Event, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT payload FROM events WHERE session_key = ? ORDER BY time DESC LIMIT ?`, key, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []protocol.Event{}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		var ev protocol.Event
		if json.Unmarshal([]byte(p), &ev) == nil {
			out = append(out, ev)
		}
	}
	return out, rows.Err()
}

// 活跃度：最近 activityBuckets × activityStep 内每段的事件数。
const (
	activityBuckets = 12
	activityStep    = 5 * time.Minute
)

func (s *Store) fillActivity(ctx context.Context, x *Session) error {
	x.Activity = make([]int, activityBuckets)
	now := time.Now()
	from := now.Add(-activityBuckets * activityStep)
	if x.LastActiveAt.Before(from) {
		return nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT time FROM events WHERE session_key = ? AND time >= ? AND name <> ?`, x.Key, ts(from), protocol.EventSummary)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return err
		}
		i := int(parseTS(t).Sub(from) / activityStep)
		if i >= 0 && i < activityBuckets {
			x.Activity[i]++
		}
	}
	return rows.Err()
}
