package store

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/NekroAI/kitestring/internal/protocol"
)

// 状态变更历史（R-153）：需求每次换状态都记一行——从什么到什么、什么时候、谁改的（你 / 哪个会话、什么身份 / 系统）、
// 经由什么（网页、Agent 工具、关联会话、放行、退回修改、计划……）。放行（R-166）、退回修改等已有自己的记录，
// 对应的状态变更在 ref 里写那条记录的编号（D-x），两边互相引用、时间线上合成一个节点。
// 只从上线起记录：没有记录的旧需求照常按已有记录显示。

// 操作者类型。
const (
	ActorUser   = "user"   // 你在网页上操作
	ActorAgent  = "agent"  // Agent 会话通过 MCP 工具 / ks 命令操作
	ActorSystem = "system" // 纸鸢自动做的（例如计划阶段解锁）
)

// Actor 是一次状态变化的操作者与来源，放在 context 里随调用链传给 SetRequirementStatus。
type Actor struct {
	Kind    string // user | agent | system
	Session string // Agent 会话键
	Role    string // 会话当时的身份；空时写入时按会话现在的身份补上
	Via     string // web | agent | create | link | release | withdraw | revise | accept | assign | plan | request
	Ref     string // 关联记录的编号（D-x 放行 / 修改意见，P-x 计划）
	// 搁置原因（R-157）：Kind 是 duplicate / changed / dropped / other，Reason 是一句话；都可空（用户搁置时可选）
	ReasonKind string
	Reason     string
}

type actorKey struct{}

// WithActor 把操作者放进 context；只给出的字段覆盖已有的（空字段沿用外层，例如中间件已标明是你在网页上操作）。
func WithActor(ctx context.Context, a Actor) context.Context {
	cur := ActorFrom(ctx)
	if a.Kind != "" {
		cur.Kind = a.Kind
		if a.Kind != ActorAgent {
			cur.Session, cur.Role = "", ""
		}
	}
	if a.Session != "" {
		cur.Session, cur.Role = a.Session, ""
	}
	if a.Role != "" {
		cur.Role = a.Role
	}
	if a.Via != "" {
		cur.Via = a.Via
	}
	if a.Ref != "" {
		cur.Ref = a.Ref
	}
	if a.ReasonKind != "" {
		cur.ReasonKind = a.ReasonKind
	}
	if a.Reason != "" {
		cur.Reason = a.Reason
	}
	return context.WithValue(ctx, actorKey{}, cur)
}

// ActorFrom 取出 context 里的操作者；没有时是「系统」。
func ActorFrom(ctx context.Context) Actor {
	if a, ok := ctx.Value(actorKey{}).(Actor); ok {
		return a
	}
	return Actor{Kind: ActorSystem}
}

// StatusChange 是一次状态变化。
type StatusChange struct {
	ID              string    `json:"id"`
	RequirementID   string    `json:"requirement_id"`
	RequirementCode string    `json:"requirement_code,omitempty"`
	From            string    `json:"from" doc:"原状态；新建需求时为空"`
	To              string    `json:"to"`
	Actor           string    `json:"actor" enum:"user,agent,system" doc:"操作者：你 / Agent 会话 / 系统"`
	Session         string    `json:"session,omitempty" doc:"操作的会话键（Agent 操作时）"`
	Role            string    `json:"role,omitempty" doc:"会话当时的身份：aide / advisor / reviewer / pm / dev"`
	Via             string    `json:"via" doc:"经由：web 网页 / agent Agent 工具 / create 新建 / link 关联会话 / release 放行 / withdraw 撤回放行 / revise 退回修改 / accept 验收通过 / assign 交出 / plan 计划 / request 用户批准了 Agent 的改状态申请（ref 是申请的 D-x，R-188）"`
	Ref             string    `json:"ref,omitempty" doc:"关联记录的编号：D-x 是这次放行、退回修改、撤回的那条放行；P-x 是计划"`
	ReasonKind      string    `json:"reason_kind,omitempty" enum:"duplicate,changed,dropped,other" doc:"搁置原因的类别：重复 / 方向变了 / 不做了 / 其他（R-157）；只有搁置的变化可能有，旧记录没有"`
	Reason          string    `json:"reason,omitempty" doc:"搁置原因的一句话（R-157）"`
	SessionTitle    string    `json:"session_title,omitempty" doc:"操作时会话的标题快照（R-195）；旧记录没有"`
	At              time.Time `json:"at"`
}

// ShelveKinds 是搁置原因的类别。
var ShelveKinds = []string{"duplicate", "changed", "dropped", "other"}

// NormShelveReason 整理搁置原因：类别不认识的归为 other，一句话去空白并截到 200 字。
func NormShelveReason(kind, reason string) (string, string) {
	kind, reason = strings.TrimSpace(kind), strings.Join(strings.Fields(reason), " ")
	if r := []rune(reason); len(r) > 200 {
		reason = string(r[:200])
	}
	if kind == "" && reason == "" {
		return "", ""
	}
	for _, k := range ShelveKinds {
		if k == kind {
			return kind, reason
		}
	}
	return "other", reason
}

// recordStatus 在事务里记一次状态变化。
func recordStatus(ctx context.Context, t *sql.Tx, reqID, from, to string) error {
	a := ActorFrom(ctx)
	if a.Kind == "" {
		a.Kind = ActorSystem
	}
	title := ""
	if a.Kind == ActorAgent && a.Session != "" {
		var role string
		title, role = sessionSnap(ctx, t, a.Session)
		if a.Role == "" {
			a.Role = role
		}
	}
	if a.Via == "" {
		switch a.Kind {
		case ActorUser:
			a.Via = "web"
		case ActorAgent:
			a.Via = "agent"
		default:
			a.Via = "system"
		}
	}
	rk, rs := "", ""
	switch to {
	case ReqShelved: // 搁置的理由（R-157）
		rk, rs = NormShelveReason(a.ReasonKind, a.Reason)
	case ReqBlocked: // 受阻的原因（R-243）；在等的 R-x / D-x 记在 ref
		_, rs = NormShelveReason("", a.Reason)
	}
	_, err := t.ExecContext(ctx, `INSERT INTO status_changes(id, requirement_id, from_status, to_status, actor, session_key, role, via, ref, reason_kind, reason, session_title, at) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		protocol.NewID(), reqID, from, to, a.Kind, a.Session, a.Role, a.Via, a.Ref, rk, rs, title, now())
	return err
}

// LinkLastStatusChange 给需求最近一次还没关联记录的状态变化补上关联记录编号（例如先改状态、后写的退回修改）。
func (s *Store) LinkLastStatusChange(ctx context.Context, reqID, ref string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE status_changes SET ref = ? WHERE id = (SELECT id FROM status_changes WHERE requirement_id = ? AND ref = '' ORDER BY at DESC, rowid DESC LIMIT 1)`, ref, reqID)
	return err
}

// StatusQuery 是状态变更的查询条件；都为空时返回全部。
type StatusQuery struct {
	RequirementID string
	ProjectID     string
	Since, Until  time.Time
	Limit         int
}

// ListStatusChanges 按时间正序列出状态变化。
func (s *Store) ListStatusChanges(ctx context.Context, q StatusQuery) ([]StatusChange, error) {
	sqlq := `SELECT c.id, c.requirement_id, r.seq, c.from_status, c.to_status, c.actor, c.session_key, c.role, c.via, c.ref, c.reason_kind, c.reason, c.session_title, c.at
		FROM status_changes c JOIN requirements r ON r.id = c.requirement_id WHERE 1 = 1`
	var args []any
	if q.RequirementID != "" {
		sqlq += ` AND c.requirement_id = ?`
		args = append(args, q.RequirementID)
	}
	if q.ProjectID != "" {
		sqlq += ` AND r.project_id = ?`
		args = append(args, q.ProjectID)
	}
	if !q.Since.IsZero() {
		sqlq += ` AND c.at >= ?`
		args = append(args, ts(q.Since))
	}
	if !q.Until.IsZero() {
		sqlq += ` AND c.at <= ?`
		args = append(args, ts(q.Until))
	}
	sqlq += ` ORDER BY c.at, c.rowid`
	if q.Limit > 0 {
		sqlq += ` LIMIT ?`
		args = append(args, q.Limit)
	}
	rows, err := s.db.QueryContext(ctx, sqlq, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []StatusChange{}
	for rows.Next() {
		var c StatusChange
		var seq int
		var at string
		if err := rows.Scan(&c.ID, &c.RequirementID, &seq, &c.From, &c.To, &c.Actor, &c.Session, &c.Role, &c.Via, &c.Ref, &c.ReasonKind, &c.Reason, &c.SessionTitle, &at); err != nil {
			return nil, err
		}
		c.RequirementCode = reqCode(seq)
		c.At = parseTS(at)
		out = append(out, c)
	}
	return out, rows.Err()
}
