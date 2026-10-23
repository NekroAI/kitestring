package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/NekroAI/kitestring/internal/protocol"
)

// 计划状态（R-43）。
const (
	PlanDraft   = "draft"   // 还没开始；Agent 拆的计划在这里等你批准
	PlanActive  = "active"  // 进行中
	PlanDone    = "done"    // 最后一个阶段已通过
	PlanShelved = "shelved" // 搁置 / 未获批准
)

// 阶段状态：未解锁 → 进行中 → 待阶段验收 → 已通过。
const (
	StageLocked = "locked"
	StageActive = "active"
	StageReview = "review"
	StagePassed = "passed"
)

// Plan 是一个计划：若干有顺序的阶段，阶段内可以放多条需求。
type Plan struct {
	ID           string    `json:"id"`
	Code         string    `json:"code"`
	ProjectID    string    `json:"project_id,omitempty"`
	ProjectName  string    `json:"project_name,omitempty"`
	Title        string    `json:"title"`
	Goal         string    `json:"goal"`
	OwnerSession string    `json:"owner_session"`
	Status       string    `json:"status" enum:"draft,active,done,shelved"`
	CreatedBy    string    `json:"created_by"`
	Paused       bool      `json:"paused"`        // 已通过阶段里有需求被重开，修好前不能继续往下
	Current      int       `json:"current"`       // 当前阶段序号（第一个未通过的阶段）；全部通过为 0
	Stages       []Stage   `json:"stages"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// Stage 是计划里的一个阶段。
type Stage struct {
	ID           string     `json:"id"`
	Seq          int        `json:"seq"`
	Title        string     `json:"title"`
	Status       string     `json:"status" enum:"locked,active,review,passed"`
	Summary      string     `json:"summary"`
	SummaryAt    *time.Time `json:"summary_at,omitempty"`
	PassedAt     *time.Time `json:"passed_at,omitempty"`
	Requirements []PlanReq  `json:"requirements"`
}

// PlanReq 是阶段里一条需求的摘要。
type PlanReq struct {
	ID       string `json:"id"`
	Code     string `json:"code"`
	Title    string `json:"title"`
	Status   string `json:"status"`
	Priority string `json:"priority"`
	Kind     string `json:"kind"`
}

// StageInput 用于新建 / 重排阶段：ID 为空表示新阶段。
type StageInput struct {
	ID           string   `json:"id,omitempty"`
	Title        string   `json:"title"`
	Requirements []string `json:"requirements"` // 需求 ID，按显示顺序
}

func planCode(seq int) string { return "P-" + strconv.Itoa(seq) }

// Finished 判断需求在阶段里是否算“做完了”：待验收或已完成。
func (r PlanReq) Finished() bool { return r.Status == ReqReview || r.Status == ReqDone }

// Stage 按序号取阶段。
func (p Plan) Stage(seq int) (Stage, bool) {
	for _, st := range p.Stages {
		if st.Seq == seq {
			return st, true
		}
	}
	return Stage{}, false
}

const planSelect = `SELECT x.id, x.seq, COALESCE(x.project_id, ''), COALESCE(p.name, ''), x.title, x.goal, x.owner_session, x.status, x.created_by, x.created_at, x.updated_at
	FROM plans x LEFT JOIN projects p ON p.id = x.project_id`

func scanPlan(row interface{ Scan(...any) error }) (Plan, error) {
	var x Plan
	var seq int
	var created, updated string
	err := row.Scan(&x.ID, &seq, &x.ProjectID, &x.ProjectName, &x.Title, &x.Goal, &x.OwnerSession, &x.Status, &x.CreatedBy, &created, &updated)
	x.Code = planCode(seq)
	x.CreatedAt = parseTS(created)
	x.UpdatedAt = parseTS(updated)
	x.Stages = []Stage{}
	return x, err
}

// CreatePlan 新建计划及其阶段；需求按 StageInput 挂到对应阶段。
func (s *Store) CreatePlan(ctx context.Context, x Plan, stages []StageInput) (Plan, error) {
	if strings.TrimSpace(x.Title) == "" {
		return x, errors.New("计划标题不能为空")
	}
	if x.Status == "" {
		x.Status = PlanDraft
	}
	id := protocol.NewID()
	err := s.tx(ctx, func(t *sql.Tx) error {
		var seq int
		if err := t.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) + 1 FROM plans`).Scan(&seq); err != nil {
			return err
		}
		n := now()
		if _, err := t.ExecContext(ctx, `INSERT INTO plans(id, seq, project_id, title, goal, owner_session, status, created_by, created_at, updated_at) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, seq, nullStr(x.ProjectID), strings.TrimSpace(x.Title), strings.TrimSpace(x.Goal), x.OwnerSession, x.Status, x.CreatedBy, n, n); err != nil {
			return err
		}
		return setStages(ctx, t, id, stages)
	})
	if err != nil {
		return x, err
	}
	return s.GetPlan(ctx, id)
}

// setStages 按输入重排计划的阶段：保留给出 ID 的阶段（状态不变），新增其余，删除没出现的；
// 需求挂到所在阶段，原先在本计划、这次没出现的需求移出计划。
func setStages(ctx context.Context, t *sql.Tx, planID string, stages []StageInput) error {
	if len(stages) == 0 {
		return errors.New("计划至少要有一个阶段")
	}
	keep := map[string]bool{}
	seen := map[string]bool{}
	for i, st := range stages {
		title := strings.TrimSpace(st.Title)
		if title == "" {
			title = fmt.Sprintf("阶段 %d", i+1)
		}
		sid := st.ID
		if sid != "" {
			res, err := t.ExecContext(ctx, `UPDATE plan_stages SET seq = ?, title = ? WHERE id = ? AND plan_id = ?`, i+1, title, sid, planID)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n == 0 {
				return fmt.Errorf("阶段不属于这个计划：%s", sid)
			}
		} else {
			sid = protocol.NewID()
			if _, err := t.ExecContext(ctx, `INSERT INTO plan_stages(id, plan_id, seq, title, status) VALUES(?, ?, ?, ?, ?)`, sid, planID, i+1, title, StageLocked); err != nil {
				return err
			}
		}
		keep[sid] = true
		for _, rid := range st.Requirements {
			if seen[rid] {
				return errors.New("同一条需求不能放进两个阶段")
			}
			seen[rid] = true
			var other sql.NullString
			err := t.QueryRowContext(ctx, `SELECT ps.plan_id FROM requirements r LEFT JOIN plan_stages ps ON ps.id = r.stage_id WHERE r.id = ?`, rid).Scan(&other)
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("需求不存在：%s", rid)
			}
			if err != nil {
				return err
			}
			if other.Valid && other.String != planID {
				var code int
				_ = t.QueryRowContext(ctx, `SELECT seq FROM requirements WHERE id = ?`, rid).Scan(&code)
				return fmt.Errorf("%s 已经在另一个计划里，一条需求只能属于一个计划", reqCode(code))
			}
			if _, err := t.ExecContext(ctx, `UPDATE requirements SET stage_id = ? WHERE id = ?`, sid, rid); err != nil {
				return err
			}
		}
	}
	rows, err := t.QueryContext(ctx, `SELECT id FROM plan_stages WHERE plan_id = ?`, planID)
	if err != nil {
		return err
	}
	var drop []string
	for rows.Next() {
		var sid string
		if err := rows.Scan(&sid); err != nil {
			rows.Close()
			return err
		}
		if !keep[sid] {
			drop = append(drop, sid)
		}
	}
	rows.Close()
	for _, sid := range drop {
		if _, err := t.ExecContext(ctx, `UPDATE requirements SET stage_id = NULL WHERE stage_id = ?`, sid); err != nil {
			return err
		}
		if _, err := t.ExecContext(ctx, `DELETE FROM plan_stages WHERE id = ?`, sid); err != nil {
			return err
		}
	}
	// 本计划里这次没列出的需求移出计划
	rows, err = t.QueryContext(ctx, `SELECT r.id FROM requirements r JOIN plan_stages ps ON ps.id = r.stage_id WHERE ps.plan_id = ?`, planID)
	if err != nil {
		return err
	}
	var out []string
	for rows.Next() {
		var rid string
		if err := rows.Scan(&rid); err != nil {
			rows.Close()
			return err
		}
		if !seen[rid] {
			out = append(out, rid)
		}
	}
	rows.Close()
	for _, rid := range out {
		if _, err := t.ExecContext(ctx, `UPDATE requirements SET stage_id = NULL WHERE id = ?`, rid); err != nil {
			return err
		}
	}
	_, err = t.ExecContext(ctx, `UPDATE plans SET updated_at = ? WHERE id = ?`, now(), planID)
	return err
}

// GetPlan 按 ID 或 P-编号读取计划，含阶段与需求。
func (s *Store) GetPlan(ctx context.Context, ref string) (Plan, error) {
	var row *sql.Row
	if seq, ok := parseRef(ref, "P"); ok {
		row = s.db.QueryRowContext(ctx, planSelect+` WHERE x.seq = ?`, seq)
	} else {
		row = s.db.QueryRowContext(ctx, planSelect+` WHERE x.id = ?`, ref)
	}
	x, err := scanPlan(row)
	if errors.Is(err, sql.ErrNoRows) {
		return x, ErrNotFound
	}
	if err != nil {
		return x, err
	}
	return x, s.fillStages(ctx, &x)
}

func (s *Store) fillStages(ctx context.Context, x *Plan) error {
	rows, err := s.db.QueryContext(ctx, `SELECT id, seq, title, status, summary, summary_at, passed_at FROM plan_stages WHERE plan_id = ? ORDER BY seq`, x.ID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var st Stage
		var sumAt, passAt string
		if err := rows.Scan(&st.ID, &st.Seq, &st.Title, &st.Status, &st.Summary, &sumAt, &passAt); err != nil {
			rows.Close()
			return err
		}
		st.SummaryAt = parseNullTS(sql.NullString{String: sumAt, Valid: sumAt != ""})
		st.PassedAt = parseNullTS(sql.NullString{String: passAt, Valid: passAt != ""})
		st.Requirements = []PlanReq{}
		x.Stages = append(x.Stages, st)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	rows, err = s.db.QueryContext(ctx, `SELECT r.id, r.seq, r.title, r.status, r.priority, r.kind, r.stage_id FROM requirements r JOIN plan_stages ps ON ps.id = r.stage_id WHERE ps.plan_id = ? ORDER BY r.seq`, x.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	idx := map[string]int{}
	for i, st := range x.Stages {
		idx[st.ID] = i
	}
	for rows.Next() {
		var r PlanReq
		var seq int
		var sid string
		if err := rows.Scan(&r.ID, &seq, &r.Title, &r.Status, &r.Priority, &r.Kind, &sid); err != nil {
			return err
		}
		r.Code = reqCode(seq)
		if i, ok := idx[sid]; ok {
			x.Stages[i].Requirements = append(x.Stages[i].Requirements, r)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, st := range x.Stages {
		if st.Status != StagePassed {
			if x.Current == 0 {
				x.Current = st.Seq
			}
			continue
		}
		for _, r := range st.Requirements {
			if r.Status != ReqDone {
				x.Paused = true
			}
		}
	}
	return nil
}

// ListPlans 列出计划，projectID 为空表示全部；新的在前。
func (s *Store) ListPlans(ctx context.Context, projectID string) ([]Plan, error) {
	q := planSelect
	var args []any
	if projectID != "" {
		q += ` WHERE x.project_id = ?`
		args = append(args, projectID)
	}
	q += ` ORDER BY x.seq DESC`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	out := []Plan{}
	for rows.Next() {
		x, err := scanPlan(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, x)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if err := s.fillStages(ctx, &out[i]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// UpdatePlan 修改计划的标题、目标、项目、负责会话；stages 非空时同时重排阶段。
func (s *Store) UpdatePlan(ctx context.Context, x Plan, stages []StageInput) (Plan, error) {
	if strings.TrimSpace(x.Title) == "" {
		return x, errors.New("计划标题不能为空")
	}
	err := s.tx(ctx, func(t *sql.Tx) error {
		if _, err := t.ExecContext(ctx, `UPDATE plans SET title = ?, goal = ?, project_id = ?, owner_session = ?, updated_at = ? WHERE id = ?`,
			strings.TrimSpace(x.Title), strings.TrimSpace(x.Goal), nullStr(x.ProjectID), x.OwnerSession, now(), x.ID); err != nil {
			return err
		}
		if stages != nil {
			return setStages(ctx, t, x.ID, stages)
		}
		return nil
	})
	if err != nil {
		return x, err
	}
	return s.GetPlan(ctx, x.ID)
}

// SetPlanStatus 修改计划状态。
func (s *Store) SetPlanStatus(ctx context.Context, id, status string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE plans SET status = ?, updated_at = ? WHERE id = ?`, status, now(), id)
	return err
}

// SetStageStatus 修改阶段状态；进入待阶段验收时写入小结，通过时记下时间。
func (s *Store) SetStageStatus(ctx context.Context, stageID, status, summary string) error {
	n := now()
	var err error
	switch status {
	case StageReview:
		_, err = s.db.ExecContext(ctx, `UPDATE plan_stages SET status = ?, summary = ?, summary_at = ? WHERE id = ?`, status, summary, n, stageID)
	case StagePassed:
		_, err = s.db.ExecContext(ctx, `UPDATE plan_stages SET status = ?, passed_at = ? WHERE id = ?`, status, n, stageID)
	default:
		_, err = s.db.ExecContext(ctx, `UPDATE plan_stages SET status = ?, passed_at = '' WHERE id = ?`, status, stageID)
	}
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE plans SET updated_at = ? WHERE id = (SELECT plan_id FROM plan_stages WHERE id = ?)`, n, stageID)
	return err
}

// DeletePlan 删除计划；其中的需求保留，只是不再属于任何计划。
func (s *Store) DeletePlan(ctx context.Context, id string) error {
	return s.tx(ctx, func(t *sql.Tx) error {
		if _, err := t.ExecContext(ctx, `UPDATE requirements SET stage_id = NULL WHERE stage_id IN (SELECT id FROM plan_stages WHERE plan_id = ?)`, id); err != nil {
			return err
		}
		_, err := t.ExecContext(ctx, `DELETE FROM plans WHERE id = ?`, id)
		return err
	})
}
