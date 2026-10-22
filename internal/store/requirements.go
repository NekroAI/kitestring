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

// 需求状态（D19）。
const (
	ReqDraft      = "draft"
	ReqReady      = "ready"
	ReqInProgress = "in_progress"
	ReqBlocked    = "blocked"
	ReqReview     = "review"
	ReqDone       = "done"
	ReqShelved    = "shelved"
)

// ReqStatuses 是全部合法需求状态，按看板顺序排列。
var ReqStatuses = []string{ReqDraft, ReqReady, ReqInProgress, ReqBlocked, ReqReview, ReqDone, ReqShelved}

// ValidReqStatus 判断状态是否合法。
func ValidReqStatus(s string) bool {
	for _, x := range ReqStatuses {
		if x == s {
			return true
		}
	}
	return false
}

// Requirement 是一条需求。
type Requirement struct {
	ID               string     `json:"id"`
	Code             string     `json:"code"`
	ProjectID        string     `json:"project_id,omitempty"`
	ProjectName      string     `json:"project_name,omitempty"`
	Title            string     `json:"title"`
	Detail           string     `json:"detail"`
	Status           string     `json:"status"`
	Priority         string     `json:"priority"`
	Source           string     `json:"source"`
	Acceptance       string     `json:"acceptance"`
	DocLink          string     `json:"doc_link"`
	Files            []FileRef  `json:"files"` // 描述里的附图
	CreatedBy        string     `json:"created_by"`
	NextActor        string     `json:"next_actor"` // user | agent | none，推导得出
	PendingDecisions int        `json:"pending_decisions"`
	Sessions         []string   `json:"sessions"`
	Evidence         []Evidence `json:"evidence,omitempty"`
	Feedback         []Decision `json:"feedback,omitempty"` // 用户的修改意见（时间倒序）
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

// Evidence 是需求的验收证据。
type Evidence struct {
	ID            string    `json:"id"`
	RequirementID string    `json:"requirement_id"`
	SessionKey    string    `json:"session_key,omitempty"`
	Kind          string    `json:"kind"`
	Summary       string    `json:"summary"`
	Link          string    `json:"link"`
	Files         []FileRef `json:"files"`
	Voided        bool      `json:"voided"`
	CreatedAt     time.Time `json:"created_at"`
}

func reqCode(seq int) string { return "R-" + strconv.Itoa(seq) }

// parseRef 把 "R-12" / "D-3" 解析为序号；否则视为内部 ID。
func parseRef(ref, prefix string) (seq int, isSeq bool) {
	r := strings.TrimSpace(ref)
	if strings.HasPrefix(strings.ToUpper(r), prefix+"-") {
		n, err := strconv.Atoi(r[len(prefix)+1:])
		return n, err == nil
	}
	if n, err := strconv.Atoi(r); err == nil {
		return n, true
	}
	return 0, false
}

const reqSelect = `SELECT r.id, r.seq, COALESCE(r.project_id, ''), COALESCE(p.name, ''), r.title, r.detail, r.status, r.priority, r.source, r.acceptance, r.doc_link, r.files, r.created_by, r.created_at, r.updated_at,
	(SELECT COUNT(*) FROM decisions d WHERE d.requirement_id = r.id AND d.status = 'pending')
	FROM requirements r LEFT JOIN projects p ON p.id = r.project_id`

func scanReq(row interface{ Scan(...any) error }) (Requirement, error) {
	var r Requirement
	var seq int
	var created, updated, files string
	err := row.Scan(&r.ID, &seq, &r.ProjectID, &r.ProjectName, &r.Title, &r.Detail, &r.Status, &r.Priority, &r.Source, &r.Acceptance, &r.DocLink, &files, &r.CreatedBy, &created, &updated, &r.PendingDecisions)
	r.Files = parseFiles(files)
	r.Code = reqCode(seq)
	r.CreatedAt = parseTS(created)
	r.UpdatedAt = parseTS(updated)
	r.Sessions = []string{}
	r.NextActor = nextActor(r.Status, r.PendingDecisions)
	return r, err
}

// nextActor 按 D19 推导现在轮到谁。
func nextActor(status string, pending int) string {
	switch {
	case pending > 0 || status == ReqReview || status == ReqDraft:
		return "user"
	case status == ReqReady || status == ReqInProgress || status == ReqBlocked:
		return "agent"
	default:
		return "none"
	}
}

// CreateRequirement 新建需求。
func (s *Store) CreateRequirement(ctx context.Context, r Requirement) (Requirement, error) {
	if r.Status == "" {
		r.Status = ReqDraft
	}
	if !ValidReqStatus(r.Status) {
		return r, fmt.Errorf("非法需求状态：%s", r.Status)
	}
	if r.Priority == "" {
		r.Priority = "P2"
	}
	id := protocol.NewID()
	err := s.tx(ctx, func(t *sql.Tx) error {
		var seq int
		if err := t.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) + 1 FROM requirements`).Scan(&seq); err != nil {
			return err
		}
		n := now()
		_, err := t.ExecContext(ctx, `INSERT INTO requirements(id, seq, project_id, title, detail, status, priority, source, acceptance, doc_link, files, created_by, created_at, updated_at) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, seq, nullStr(r.ProjectID), r.Title, r.Detail, r.Status, r.Priority, r.Source, r.Acceptance, r.DocLink, filesJSON(r.Files), r.CreatedBy, n, n)
		return err
	})
	if err != nil {
		return r, err
	}
	return s.GetRequirement(ctx, id)
}

// GetRequirement 按 ID 或 R-编号读取需求，含关联会话与证据。
func (s *Store) GetRequirement(ctx context.Context, ref string) (Requirement, error) {
	var row *sql.Row
	if seq, ok := parseRef(ref, "R"); ok {
		row = s.db.QueryRowContext(ctx, reqSelect+` WHERE r.seq = ?`, seq)
	} else {
		row = s.db.QueryRowContext(ctx, reqSelect+` WHERE r.id = ?`, ref)
	}
	r, err := scanReq(row)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	if err != nil {
		return r, err
	}
	if err := s.fillReqSessions(ctx, &r); err != nil {
		return r, err
	}
	if r.Evidence, err = s.ListEvidence(ctx, r.ID); err != nil {
		return r, err
	}
	r.Feedback, err = s.queryDecs(ctx, decSelect+` WHERE x.requirement_id = ? AND x.kind IN (?, ?, ?) ORDER BY x.seq DESC`, r.ID, KindFeedback, KindNote, KindAssign)
	return r, err
}

func (s *Store) fillReqSessions(ctx context.Context, r *Requirement) error {
	rows, err := s.db.QueryContext(ctx, `SELECT session_key FROM requirement_sessions WHERE requirement_id = ? ORDER BY linked_at`, r.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return err
		}
		r.Sessions = append(r.Sessions, k)
	}
	return rows.Err()
}

// ListRequirements 列出需求，projectID 或 statuses 为空表示不过滤。
func (s *Store) ListRequirements(ctx context.Context, projectID string, statuses []string) ([]Requirement, error) {
	q := reqSelect + ` WHERE 1 = 1`
	var args []any
	if projectID != "" {
		q += ` AND r.project_id = ?`
		args = append(args, projectID)
	}
	if len(statuses) > 0 {
		q += ` AND r.status IN (?` + strings.Repeat(", ?", len(statuses)-1) + `)`
		for _, st := range statuses {
			args = append(args, st)
		}
	}
	q += ` ORDER BY r.priority, r.seq DESC`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Requirement{}
	for rows.Next() {
		r, err := scanReq(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if err := s.fillReqSessions(ctx, &out[i]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// UpdateRequirement 更新需求的可编辑字段；状态变化遵循 SetRequirementStatus 的规则。
func (s *Store) UpdateRequirement(ctx context.Context, r Requirement) (Requirement, error) {
	cur, err := s.GetRequirement(ctx, r.ID)
	if err != nil {
		return r, err
	}
	if r.Priority == "" {
		r.Priority = cur.Priority
	}
	if r.Files == nil {
		r.Files = cur.Files // 未提供附图字段时保持不变
	}
	_, err = s.db.ExecContext(ctx, `UPDATE requirements SET project_id = ?, title = ?, detail = ?, priority = ?, source = ?, acceptance = ?, doc_link = ?, files = ?, updated_at = ? WHERE id = ?`,
		nullStr(r.ProjectID), r.Title, r.Detail, r.Priority, r.Source, r.Acceptance, r.DocLink, filesJSON(r.Files), now(), cur.ID)
	if err != nil {
		return r, err
	}
	if r.Status != "" && r.Status != cur.Status {
		return s.SetRequirementStatus(ctx, cur.ID, r.Status)
	}
	return s.GetRequirement(ctx, cur.ID)
}

// SetRequirementStatus 修改需求状态；从“待验收”退回（未进入已完成）时作废已有证据（D19）。
func (s *Store) SetRequirementStatus(ctx context.Context, ref, status string) (Requirement, error) {
	if !ValidReqStatus(status) {
		return Requirement{}, fmt.Errorf("非法需求状态：%s", status)
	}
	cur, err := s.GetRequirement(ctx, ref)
	if err != nil {
		return cur, err
	}
	err = s.tx(ctx, func(t *sql.Tx) error {
		if _, err := t.ExecContext(ctx, `UPDATE requirements SET status = ?, updated_at = ? WHERE id = ?`, status, now(), cur.ID); err != nil {
			return err
		}
		if cur.Status == ReqReview && status != ReqDone && status != ReqReview {
			_, err := t.ExecContext(ctx, `UPDATE evidence SET voided = 1 WHERE requirement_id = ?`, cur.ID)
			return err
		}
		return nil
	})
	if err != nil {
		return cur, err
	}
	return s.GetRequirement(ctx, cur.ID)
}

// RequirementsUpdatedBetween 返回时间窗内有变化的需求。
func (s *Store) RequirementsUpdatedBetween(ctx context.Context, from, to time.Time) ([]Requirement, error) {
	rows, err := s.db.QueryContext(ctx, reqSelect+` WHERE r.updated_at >= ? AND r.updated_at <= ? ORDER BY r.updated_at`, ts(from), ts(to))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Requirement{}
	for rows.Next() {
		r, err := scanReq(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// DeleteRequirement 删除需求。
func (s *Store) DeleteRequirement(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM requirements WHERE id = ?`, id)
	return err
}

// LinkSession 关联会话与需求（D5）。
func (s *Store) LinkSession(ctx context.Context, reqID, sessionKey string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO requirement_sessions(requirement_id, session_key, linked_at) VALUES(?, ?, ?) ON CONFLICT DO NOTHING`, reqID, sessionKey, now())
	return err
}

// UnlinkSession 取消关联。
func (s *Store) UnlinkSession(ctx context.Context, reqID, sessionKey string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM requirement_sessions WHERE requirement_id = ? AND session_key = ?`, reqID, sessionKey)
	return err
}

// AddEvidence 为需求附加证据。
func (s *Store) AddEvidence(ctx context.Context, e Evidence) (Evidence, error) {
	e.ID = protocol.NewID()
	e.CreatedAt = time.Now()
	_, err := s.db.ExecContext(ctx, `INSERT INTO evidence(id, requirement_id, session_key, kind, summary, link, files, created_at) VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ID, e.RequirementID, nullStr(e.SessionKey), e.Kind, e.Summary, e.Link, filesJSON(e.Files), ts(e.CreatedAt))
	return e, err
}

// ListEvidence 列出需求的证据。
func (s *Store) ListEvidence(ctx context.Context, reqID string) ([]Evidence, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, requirement_id, COALESCE(session_key, ''), kind, summary, link, files, voided, created_at FROM evidence WHERE requirement_id = ? ORDER BY created_at`, reqID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Evidence{}
	for rows.Next() {
		var e Evidence
		var created, files string
		if err := rows.Scan(&e.ID, &e.RequirementID, &e.SessionKey, &e.Kind, &e.Summary, &e.Link, &files, &e.Voided, &created); err != nil {
			return nil, err
		}
		e.Files = parseFiles(files)
		e.CreatedAt = parseTS(created)
		out = append(out, e)
	}
	return out, rows.Err()
}
