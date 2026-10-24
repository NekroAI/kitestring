package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/NekroAI/kitestring/internal/blocks"
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
	Kind             string     `json:"kind"` // design = 设计类需求（标注，Agent 按需要交方案图让你选）；空 = 普通
	Source           string     `json:"source"`
	Acceptance       string     `json:"acceptance"`
	DocLink          string     `json:"doc_link"`
	Files            []FileRef  `json:"files"` // 描述里的附图
	CreatedBy        string     `json:"created_by"`
	NextActor        string     `json:"next_actor"` // user | agent | none，推导得出
	PendingDecisions int        `json:"pending_decisions"`
	Sessions         []string   `json:"sessions"`
	PlanID           string     `json:"plan_id,omitempty"` // 所属计划（R-43），一条需求最多属于一个计划
	PlanCode         string     `json:"plan_code,omitempty"`
	StageSeq         int        `json:"stage_seq,omitempty"` // 所在阶段（从 1 开始）
	StageTitle       string     `json:"stage_title,omitempty"`
	StageStatus      string     `json:"stage_status,omitempty"` // locked = 前一阶段还没通过
	OfferedTo        string     `json:"offered_to,omitempty"`   // 待开始时已交给哪个会话（交出或排队送达）；状态一变就清空
	OfferedAt        *time.Time `json:"offered_at,omitempty"`
	Evidence         []Evidence `json:"evidence,omitempty"`
	Reviews          []Evidence `json:"reviews,omitempty"`  // 审查员的审查意见（R-88）：不改状态、不算验收证据
	Feedback         []Decision `json:"feedback,omitempty"` // 用户的修改意见（时间倒序）
	// Release：最近一次没撤回的放行（R-166）——PM / Adv 把草稿改为待开始时留下的记录：谁、什么身份、为什么
	Release *Release `json:"release,omitempty"`
	// FromDecision：产生这条需求的决策（D-x，R-154），Agent 新建时声明
	FromDecision string `json:"from_decision,omitempty"`
	// StatusChanges：状态变更历史（R-153），时间正序；上线前的旧需求没有
	StatusChanges []StatusChange `json:"status_changes,omitempty"`
	// Review：待验收时这一轮的评审验证情况（R-232、R-237），推导得出；没交给评审、也没收到审查意见时为空
	Review    *ReviewState `json:"review,omitempty"`
	CreatedAt time.Time    `json:"created_at"`
	UpdatedAt time.Time    `json:"updated_at"`
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
	// Payload 是结构化证据（R-97）：一句结论、清单与表达块、要你做的、折叠的细节。旧证据没有，只按 summary 显示。
	Payload *blocks.Payload `json:"payload,omitempty"`
	Voided  bool            `json:"voided"`
	// 作废留痕（R-189）：Agent 主动作废的证据带理由、谁、什么时候；验收被退回时批量作废的没有这几项
	VoidReason string     `json:"void_reason,omitempty" doc:"作废理由（Agent 主动作废时必填；验收退回时批量作废的为空）"`
	VoidedBy   string     `json:"voided_by,omitempty" doc:"作废的会话键"`
	VoidedRole string     `json:"voided_role,omitempty" doc:"作废时该会话的身份"`
	VoidedAt   *time.Time `json:"voided_at,omitempty"`
	// 交证据时会话的标题与身份快照（R-195）；旧证据没有
	SessionTitle string    `json:"session_title,omitempty" doc:"交证据时会话的标题快照（R-195）"`
	Role         string    `json:"role,omitempty" doc:"交证据时会话的身份快照（R-195）"`
	CreatedAt    time.Time `json:"created_at"`
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

const reqSelect = `SELECT r.id, r.seq, COALESCE(r.project_id, ''), COALESCE(p.name, ''), r.title, r.detail, r.status, r.priority, r.source, r.acceptance, r.doc_link, r.files, r.created_by, r.created_at, r.updated_at, r.kind,
	(SELECT COUNT(*) FROM decisions d WHERE d.requirement_id = r.id AND d.status = 'pending'),
	COALESCE(ps.plan_id, ''), COALESCE(pl.seq, 0), COALESCE(ps.seq, 0), COALESCE(ps.title, ''), COALESCE(ps.status, ''), r.offered_to, r.offered_at,
	COALESCE((SELECT x.seq || char(31) || x.assigned_by || char(31) || x.context || char(31) || x.answer || char(31) || x.created_at FROM decisions x
		WHERE x.requirement_id = r.id AND x.kind = 'release' AND x.close_reason = '' ORDER BY x.seq DESC LIMIT 1), ''), r.from_decision
	FROM requirements r LEFT JOIN projects p ON p.id = r.project_id
	LEFT JOIN plan_stages ps ON ps.id = r.stage_id LEFT JOIN plans pl ON pl.id = ps.plan_id`

// normKind：只认 design，其它（含 feature）都存为空，即普通需求。
func normKind(k string) string {
	if k == "design" {
		return k
	}
	return ""
}

func scanReq(row interface{ Scan(...any) error }) (Requirement, error) {
	var r Requirement
	var seq, planSeq int
	var created, updated, files, offeredAt, release string
	err := row.Scan(&r.ID, &seq, &r.ProjectID, &r.ProjectName, &r.Title, &r.Detail, &r.Status, &r.Priority, &r.Source, &r.Acceptance, &r.DocLink, &files, &r.CreatedBy, &created, &updated, &r.Kind, &r.PendingDecisions,
		&r.PlanID, &planSeq, &r.StageSeq, &r.StageTitle, &r.StageStatus, &r.OfferedTo, &offeredAt, &release, &r.FromDecision)
	r.Release = parseRelease(release)
	r.OfferedAt = parseNullTS(sql.NullString{String: offeredAt, Valid: offeredAt != ""})
	r.Files = parseFiles(files)
	r.Code = reqCode(seq)
	r.NextActor = nextActor(r.Status, r.PendingDecisions)
	if planSeq > 0 {
		r.PlanCode = planCode(planSeq)
	}
	if r.StageStatus == StageLocked {
		r.NextActor = "none" // 排在计划后面的阶段里：前一阶段通过前谁都不用动
	}
	r.CreatedAt = parseTS(created)
	r.UpdatedAt = parseTS(updated)
	r.Sessions = []string{}
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

// NormTitle 把标题归一化后比较是否同名：去首尾与多余空白，忽略大小写。
func NormTitle(t string) string { return strings.ToLower(strings.Join(strings.Fields(t), " ")) }

// OpenRequirementByTitle 在项目里找同名且未关闭（不是已完成 / 已搁置）的需求，没有则返回 ErrNotFound。
func (s *Store) OpenRequirementByTitle(ctx context.Context, projectID, title string) (Requirement, error) {
	if projectID == "" || NormTitle(title) == "" {
		return Requirement{}, ErrNotFound
	}
	list, err := s.ListRequirements(ctx, projectID, []string{ReqDraft, ReqReady, ReqInProgress, ReqBlocked, ReqReview})
	if err != nil {
		return Requirement{}, err
	}
	want := NormTitle(title)
	for _, r := range list {
		if NormTitle(r.Title) == want {
			return r, nil
		}
	}
	return Requirement{}, ErrNotFound
}

// CreateRequirement 新建需求。
// codePrefix 是标题开头的编号前缀，如「R-95-D 」「P-3：」。编号由纸鸢自己画在标题旁边，写进标题会叠在一起（R-129）。
var codePrefix = regexp.MustCompile(`^(?:[RPD]-\d+(?:-[A-Za-z0-9]+)?[\s:：、.．\-—]*)+`)

// CleanTitle 去掉标题开头的编号前缀；去完为空时保留原样。
func CleanTitle(title string) string {
	t := strings.TrimSpace(title)
	if c := strings.TrimSpace(codePrefix.ReplaceAllString(t, "")); c != "" {
		return c
	}
	return t
}

func (s *Store) CreateRequirement(ctx context.Context, r Requirement) (Requirement, error) {
	r.Title = CleanTitle(r.Title)
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
		_, err := t.ExecContext(ctx, `INSERT INTO requirements(id, seq, project_id, title, detail, status, priority, source, acceptance, doc_link, files, created_by, kind, created_at, updated_at, from_decision) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, seq, nullStr(r.ProjectID), r.Title, r.Detail, r.Status, r.Priority, r.Source, r.Acceptance, r.DocLink, filesJSON(r.Files), r.CreatedBy, normKind(r.Kind), n, n, r.FromDecision)
		if err != nil {
			return err
		}
		return recordStatus(WithActor(ctx, Actor{Via: "create"}), t, id, "", r.Status)
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
	if r.Reviews, err = s.ListReviews(ctx, r.ID); err != nil {
		return r, err
	}
	if r.StatusChanges, err = s.ListStatusChanges(ctx, StatusQuery{RequirementID: r.ID}); err != nil {
		return r, err
	}
	if r.Feedback, err = s.queryDecs(ctx, decSelect+` WHERE x.requirement_id = ? AND x.kind IN (?, ?, ?, ?, ?, ?) ORDER BY x.seq DESC`, r.ID, KindFeedback, KindNote, KindAssign, KindReviewTask, KindRelease, KindEdit); err != nil {
		return r, err
	}
	r.Review = s.ReviewOf(ctx, r, time.Now())
	return r, nil
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
		if err := s.fillReviewState(ctx, &out[i]); err != nil {
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
	if r.Kind == "" {
		r.Kind = cur.Kind // 未提供类型时保持不变；传 feature 表示改回普通需求
	}
	_, err = s.db.ExecContext(ctx, `UPDATE requirements SET project_id = ?, title = ?, detail = ?, priority = ?, source = ?, acceptance = ?, doc_link = ?, files = ?, kind = ?, updated_at = ? WHERE id = ?`,
		nullStr(r.ProjectID), r.Title, r.Detail, r.Priority, r.Source, r.Acceptance, r.DocLink, filesJSON(r.Files), normKind(r.Kind), now(), cur.ID)
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
	var voided []string
	err = s.tx(ctx, func(t *sql.Tx) error {
		// 状态一变，“已交给某会话”的记录就作废：再回到待开始时重新排队
		if _, err := t.ExecContext(ctx, `UPDATE requirements SET status = ?, offered_to = CASE WHEN status = ? THEN offered_to ELSE '' END, offered_at = CASE WHEN status = ? THEN offered_at ELSE '' END, updated_at = ? WHERE id = ?`, status, status, status, now(), cur.ID); err != nil {
			return err
		}
		if cur.Status != status {
			if err := recordStatus(ctx, t, cur.ID, cur.Status, status); err != nil {
				return err
			}
			// 状态被改动：还在等批准的改状态申请自动作废（R-188）
			var err error
			if voided, err = voidStatusRequests(ctx, t, cur.ID); err != nil {
				return err
			}
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
	if len(voided) > 0 && s.OnRequestVoided != nil {
		s.OnRequestVoided(voided)
	}
	return s.GetRequirement(ctx, cur.ID)
}

// SetOffered 记下待开始的需求已交给哪个会话（交出或排队送达）。
func (s *Store) SetOffered(ctx context.Context, id, sessionKey string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE requirements SET offered_to = ?, offered_at = ? WHERE id = ?`, sessionKey, now(), id)
	return err
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
	e.SessionTitle, e.Role = sessionSnap(ctx, s.db, e.SessionKey)
	_, err := s.db.ExecContext(ctx, `INSERT INTO evidence(id, requirement_id, session_key, kind, summary, link, files, payload, session_title, role, created_at) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ID, e.RequirementID, nullStr(e.SessionKey), e.Kind, e.Summary, e.Link, filesJSON(e.Files), blocks.Encode(e.Payload), e.SessionTitle, e.Role, ts(e.CreatedAt))
	return e, err
}

// EvidenceReview 是审查意见在 evidence 表里的 kind（R-88）。审查意见和证据同形（摘要、附图、会话、时间），
// 复用同一张表免迁移；读出时分开：ListEvidence 不含审查意见，ListReviews 只含审查意见。
const EvidenceReview = "review"

// ListEvidence 列出需求的证据（不含审查意见）。
func (s *Store) ListEvidence(ctx context.Context, reqID string) ([]Evidence, error) {
	return s.listEvidence(ctx, reqID, "kind <> ?")
}

// ListReviews 列出需求的审查意见（R-88）。
func (s *Store) ListReviews(ctx context.Context, reqID string) ([]Evidence, error) {
	return s.listEvidence(ctx, reqID, "kind = ?")
}

func (s *Store) listEvidence(ctx context.Context, reqID, cond string) ([]Evidence, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+evidenceCols+` FROM evidence WHERE requirement_id = ? AND `+cond+` ORDER BY created_at`, reqID, EvidenceReview)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Evidence{}
	for rows.Next() {
		e, err := scanEvidence(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

const evidenceCols = `id, requirement_id, COALESCE(session_key, ''), kind, summary, link, files, payload, voided, void_reason, voided_by, voided_role, voided_at, session_title, role, created_at`

func scanEvidence(row interface{ Scan(...any) error }) (Evidence, error) {
	var e Evidence
	var created, files, payload, voidedAt string
	if err := row.Scan(&e.ID, &e.RequirementID, &e.SessionKey, &e.Kind, &e.Summary, &e.Link, &files, &payload, &e.Voided, &e.VoidReason, &e.VoidedBy, &e.VoidedRole, &voidedAt, &e.SessionTitle, &e.Role, &created); err != nil {
		return e, err
	}
	e.Payload = blocks.Decode[blocks.Payload](payload)
	e.Files = parseFiles(files)
	e.CreatedAt = parseTS(created)
	if voidedAt != "" {
		t := parseTS(voidedAt)
		e.VoidedAt = &t
	}
	return e, nil
}

// GetEvidence 按编号取一条证据（含审查意见）。
func (s *Store) GetEvidence(ctx context.Context, id string) (Evidence, error) {
	e, err := scanEvidence(s.db.QueryRowContext(ctx, `SELECT `+evidenceCols+` FROM evidence WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return e, ErrNotFound
	}
	return e, err
}

// VoidEvidence 作废一条证据（R-189）：记下谁、什么身份、什么时候、为什么；已作废的不能重复作废。
func (s *Store) VoidEvidence(ctx context.Context, id, by, role, reason string) (Evidence, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE evidence SET voided = 1, void_reason = ?, voided_by = ?, voided_role = ?, voided_at = ? WHERE id = ? AND voided = 0`, reason, by, role, now(), id)
	if err != nil {
		return Evidence{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Evidence{}, fmt.Errorf("这条证据已经作废")
	}
	return s.GetEvidence(ctx, id)
}
