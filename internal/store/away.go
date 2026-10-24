package store

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"

	"github.com/NekroAI/kitestring/internal/protocol"
)

// 暂时离开（R-210）：你点「暂时离开」记一行，只能你手动结束；期间的推送分级、你在各设备上的处理、阶段性报告记在 away_events 里。

// 离开期间事件的分档。
const (
	AwayPush    = "push"    // 推了
	AwayHold    = "hold"    // 勿扰：本该推，先攒着
	AwayMerge   = "merge"   // 合并进报告，不推
	AwaySkip    = "skip"    // 不推（主助理可代答、会话空闲）
	AwayUser    = "user"    // 你在某台设备上处理了
	AwaySettled = "settled" // 能处理的都处理完了：阶段性报告
)

// 授权范围：none = 不代答（主助理只写建议）。
const AwayScopeNone = "none"

// Away 是一次离开。
type Away struct {
	ID            string     `json:"id"`
	Code          string     `json:"code" doc:"离开编号（A-x）"`
	StartedAt     time.Time  `json:"started_at" doc:"点「暂时离开」的时刻，时段报告从这里算起"`
	ExpectMinutes int        `json:"expect_minutes" doc:"预估离开多久（分钟）；0 = 不确定"`
	Scope         string     `json:"scope" enum:"none,low,medium" doc:"主助理可代答到哪一档：none 不代答（只写建议）/ low / medium"`
	HoldPush      bool       `json:"hold_push" doc:"这次离开期间连推送类也先攒着（拿不准的仍推）"`
	EndedAt       *time.Time `json:"ended_at,omitempty" doc:"你点「结束离开」的时刻；空 = 还在离开中"`
	CalmSince     *time.Time `json:"calm_since,omitempty" doc:"「能处理的都处理完」从何时开始一直成立"`
	SettledAt     *time.Time `json:"settled_at,omitempty" doc:"最近一次生成阶段性报告的时间"`
	Worked        bool       `json:"-"` // 上次阶段性报告之后有没有发生过事
}

// Active 是否还在离开中。
func (a Away) Active() bool { return a.EndedAt == nil }

func awayCode(seq int) string { return "A-" + strconv.Itoa(seq) }

const awaySelect = `SELECT id, seq, started_at, expect_minutes, scope, hold_push, ended_at, calm_since, settled_at, worked FROM aways`

func scanAway(row interface{ Scan(...any) error }) (Away, error) {
	var a Away
	var seq int
	var started, ended, calm, settled string
	err := row.Scan(&a.ID, &seq, &started, &a.ExpectMinutes, &a.Scope, &a.HoldPush, &ended, &calm, &settled, &a.Worked)
	a.Code = awayCode(seq)
	a.StartedAt = parseTS(started)
	a.EndedAt = parseNullTS(sql.NullString{String: ended, Valid: ended != ""})
	a.CalmSince = parseNullTS(sql.NullString{String: calm, Valid: calm != ""})
	a.SettledAt = parseNullTS(sql.NullString{String: settled, Valid: settled != ""})
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return a, err
}

// StartAway 开始一次离开；已经在离开中时返回 ErrAwayActive。
func (s *Store) StartAway(ctx context.Context, at time.Time, expectMinutes int, scope string, holdPush bool) (Away, error) {
	id := protocol.NewID()
	err := s.tx(ctx, func(t *sql.Tx) error {
		var n int
		if err := t.QueryRowContext(ctx, `SELECT COUNT(*) FROM aways WHERE ended_at = ''`).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return ErrAwayActive
		}
		var seq int
		if err := t.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) + 1 FROM aways`).Scan(&seq); err != nil {
			return err
		}
		_, err := t.ExecContext(ctx, `INSERT INTO aways(id, seq, started_at, expect_minutes, scope, hold_push) VALUES(?, ?, ?, ?, ?, ?)`,
			id, seq, ts(at), expectMinutes, scope, holdPush)
		return err
	})
	if err != nil {
		return Away{}, err
	}
	return s.GetAway(ctx, id)
}

// ErrAwayActive：已经在离开中。
var ErrAwayActive = errors.New("已经在离开中：先结束这次离开")

// GetAway 按 ID 或 A-编号读取。
func (s *Store) GetAway(ctx context.Context, ref string) (Away, error) {
	if seq, ok := parseRef(ref, "A"); ok {
		return scanAway(s.db.QueryRowContext(ctx, awaySelect+` WHERE seq = ?`, seq))
	}
	return scanAway(s.db.QueryRowContext(ctx, awaySelect+` WHERE id = ?`, ref))
}

// ActiveAway 返回进行中的离开；没有时返回 ErrNotFound。
func (s *Store) ActiveAway(ctx context.Context) (Away, error) {
	return scanAway(s.db.QueryRowContext(ctx, awaySelect+` WHERE ended_at = '' ORDER BY seq DESC LIMIT 1`))
}

// LatestAway 返回最近一次离开（进行中或已结束）；从没离开过时返回 ErrNotFound。
func (s *Store) LatestAway(ctx context.Context) (Away, error) {
	return scanAway(s.db.QueryRowContext(ctx, awaySelect+` ORDER BY seq DESC LIMIT 1`))
}

// EndAway 结束离开；已结束的返回错误。
func (s *Store) EndAway(ctx context.Context, id string, at time.Time) (Away, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE aways SET ended_at = ? WHERE id = ? AND ended_at = ''`, ts(at), id)
	if err != nil {
		return Away{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Away{}, errors.New("这次离开已经结束了")
	}
	return s.GetAway(ctx, id)
}

// SetAwayCalm 记下「都处理完」开始成立的时间（nil = 不再成立）。
func (s *Store) SetAwayCalm(ctx context.Context, id string, since *time.Time) error {
	v := ""
	if since != nil {
		v = ts(*since)
	}
	_, err := s.db.ExecContext(ctx, `UPDATE aways SET calm_since = ? WHERE id = ?`, v, id)
	return err
}

// SetAwaySettled 记下生成阶段性报告的时间，清掉「发生过事」。
func (s *Store) SetAwaySettled(ctx context.Context, id string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE aways SET settled_at = ?, worked = 0 WHERE id = ?`, ts(at), id)
	return err
}

// MarkAwayWorked 记下离开期间发生过事（会话在跑、有推送或处理）。
func (s *Store) MarkAwayWorked(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE aways SET worked = 1 WHERE id = ? AND worked = 0`, id)
	return err
}

// AwayHandled 统计 since 之后主助理处理了几件：代答、放行草稿、写验收建议。
func (s *Store) AwayHandled(ctx context.Context, since time.Time) (proxied, released, advised int, err error) {
	t := ts(since)
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM decisions WHERE proxy_by <> '' AND proxy_at >= ?`, t).Scan(&proxied); err != nil {
		return
	}
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM decisions WHERE kind = ? AND created_at >= ?`, KindRelease, t).Scan(&released); err != nil {
		return
	}
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM review_advice WHERE at >= ?`, t).Scan(&advised)
	return
}

// AwayEvent 是离开期间的一件事。
type AwayEvent struct {
	At        time.Time `json:"at"`
	Tier      string    `json:"tier" enum:"push,hold,merge,skip,user,settled" doc:"push 推了 / hold 勿扰先攒着 / merge 合并进报告 / skip 不推 / user 你在某台设备上处理了 / settled 阶段性报告"`
	Event     string    `json:"event" doc:"通知事件（decision_raised、requirement_review…）或你的操作（answer、review、plan_start…）"`
	ProjectID string    `json:"project_id,omitempty"`
	Ref       string    `json:"ref,omitempty" doc:"关联编号：D-x / R-x / P-x"`
	Title     string    `json:"title"`
	Body      string    `json:"body,omitempty"`
	Reason    string    `json:"reason,omitempty" doc:"为什么归到这一档"`
	Device    string    `json:"device,omitempty" doc:"你处理时用的设备，如 iPhone、Mac"`
	Normal    bool      `json:"normal" doc:"不在离开时这条会不会推送"`
}

// AddAwayEvent 记一件事。
func (s *Store) AddAwayEvent(ctx context.Context, awayID string, e AwayEvent) error {
	if e.At.IsZero() {
		e.At = time.Now()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO away_events(id, away_id, at, tier, event, project_id, ref, title, body, reason, device, normal) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		protocol.NewID(), awayID, ts(e.At), e.Tier, e.Event, e.ProjectID, e.Ref, e.Title, e.Body, e.Reason, e.Device, e.Normal)
	return err
}

// AwayEvents 列出一次离开里的事（按时间先后）。
func (s *Store) AwayEvents(ctx context.Context, awayID string) ([]AwayEvent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT at, tier, event, project_id, ref, title, body, reason, device, normal FROM away_events WHERE away_id = ? ORDER BY at, rowid`, awayID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AwayEvent{}
	for rows.Next() {
		var e AwayEvent
		var at string
		if err := rows.Scan(&at, &e.Tier, &e.Event, &e.ProjectID, &e.Ref, &e.Title, &e.Body, &e.Reason, &e.Device, &e.Normal); err != nil {
			return nil, err
		}
		e.At = parseTS(at)
		out = append(out, e)
	}
	return out, rows.Err()
}

// LinkGrantAway 把授权窗口记到某次离开名下。
func (s *Store) LinkGrantAway(ctx context.Context, grantID, awayID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE grants SET away_id = ? WHERE id = ?`, awayID, grantID)
	return err
}

// AwayGrants 列出某次离开开的授权窗口。
func (s *Store) AwayGrants(ctx context.Context, awayID string) ([]Grant, error) {
	rows, err := s.db.QueryContext(ctx, grantSelect+` WHERE g.away_id = ? ORDER BY g.seq`, awayID)
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

// AwayCalm 是判断「能处理的都处理完」用的计数（不含隐藏项目、后台任务）。
type AwayCalm struct {
	Running   int // 运行中的会话（含本轮结束但后台任务还在跑的）
	Queued    int // 待开始（排队）的需求
	Unadvised int // 没有主助理验收建议的待验收需求
}

// Calm 三个计数都为 0。
func (c AwayCalm) Calm() bool { return c.Running == 0 && c.Queued == 0 && c.Unadvised == 0 }

// AwayCalmCounts 统计「能处理的都处理完」的三项。
func (s *Store) AwayCalmCounts(ctx context.Context) (AwayCalm, error) {
	var c AwayCalm
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions s LEFT JOIN projects p ON p.id = s.project_id
		WHERE s.state IN (?, ?) AND s.background = 0 AND COALESCE(p.hidden, 0) = 0`, StateRunning, StateBackground).Scan(&c.Running)
	if err != nil {
		return c, err
	}
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM requirements r LEFT JOIN projects p ON p.id = r.project_id
		WHERE r.status = ? AND COALESCE(p.hidden, 0) = 0`, ReqReady).Scan(&c.Queued); err != nil {
		return c, err
	}
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM requirements r LEFT JOIN projects p ON p.id = r.project_id
		WHERE r.status = ? AND COALESCE(p.hidden, 0) = 0 AND NOT EXISTS (
			SELECT 1 FROM review_advice a WHERE a.requirement_id = r.id
			AND a.at >= COALESCE((SELECT MAX(e.created_at) FROM evidence e WHERE e.requirement_id = r.id AND e.kind <> ? AND e.voided = 0), ''))`,
		ReqReview, EvidenceReview).Scan(&c.Unadvised)
	return c, err
}
