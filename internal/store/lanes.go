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

// 工作台泳道时间线（R-205）：每个会话、每个子任务每分钟的调用次数，加上长命令与 ks wait 待命两种时段、子任务名单。
// 历史只增不改：新事件只把它所在那一分钟加一；网页按分钟合并，实时只收当前分钟的新值。
// 规则只写在 LaneTracker 里，实时（hub 收到事件时）和补录（旧库升级时从事件记录算出历史）共用一套。

const (
	// LongTool：工具从开始到结束超过这么久，在时间线中线画一段实心细带。
	LongTool = 20 * time.Second
	// LaneKeep：分钟级汇总、时段、子任务名单保留这么久；更早的只剩小时级（lane_hours）。
	LaneKeep = 30 * 24 * time.Hour
	// 子代理没收到 SubagentStop（被中断等）时，这么久没有动作就当它已结束（和 agentTaskIdle 一致）。
	LaneAgentIdle = agentTaskIdle

	SpanTool = "tool" // 长命令
	SpanWait = "wait" // ks wait 待命

	laneBackfillKey = "lanes_backfill_v1"
)

// LaneRow 是一个会话（agent 为空）或一个子任务在时间范围内有调用的格子（每分钟或每小时），按时间升序。
// token 用量（R-206）按模型分行，另在 LaneData.Tokens 里。
type LaneRow struct {
	Key     string  `json:"k" doc:"会话键"`
	Agent   string  `json:"a" doc:"子任务的 agent_id；主会话为空"`
	Minutes []int64 `json:"m" doc:"格子起点，Unix 分钟（秒 / 60），升序；按小时取时为整点"`
	Calls   []int   `json:"c" doc:"与 m 对应的调用次数"`
}

// LaneSpan 是时间线上的一个时段：长命令或待命。
type LaneSpan struct {
	Key   string `json:"k"`
	Agent string `json:"a"`
	Kind  string `json:"kind" enum:"tool,wait" doc:"tool 长命令（超过 20 秒）；wait 用 ks wait 待命"`
	Ref   string `json:"ref" doc:"tool_use_id 或 KsWait 事件 id"`
	Label string `json:"label" doc:"长命令的动作简述"`
	Start int64  `json:"start" doc:"开始，Unix 毫秒"`
	End   *int64 `json:"end,omitempty" doc:"结束，Unix 毫秒；还没结束时为空"`
	Until *int64 `json:"until,omitempty" doc:"待命最长等到什么时候（没结束时最多画到这里）"`
}

// LaneAgent 是一个子任务：名字、类型、起止。结束后照样返回，时间线上留到滑出窗口。
type LaneAgent struct {
	Key   string `json:"k"`
	Agent string `json:"a"`
	Label string `json:"label"`
	Type  string `json:"type"`
	Start int64  `json:"start" doc:"Unix 毫秒"`
	End   *int64 `json:"end,omitempty" doc:"结束（SubagentStop 或会话结束），Unix 毫秒；没结束为空"`
}

// LaneData 是一段时间里全部会话的时间线数据。
type LaneData struct {
	Now    int64       `json:"now" doc:"服务端当前时间，Unix 毫秒"`
	Step   int         `json:"step" doc:"格子大小（分钟）：1 每分钟，60 每小时"`
	From   int64       `json:"from" doc:"范围起点，Unix 分钟（含）"`
	To     int64       `json:"to" doc:"范围终点，Unix 分钟（不含）"`
	Rows   []LaneRow   `json:"rows"`
	Spans  []LaneSpan  `json:"spans"`
	Agents []LaneAgent `json:"agents"`
	// Sessions：范围内有调用的会话的简要信息（长档位里早已结束、不在会话列表里的会话也要能画出标题）
	Sessions []LaneSession `json:"sessions"`
	// token 用量（R-206）：本机读得到对话记录的会话才有；没有的会话不画下半部分
	Tokens []LaneTok  `json:"tok"`
	Colds  []LaneCold `json:"colds" doc:"范围内的冷启动（缓存重建）"`
	// 后台命令（R-208）：和范围有交集的；还在跑的一直算
	Shells []protocol.ShellCmd `json:"shells"`
}

// LaneSession 是时间线上一个会话的简要信息。
type LaneSession struct {
	Key          string     `json:"k"`
	Title        string     `json:"title"`
	Role         string     `json:"role"`
	Tool         string     `json:"tool"`
	ProjectID    string     `json:"project_id"`
	ProjectName  string     `json:"project_name"`
	Cwd          string     `json:"cwd"`
	Background   bool       `json:"background"`
	LastActiveAt time.Time  `json:"last_active_at"`
	EndedAt      *time.Time `json:"ended_at,omitempty"`
}

// LaneMinute 是实时推送的一格：某会话 / 子任务某一分钟、以及它所在那个小时现在的调用次数（绝对值，网页直接替换）。
type LaneMinute struct {
	Key       string `json:"k"`
	Agent     string `json:"a"`
	Minute    int64  `json:"m"`
	Calls     int    `json:"c"`
	HourCalls int    `json:"hc"`
}

func unixMinute(t time.Time) int64 { return t.Unix() / 60 }

func ms(t time.Time) int64 { return t.UnixMilli() }

func toolSpanLabel(ev protocol.Event) string {
	name := strings.TrimPrefix(ev.ToolName, "mcp__")
	if ev.ToolBrief == "" {
		return name
	}
	return name + " · " + ev.ToolBrief
}

// isWaitTool：前台跑的 ks wait（Codex）是待命，不算长命令。
func isWaitTool(ev protocol.Event) bool { return strings.Contains(ev.ToolBrief, "ks wait") }

// ---------- 规则 ----------

type pendingTool struct {
	key, agent, ref, label string
	start                  time.Time
}

// waitState：会话的 ks wait 是否挂着、这一轮是否在跑。待命 = 挂着 ks wait 且不在跑一轮
// （Claude Code 在后台跑 ks wait，一轮结束才算待命，你插话后又在跑；Codex 在前台跑，挂着就是待命，和会话状态的规则一致）。
type waitState struct {
	open   bool       // ks wait 挂着
	inTurn bool       // 正在跑一轮
	until  *time.Time // 最长等到
}

// LaneTracker 记着已开始、还没结束的工具调用和 ks wait 的状态，把事件翻译成要写的汇总。调用方负责加锁。
type LaneTracker struct {
	pending map[string]pendingTool // 会话键 + tool_use_id
	waits   map[string]*waitState  // 会话键
}

func NewLaneTracker() *LaneTracker {
	return &LaneTracker{pending: map[string]pendingTool{}, waits: map[string]*waitState{}}
}

func (t *LaneTracker) wait(key string) *waitState {
	w := t.waits[key]
	if w == nil {
		w = &waitState{}
		t.waits[key] = w
	}
	return w
}

// parkSpan 开一段待命虚线（ref 用引起它的事件 id）。
func parkSpan(key string, ev protocol.Event, until *time.Time) LaneSpan {
	s := LaneSpan{Key: key, Kind: SpanWait, Ref: ev.ID, Start: ms(ev.Time)}
	if until != nil {
		u := ms(*until)
		s.Until = &u
	}
	return s
}

// LaneEffect 是一条事件对时间线的影响。
type LaneEffect struct {
	Key   string
	Agent string
	At    time.Time
	Call  bool // 这一分钟加一次调用

	Spans     []LaneSpan // 要写入的时段（已结束的长命令、新开始的待命）
	CloseWait bool       // 结束这个会话还开着的待命（At 为结束时间）

	AgentSeen  bool   // 确保子任务在名单里（Label / Type 非空时更新）
	AgentLabel string // SubagentStart 带的名字
	AgentType  string
	AgentEnd   bool // 子任务结束
	AllEnd     bool // 会话结束：全部子任务结束

	Started   string // 新开始的工具调用（tool_use_id），hub 据此在 20 秒后推一次“还在跑的长命令”
	Unmatched string // 结束的工具调用没找到开始（例如 hub 重启过），hub 去事件记录里找开始时间
}

func pkey(key, id string) string { return key + "\x00" + id }

// Apply 处理一条事件。key 是会话键。
func (t *LaneTracker) Apply(key string, ev protocol.Event) LaneEffect {
	e := LaneEffect{Key: key, Agent: ev.AgentID, At: ev.Time}
	switch ev.Name {
	case "PreToolUse":
		e.Call = true
		e.AgentSeen = ev.AgentID != ""
		if ev.ToolUseID != "" && !isWaitTool(ev) {
			t.pending[pkey(key, ev.ToolUseID)] = pendingTool{key: key, agent: ev.AgentID, ref: ev.ToolUseID, label: toolSpanLabel(ev), start: ev.Time}
			e.Started = ev.ToolUseID
		}
	case "PostToolUse":
		if ev.ToolUseID == "" || isWaitTool(ev) {
			break
		}
		p, ok := t.pending[pkey(key, ev.ToolUseID)]
		if !ok {
			e.Unmatched = ev.ToolUseID
			break
		}
		delete(t.pending, pkey(key, ev.ToolUseID))
		if s, ok := closeSpan(p, ev.Time); ok {
			e.Spans = append(e.Spans, s)
		}
	case "SubagentStart":
		if ev.AgentID != "" {
			e.AgentSeen, e.AgentLabel, e.AgentType = true, ev.ToolBrief, ev.AgentType
		}
	case "SubagentStop":
		if ev.AgentID != "" {
			e.AgentEnd = true
			e.Spans = t.flush(key, func(p pendingTool) bool { return p.agent == ev.AgentID }, ev.Time)
		}
	case "UserPromptSubmit":
		// 又开始跑一轮：Claude Code 的 ks wait 还挂在后台，但这段时间不算待命
		e.Agent = ""
		w := t.wait(key)
		w.inTurn = true
		if w.open && !strings.HasPrefix(key, protocol.ToolCodex+":") {
			e.CloseWait = true
		}
	case "Stop", "Interrupt":
		// 一轮结束：主线程上还没收到结束的工具调用（被打断）到这里为止；挂着 ks wait 的从这里开始待命
		e.Agent = ""
		e.Spans = t.flush(key, func(p pendingTool) bool { return p.agent == "" }, ev.Time)
		w := t.wait(key)
		if w.open && w.inTurn && !strings.HasPrefix(key, protocol.ToolCodex+":") {
			e.Spans = append(e.Spans, parkSpan(key, ev, w.until))
		}
		w.inTurn = false
	case protocol.EventWait:
		e.Agent = ""
		e.CloseWait = true // 上一次没收到结束的待命，到这次开始为止
		w := t.wait(key)
		w.open, w.until = true, ev.Until
		if !w.inTurn || strings.HasPrefix(key, protocol.ToolCodex+":") {
			e.Spans = append(e.Spans, parkSpan(key, ev, ev.Until))
		}
	case protocol.EventWaitEnd:
		e.Agent = ""
		e.CloseWait = true
		w := t.wait(key)
		w.open, w.until = false, nil
	case "SessionEnd":
		e.Agent = ""
		e.CloseWait, e.AllEnd = true, true
		delete(t.waits, key)
		e.Spans = t.flush(key, func(pendingTool) bool { return true }, ev.Time)
	}
	return e
}

// Pending 返回还没结束、已经跑了超过 LongTool 的工具调用（时间线上画到现在）。
func (t *LaneTracker) Pending(now time.Time) []LaneSpan {
	out := []LaneSpan{}
	for _, p := range t.pending {
		if now.Sub(p.start) >= LongTool {
			out = append(out, LaneSpan{Key: p.key, Agent: p.agent, Kind: SpanTool, Ref: p.ref, Label: p.label, Start: ms(p.start)})
		}
	}
	return out
}

// Open 返回一个还没结束的工具调用（hub 的 20 秒定时器用）。
func (t *LaneTracker) Open(key, ref string) (LaneSpan, bool) {
	p, ok := t.pending[pkey(key, ref)]
	if !ok {
		return LaneSpan{}, false
	}
	return LaneSpan{Key: p.key, Agent: p.agent, Kind: SpanTool, Ref: p.ref, Label: p.label, Start: ms(p.start)}, true
}

// Expire 丢掉开始太久还没结束的调用（没收到结束通知，避免一直画到现在）。
func (t *LaneTracker) Expire(before time.Time) {
	for k, p := range t.pending {
		if p.start.Before(before) {
			delete(t.pending, k)
		}
	}
}

func (t *LaneTracker) flush(key string, match func(pendingTool) bool, end time.Time) []LaneSpan {
	var out []LaneSpan
	for k, p := range t.pending {
		if p.key != key || !match(p) {
			continue
		}
		delete(t.pending, k)
		if s, ok := closeSpan(p, end); ok {
			out = append(out, s)
		}
	}
	return out
}

func closeSpan(p pendingTool, end time.Time) (LaneSpan, bool) {
	if end.Sub(p.start) < LongTool {
		return LaneSpan{}, false
	}
	e := ms(end)
	return LaneSpan{Key: p.key, Agent: p.agent, Kind: SpanTool, Ref: p.ref, Label: p.label, Start: ms(p.start), End: &e}, true
}

// LongSpan 用找到的开始时间补出长命令（hub 重启后结束的调用）；不够长时 ok 为假。
func LongSpan(key, agent, ref, label string, start, end time.Time) (LaneSpan, bool) {
	return closeSpan(pendingTool{key: key, agent: agent, ref: ref, label: label, start: start}, end)
}

// ---------- 写入 ----------

// LaneWrite 是一条事件写库后的结果，hub 把它推给网页。
type LaneWrite struct {
	Minute *LaneMinute
	Spans  []LaneSpan
	Agents []LaneAgent
	Tokens []LaneTokMinute // token 用量（R-206）
	Colds  []LaneCold
	Shells []protocol.ShellCmd // 后台命令（R-208）
}

// ApplyLane 把一条事件的影响写进汇总表。
func (s *Store) ApplyLane(ctx context.Context, e LaneEffect) (LaneWrite, error) {
	var w LaneWrite
	err := s.tx(ctx, func(tx *sql.Tx) error {
		if e.Call {
			m := unixMinute(e.At)
			var c int
			if err := tx.QueryRowContext(ctx, `INSERT INTO lane_minutes(session_key, agent_id, minute, calls) VALUES(?, ?, ?, 1)
				ON CONFLICT(session_key, agent_id, minute) DO UPDATE SET calls = calls + 1 RETURNING calls`, e.Key, e.Agent, m).Scan(&c); err != nil {
				return err
			}
			var hc int
			if err := tx.QueryRowContext(ctx, `INSERT INTO lane_hours(session_key, agent_id, minute, calls) VALUES(?, ?, ?, 1)
				ON CONFLICT(session_key, agent_id, minute) DO UPDATE SET calls = calls + 1 RETURNING calls`, e.Key, e.Agent, m/60*60).Scan(&hc); err != nil {
				return err
			}
			w.Minute = &LaneMinute{Key: e.Key, Agent: e.Agent, Minute: m, Calls: c, HourCalls: hc}
		}
		if e.CloseWait {
			rows, err := tx.QueryContext(ctx, `UPDATE lane_spans SET end_ms = ? WHERE session_key = ? AND kind = 'wait' AND end_ms IS NULL AND start_ms <= ?
				RETURNING agent_id, ref, label, start_ms, until_ms`, ms(e.At), e.Key, ms(e.At))
			if err != nil {
				return err
			}
			end := ms(e.At)
			for rows.Next() {
				sp := LaneSpan{Key: e.Key, Kind: SpanWait, End: &end}
				var until sql.NullInt64
				if err := rows.Scan(&sp.Agent, &sp.Ref, &sp.Label, &sp.Start, &until); err != nil {
					rows.Close()
					return err
				}
				if until.Valid {
					sp.Until = &until.Int64
				}
				w.Spans = append(w.Spans, sp)
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return err
			}
		}
		for _, sp := range e.Spans {
			if err := saveSpan(ctx, tx, sp); err != nil {
				return err
			}
			w.Spans = append(w.Spans, sp)
		}
		var touched []string
		if e.AgentSeen && e.Agent != "" {
			// 新建的、名字 / 类型变了的才推；同一子任务后续的调用不重复推
			var a string
			err := tx.QueryRowContext(ctx, `INSERT INTO lane_agents(session_key, agent_id, label, agent_type, started_ms) VALUES(?, ?, ?, ?, ?)
				ON CONFLICT(session_key, agent_id) DO UPDATE SET
					label = CASE WHEN excluded.label <> '' THEN excluded.label ELSE label END,
					agent_type = CASE WHEN excluded.agent_type <> '' THEN excluded.agent_type ELSE agent_type END,
					started_ms = MIN(started_ms, excluded.started_ms)
				WHERE (excluded.label <> '' AND excluded.label <> label) OR (excluded.agent_type <> '' AND excluded.agent_type <> agent_type) OR excluded.started_ms < started_ms
				RETURNING agent_id`,
				e.Key, e.Agent, e.AgentLabel, e.AgentType, ms(e.At)).Scan(&a)
			switch {
			case err == nil:
				touched = append(touched, a)
			case !errors.Is(err, sql.ErrNoRows):
				return err
			}
		}
		if e.AgentEnd && e.Agent != "" {
			// 只结束名单里有的：Claude Code 自己的内部小代理（没有类型、不调工具）也发 SubagentStop，不进名单
			res, err := tx.ExecContext(ctx, `UPDATE lane_agents SET ended_ms = ? WHERE session_key = ? AND agent_id = ? AND ended_ms IS NULL`, ms(e.At), e.Key, e.Agent)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n > 0 {
				touched = append(touched, e.Agent)
			}
		}
		if e.AllEnd {
			rows, err := tx.QueryContext(ctx, `UPDATE lane_agents SET ended_ms = ? WHERE session_key = ? AND ended_ms IS NULL RETURNING agent_id`, ms(e.At), e.Key)
			if err != nil {
				return err
			}
			for rows.Next() {
				var a string
				if err := rows.Scan(&a); err != nil {
					rows.Close()
					return err
				}
				touched = append(touched, a)
			}
			rows.Close()
		}
		for _, a := range touched {
			ag, err := getAgent(ctx, tx, e.Key, a)
			if err != nil {
				return err
			}
			w.Agents = append(w.Agents, ag)
		}
		return nil
	})
	return w, err
}

func saveSpan(ctx context.Context, tx *sql.Tx, sp LaneSpan) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO lane_spans(session_key, agent_id, kind, ref, label, start_ms, end_ms, until_ms) VALUES(?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(session_key, kind, ref) DO UPDATE SET end_ms = COALESCE(excluded.end_ms, end_ms), until_ms = COALESCE(excluded.until_ms, until_ms)`,
		sp.Key, sp.Agent, sp.Kind, sp.Ref, sp.Label, sp.Start, sp.End, sp.Until)
	return err
}

func getAgent(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, key, agent string) (LaneAgent, error) {
	a := LaneAgent{Key: key, Agent: agent}
	var end sql.NullInt64
	err := q.QueryRowContext(ctx, `SELECT label, agent_type, started_ms, ended_ms FROM lane_agents WHERE session_key = ? AND agent_id = ?`, key, agent).Scan(&a.Label, &a.Type, &a.Start, &end)
	if end.Valid {
		a.End = &end.Int64
	}
	return a, err
}

// PreToolStart 在事件记录里找某次工具调用的开始（hub 重启后丢了内存里的记录时用），只往前找一天。
func (s *Store) PreToolStart(ctx context.Context, key, toolUseID string, end time.Time) (protocol.Event, bool) {
	var p string
	err := s.db.QueryRowContext(ctx, `SELECT payload FROM events WHERE session_key = ? AND name = 'PreToolUse' AND time >= ? AND time <= ? AND payload LIKE ? ORDER BY time DESC LIMIT 1`,
		key, ts(end.Add(-24*time.Hour)), ts(end), `%"tool_use_id":"`+toolUseID+`"%`).Scan(&p)
	if err != nil {
		return protocol.Event{}, false
	}
	var ev protocol.Event
	return ev, json.Unmarshal([]byte(p), &ev) == nil
}

// ---------- 读取 ----------

// Lanes 返回 [from, to) 内全部会话的时间线数据。hourly 为真时按小时取（近 1 周档），否则每分钟。
func (s *Store) Lanes(ctx context.Context, from, to time.Time, hourly bool) (LaneData, error) {
	out := LaneData{Now: ms(time.Now()), Step: 1, From: unixMinute(from), To: unixMinute(to), Rows: []LaneRow{}, Spans: []LaneSpan{}, Agents: []LaneAgent{}, Sessions: []LaneSession{}}
	table := "lane_minutes"
	if hourly {
		table, out.Step = "lane_hours", 60
		out.From = out.From / 60 * 60
	}
	rows, err := s.db.QueryContext(ctx, `SELECT session_key, agent_id, minute, calls FROM `+table+` WHERE minute >= ? AND minute < ? ORDER BY session_key, agent_id, minute`, out.From, out.To)
	if err != nil {
		return out, err
	}
	var cur *LaneRow
	for rows.Next() {
		var k, a string
		var m int64
		var c int
		if err := rows.Scan(&k, &a, &m, &c); err != nil {
			rows.Close()
			return out, err
		}
		if cur == nil || cur.Key != k || cur.Agent != a {
			out.Rows = append(out.Rows, LaneRow{Key: k, Agent: a})
			cur = &out.Rows[len(out.Rows)-1]
		}
		cur.Minutes = append(cur.Minutes, m)
		cur.Calls = append(cur.Calls, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}
	if out.Tokens, out.Colds, err = s.laneTokens(ctx, out.From, out.To, hourly); err != nil {
		return out, err
	}
	fromMs, toMs := out.From*60000, out.To*60000
	if out.Shells, err = s.laneShells(ctx, fromMs, toMs); err != nil {
		return out, err
	}
	srows, err := s.db.QueryContext(ctx, `SELECT session_key, agent_id, kind, ref, label, start_ms, end_ms, until_ms FROM lane_spans
		WHERE start_ms < ? AND (end_ms IS NULL OR end_ms >= ?) ORDER BY start_ms`, toMs, fromMs)
	if err != nil {
		return out, err
	}
	for srows.Next() {
		var sp LaneSpan
		var end, until sql.NullInt64
		if err := srows.Scan(&sp.Key, &sp.Agent, &sp.Kind, &sp.Ref, &sp.Label, &sp.Start, &end, &until); err != nil {
			srows.Close()
			return out, err
		}
		if end.Valid {
			sp.End = &end.Int64
		}
		if until.Valid {
			sp.Until = &until.Int64
		}
		// 没结束的待命最多画到 until；until 也早于范围起点的不要
		if sp.End == nil && sp.Until != nil && *sp.Until < fromMs {
			continue
		}
		out.Spans = append(out.Spans, sp)
	}
	srows.Close()
	if err := srows.Err(); err != nil {
		return out, err
	}
	// 子任务：在范围内结束的、开始的，或没结束但范围内有调用的（没结束又早没动作的不要）
	arows, err := s.db.QueryContext(ctx, `SELECT a.session_key, a.agent_id, a.label, a.agent_type, a.started_ms, a.ended_ms FROM lane_agents a
		WHERE a.started_ms < ? AND (a.ended_ms >= ? OR (a.ended_ms IS NULL AND (a.started_ms >= ? OR EXISTS (
			SELECT 1 FROM lane_minutes m WHERE m.session_key = a.session_key AND m.agent_id = a.agent_id AND m.minute >= ?))))
		ORDER BY a.started_ms`, toMs, fromMs, fromMs, out.From)
	if err != nil {
		return out, err
	}
	for arows.Next() {
		var a LaneAgent
		var end sql.NullInt64
		if err := arows.Scan(&a.Key, &a.Agent, &a.Label, &a.Type, &a.Start, &end); err != nil {
			arows.Close()
			return out, err
		}
		if end.Valid {
			a.End = &end.Int64
		}
		out.Agents = append(out.Agents, a)
	}
	arows.Close()
	if err := arows.Err(); err != nil {
		return out, err
	}
	qrows, err := s.db.QueryContext(ctx, `SELECT s.key, s.title, s.role, s.tool, COALESCE(s.project_id, ''), COALESCE(p.name, ''), s.cwd, s.background, s.last_active_at, s.ended_at
		FROM sessions s LEFT JOIN projects p ON p.id = s.project_id
		WHERE s.key IN (SELECT session_key FROM lane_hours WHERE minute >= ? AND minute < ? UNION SELECT session_key FROM lane_token_hours WHERE minute >= ? AND minute < ?
			UNION SELECT session_key FROM lane_shells WHERE start_ms < ? AND (end_ms >= ? OR (end_ms IS NULL AND status = 'running')))`,
		out.From/60*60, out.To, out.From/60*60, out.To, toMs, fromMs)
	if err != nil {
		return out, err
	}
	defer qrows.Close()
	for qrows.Next() {
		var x LaneSession
		var active string
		var ended sql.NullString
		if err := qrows.Scan(&x.Key, &x.Title, &x.Role, &x.Tool, &x.ProjectID, &x.ProjectName, &x.Cwd, &x.Background, &active, &ended); err != nil {
			return out, err
		}
		x.LastActiveAt = parseTS(active)
		x.EndedAt = parseNullTS(ended)
		out.Sessions = append(out.Sessions, x)
	}
	return out, qrows.Err()
}

// ---------- 保留与压缩 ----------

// CompactLanes 删掉 LaneKeep 之前的分钟（小时级一直在 lane_hours 里累计着）、时段与子任务名单。返回删掉的分钟行数。
func (s *Store) CompactLanes(ctx context.Context, now time.Time) (int, error) {
	cut := now.Add(-LaneKeep)
	res, err := s.db.ExecContext(ctx, `DELETE FROM lane_minutes WHERE minute < ?`, unixMinute(cut))
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	if _, err := s.db.ExecContext(ctx, `DELETE FROM lane_tokens WHERE minute < ?`, unixMinute(cut)); err != nil {
		return int(n), err
	}
	c := ms(cut)
	if _, err := s.db.ExecContext(ctx, `DELETE FROM lane_colds WHERE at_ms < ?`, c); err != nil {
		return int(n), err
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM lane_shells WHERE start_ms < ? AND (end_ms < ? OR (end_ms IS NULL AND status IN ('lost', 'ended')))`, c, c); err != nil {
		return int(n), err
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM lane_spans WHERE start_ms < ? AND (end_ms < ? OR (end_ms IS NULL AND COALESCE(until_ms, 0) < ?))`, c, c, c); err != nil {
		return int(n), err
	}
	_, err = s.db.ExecContext(ctx, `DELETE FROM lane_agents WHERE started_ms < ? AND (ended_ms IS NULL OR ended_ms < ?)`, c, c)
	return int(n), err
}

// ---------- 补录 ----------

// backfillLanes：旧库第一次升级时，从已有的事件记录算出历史分钟、时段和子任务名单（只做一次）。
func backfillLanes(db *sql.DB) error {
	var done string
	_ = db.QueryRow(`SELECT value FROM settings WHERE key = ?`, laneBackfillKey).Scan(&done)
	if done != "" {
		return nil
	}
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if err := backfillLanesTx(ctx, tx); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO settings(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, laneBackfillKey, now()); err != nil {
		return err
	}
	return tx.Commit()
}

func backfillLanesTx(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT session_key, payload FROM events
		WHERE name IN ('PreToolUse', 'PostToolUse', 'SubagentStart', 'SubagentStop', 'UserPromptSubmit', 'Stop', 'Interrupt', 'KsWait', 'KsWaitEnd', 'SessionEnd') ORDER BY time, id`)
	if err != nil {
		return err
	}
	type mk struct {
		k, a string
		m    int64
	}
	type ak struct{ k, a string }
	type agent struct {
		label, typ string
		start      int64
		end        *int64
	}
	calls := map[mk]int{}
	agents := map[ak]*agent{}
	var spans []LaneSpan
	openWait := map[string]int{} // 会话 → spans 里还开着的待命下标
	tr := NewLaneTracker()
	for rows.Next() {
		var key, p string
		if err := rows.Scan(&key, &p); err != nil {
			rows.Close()
			return err
		}
		var ev protocol.Event
		if json.Unmarshal([]byte(p), &ev) != nil || ev.Time.IsZero() {
			continue
		}
		e := tr.Apply(key, ev)
		at := ms(e.At)
		if e.Call {
			calls[mk{key, e.Agent, unixMinute(e.At)}]++
		}
		if e.CloseWait {
			if i, ok := openWait[key]; ok {
				end := at
				spans[i].End = &end
				delete(openWait, key)
			}
		}
		for _, sp := range e.Spans {
			spans = append(spans, sp)
			if sp.Kind == SpanWait && sp.End == nil {
				openWait[key] = len(spans) - 1
			}
		}
		if e.Agent != "" && (e.AgentSeen || e.AgentEnd) {
			g := agents[ak{key, e.Agent}]
			if g == nil && !e.AgentSeen {
				continue // 名单里没有的子代理结束：内部小代理，不记
			}
			if g == nil {
				g = &agent{start: at}
				agents[ak{key, e.Agent}] = g
			}
			if e.AgentLabel != "" {
				g.label = e.AgentLabel
			}
			if e.AgentType != "" {
				g.typ = e.AgentType
			}
			g.start = min(g.start, at)
			if e.AgentEnd && g.end == nil {
				g.end = &at
			}
		}
		if e.AllEnd {
			for k, g := range agents {
				if k.k == key && g.end == nil {
					end := at
					g.end = &end
				}
			}
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	ins, err := tx.PrepareContext(ctx, `INSERT OR REPLACE INTO lane_minutes(session_key, agent_id, minute, calls) VALUES(?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer ins.Close()
	for k, c := range calls {
		if _, err := ins.ExecContext(ctx, k.k, k.a, k.m, c); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO lane_hours(session_key, agent_id, minute, calls)
		SELECT session_key, agent_id, minute / 60 * 60, SUM(calls) FROM lane_minutes GROUP BY 1, 2, 3`); err != nil {
		return err
	}
	for _, sp := range spans {
		if err := saveSpan(ctx, tx, sp); err != nil {
			return err
		}
	}
	for k, g := range agents {
		if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO lane_agents(session_key, agent_id, label, agent_type, started_ms, ended_ms) VALUES(?, ?, ?, ?, ?, ?)`,
			k.k, k.a, g.label, g.typ, g.start, g.end); err != nil {
			return err
		}
	}
	return nil
}

// AgentLabel 返回会话 key 下子任务 agent 的名字（SubagentStart 时记下的描述）；没有记录时为空（R-191）。
func (s *Store) AgentLabel(ctx context.Context, key, agent string) string {
	if key == "" || agent == "" {
		return ""
	}
	a, err := getAgent(ctx, s.db, key, agent)
	if err != nil {
		return ""
	}
	return a.Label
}
