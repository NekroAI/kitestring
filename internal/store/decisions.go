package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/NekroAI/kitestring/internal/protocol"
)

// 决策状态（requirements.md 4.2）。
const (
	DecPending   = "pending"   // 待决策
	DecAnswered  = "answered"  // 已决策待接收
	DecDelivered = "delivered" // 已接收
	DecClosed    = "closed"    // 已关闭（不再注入）
)

// 决策类型：agent 提出的决策；用户对需求的修改意见、留言（创建即为“已决策待接收”，沿用同一条送达通道）。
// 修改意见会把需求退回进行中并开启新一轮；留言只送达，不改需求状态。
const (
	KindDecision = "decision"
	KindFeedback = "feedback"
	KindNote     = "note"
	KindAssign   = "assign" // 你把需求交给某个会话开工
	KindPlan     = "plan"   // 计划进展（开工、阶段通过 / 退回、未获批准），送达计划的负责会话
	KindQueue    = "queue"  // 会话空闲时整批送达的待开始需求（R-66）
)

// Decision 是一条待用户决策的问题。
type Decision struct {
	ID               string      `json:"id"`
	Code             string      `json:"code"`
	SessionKey       string      `json:"session_key,omitempty"`
	OriginSession    string      `json:"origin_session,omitempty"`
	SessionState     string      `json:"session_state,omitempty"` // 目标会话当前状态
	DeviceName       string      `json:"device_name,omitempty"`
	ProjectID        string      `json:"project_id,omitempty"`
	ProjectName      string      `json:"project_name,omitempty"`
	RequirementID    string      `json:"requirement_id,omitempty"`
	RequirementCode  string      `json:"requirement_code,omitempty"`
	PlanID           string      `json:"plan_id,omitempty"`
	PlanCode         string      `json:"plan_code,omitempty"`
	Question         string      `json:"question"`
	Context          string      `json:"context"`
	Options          []string    `json:"options"`
	OptionFiles      [][]FileRef `json:"option_files"`  // 每个选项的效果图（与 options 一一对应），用于方案对比
	Approval         bool        `json:"approval"`      // 请你批准（选项固定为“批准 / 拒绝”，两种都可以附言）
	ContextFiles     []FileRef   `json:"context_files"` // 问题本身附带的图（例如待批准的方案图）
	Recommendation   string      `json:"recommendation"`
	ShortTitle       string      `json:"short_title"` // 模型生成的短标题
	Kind             string      `json:"kind" enum:"decision,feedback,note,assign,plan,queue"`
	AnswerFiles      []FileRef   `json:"answer_files"`
	RequirementTitle string      `json:"requirement_title,omitempty"`
	Status           string      `json:"status"`
	Answer           string      `json:"answer"`
	CreatedAt        time.Time   `json:"created_at"`
	AnsweredAt       *time.Time  `json:"answered_at,omitempty"`
	DeliveredAt      *time.Time  `json:"delivered_at,omitempty"`
	DeliveredTurn    string      `json:"delivered_turn,omitempty"`
}

func decCode(seq int) string { return "D-" + strconv.Itoa(seq) }

const decSelect = `SELECT x.id, x.seq, COALESCE(x.session_key, ''), COALESCE(x.origin_session, ''), COALESCE(s.state, ''), COALESCE(dv.name, ''),
	COALESCE(x.project_id, ''), COALESCE(p.name, ''), COALESCE(x.requirement_id, ''), COALESCE(r.seq, 0), COALESCE(r.title, ''),
	x.question, x.context, x.options, x.option_files, x.approval, x.context_files, x.recommendation, x.short_title, x.kind, x.answer_files, x.status, x.answer, x.created_at, x.answered_at, x.delivered_at, x.delivered_turn,
	COALESCE(x.plan_id, ''), COALESCE(pl.seq, 0)
	FROM decisions x
	LEFT JOIN sessions s ON s.key = x.session_key
	LEFT JOIN devices dv ON dv.id = s.device_id
	LEFT JOIN projects p ON p.id = x.project_id
	LEFT JOIN requirements r ON r.id = x.requirement_id
	LEFT JOIN plans pl ON pl.id = x.plan_id`

func scanDec(row interface{ Scan(...any) error }) (Decision, error) {
	var d Decision
	var seq, rseq, pseq int
	var options, optFiles, ctxFiles, created, files string
	var answered, delivered sql.NullString
	err := row.Scan(&d.ID, &seq, &d.SessionKey, &d.OriginSession, &d.SessionState, &d.DeviceName, &d.ProjectID, &d.ProjectName, &d.RequirementID, &rseq, &d.RequirementTitle,
		&d.Question, &d.Context, &options, &optFiles, &d.Approval, &ctxFiles, &d.Recommendation, &d.ShortTitle, &d.Kind, &files, &d.Status, &d.Answer, &created, &answered, &delivered, &d.DeliveredTurn,
		&d.PlanID, &pseq)
	d.Code = decCode(seq)
	if pseq > 0 {
		d.PlanCode = planCode(pseq)
	}
	d.AnswerFiles = parseFiles(files)
	if rseq > 0 {
		d.RequirementCode = reqCode(rseq)
	}
	d.Options = []string{}
	_ = json.Unmarshal([]byte(options), &d.Options)
	d.OptionFiles = [][]FileRef{}
	_ = json.Unmarshal([]byte(optFiles), &d.OptionFiles)
	d.ContextFiles = parseFiles(ctxFiles)
	d.CreatedAt = parseTS(created)
	d.AnsweredAt = parseNullTS(answered)
	d.DeliveredAt = parseNullTS(delivered)
	return d, err
}

// CreateDecision 新建待决策项。
func (s *Store) CreateDecision(ctx context.Context, d Decision) (Decision, error) {
	if strings.TrimSpace(d.Question) == "" {
		return d, errors.New("问题不能为空")
	}
	opts, _ := json.Marshal(d.Options)
	id := protocol.NewID()
	err := s.tx(ctx, func(t *sql.Tx) error {
		var seq int
		if err := t.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) + 1 FROM decisions`).Scan(&seq); err != nil {
			return err
		}
		n := now()
		if d.Kind == KindFeedback || d.Kind == KindNote || d.Kind == KindAssign || d.Kind == KindPlan || d.Kind == KindQueue {
			// 修改意见：用户写下即为答复，直接进入“已决策待接收”。
			_, err := t.ExecContext(ctx, `INSERT INTO decisions(id, seq, session_key, origin_session, project_id, requirement_id, plan_id, question, context, options, recommendation, kind, status, answer, answer_files, answered_at, created_at) VALUES(?, ?, ?, ?, ?, ?, ?, ?, '', '[]', '', ?, ?, ?, ?, ?, ?)`,
				id, seq, nullStr(d.SessionKey), nullStr(d.SessionKey), nullStr(d.ProjectID), nullStr(d.RequirementID), nullStr(d.PlanID), d.Question, d.Kind, DecAnswered, d.Answer, filesJSON(d.AnswerFiles), n, n)
			return err
		}
		optFiles := d.OptionFiles
		if optFiles == nil {
			optFiles = [][]FileRef{}
		}
		of, _ := json.Marshal(optFiles)
		_, err := t.ExecContext(ctx, `INSERT INTO decisions(id, seq, session_key, origin_session, project_id, requirement_id, question, context, options, option_files, approval, context_files, recommendation, status, created_at) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, seq, nullStr(d.SessionKey), nullStr(d.SessionKey), nullStr(d.ProjectID), nullStr(d.RequirementID), d.Question, d.Context, string(opts), string(of), d.Approval, filesJSON(d.ContextFiles), d.Recommendation, DecPending, n)
		return err
	})
	if err != nil {
		return d, err
	}
	return s.GetDecision(ctx, id)
}

// GetDecision 按 ID 或 D-编号读取决策。
func (s *Store) GetDecision(ctx context.Context, ref string) (Decision, error) {
	var row *sql.Row
	if seq, ok := parseRef(ref, "D"); ok {
		row = s.db.QueryRowContext(ctx, decSelect+` WHERE x.seq = ?`, seq)
	} else {
		row = s.db.QueryRowContext(ctx, decSelect+` WHERE x.id = ?`, ref)
	}
	d, err := scanDec(row)
	if errors.Is(err, sql.ErrNoRows) {
		return d, ErrNotFound
	}
	return d, err
}

// ListDecisions 列出决策，statuses 为空表示全部。
func (s *Store) ListDecisions(ctx context.Context, statuses []string, limit int) ([]Decision, error) {
	q := decSelect
	var args []any
	if len(statuses) > 0 {
		q += ` WHERE x.status IN (?` + strings.Repeat(", ?", len(statuses)-1) + `)`
		for _, st := range statuses {
			args = append(args, st)
		}
	}
	q += ` ORDER BY x.seq DESC LIMIT ?`
	args = append(args, limit)
	return s.queryDecs(ctx, q, args...)
}

func (s *Store) queryDecs(ctx context.Context, q string, args ...any) ([]Decision, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Decision{}
	for rows.Next() {
		d, err := scanDec(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// AnswerDecision 记录用户答复，状态进入“已决策待接收”。已接收的决策允许改答，会重新进入待接收。
func (s *Store) AnswerDecision(ctx context.Context, ref, answer string, files []FileRef) (Decision, error) {
	d, err := s.GetDecision(ctx, ref)
	if err != nil {
		return d, err
	}
	if d.Status == DecClosed {
		return d, fmt.Errorf("%s 已关闭", d.Code)
	}
	if strings.TrimSpace(answer) == "" && len(files) == 0 {
		return d, errors.New("答复不能为空")
	}
	_, err = s.db.ExecContext(ctx, `UPDATE decisions SET status = ?, answer = ?, answer_files = ?, answered_at = ?, delivered_at = NULL, delivered_turn = '' WHERE id = ?`, DecAnswered, answer, filesJSON(files), now(), d.ID)
	if err != nil {
		return d, err
	}
	return s.GetDecision(ctx, d.ID)
}

// MarkDelivered 标记决策已注入（幂等）。
func (s *Store) MarkDelivered(ctx context.Context, id, sessionKey, turn string, at time.Time) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE decisions SET status = ?, delivered_at = ?, delivered_turn = ? WHERE id = ? AND status = ? AND session_key = ?`,
		DecDelivered, ts(at), turn, id, DecAnswered, sessionKey)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ReassignDecision 把决策转交给另一个会话（来源会话已结束时由用户操作，D15）。
func (s *Store) ReassignDecision(ctx context.Context, ref, sessionKey string) (Decision, error) {
	d, err := s.GetDecision(ctx, ref)
	if err != nil {
		return d, err
	}
	status := d.Status
	if status == DecDelivered {
		status = DecAnswered // 转交后需要重新注入
	}
	_, err = s.db.ExecContext(ctx, `UPDATE decisions SET session_key = ?, status = ? WHERE id = ?`, sessionKey, status, d.ID)
	if err != nil {
		return d, err
	}
	return s.GetDecision(ctx, d.ID)
}

// ResendDecision 让已接收的决策重新进入待接收（Web 上手动重发）。
func (s *Store) ResendDecision(ctx context.Context, ref string) (Decision, error) {
	d, err := s.GetDecision(ctx, ref)
	if err != nil {
		return d, err
	}
	if d.Status != DecDelivered {
		return d, fmt.Errorf("%s 尚未接收，无需重发", d.Code)
	}
	_, err = s.db.ExecContext(ctx, `UPDATE decisions SET status = ? WHERE id = ?`, DecAnswered, d.ID)
	if err != nil {
		return d, err
	}
	return s.GetDecision(ctx, d.ID)
}

// SetDecisionTitle 写入模型生成的短标题。
func (s *Store) SetDecisionTitle(ctx context.Context, id, title string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE decisions SET short_title = ? WHERE id = ?`, title, id)
	return err
}

// DecisionsBetween 返回时间窗内提出或作答的决策，供时段回顾使用。
func (s *Store) DecisionsBetween(ctx context.Context, from, to time.Time) ([]Decision, error) {
	return s.queryDecs(ctx, decSelect+` WHERE (x.created_at >= ? AND x.created_at <= ?) OR (x.answered_at >= ? AND x.answered_at <= ?) ORDER BY x.seq`, ts(from), ts(to), ts(from), ts(to))
}

// CloseDecision 关闭决策，不再注入。
func (s *Store) CloseDecision(ctx context.Context, ref string) (Decision, error) {
	d, err := s.GetDecision(ctx, ref)
	if err != nil {
		return d, err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE decisions SET status = ? WHERE id = ?`, DecClosed, d.ID)
	if err != nil {
		return d, err
	}
	return s.GetDecision(ctx, d.ID)
}

// DecisionsForDevice 返回目标会话位于该设备的待注入决策：“已决策待接收”的全部，
// 以及 since 之后已接收的（供会话恢复 / 压缩时补注入，D15）。
// 不排除已结束的会话：同一会话被恢复（resume，session_id 不变）时仍应收到答复。
func (s *Store) DecisionsForDevice(ctx context.Context, deviceID string, since time.Time) ([]Decision, error) {
	return s.queryDecs(ctx, decSelect+` WHERE s.device_id = ? AND (x.status = ? OR (x.status = ? AND x.delivered_at >= ?)) ORDER BY x.seq`,
		deviceID, DecAnswered, DecDelivered, ts(since))
}
