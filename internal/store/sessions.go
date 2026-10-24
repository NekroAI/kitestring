package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
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
	Activity         []int            `json:"activity"`            // 最近 60 分钟每 5 分钟的事件数，旧 → 新
	Title            string           `json:"title"`               // 模型生成的会话标题
	DigestDone       string           `json:"digest_done"`         // 等你回复时：这一轮做了什么
	DigestAsk        string           `json:"digest_ask"`          // 等你回复时：在等你决定什么
	ReplyAsk         string           `json:"reply_ask,omitempty"` // 最后一条回复在问你时，它的最后一段（R-117）；你回复后清空
	StartedAt        time.Time        `json:"started_at"`
	LastActiveAt     time.Time        `json:"last_active_at"`
	EndedAt          *time.Time       `json:"ended_at,omitempty"`
	Instance         string           `json:"-"`                                    // 最新的终端（agent 进程）；同一对话可能在多个终端同时打开
	PrevInstance     string           `json:"-"`                                    // 被取代、可能仍开着的较早终端
	OtherTerminal    bool             `json:"other_terminal"`                       // 另有一个较早的终端也开着这个对话
	DoneAt           *time.Time       `json:"done_at,omitempty"`                    // 你手动标为已完成；你再给它发消息时自动清除
	Parked           bool             `json:"parked"`                               // Agent 用 ks wait 挂起等你的答复 / 验收 / 留言
	ParkedUntil      *time.Time       `json:"parked_until,omitempty"`               // 挂起最长等到什么时候（R-74）
	ToolsStale       string           `json:"tools_stale"`                          // 会话里的 Kitestring 工具（MCP）是升级前的版本时为该版本，最新时为空（R-59）
	ContextTokens    int              `json:"context_tokens"`                       // 最近一轮结束时的上下文 token 数（R-60）
	ContextMax       int              `json:"context_max"`                          // 上下文窗口；不知道时为 0
	Background       bool             `json:"background"`                           // 桌面应用自己在后台发起的任务（如 Codex 的项目建议），不是你的对话：不进工作台与各种计数、不参与派活（R-179）
	Role             string           `json:"role" enum:",pm,dev,advisor,reviewer"` // 会话身份（R-82）：pm 开发 dev 顾问 advisor 审查员 reviewer，空为无身份
	RecentTools      []ToolAct        `json:"recent_tools"`                         // 最近几次工具调用，旧 → 新（工作台滚动展示）
	Ticks            []time.Time      `json:"ticks"`                                // 主会话近 10 分钟的工具调用时刻，旧 → 新，最多 maxSessionTicks 个（工作台时间线，R-170）
	Tasks            []BgTask         `json:"tasks"`                                // 在跑的后台任务：后台 shell 与子代理（R-162），开始早的在前
	Requirements     []RequirementRef `json:"requirements"`
	PendingDecisions int              `json:"pending_decisions"`
}

// BgTask 是会话下一个在跑的后台任务（R-162）：只有类型、标签、开始时间与最近动作，不含命令输出与对话内容。
type BgTask struct {
	ID           string      `json:"id"`                      // 子代理为 agent_id；后台 shell 为 daemon 给的进程标识
	Kind         string      `json:"kind" enum:"shell,agent"` // shell 后台命令；agent 子代理
	Label        string      `json:"label"`                   // 后台命令的描述或命令（截断）；子代理的任务描述
	AgentType    string      `json:"agent_type,omitempty"`    // 子代理类型（Explore、general-purpose…）
	StartedAt    time.Time   `json:"started_at"`
	LastAction   string      `json:"last_action,omitempty"` // 子代理最近一次工具动作
	LastActionAt *time.Time  `json:"last_action_at,omitempty"`
	Ticks        []time.Time `json:"ticks"` // 近 10 分钟的工具调用时刻（子代理），旧 → 新
}

// 子代理没收到 SubagentStop（被中断等）时，这么久没有动作就不再算在跑。
const agentTaskIdle = 30 * time.Minute

// LiveTasks 去掉久无动作的子代理、过期的刻度。
func LiveTasks(ts []BgTask, now time.Time) []BgTask {
	out := []BgTask{}
	for _, t := range ts {
		if t.Kind == "agent" {
			last := t.StartedAt
			if t.LastActionAt != nil && t.LastActionAt.After(last) {
				last = *t.LastActionAt
			}
			if now.Sub(last) > agentTaskIdle {
				continue
			}
		}
		ticks := []time.Time{}
		for _, at := range t.Ticks {
			if now.Sub(at) <= 10*time.Minute {
				ticks = append(ticks, at)
			}
		}
		t.Ticks = ticks
		out = append(out, t)
	}
	return out
}

// ToolAct 是一次工具调用的简述。
type ToolAct struct {
	Label string    `json:"label"`
	At    time.Time `json:"at"`
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
	s.title, s.digest_done, s.digest_ask, s.reply_ask, s.instance, s.prev_instance, s.done_at, s.parked, s.parked_until, s.tools_stale, s.context_tokens, s.context_max, s.role, s.tasks, s.background,
	s.started_at, s.last_active_at, s.ended_at
	FROM sessions s JOIN devices d ON d.id = s.device_id LEFT JOIN projects p ON p.id = s.project_id`

func scanSession(row interface{ Scan(...any) error }) (Session, error) {
	var x Session
	var started, active, since, toolAt, doneAt, parkedUntil, tasks string
	var ended sql.NullString
	err := row.Scan(&x.Key, &x.Tool, &x.SessionID, &x.DeviceID, &x.DeviceName, &x.ProjectID, &x.ProjectName,
		&x.Client, &x.Model, &x.Cwd, &x.Branch, &x.Dirty, &x.State, &x.StateDetail, &x.LastPrompt, &x.Summary,
		&since, &x.Turns, &x.TurnTools, &x.LastTool, &toolAt, &x.Subagents, &x.PermMode, &x.Compactions, &x.Worktree,
		&x.Title, &x.DigestDone, &x.DigestAsk, &x.ReplyAsk, &x.Instance, &x.PrevInstance, &doneAt, &x.Parked, &parkedUntil, &x.ToolsStale, &x.ContextTokens, &x.ContextMax, &x.Role, &tasks, &x.Background,
		&started, &active, &ended)
	x.Tasks = []BgTask{}
	if tasks != "" && tasks != "[]" {
		_ = json.Unmarshal([]byte(tasks), &x.Tasks)
		x.Tasks = LiveTasks(x.Tasks, time.Now())
	}
	x.StartedAt = parseTS(started)
	x.StateSince = parseTS(since)
	if toolAt != "" {
		t := parseTS(toolAt)
		x.LastToolAt = &t
	}
	x.Activity = []int{}
	x.RecentTools = []ToolAct{}
	x.Ticks = []time.Time{}
	x.OtherTerminal = x.PrevInstance != ""
	if doneAt != "" {
		t := parseTS(doneAt)
		x.DoneAt = &t
	}
	if parkedUntil != "" && x.Parked {
		t := parseTS(parkedUntil)
		x.ParkedUntil = &t
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
// background 为假时不含后台任务（工作台与各种计数用），为真时只列后台任务（⌘K 里找它们，R-179）。
func (s *Store) ListSessions(ctx context.Context, activeWithin time.Duration, background bool) ([]Session, error) {
	rows, err := s.db.QueryContext(ctx, sessionSelect+` WHERE s.background = ? AND (s.last_active_at >= ? OR s.key IN (SELECT session_key FROM decisions WHERE status = 'pending'))
		ORDER BY s.last_active_at DESC`, background, ts(time.Now().Add(-activeWithin)))
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
		if x.State != StateEnded {
			if err := s.fillRecentTools(ctx, x); err != nil {
				return err
			}
			if err := s.fillTicks(ctx, x); err != nil {
				return err
			}
		}
	}
	return nil
}

// fillRecentTools 取主会话最近 3 次工具调用，让工作台一打开就能看到它最近在做什么。
func (s *Store) fillRecentTools(ctx context.Context, x *Session) error {
	rows, err := s.db.QueryContext(ctx, `SELECT payload FROM events WHERE session_key = ? AND name = 'PreToolUse' ORDER BY time DESC LIMIT 12`, x.Key)
	if err != nil {
		return err
	}
	defer rows.Close()
	var acts []ToolAct
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return err
		}
		var ev protocol.Event
		if json.Unmarshal([]byte(p), &ev) != nil || ev.ToolName == "" || ev.AgentID != "" || len(acts) >= 3 {
			continue // 子代理的工具调用记在子代理名下（R-162），不混进主会话的最近动作
		}
		label := strings.TrimPrefix(ev.ToolName, "mcp__")
		if ev.ToolBrief != "" {
			label += " · " + ev.ToolBrief
		}
		acts = append([]ToolAct{{Label: label, At: ev.Time}}, acts...)
	}
	x.RecentTools = acts
	if x.RecentTools == nil {
		x.RecentTools = []ToolAct{}
	}
	return rows.Err()
}

// 工作台时间线（R-170）：近 10 分钟、每次工具调用一个刻度；点数设上限，免得极密的会话把推送撑大。
const (
	tickWindow      = 10 * time.Minute
	maxSessionTicks = 120
)

// fillTicks 取主会话近 10 分钟的工具调用时刻（子代理的记在子泳道上，不算在这里）。
func (s *Store) fillTicks(ctx context.Context, x *Session) error {
	from := time.Now().Add(-tickWindow)
	if x.LastActiveAt.Before(from) {
		return nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT time FROM events WHERE session_key = ? AND name = 'PreToolUse' AND time >= ?
		AND COALESCE(json_extract(payload, '$.agent_id'), '') = '' ORDER BY time DESC LIMIT ?`, x.Key, ts(from), maxSessionTicks)
	if err != nil {
		return err
	}
	defer rows.Close()
	var out []time.Time
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return err
		}
		out = append(out, parseTS(t))
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	if out != nil {
		x.Ticks = out
	}
	return rows.Err()
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
	parkedUntil := ""
	if x.ParkedUntil != nil && x.Parked {
		parkedUntil = ts(*x.ParkedUntil)
	}
	tasks := "[]"
	if len(x.Tasks) > 0 {
		b, _ := json.Marshal(x.Tasks)
		tasks = string(b)
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO sessions(key, tool, session_id, device_id, project_id, client, model, cwd, branch, dirty, state, state_detail, last_prompt, summary,
			state_since, turns, turn_tools, last_tool, last_tool_at, subagents, perm_mode, compactions, worktree, instance, prev_instance, done_at, parked, parked_until, tasks, background, started_at, last_active_at, ended_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET device_id = excluded.device_id, project_id = excluded.project_id, client = excluded.client, model = excluded.model,
			cwd = excluded.cwd, branch = excluded.branch, dirty = excluded.dirty, state = excluded.state, state_detail = excluded.state_detail,
			last_prompt = excluded.last_prompt, summary = excluded.summary, state_since = excluded.state_since, turns = excluded.turns,
			turn_tools = excluded.turn_tools, last_tool = excluded.last_tool, last_tool_at = excluded.last_tool_at, subagents = excluded.subagents,
			perm_mode = excluded.perm_mode, compactions = excluded.compactions, worktree = excluded.worktree,
			instance = excluded.instance, prev_instance = excluded.prev_instance, done_at = excluded.done_at, parked = excluded.parked, parked_until = excluded.parked_until, tasks = excluded.tasks, background = excluded.background,
			last_active_at = excluded.last_active_at, ended_at = excluded.ended_at`,
		x.Key, x.Tool, x.SessionID, x.DeviceID, nullStr(x.ProjectID), x.Client, x.Model, x.Cwd, x.Branch, x.Dirty, x.State, x.StateDetail,
		x.LastPrompt, x.Summary, since, x.Turns, x.TurnTools, x.LastTool, toolAt, x.Subagents, x.PermMode, x.Compactions, x.Worktree,
		x.Instance, x.PrevInstance, doneAt, x.Parked, parkedUntil, tasks, x.Background, ts(x.StartedAt), ts(x.LastActiveAt), ended)
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
	Summary, Title, DigestDone, DigestAsk string
}

// SetSessionAI 更新模型生成的字段（异步生成完成后调用）。
func (s *Store) SetSessionAI(ctx context.Context, key string, a SessionAI) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sessions SET
		summary = CASE WHEN ? <> '' THEN ? ELSE summary END,
		title = CASE WHEN ? <> '' THEN ? ELSE title END,
		digest_done = CASE WHEN ? <> '' THEN ? ELSE digest_done END,
		digest_ask = CASE WHEN ? <> '' THEN ? ELSE digest_ask END
		WHERE key = ?`,
		a.Summary, a.Summary, a.Title, a.Title, a.DigestDone, a.DigestDone, a.DigestAsk, a.DigestAsk, key)
	return err
}

// SetToolsStale 记下会话的 Kitestring 工具是否过期（v 为过期工具的版本，空表示已是最新）。
func (s *Store) SetToolsStale(ctx context.Context, key, v string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sessions SET tools_stale = ? WHERE key = ?`, v, key)
	return err
}

// SetWaitOn 记下会话 ks wait --on 的订阅（R-164）：空为按身份默认，"none" 为不订阅，其它为逗号分隔的事件。
func (s *Store) SetWaitOn(ctx context.Context, key, v string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sessions SET wait_on = ? WHERE key = ?`, v, key)
	return err
}

// Subscriber 是可能订阅了项目事件的会话（R-164）。
type Subscriber struct {
	Key    string
	Role   string
	WaitOn string
}

// WaitSubscribers 列出项目里在线（挂着 ks wait 或正在跑）、有身份或给过 --on 的会话，最近活跃的在前。
// 一轮结束没挂 ks wait、已结束、失联、你标为已完成的不算：不在线时不给它攒消息。
func (s *Store) WaitSubscribers(ctx context.Context, projectID string) ([]Subscriber, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, role, wait_on FROM sessions
		WHERE project_id = ? AND (role <> '' OR wait_on <> '') AND done_at = '' AND state NOT IN (?, ?)
			AND (parked = 1 OR state IN (?, ?, ?, ?))
		ORDER BY last_active_at DESC`, projectID, StateEnded, StateLost, StateParked, StateRunning, StateBackground, StateWaitApproval)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Subscriber{}
	for rows.Next() {
		var x Subscriber
		if err := rows.Scan(&x.Key, &x.Role, &x.WaitOn); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// SetContext 记下会话最近一轮结束时的上下文占用。
func (s *Store) SetContext(ctx context.Context, key string, used, window int) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sessions SET context_tokens = ?, context_max = ? WHERE key = ?`, used, window, key)
	return err
}

// LastReply 是会话最近一轮结束时的最后一条回复（R-109）。
type LastReply struct {
	Text string    `json:"text"`
	At   time.Time `json:"at"`
}

// SetLastReply 记下会话一轮结束时的最后回复；比已存的旧（乱序补传）时不覆盖。
func (s *Store) SetLastReply(ctx context.Context, key, text string, at time.Time) error {
	if cur, err := s.GetLastReply(ctx, key); err != nil {
		return err
	} else if cur != nil && cur.At.After(at) {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `UPDATE sessions SET last_reply = ?, last_reply_at = ?, reply_ask = ? WHERE key = ?`, text, ts(at), AskTail(text), key)
	return err
}

// ClearReplyAsk：你已经回复了它（终端里发了消息，或在工作台回复），不再算“在等你回复”（R-117）。
func (s *Store) ClearReplyAsk(ctx context.Context, key string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sessions SET reply_ask = '' WHERE key = ?`, key)
	return err
}

// GetLastReply 读取会话的最后回复；还没有时返回 nil。
func (s *Store) GetLastReply(ctx context.Context, key string) (*LastReply, error) {
	var text, at string
	err := s.db.QueryRowContext(ctx, `SELECT last_reply, last_reply_at FROM sessions WHERE key = ?`, key).Scan(&text, &at)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && text == "") {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &LastReply{Text: text, At: parseTS(at)}, nil
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
	rows, err := s.db.QueryContext(ctx, sessionSelect+` WHERE s.background = 0 AND s.key IN (SELECT DISTINCT session_key FROM events WHERE time >= ? AND time <= ?) ORDER BY s.last_active_at DESC`, ts(from), ts(to))
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
	rows, err := s.db.QueryContext(ctx, `SELECT payload FROM events WHERE session_key = ? AND name <> 'KsTasks' ORDER BY time DESC LIMIT ?`, key, limit)
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
	rows, err := s.db.QueryContext(ctx, `SELECT time FROM events WHERE session_key = ? AND time >= ? AND name <> 'KsTasks'`, x.Key, ts(from))
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

// SetRole 设置会话身份（R-82）。同一项目只保留一个 PM：设为 pm 时，同项目原来的 PM 降为无身份，
// 返回被降级的会话键。会话不属于任何项目时不做唯一性处理。
func (s *Store) SetRole(ctx context.Context, key, role string) (demoted []string, err error) {
	err = s.tx(ctx, func(t *sql.Tx) error {
		var project sql.NullString
		if err := t.QueryRowContext(ctx, `SELECT project_id FROM sessions WHERE key = ?`, key).Scan(&project); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if role == "pm" && project.Valid && project.String != "" {
			rows, err := t.QueryContext(ctx, `SELECT key FROM sessions WHERE project_id = ? AND role = 'pm' AND key <> ?`, project.String, key)
			if err != nil {
				return err
			}
			for rows.Next() {
				var k string
				if err := rows.Scan(&k); err != nil {
					rows.Close()
					return err
				}
				demoted = append(demoted, k)
			}
			rows.Close()
			if _, err := t.ExecContext(ctx, `UPDATE sessions SET role = '' WHERE project_id = ? AND role = 'pm' AND key <> ?`, project.String, key); err != nil {
				return err
			}
		}
		_, err := t.ExecContext(ctx, `UPDATE sessions SET role = ? WHERE key = ?`, role, key)
		return err
	})
	return demoted, err
}

// RolesForDevice 返回该设备上有身份的会话：会话键 → 身份，下发给 daemon 用于注入与常驻检查。
func (s *Store) RolesForDevice(ctx context.Context, deviceID string) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, role FROM sessions WHERE device_id = ? AND role <> ''`, deviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, r string
		if err := rows.Scan(&k, &r); err != nil {
			return nil, err
		}
		out[k] = r
	}
	return out, rows.Err()
}

// RoleSessions 列出项目里有身份的会话（含已结束的），最近活跃的在前（R-86）。
func (s *Store) RoleSessions(ctx context.Context, projectID string) ([]Session, error) {
	rows, err := s.db.QueryContext(ctx, sessionSelect+` WHERE s.project_id = ? AND s.role <> '' ORDER BY s.last_active_at DESC`, projectID)
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

// Presence 是常驻身份（PM / 开发）的在线状态（R-84 / R-86）：挂着 ks wait 为待命；在跑、等授权、在问你为工作中；
// 一轮结束却没挂 ks wait、已结束或失联为离线。其它身份返回空。
func (x Session) Presence() string {
	if x.Role != "pm" && x.Role != "dev" {
		return ""
	}
	switch {
	case x.State == StateEnded || x.State == StateLost:
		return "offline"
	case x.Parked || x.State == StateParked:
		return "standby"
	case x.State == StateRunning || x.State == StateBackground || x.State == StateWaitApproval:
		return "working"
	case x.State == StateWaitReply && askingUser(x.StateDetail):
		return "working"
	}
	return "offline"
}

func askingUser(detail string) bool {
	for _, p := range []string{"AskUserQuestion", "ExitPlanMode", "request_user_input"} {
		if strings.HasPrefix(detail, p) {
			return true
		}
	}
	return false
}

// BackfillBackground 给旧数据补后台任务标记（R-179）：看每个未标记会话的第一条用户提示词，
// 命中 isBg 的标上。没有事件记录时退回用会话上存的最后消息。返回新标记的会话数。
func (s *Store) BackfillBackground(ctx context.Context, isBg func(tool, prompt string) bool) (int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, tool, last_prompt FROM sessions WHERE background = 0 AND tool = 'codex'`)
	if err != nil {
		return 0, err
	}
	type item struct{ key, tool, last string }
	var items []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.key, &it.tool, &it.last); err != nil {
			rows.Close()
			return 0, err
		}
		items = append(items, it)
	}
	rows.Close()
	n := 0
	for _, it := range items {
		first := it.last
		var p string
		err := s.db.QueryRowContext(ctx, `SELECT payload FROM events WHERE session_key = ? AND name = 'UserPromptSubmit' ORDER BY time LIMIT 1`, it.key).Scan(&p)
		if err == nil {
			var ev protocol.Event
			if json.Unmarshal([]byte(p), &ev) == nil && ev.Prompt != "" {
				first = ev.Prompt
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return n, err
		}
		if !isBg(it.tool, first) {
			continue
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE sessions SET background = 1 WHERE key = ?`, it.key); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}
