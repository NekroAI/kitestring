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
	StateBackground   = "background" // 本轮已结束，但还有后台任务在跑（例如等 CI、后台子 Agent）
	StateParked       = "parked"     // 待命：Agent 用 ks wait 挂着，你在 Kitestring 答复 / 验收 / 留言后它立刻继续
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
	Activity         []int            `json:"activity"`    // 最近 60 分钟每 5 分钟的事件数，旧 → 新
	Title            string           `json:"title"`       // 模型生成的会话标题
	Phase            string           `json:"phase"`       // 模型判断的阶段：planning implementing debugging testing done blocked
	DigestDone       string           `json:"digest_done"` // 等你回复时：这一轮做了什么
	DigestAsk        string           `json:"digest_ask"`  // 等你回复时：在等你决定什么
	StartedAt        time.Time        `json:"started_at"`
	LastActiveAt     time.Time        `json:"last_active_at"`
	EndedAt          *time.Time       `json:"ended_at,omitempty"`
	Instance         string           `json:"-"`              // 最新的终端（agent 进程）；同一对话可能在多个终端同时打开
	PrevInstance     string           `json:"-"`              // 被取代、可能仍开着的较早终端
	OtherTerminal    bool             `json:"other_terminal"` // 另有一个较早的终端也开着这个对话
	DoneAt           *time.Time       `json:"done_at,omitempty"` // 你手动标为已完成；你再给它发消息时自动清除
	Parked           bool             `json:"parked"`            // Agent 用 ks wait 挂起等你的答复 / 验收 / 留言
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
	s.title, s.phase, s.digest_done, s.digest_ask, s.instance, s.prev_instance, s.done_at, s.parked,
	s.started_at, s.last_active_at, s.ended_at
	FROM sessions s JOIN devices d ON d.id = s.device_id LEFT JOIN projects p ON p.id = s.project_id`

func scanSession(row interface{ Scan(...any) error }) (Session, error) {
	var x Session
	var started, active, since, toolAt, doneAt string
	var ended sql.NullString
	err := row.Scan(&x.Key, &x.Tool, &x.SessionID, &x.DeviceID, &x.DeviceName, &x.ProjectID, &x.ProjectName,
		&x.Client, &x.Model, &x.Cwd, &x.Branch, &x.Dirty, &x.State, &x.StateDetail, &x.LastPrompt, &x.Summary,
		&since, &x.Turns, &x.TurnTools, &x.LastTool, &toolAt, &x.Subagents, &x.PermMode, &x.Compactions, &x.Worktree,
		&x.Title, &x.Phase, &x.DigestDone, &x.DigestAsk, &x.Instance, &x.PrevInstance, &doneAt, &x.Parked,
		&started, &active, &ended)
	x.StartedAt = parseTS(started)
	x.StateSince = parseTS(since)
	if toolAt != "" {
		t := parseTS(toolAt)
		x.LastToolAt = &t
	}
	x.Activity = []int{}
	x.OtherTerminal = x.PrevInstance != ""
	if doneAt != "" {
		t := parseTS(doneAt)
		x.DoneAt = &t
	}
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

// ListSessions 列出 activeWithin 内活跃过的会话，以及更早但仍有待决策的会话。
func (s *Store) ListSessions(ctx context.Context, activeWithin time.Duration) ([]Session, error) {
	rows, err := s.db.QueryContext(ctx, sessionSelect+` WHERE s.last_active_at >= ? OR s.key IN (SELECT session_key FROM decisions WHERE status = 'pending')
		ORDER BY s.last_active_at DESC`, ts(time.Now().Add(-activeWithin)))
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
	doneAt := ""
	if x.DoneAt != nil {
		doneAt = ts(*x.DoneAt)
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO sessions(key, tool, session_id, device_id, project_id, client, model, cwd, branch, dirty, state, state_detail, last_prompt, summary,
			state_since, turns, turn_tools, last_tool, last_tool_at, subagents, perm_mode, compactions, worktree, instance, prev_instance, done_at, parked, started_at, last_active_at, ended_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET device_id = excluded.device_id, project_id = excluded.project_id, client = excluded.client, model = excluded.model,
			cwd = excluded.cwd, branch = excluded.branch, dirty = excluded.dirty, state = excluded.state, state_detail = excluded.state_detail,
			last_prompt = excluded.last_prompt, summary = excluded.summary, state_since = excluded.state_since, turns = excluded.turns,
			turn_tools = excluded.turn_tools, last_tool = excluded.last_tool, last_tool_at = excluded.last_tool_at, subagents = excluded.subagents,
			perm_mode = excluded.perm_mode, compactions = excluded.compactions, worktree = excluded.worktree,
			instance = excluded.instance, prev_instance = excluded.prev_instance, done_at = excluded.done_at, parked = excluded.parked,
			last_active_at = excluded.last_active_at, ended_at = excluded.ended_at`,
		x.Key, x.Tool, x.SessionID, x.DeviceID, nullStr(x.ProjectID), x.Client, x.Model, x.Cwd, x.Branch, x.Dirty, x.State, x.StateDetail,
		x.LastPrompt, x.Summary, since, x.Turns, x.TurnTools, x.LastTool, toolAt, x.Subagents, x.PermMode, x.Compactions, x.Worktree,
		x.Instance, x.PrevInstance, doneAt, x.Parked, ts(x.StartedAt), ts(x.LastActiveAt), ended)
	return err
}

// SetSessionDone 手动标记 / 取消“已完成”。
func (s *Store) SetSessionDone(ctx context.Context, key string, done bool) error {
	v := ""
	if done {
		v = now()
	}
	res, err := s.db.ExecContext(ctx, `UPDATE sessions SET done_at = ? WHERE key = ?`, v, key)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SessionAI 是模型生成的会话信息；空字段表示不修改。
type SessionAI struct {
	Summary, Title, Phase, DigestDone, DigestAsk string
}

// SetSessionAI 更新模型生成的字段（异步生成完成后调用）。
func (s *Store) SetSessionAI(ctx context.Context, key string, a SessionAI) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sessions SET
		summary = CASE WHEN ? <> '' THEN ? ELSE summary END,
		title = CASE WHEN ? <> '' THEN ? ELSE title END,
		phase = CASE WHEN ? <> '' THEN ? ELSE phase END,
		digest_done = CASE WHEN ? <> '' THEN ? ELSE digest_done END,
		digest_ask = CASE WHEN ? <> '' THEN ? ELSE digest_ask END
		WHERE key = ?`,
		a.Summary, a.Summary, a.Title, a.Title, a.Phase, a.Phase, a.DigestDone, a.DigestDone, a.DigestAsk, a.DigestAsk, key)
	return err
}

// ClearDigest 在新一轮开始时清掉上一轮的“等你回复”要点。
func (s *Store) ClearDigest(ctx context.Context, key string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sessions SET digest_done = '', digest_ask = '' WHERE key = ?`, key)
	return err
}

// RepairPrompts 修复被自动消息（如后台任务通知）覆盖的“最后消息”：
// 用 clean 重新整理，必要时回溯到最近一条真正由用户发出的消息。返回修复的会话数。
func (s *Store) RepairPrompts(ctx context.Context, clean func(string) (string, bool)) (int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, last_prompt FROM sessions WHERE last_prompt LIKE '<%'`)
	if err != nil {
		return 0, err
	}
	type item struct{ key, prompt string }
	var items []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.key, &it.prompt); err != nil {
			rows.Close()
			return 0, err
		}
		items = append(items, it)
	}
	rows.Close()
	fixed := 0
	for _, it := range items {
		text, auto := clean(it.prompt)
		if auto {
			text = ""
			evs, err := s.SessionEvents(ctx, it.key, 500)
			if err != nil {
				return fixed, err
			}
			for _, ev := range evs { // 时间倒序
				if ev.Name != "UserPromptSubmit" {
					continue
				}
				if t, a := clean(ev.Prompt); !a && t != "" {
					text = t
					break
				}
			}
		}
		if text == it.prompt {
			continue
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE sessions SET last_prompt = ? WHERE key = ?`, text, it.key); err != nil {
			return fixed, err
		}
		fixed++
	}
	return fixed, nil
}

// SessionsBetween 返回在 [from, to] 内有过事件的会话，供时段回顾使用。
func (s *Store) SessionsBetween(ctx context.Context, from, to time.Time) ([]Session, error) {
	rows, err := s.db.QueryContext(ctx, sessionSelect+` WHERE s.key IN (SELECT DISTINCT session_key FROM events WHERE time >= ? AND time <= ?) ORDER BY s.last_active_at DESC`, ts(from), ts(to))
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
	return out, rows.Err()
}

// PromptsBetween 返回会话在时间窗内用户发出的消息（已截断）。
func (s *Store) PromptsBetween(ctx context.Context, key string, from, to time.Time, limit int) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT payload FROM events WHERE session_key = ? AND name = 'UserPromptSubmit' AND time >= ? AND time <= ? ORDER BY time LIMIT ?`, key, ts(from), ts(to), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		var ev protocol.Event
		if json.Unmarshal([]byte(p), &ev) == nil && ev.Prompt != "" {
			out = append(out, ev.Prompt)
		}
	}
	return out, rows.Err()
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
	rows, err := s.db.QueryContext(ctx, `SELECT time FROM events WHERE session_key = ? AND time >= ?`, x.Key, ts(from))
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
