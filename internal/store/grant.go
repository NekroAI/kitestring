package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/NekroAI/kitestring/internal/blocks"
	"github.com/NekroAI/kitestring/internal/protocol"
)

// 参谋授权窗口（R-163）：你离开前在网页上授权一个 Adv 会话，在到期前代你答复风险档不超过 MaxRisk 的决策。
// 窗口开启、撤销都留在 grants 表里（不删除），到期按 ExpiresAt 判断；代答记在决策自己的 proxy_* 列上。

// 风险档：提问方在 decision_raise 里标，参谋代答时再给出自己的判断，取两者中更高的一档（只升不降）。
const (
	RiskLow    = "low"
	RiskMedium = "medium"
	RiskHigh   = "high"
)

// 授权窗口状态。
const (
	GrantActive  = "active"
	GrantRevoked = "revoked"
	GrantExpired = "expired"
)

// RiskRank 把风险档换成可比较的数：low 1、medium 2、high 3；空或不认识的按 high 算。
func RiskRank(r string) int {
	switch r {
	case RiskLow:
		return 1
	case RiskMedium:
		return 2
	}
	return 3
}

// MaxRisk 返回两档中更高的一档（空按 high）。
func MaxRisk(a, b string) string {
	if RiskRank(a) >= RiskRank(b) {
		return NormRisk(a)
	}
	return NormRisk(b)
}

// NormRisk 规范化风险档：空或不认识的按 high。
func NormRisk(r string) string {
	switch r {
	case RiskLow, RiskMedium:
		return r
	}
	return RiskHigh
}

// Grant 是一个授权窗口。
type Grant struct {
	ID          string     `json:"id"`
	Code        string     `json:"code" doc:"窗口编号（G-x）"`
	ProjectID   string     `json:"project_id"`
	ProjectName string     `json:"project_name"`
	SessionKey  string     `json:"session_key" doc:"被授权的 Adv 会话"`
	MaxRisk     string     `json:"max_risk" enum:"low,medium" doc:"可代答的最高风险档"`
	ExpiresAt   time.Time  `json:"expires_at"`
	CreatedAt   time.Time  `json:"created_at"`
	RevokedAt   *time.Time `json:"revoked_at,omitempty"`
	Status      string     `json:"status" enum:"active,revoked,expired" doc:"生效中 / 已撤销 / 已到期"`
	Proxied     int        `json:"proxied" doc:"这个窗口内代答了几条"`
}

func grantCode(seq int) string { return "G-" + strconv.Itoa(seq) }

const grantSelect = `SELECT g.id, g.seq, g.project_id, COALESCE(p.name, ''), g.session_key, g.max_risk, g.expires_at, g.created_at, g.revoked_at,
	(SELECT COUNT(*) FROM decisions d WHERE d.proxy_by = g.session_key AND d.project_id = g.project_id AND d.proxy_at >= g.created_at AND d.proxy_at <= g.expires_at AND (g.revoked_at = '' OR d.proxy_at <= g.revoked_at))
	FROM grants g LEFT JOIN projects p ON p.id = g.project_id`

func scanGrant(row interface{ Scan(...any) error }, at time.Time) (Grant, error) {
	var g Grant
	var seq int
	var exp, created, revoked string
	err := row.Scan(&g.ID, &seq, &g.ProjectID, &g.ProjectName, &g.SessionKey, &g.MaxRisk, &exp, &created, &revoked, &g.Proxied)
	g.Code = grantCode(seq)
	g.ExpiresAt, g.CreatedAt = parseTS(exp), parseTS(created)
	g.RevokedAt = parseNullTS(sql.NullString{String: revoked, Valid: revoked != ""})
	switch {
	case g.RevokedAt != nil:
		g.Status = GrantRevoked
	case !at.Before(g.ExpiresAt):
		g.Status = GrantExpired
	default:
		g.Status = GrantActive
	}
	return g, err
}

// CreateGrant 开一个授权窗口。同一项目同时只能有一个生效中的窗口。
func (s *Store) CreateGrant(ctx context.Context, projectID, sessionKey, maxRisk string, expires time.Time) (Grant, error) {
	if maxRisk != RiskLow && maxRisk != RiskMedium {
		return Grant{}, errors.New("范围只能是 low（只代答低风险）或 medium（低、中风险）")
	}
	if cur, err := s.ActiveGrant(ctx, projectID); err == nil {
		return Grant{}, fmt.Errorf("这个项目已有生效中的授权 %s（至 %s），先撤销再开新的", cur.Code, cur.ExpiresAt.Local().Format("01-02 15:04"))
	}
	id := protocol.NewID()
	err := s.tx(ctx, func(t *sql.Tx) error {
		var seq int
		if err := t.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) + 1 FROM grants`).Scan(&seq); err != nil {
			return err
		}
		_, err := t.ExecContext(ctx, `INSERT INTO grants(id, seq, project_id, session_key, max_risk, expires_at, created_at) VALUES(?, ?, ?, ?, ?, ?, ?)`,
			id, seq, projectID, sessionKey, maxRisk, ts(expires), now())
		return err
	})
	if err != nil {
		return Grant{}, err
	}
	return s.GetGrant(ctx, id)
}

// GetGrant 按 ID 或 G-编号读取窗口。
func (s *Store) GetGrant(ctx context.Context, ref string) (Grant, error) {
	var row *sql.Row
	if seq, ok := parseRef(ref, "G"); ok {
		row = s.db.QueryRowContext(ctx, grantSelect+` WHERE g.seq = ?`, seq)
	} else {
		row = s.db.QueryRowContext(ctx, grantSelect+` WHERE g.id = ?`, ref)
	}
	g, err := scanGrant(row, time.Now())
	if errors.Is(err, sql.ErrNoRows) {
		return g, ErrNotFound
	}
	return g, err
}

// ListGrants 列出窗口（新的在前），projectID 为空表示全部。
func (s *Store) ListGrants(ctx context.Context, projectID string, limit int) ([]Grant, error) {
	q, args := grantSelect, []any{}
	if projectID != "" {
		q += ` WHERE g.project_id = ?`
		args = append(args, projectID)
	}
	q += ` ORDER BY g.seq DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Grant{}
	at := time.Now()
	for rows.Next() {
		g, err := scanGrant(rows, at)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// ActiveGrant 返回项目当前生效中的窗口；没有时返回 ErrNotFound。
func (s *Store) ActiveGrant(ctx context.Context, projectID string) (Grant, error) {
	row := s.db.QueryRowContext(ctx, grantSelect+` WHERE g.project_id = ? AND g.revoked_at = '' AND g.expires_at > ? ORDER BY g.seq DESC LIMIT 1`, projectID, now())
	g, err := scanGrant(row, time.Now())
	if errors.Is(err, sql.ErrNoRows) {
		return g, ErrNotFound
	}
	return g, err
}

// HasTell 判断会话是否已经收到过标题为 question 的团队消息（一次性提示去重用，Hub 重启后仍有效）。
func (s *Store) HasTell(ctx context.Context, key, question string) bool {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM decisions WHERE kind = ? AND session_key = ? AND question = ?`, KindTell, key, question).Scan(&n)
	return err == nil && n > 0
}

// RevokeGrant 撤销窗口；已撤销或已到期的返回错误。
func (s *Store) RevokeGrant(ctx context.Context, ref string) (Grant, error) {
	g, err := s.GetGrant(ctx, ref)
	if err != nil {
		return g, err
	}
	if g.Status != GrantActive {
		return g, fmt.Errorf("%s 已撤销或已到期", g.Code)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE grants SET revoked_at = ? WHERE id = ?`, now(), g.ID); err != nil {
		return g, err
	}
	return s.GetGrant(ctx, g.ID)
}

// ProxyAnswerDecision 记下参谋代答：状态进入“已决策待接收”，送达提问方；代答人、理由、风险档、原文都留在决策上。
// verdict 是批准类决策的结论（R-239），规则同 AnswerDecisionPoints。
func (s *Store) ProxyAnswerDecision(ctx context.Context, id, by, answer, reason, risk string, choices []int, verdict string) (Decision, error) {
	answer = strings.TrimSpace(answer)
	d0, err := s.GetDecision(ctx, id)
	if err != nil {
		return d0, err
	}
	verdict = answerVerdict(d0, answer, verdict)
	n := now()
	res, err := s.db.ExecContext(ctx, `UPDATE decisions SET status = ?, answer = ?, answer_files = '[]', choices = ?, answered_at = ?, delivered_at = NULL, delivered_turn = '', verdict = ?,
		proxy_by = ?, proxy_reason = ?, proxy_risk = ?, proxy_answer = ?, proxy_at = ?, overridden_at = '' WHERE id = ? AND status = ?`,
		DecAnswered, answer, blocks.Encode(choices), n, verdict, by, strings.TrimSpace(reason), risk, answer, n, id, DecPending)
	if err != nil {
		return Decision{}, err
	}
	if k, _ := res.RowsAffected(); k == 0 {
		return Decision{}, errors.New("这条决策已经不是待决策（可能用户刚答复或已撤回）")
	}
	return s.GetDecision(ctx, id)
}

// ProxiedDecisions 列出参谋代答过的决策（新的在前），projectID 为空表示全部。
func (s *Store) ProxiedDecisions(ctx context.Context, projectID string, limit int) ([]Decision, error) {
	q, args := decSelect+` WHERE x.proxy_by <> ''`, []any{}
	if projectID != "" {
		q += ` AND x.project_id = ?`
		args = append(args, projectID)
	}
	q += ` ORDER BY x.proxy_at DESC LIMIT ?`
	args = append(args, limit)
	return s.queryDecs(ctx, q, args...)
}
