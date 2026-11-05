package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	"github.com/NekroAI/kitestring/internal/protocol"
)

// 会话详情页按轮的时间线（R-273）用到的读写：每轮回复、按时间段取事件、这个会话的纸鸢记录。
// 纸鸢动作只从明确记录来（设计原则第 11 条）：需求状态变化、证据、决策 / 团队消息 / 留言、派子任务、简报、交接；不从工具参数猜。

// WebReplyQuestion 是网页上直接给会话发的话（POST /api/sessions/{key}/reply）记成留言时的标题，据此在时间线上标「网页」。
const WebReplyQuestion = "回复你的最后一条消息"

// SaveTurnReply 存下一轮结束时的回复（按 Stop 事件一行，重复上报不重复存）。
func (s *Store) SaveTurnReply(ctx context.Context, eventID, key, turnID, text string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO turn_replies(event_id, session_key, turn_id, at, text) VALUES(?, ?, ?, ?, ?) ON CONFLICT(event_id) DO NOTHING`,
		eventID, key, turnID, ts(at), text)
	return err
}

// TurnReplies 返回会话在 [from, to) 内各 Stop 事件的回复，键是事件 ID。from / to 为零值表示不限。
func (s *Store) TurnReplies(ctx context.Context, key string, from, to time.Time) (map[string]string, error) {
	q, args := `SELECT event_id, text FROM turn_replies WHERE session_key = ?`, []any{key}
	q, args = timeRange(q, args, "at", from, to)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, text string
		if err := rows.Scan(&id, &text); err != nil {
			return nil, err
		}
		out[id] = text
	}
	return out, rows.Err()
}

func timeRange(q string, args []any, col string, from, to time.Time) (string, []any) {
	if !from.IsZero() {
		q += ` AND ` + col + ` >= ?`
		args = append(args, ts(from))
	}
	if !to.IsZero() {
		q += ` AND ` + col + ` < ?`
		args = append(args, ts(to))
	}
	return q, args
}

// TurnStarts 返回会话主线程（不含子任务）在 before 之前最近 limit 次提问（UserPromptSubmit）的时间，新的在前。before 为零值表示不限。
func (s *Store) TurnStarts(ctx context.Context, key string, before time.Time, limit int) ([]time.Time, error) {
	q, args := `SELECT time FROM events WHERE session_key = ? AND name = 'UserPromptSubmit' AND COALESCE(json_extract(payload, '$.agent_id'), '') = ''`, []any{key}
	q, args = timeRange(q, args, "time", time.Time{}, before)
	q += ` ORDER BY time DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []time.Time
	for rows.Next() {
		var at string
		if err := rows.Scan(&at); err != nil {
			return nil, err
		}
		out = append(out, parseTS(at))
	}
	return out, rows.Err()
}

// EventsBetween 返回会话在 [from, to) 内的事件（时间正序），不含后台任务快照和工具结果。
func (s *Store) EventsBetween(ctx context.Context, key string, from, to time.Time) ([]protocol.Event, error) {
	q, args := `SELECT payload FROM events WHERE session_key = ? AND name NOT IN ('KsTasks', 'PostToolUse')`, []any{key}
	q, args = timeRange(q, args, "time", from, to)
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY time`, args...)
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

// SessionText 是会话全部的提问与回复文字（主线程），用来算对象索引里「提到这条的轮」。
type SessionText struct {
	TurnTime time.Time // 提问时间（回复为 Stop 时间）
	Prompt   bool
	EventID  string
	Text     string
}

// SessionTexts 返回会话主线程的提问与 Stop 回复（含兜底的摘要用回复），时间正序。
func (s *Store) SessionTexts(ctx context.Context, key string) ([]SessionText, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT e.id, e.name, e.time, COALESCE(json_extract(e.payload, '$.prompt'), ''), COALESCE(json_extract(e.payload, '$.source'), ''), COALESCE(r.text, json_extract(e.payload, '$.last_reply'), '')
		FROM events e LEFT JOIN turn_replies r ON r.event_id = e.id
		WHERE e.session_key = ? AND e.name IN ('UserPromptSubmit', 'Stop') AND COALESCE(json_extract(e.payload, '$.agent_id'), '') = '' ORDER BY e.time`, key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SessionText
	for rows.Next() {
		var id, name, at, prompt, source, reply string
		if err := rows.Scan(&id, &name, &at, &prompt, &source, &reply); err != nil {
			return nil, err
		}
		if name == "UserPromptSubmit" {
			if source == protocol.SourceAuto {
				prompt = ""
			}
			out = append(out, SessionText{TurnTime: parseTS(at), Prompt: true, EventID: id, Text: prompt})
		} else {
			out = append(out, SessionText{TurnTime: parseTS(at), EventID: id, Text: reply})
		}
	}
	return out, rows.Err()
}

// Act 是会话的一条纸鸢记录（R-273）：需求状态变化、证据、提决策、团队消息、留言、修改、放行、派子任务、简报、交接、收到的消息。
type Act struct {
	ID   string    `json:"id"`
	Kind string    `json:"kind"`
	At   time.Time `json:"at"`
	// 需求 / 决策
	Req       string `json:"req,omitempty"`
	ReqTitle  string `json:"req_title,omitempty"`
	ReqStatus string `json:"req_status,omitempty"`
	Dec       string `json:"dec,omitempty"`
	DecTitle  string `json:"dec_title,omitempty"`
	DecStatus string `json:"dec_status,omitempty"`
	// 状态变化
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
	Via  string `json:"via,omitempty"`
	// 一句话正文：消息 / 留言原文、证据结论、简报一句话、子任务名字
	Text string `json:"text,omitempty"`
	// 对方：团队消息的收件 / 发件会话，收到的消息的来源
	Peer     string `json:"peer,omitempty"`
	PeerRole string `json:"peer_role,omitempty"`
	// Agent 是发出这条记录的子任务（团队消息带 from_agent）
	Agent string `json:"agent,omitempty"`
	FYI   bool   `json:"fyi,omitempty"`
	// Note：额外一句（撤回原因、作废、子任务类型等）
	Note string `json:"note,omitempty"`
	// 收到的消息（送进这个会话的）：送达时间与送达的轮；Web 为网页上直接发的话
	DeliveredAt   *time.Time `json:"delivered_at,omitempty"`
	DeliveredTurn string     `json:"-"`
	Status        string     `json:"-"`
	Web           bool       `json:"web,omitempty"`
	EndedAt       *time.Time `json:"ended_at,omitempty"`
}

// 记录的种类。
const (
	ActStatus   = "status"   // 改需求状态（via = link 是关联开始做）
	ActEvidence = "evidence" // 交证据
	ActRaise    = "raise"    // 提决策
	ActTellOut  = "tell_out" // 发团队消息
	ActComment  = "comment"  // 在需求下留言
	ActEdit     = "edit"     // 改需求
	ActRelease  = "release"  // 放行草稿
	ActAgent    = "agent"    // 派子任务
	ActBrief    = "brief"    // 更新简报
	ActHandoff  = "handoff"  // 写交接
	ActTakeover = "takeover" // 接任
	ActInbox    = "inbox"    // 收到的消息（团队消息、你的答复、留言、交给它的需求……）
)

// SessionActs 列出会话的全部纸鸢记录（时间正序）。只读明确记录。
func (s *Store) SessionActs(ctx context.Context, key string) ([]Act, error) {
	var out []Act
	// 需求状态变化
	rows, err := s.db.QueryContext(ctx, `SELECT c.id, c.from_status, c.to_status, c.via, c.at, r.seq, r.title, r.status FROM status_changes c JOIN requirements r ON r.id = c.requirement_id WHERE c.session_key = ?`, key)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var a Act
		var at string
		var seq int
		if err := rows.Scan(&a.ID, &a.From, &a.To, &a.Via, &at, &seq, &a.ReqTitle, &a.ReqStatus); err != nil {
			rows.Close()
			return nil, err
		}
		a.Kind, a.At, a.Req = ActStatus, parseTS(at), reqCode(seq)
		out = append(out, a)
	}
	rows.Close()
	// 证据
	rows, err = s.db.QueryContext(ctx, `SELECT e.id, e.summary, e.payload, e.voided, e.created_at, r.seq, r.title, r.status FROM evidence e JOIN requirements r ON r.id = e.requirement_id WHERE e.session_key = ?`, key)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var a Act
		var summary, payload, at string
		var voided bool
		var seq int
		if err := rows.Scan(&a.ID, &summary, &payload, &voided, &at, &seq, &a.ReqTitle, &a.ReqStatus); err != nil {
			rows.Close()
			return nil, err
		}
		a.Kind, a.At, a.Req = ActEvidence, parseTS(at), reqCode(seq)
		var p struct {
			Verdict string `json:"verdict"`
		}
		if payload != "" {
			_ = json.Unmarshal([]byte(payload), &p)
		}
		a.Text = p.Verdict
		if a.Text == "" {
			a.Text = oneLine(summary)
		}
		if voided {
			a.Note = "voided"
		}
		out = append(out, a)
	}
	rows.Close()
	// 决策、团队消息、留言、修改、放行（这个会话发出的），以及送进这个会话的消息
	decs, err := s.queryDecs(ctx, decSelect+` WHERE (x.origin_session = ? AND x.kind = ?) OR (x.assigned_by = ? AND x.kind IN (?, ?, ?, ?)) OR (x.session_key = ? AND x.kind NOT IN (?, ?, ?))`,
		key, KindDecision, key, KindTell, KindComment, KindEdit, KindRelease, key, KindRelease, KindEdit, KindComment)
	if err != nil {
		return nil, err
	}
	peers := map[string][2]string{}
	peer := func(k string) (string, string) {
		if k == "" {
			return "", ""
		}
		if v, ok := peers[k]; ok {
			return v[0], v[1]
		}
		var title, role, prompt string
		_ = s.db.QueryRowContext(ctx, `SELECT title, role, last_prompt FROM sessions WHERE key = ?`, k).Scan(&title, &role, &prompt)
		if title == "" {
			title = prompt
		}
		peers[k] = [2]string{title, role}
		return title, role
	}
	for _, d := range decs {
		a := Act{ID: d.ID, At: d.CreatedAt, Agent: d.FromAgent, FYI: d.FYI}
		a.Req, a.ReqTitle = d.RequirementCode, d.RequirementTitle
		mine := d.OriginSession == key && d.Kind == KindDecision || d.AssignedBy == key && (d.Kind == KindTell || d.Kind == KindComment || d.Kind == KindEdit || d.Kind == KindRelease)
		switch {
		case mine && d.Kind == KindDecision:
			a.Kind, a.Dec, a.DecTitle, a.DecStatus = ActRaise, d.Code, decTitle(d), d.Status
			if d.Status == DecClosed {
				a.Note = d.CloseReason
				if a.Note == "" {
					a.Note = "withdrawn"
				}
			}
			out = append(out, a)
			if d.SessionKey != key || (d.Status != DecDelivered && d.Status != DecAnswered) {
				continue
			}
			// 你的答复送回这个会话：另记一条收到的消息
			b := Act{ID: d.ID + ":answer", Kind: ActInbox, Via: KindDecision, Dec: d.Code, DecTitle: decTitle(d), DecStatus: d.Status, Req: a.Req, ReqTitle: a.ReqTitle, Text: d.Answer, Status: d.Status, DeliveredAt: d.DeliveredAt, DeliveredTurn: d.DeliveredTurn, PeerRole: "user"}
			if d.AnsweredAt != nil {
				b.At = *d.AnsweredAt
			}
			out = append(out, b)
		case mine:
			a.Kind = map[string]string{KindTell: ActTellOut, KindComment: ActComment, KindEdit: ActEdit, KindRelease: ActRelease}[d.Kind]
			a.Text = d.Answer
			if d.Kind == KindTell {
				a.Peer, a.PeerRole = peer(d.SessionKey)
			}
			out = append(out, a)
		case d.Kind == KindDecision:
			// 别的会话提的决策转交给它：只记收到的答复
			if d.Status != DecDelivered && d.Status != DecAnswered {
				continue
			}
			a.Kind, a.Via, a.Dec, a.DecTitle, a.DecStatus, a.Text, a.Status, a.DeliveredAt, a.DeliveredTurn, a.PeerRole = ActInbox, KindDecision, d.Code, decTitle(d), d.Status, d.Answer, d.Status, d.DeliveredAt, d.DeliveredTurn, "user"
			if d.AnsweredAt != nil {
				a.At = *d.AnsweredAt
			}
			out = append(out, a)
		default:
			// 送进这个会话的消息：团队消息、你的留言 / 网页上发的话、交给它的需求、计划进展……
			if d.Status == DecPending {
				continue
			}
			a.Kind, a.Via, a.Status, a.DeliveredAt, a.DeliveredTurn = ActInbox, d.Kind, d.Status, d.DeliveredAt, d.DeliveredTurn
			a.Text = d.Answer
			if a.Text == "" {
				a.Text = d.Question
			}
			if d.Kind == KindTell {
				a.Peer, a.PeerRole = peer(d.AssignedBy)
				if a.PeerRole == "" {
					a.PeerRole = d.FromRole
				}
			} else {
				a.PeerRole = "user"
			}
			a.Web = d.Kind == KindNote && d.RequirementID == "" && d.Question == WebReplyQuestion
			out = append(out, a)
		}
	}
	// 派子任务
	rows, err = s.db.QueryContext(ctx, `SELECT agent_id, label, agent_type, started_ms, ended_ms FROM lane_agents WHERE session_key = ?`, key)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var a Act
		var started int64
		var ended sql.NullInt64
		if err := rows.Scan(&a.ID, &a.Text, &a.Note, &started, &ended); err != nil {
			rows.Close()
			return nil, err
		}
		a.Kind, a.At, a.Agent = ActAgent, time.UnixMilli(started).UTC(), a.ID
		a.ID = "agent:" + a.ID
		if ended.Valid {
			e := time.UnixMilli(ended.Int64).UTC()
			a.EndedAt = &e
		}
		out = append(out, a)
	}
	rows.Close()
	// 简报（项目上只留最新一份：是它写的才记）
	rows, err = s.db.QueryContext(ctx, `SELECT id, brief_headline, brief, brief_at FROM projects WHERE brief_by = ? AND brief_at <> ''`, key)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, head, text, at string
		if err := rows.Scan(&id, &head, &text, &at); err != nil {
			rows.Close()
			return nil, err
		}
		if head == "" {
			head = oneLine(text)
		}
		out = append(out, Act{ID: "brief:" + id, Kind: ActBrief, At: parseTS(at), Text: head})
	}
	rows.Close()
	// 交接
	hs, err := s.HandoffsOf(ctx, key)
	if err != nil {
		return nil, err
	}
	for _, h := range hs {
		if h.FromSession == key && h.WrittenAt != nil {
			out = append(out, Act{ID: "handoff:" + h.ID, Kind: ActHandoff, At: *h.WrittenAt, Text: oneLine(h.Doing), PeerRole: h.Role})
		}
		if h.TakenBy == key && h.TakenAt != nil {
			out = append(out, Act{ID: "takeover:" + h.ID, Kind: ActTakeover, At: *h.TakenAt, Peer: h.FromTitle, PeerRole: h.Role})
		}
	}
	sortActs(out)
	return out, nil
}

func sortActs(a []Act) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j].At.Before(a[j-1].At); j-- {
			a[j], a[j-1] = a[j-1], a[j]
		}
	}
}

func decTitle(d Decision) string {
	if d.ShortTitle != "" {
		return d.ShortTitle
	}
	return oneLine(d.Question)
}

// oneLine：一段文字的第一行（去掉 Markdown 标题 / 列表记号），最长 120 字。
func oneLine(s string) string {
	s = strings.TrimLeft(strings.TrimSpace(s), "#*- ")
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	if r := []rune(s); len(r) > 120 {
		s = string(r[:120]) + "…"
	}
	return s
}

// AgentModels 返回会话各子任务用量最多的模型（按 token 用量记录，R-267 同一口径），键是 agent_id。
func (s *Store) AgentModels(ctx context.Context, key string) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT agent_id, model, SUM(inp + cw + cr + out) AS n FROM (
		SELECT agent_id, model, inp, cw, cr, out FROM lane_tokens WHERE session_key = ? AND agent_id <> ''
		UNION ALL SELECT agent_id, model, inp, cw, cr, out FROM lane_token_hours WHERE session_key = ? AND agent_id <> ''
	) GROUP BY agent_id, model ORDER BY n`, key, key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, model string
		var n int64
		if err := rows.Scan(&id, &model, &n); err != nil {
			return nil, err
		}
		if model != "" {
			out[id] = model // 按用量升序，最后写的是用得最多的
		}
	}
	return out, rows.Err()
}

// RunningShells 返回会话还在跑的后台命令（R-208 的记录）：会话详情页头部列名字和时长，不列命令（R-273）。
func (s *Store) RunningShells(ctx context.Context, key string) ([]protocol.ShellCmd, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+shellCols+` FROM lane_shells WHERE session_key = ? AND status = 'running' ORDER BY start_ms`, key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []protocol.ShellCmd{}
	for rows.Next() {
		c, err := scanShell(rows)
		if err != nil {
			return out, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
