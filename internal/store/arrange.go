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

// 当前安排（R-245）：用户口头交代的临时安排落成项目上的短记录（N-x），任何会话都能记、标明是谁记的，用户在网页上也能加；
// 带到期时间或手动撤销，状态按时间推算，留痕不删。接任概览和压缩后的关键状态都带上生效中的几条。

// 安排状态。
const (
	ArrangeActive  = "active"
	ArrangeExpired = "expired"
	ArrangeRevoked = "revoked"
)

// ArrangeByUser 是用户在网页上记 / 撤的安排的署名。
const ArrangeByUser = "user"

// ArrangeTextMax 是一条安排的字数上限（一两句话）。
const ArrangeTextMax = 200

// Arrangement 是一条当前安排。
type Arrangement struct {
	ID          string     `json:"id"`
	Code        string     `json:"code" doc:"安排编号（N-x）"`
	ProjectID   string     `json:"project_id"`
	ProjectName string     `json:"project_name"`
	Text        string     `json:"text"`
	BySession   string     `json:"by_session" doc:"记下它的会话；用户在网页上加的为空"`
	ByTitle     string     `json:"by_title" doc:"记下它的会话当时的标题；用户加的为空"`
	ByRole      string     `json:"by_role" doc:"记下它的会话当时的身份"`
	CreatedAt   time.Time  `json:"created_at"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty" doc:"到期时间；不填表示一直有效，直到撤销"`
	RevokedAt   *time.Time `json:"revoked_at,omitempty"`
	RevokedBy   string     `json:"revoked_by,omitempty" doc:"撤销它的会话键；user 表示用户在网页上撤的"`
	Status      string     `json:"status" enum:"active,expired,revoked" doc:"生效中 / 已到期 / 已撤销"`
}

// Who 是安排的署名：用户加的写「用户」，会话加的写它当时的标题（没有标题时写会话键）。
func (a Arrangement) Who() string {
	if a.BySession == "" {
		return "用户"
	}
	if a.ByTitle != "" {
		return a.ByTitle
	}
	return a.BySession
}

func arrangeCode(seq int) string { return "N-" + strconv.Itoa(seq) }

const arrangeSelect = `SELECT a.id, a.seq, a.project_id, COALESCE(p.name, ''), a.text, a.by_session, a.by_title, a.by_role, a.created_at, a.expires_at, a.revoked_at, a.revoked_by
	FROM arrangements a LEFT JOIN projects p ON p.id = a.project_id`

func scanArrangement(row interface{ Scan(...any) error }, at time.Time) (Arrangement, error) {
	var a Arrangement
	var seq int
	var created, exp, revoked string
	if err := row.Scan(&a.ID, &seq, &a.ProjectID, &a.ProjectName, &a.Text, &a.BySession, &a.ByTitle, &a.ByRole, &created, &exp, &revoked, &a.RevokedBy); err != nil {
		return a, err
	}
	a.Code = arrangeCode(seq)
	a.CreatedAt = parseTS(created)
	a.ExpiresAt = parseNullTS(sql.NullString{String: exp, Valid: exp != ""})
	a.RevokedAt = parseNullTS(sql.NullString{String: revoked, Valid: revoked != ""})
	switch {
	case a.RevokedAt != nil:
		a.Status = ArrangeRevoked
	case a.ExpiresAt != nil && !at.Before(*a.ExpiresAt):
		a.Status = ArrangeExpired
	default:
		a.Status = ArrangeActive
	}
	return a, nil
}

// AddArrangement 记一条安排。by 是记下它的会话键，用户在网页上加时为空；expires 为空表示一直有效。
func (s *Store) AddArrangement(ctx context.Context, projectID, text, by string, expires *time.Time) (Arrangement, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return Arrangement{}, errors.New("安排的内容不能为空")
	}
	if n := len([]rune(text)); n > ArrangeTextMax {
		return Arrangement{}, fmt.Errorf("安排写了 %d 字，超过 %d 字：一两句话写清要怎样、到什么时候", n, ArrangeTextMax)
	}
	if projectID == "" {
		return Arrangement{}, errors.New("安排要记在项目上：先在项目目录里运行，或指定项目")
	}
	if expires != nil && !expires.After(time.Now()) {
		return Arrangement{}, errors.New("到期时间已经过了")
	}
	title, role := sessionSnap(ctx, s.db, by)
	exp := ""
	if expires != nil {
		exp = ts(*expires)
	}
	id := protocol.NewID()
	err := s.tx(ctx, func(t *sql.Tx) error {
		var seq int
		if err := t.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) + 1 FROM arrangements`).Scan(&seq); err != nil {
			return err
		}
		_, err := t.ExecContext(ctx, `INSERT INTO arrangements(id, seq, project_id, text, by_session, by_title, by_role, created_at, expires_at) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, seq, projectID, text, by, title, role, now(), exp)
		return err
	})
	if err != nil {
		return Arrangement{}, err
	}
	return s.GetArrangement(ctx, id)
}

// GetArrangement 按 ID 或编号（N-x）读一条安排。
func (s *Store) GetArrangement(ctx context.Context, ref string) (Arrangement, error) {
	var row *sql.Row
	if seq, ok := parseRef(ref, "N"); ok {
		row = s.db.QueryRowContext(ctx, arrangeSelect+` WHERE a.seq = ?`, seq)
	} else {
		row = s.db.QueryRowContext(ctx, arrangeSelect+` WHERE a.id = ?`, ref)
	}
	a, err := scanArrangement(row, time.Now())
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	return a, err
}

// ListArrangements 列出安排（新的在前）。projectID 为空时列全部项目；all 为假时只列生效中的。
func (s *Store) ListArrangements(ctx context.Context, projectID string, all bool) ([]Arrangement, error) {
	q, args := arrangeSelect+` WHERE 1 = 1`, []any{}
	if projectID != "" {
		q += ` AND a.project_id = ?`
		args = append(args, projectID)
	}
	if !all {
		q += ` AND a.revoked_at = '' AND (a.expires_at = '' OR a.expires_at > ?)`
		args = append(args, now())
	}
	q += ` ORDER BY a.seq DESC`
	if all {
		q += ` LIMIT 50`
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Arrangement{}
	at := time.Now()
	for rows.Next() {
		a, err := scanArrangement(rows, at)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// RevokeArrangement 撤销一条生效中的安排。by 是撤销它的会话键，用户在网页上撤时为 ArrangeByUser。
func (s *Store) RevokeArrangement(ctx context.Context, ref, by string) (Arrangement, error) {
	a, err := s.GetArrangement(ctx, ref)
	if err != nil {
		return a, err
	}
	switch a.Status {
	case ArrangeRevoked:
		return a, fmt.Errorf("%s 已经撤销过了", a.Code)
	case ArrangeExpired:
		return a, fmt.Errorf("%s 已经到期，不用再撤", a.Code)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE arrangements SET revoked_at = ?, revoked_by = ? WHERE id = ? AND revoked_at = ''`, now(), by, a.ID); err != nil {
		return a, err
	}
	return s.GetArrangement(ctx, a.ID)
}

// ParseUntil 解析安排的到期时间：时长（30m、2h、1d）或日期（2026-11-02、2026-11-02 18:00、11-02 18:00、18:00，按本地时间）。
// 空串返回 nil（一直有效）。
func ParseUntil(s string, now time.Time) (*time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	if strings.HasSuffix(s, "d") {
		if n, err := strconv.Atoi(strings.TrimSuffix(s, "d")); err == nil && n > 0 {
			t := now.Add(time.Duration(n) * 24 * time.Hour)
			return &t, nil
		}
	}
	if d, err := time.ParseDuration(s); err == nil {
		if d <= 0 {
			return nil, errors.New("到期时长要大于 0")
		}
		t := now.Add(d)
		return &t, nil
	}
	loc := now.Location()
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04", "2006-01-02T15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, s, loc); err == nil {
			if layout == "2006-01-02" {
				t = t.Add(24*time.Hour - time.Second) // 只写日期：到那天结束
			}
			return &t, nil
		}
	}
	if t, err := time.ParseInLocation("01-02 15:04", s, loc); err == nil {
		t = time.Date(now.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), 0, 0, loc)
		return &t, nil
	}
	if t, err := time.ParseInLocation("15:04", s, loc); err == nil {
		t = time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), 0, 0, loc)
		if !t.After(now) {
			t = t.Add(24 * time.Hour)
		}
		return &t, nil
	}
	return nil, fmt.Errorf("认不出到期时间 %q：写时长（30m、2h、1d）或日期（2026-11-02、2026-11-02 18:00、18:00）", s)
}
