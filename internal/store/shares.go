package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/NekroAI/kitestring/internal/protocol"
)

// 分享（R-281）：计划、阶段、需求、会话挂到一个分享上，访客凭带名字的链接免登录只读访问。

// 分享状态，按 expires_at / revoked_at 推算。
const (
	ShareActive  = "active"  // 生效中
	ShareExpired = "expired" // 已失效（到期）
	ShareRevoked = "revoked" // 已撤回
)

// 挂载项的类别。
const (
	ShareKindPlan    = "plan"
	ShareKindStage   = "stage"
	ShareKindReq     = "req"
	ShareKindSession = "session"
)

// Share 是一个分享。
type Share struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	ProjectID   string      `json:"project_id,omitempty"`
	HasPassword bool        `json:"has_password" doc:"设了访问密码（所有链接共用）"`
	ExpiresAt   *time.Time  `json:"expires_at,omitempty" doc:"到期时间；省略表示永久"`
	RevokedAt   *time.Time  `json:"revoked_at,omitempty" doc:"撤回的时间；重新启用时清除"`
	Status      string      `json:"status" enum:"active,expired,revoked" doc:"active 生效中 / expired 已失效（到期）/ revoked 已撤回"`
	Items       []ShareItem `json:"items"`
	Links       []ShareLink `json:"links"`
	CreatedAt   time.Time   `json:"created_at"`
	UpdatedAt   time.Time   `json:"updated_at"`

	PasswordHash string `json:"-"`
}

// ShareItem 是分享上的一个挂载项。
type ShareItem struct {
	Kind string `json:"kind" enum:"plan,stage,req,session"`
	// Ref：计划 / 阶段 / 需求的 ID，会话键。写入时计划、需求也可以写编号（P-x / R-x）
	Ref      string `json:"ref"`
	Auto     bool   `json:"auto" doc:"包含以后新增的：计划后来新增的阶段和需求、阶段后来放进的需求、需求后来关联的会话自动纳入"`
	Excluded bool   `json:"excluded" doc:"被单独取消：上级开着「包含以后新增的」也不会把它带回来"`
}

// ShareLink 是一个访客的链接。
type ShareLink struct {
	ID          string     `json:"id"`
	ShareID     string     `json:"share_id"`
	Name        string     `json:"name"`
	Token       string     `json:"token"`
	CreatedAt   time.Time  `json:"created_at"`
	RevokedAt   *time.Time `json:"revoked_at,omitempty"`
	LastVisitAt *time.Time `json:"last_visit_at,omitempty"`
	Visits      int        `json:"visits"`
}

// StatusAt 推算分享在某时刻的状态。
func (x Share) StatusAt(t time.Time) string {
	switch {
	case x.RevokedAt != nil:
		return ShareRevoked
	case x.ExpiresAt != nil && !t.Before(*x.ExpiresAt):
		return ShareExpired
	}
	return ShareActive
}

const shareSelect = `SELECT id, name, COALESCE(project_id, ''), password_hash, expires_at, revoked_at, created_at, updated_at FROM shares`

func scanShare(row interface{ Scan(...any) error }) (Share, error) {
	var x Share
	var exp, rev, created, updated string
	if err := row.Scan(&x.ID, &x.Name, &x.ProjectID, &x.PasswordHash, &exp, &rev, &created, &updated); err != nil {
		return x, err
	}
	x.HasPassword = x.PasswordHash != ""
	x.ExpiresAt = parseNullTS(sql.NullString{String: exp, Valid: exp != ""})
	x.RevokedAt = parseNullTS(sql.NullString{String: rev, Valid: rev != ""})
	x.CreatedAt, x.UpdatedAt = parseTS(created), parseTS(updated)
	x.Status = x.StatusAt(time.Now())
	x.Items, x.Links = []ShareItem{}, []ShareLink{}
	return x, nil
}

// NormShareItems 整理挂载项：计划、需求的编号换成 ID，阶段、会话核对存在；同一项只留一条。
func (s *Store) NormShareItems(ctx context.Context, items []ShareItem) ([]ShareItem, error) {
	out := []ShareItem{}
	seen := map[string]bool{}
	for _, it := range items {
		ref := strings.TrimSpace(it.Ref)
		var err error
		switch it.Kind {
		case ShareKindPlan:
			var p Plan
			if p, err = s.GetPlan(ctx, ref); err == nil {
				ref = p.ID
			}
		case ShareKindReq:
			var r Requirement
			if r, err = s.GetRequirement(ctx, ref); err == nil {
				ref = r.ID
			}
		case ShareKindStage:
			err = s.db.QueryRowContext(ctx, `SELECT id FROM plan_stages WHERE id = ?`, ref).Scan(&ref)
		case ShareKindSession:
			err = s.db.QueryRowContext(ctx, `SELECT key FROM sessions WHERE key = ?`, ref).Scan(&ref)
		default:
			return nil, fmt.Errorf("不认识的挂载类别：%s", it.Kind)
		}
		if errors.Is(err, sql.ErrNoRows) || errors.Is(err, ErrNotFound) {
			return nil, fmt.Errorf("要分享的内容不存在：%s", it.Ref)
		}
		if err != nil {
			return nil, err
		}
		k := it.Kind + "|" + ref
		if seen[k] {
			continue
		}
		seen[k] = true
		it.Ref = ref
		if it.Kind == ShareKindSession {
			it.Auto = false // 会话下面没有再挂的
		}
		out = append(out, it)
	}
	return out, nil
}

// CreateShare 新建分享，items 应已整理（NormShareItems）。
func (s *Store) CreateShare(ctx context.Context, x Share, items []ShareItem) (Share, error) {
	x.ID = protocol.NewID()
	n := now()
	err := s.tx(ctx, func(t *sql.Tx) error {
		if _, err := t.ExecContext(ctx, `INSERT INTO shares(id, name, project_id, password_hash, expires_at, created_at, updated_at) VALUES(?, ?, ?, ?, ?, ?, ?)`,
			x.ID, x.Name, nullStr(x.ProjectID), x.PasswordHash, tsOrEmpty(x.ExpiresAt), n, n); err != nil {
			return err
		}
		return setShareItems(ctx, t, x.ID, items)
	})
	if err != nil {
		return x, err
	}
	return s.GetShare(ctx, x.ID)
}

func tsOrEmpty(t *time.Time) string {
	if t == nil {
		return ""
	}
	return ts(*t)
}

func setShareItems(ctx context.Context, t *sql.Tx, id string, items []ShareItem) error {
	if _, err := t.ExecContext(ctx, `DELETE FROM share_items WHERE share_id = ?`, id); err != nil {
		return err
	}
	for _, it := range items {
		if _, err := t.ExecContext(ctx, `INSERT INTO share_items(share_id, kind, ref, auto, excluded) VALUES(?, ?, ?, ?, ?)`, id, it.Kind, it.Ref, it.Auto, it.Excluded); err != nil {
			return err
		}
	}
	return nil
}

// ShareUpdate 是对分享的修改；nil 表示不改。
type ShareUpdate struct {
	Name         *string
	PasswordHash *string // "" = 去掉密码
	ExpiresAt    **time.Time
	Items        []ShareItem // nil = 不改；空切片 = 清空
}

// UpdateShare 修改分享。
func (s *Store) UpdateShare(ctx context.Context, id string, u ShareUpdate) (Share, error) {
	if _, err := s.GetShare(ctx, id); err != nil {
		return Share{}, err
	}
	err := s.tx(ctx, func(t *sql.Tx) error {
		set := func(col string, v any) error {
			_, err := t.ExecContext(ctx, `UPDATE shares SET `+col+` = ?, updated_at = ? WHERE id = ?`, v, now(), id)
			return err
		}
		if u.Name != nil {
			if err := set("name", *u.Name); err != nil {
				return err
			}
		}
		if u.PasswordHash != nil {
			if err := set("password_hash", *u.PasswordHash); err != nil {
				return err
			}
		}
		if u.ExpiresAt != nil {
			if err := set("expires_at", tsOrEmpty(*u.ExpiresAt)); err != nil {
				return err
			}
		}
		if u.Items != nil {
			if err := setShareItems(ctx, t, id, u.Items); err != nil {
				return err
			}
			return set("updated_at", now())
		}
		return nil
	})
	if err != nil {
		return Share{}, err
	}
	return s.GetShare(ctx, id)
}

// SetShareRevoked 撤回 / 重新启用分享。
func (s *Store) SetShareRevoked(ctx context.Context, id string, revoked bool) error {
	v := ""
	if revoked {
		v = now()
	}
	res, err := s.db.ExecContext(ctx, `UPDATE shares SET revoked_at = ?, updated_at = ? WHERE id = ?`, v, now(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteShare 删除分享和它的全部链接。
func (s *Store) DeleteShare(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM shares WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// GetShare 读取分享，带挂载项和链接。
func (s *Store) GetShare(ctx context.Context, id string) (Share, error) {
	x, err := scanShare(s.db.QueryRowContext(ctx, shareSelect+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return x, ErrNotFound
	}
	if err != nil {
		return x, err
	}
	if x.Items, err = s.shareItems(ctx, id); err != nil {
		return x, err
	}
	x.Links, err = s.shareLinks(ctx, `WHERE share_id = ? ORDER BY created_at`, id)
	return x, err
}

// ListShares 列出全部分享（新的在前），带挂载项和链接。
func (s *Store) ListShares(ctx context.Context) ([]Share, error) {
	rows, err := s.db.QueryContext(ctx, shareSelect+` ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	out := []Share{}
	for rows.Next() {
		x, err := scanShare(rows)
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
		if out[i].Items, err = s.shareItems(ctx, out[i].ID); err != nil {
			return nil, err
		}
		if out[i].Links, err = s.shareLinks(ctx, `WHERE share_id = ? ORDER BY created_at`, out[i].ID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Store) shareItems(ctx context.Context, id string) ([]ShareItem, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT kind, ref, auto, excluded FROM share_items WHERE share_id = ? ORDER BY rowid`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ShareItem{}
	for rows.Next() {
		var it ShareItem
		if err := rows.Scan(&it.Kind, &it.Ref, &it.Auto, &it.Excluded); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

const linkSelect = `SELECT id, share_id, name, token, created_at, revoked_at, last_visit_at, visits FROM share_links `

func (s *Store) shareLinks(ctx context.Context, where string, args ...any) ([]ShareLink, error) {
	rows, err := s.db.QueryContext(ctx, linkSelect+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ShareLink{}
	for rows.Next() {
		var l ShareLink
		var created, rev, visit string
		if err := rows.Scan(&l.ID, &l.ShareID, &l.Name, &l.Token, &created, &rev, &visit, &l.Visits); err != nil {
			return nil, err
		}
		l.CreatedAt = parseTS(created)
		l.RevokedAt = parseNullTS(sql.NullString{String: rev, Valid: rev != ""})
		l.LastVisitAt = parseNullTS(sql.NullString{String: visit, Valid: visit != ""})
		out = append(out, l)
	}
	return out, rows.Err()
}

// CreateShareLink 给分享加一条访客链接。
func (s *Store) CreateShareLink(ctx context.Context, shareID, name, token string) (ShareLink, error) {
	if _, err := s.GetShare(ctx, shareID); err != nil {
		return ShareLink{}, err
	}
	id := protocol.NewID()
	if _, err := s.db.ExecContext(ctx, `INSERT INTO share_links(id, share_id, name, token, created_at) VALUES(?, ?, ?, ?, ?)`, id, shareID, name, token, now()); err != nil {
		return ShareLink{}, err
	}
	return s.GetShareLink(ctx, shareID, id)
}

// GetShareLink 读取分享下的一条链接。
func (s *Store) GetShareLink(ctx context.Context, shareID, id string) (ShareLink, error) {
	ls, err := s.shareLinks(ctx, `WHERE share_id = ? AND id = ?`, shareID, id)
	if err != nil {
		return ShareLink{}, err
	}
	if len(ls) == 0 {
		return ShareLink{}, ErrNotFound
	}
	return ls[0], nil
}

// ShareLinkByToken 按 token 找链接；找不到返回 ErrNotFound。
func (s *Store) ShareLinkByToken(ctx context.Context, token string) (ShareLink, error) {
	if token == "" {
		return ShareLink{}, ErrNotFound
	}
	ls, err := s.shareLinks(ctx, `WHERE token = ?`, token)
	if err != nil {
		return ShareLink{}, err
	}
	if len(ls) == 0 {
		return ShareLink{}, ErrNotFound
	}
	return ls[0], nil
}

// UpdateShareLink 改链接的名字或撤回 / 恢复；nil 表示不改。
func (s *Store) UpdateShareLink(ctx context.Context, shareID, id string, name *string, revoked *bool) (ShareLink, error) {
	if _, err := s.GetShareLink(ctx, shareID, id); err != nil {
		return ShareLink{}, err
	}
	if name != nil {
		if _, err := s.db.ExecContext(ctx, `UPDATE share_links SET name = ? WHERE id = ?`, *name, id); err != nil {
			return ShareLink{}, err
		}
	}
	if revoked != nil {
		v := ""
		if *revoked {
			v = now()
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE share_links SET revoked_at = ? WHERE id = ?`, v, id); err != nil {
			return ShareLink{}, err
		}
	}
	return s.GetShareLink(ctx, shareID, id)
}

// DeleteShareLink 删除一条链接。
func (s *Store) DeleteShareLink(ctx context.Context, shareID, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM share_links WHERE share_id = ? AND id = ?`, shareID, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// TouchShareLink 记一次访问。
func (s *Store) TouchShareLink(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE share_links SET visits = visits + 1, last_visit_at = ? WHERE id = ?`, now(), id)
	return err
}

// ---------- 分享范围 ----------

// ShareScope 是一个分享实际包含的内容：挂载项展开「包含以后新增的」、去掉单独取消的之后的结果。
type ShareScope struct {
	Plans    map[string]bool // 计划 ID
	Stages   map[string]bool // 阶段 ID
	Reqs     map[string]bool // 需求 ID
	Sessions map[string]bool // 会话键
}

// HasReq 判断需求在不在分享里。
func (sc ShareScope) HasReq(id string) bool { return sc.Reqs[id] }

// ResolveShare 按挂载项算出分享实际包含的内容。
// 规则：明确挂上的项按自己的 auto；上级开着 auto 时，它下面现在和以后的项都算进来（也带着 auto 往下传），
// 除非那一项被单独取消（excluded）——单独取消的项连同只经由它带进来的下级都不算。
func (s *Store) ResolveShare(ctx context.Context, items []ShareItem) (ShareScope, error) {
	sc := ShareScope{Plans: map[string]bool{}, Stages: map[string]bool{}, Reqs: map[string]bool{}, Sessions: map[string]bool{}}
	rows := map[string]ShareItem{}
	for _, it := range items {
		rows[it.Kind+"|"+it.Ref] = it
	}
	// 明确挂上的项：included / auto
	type node struct{ in, auto bool }
	decide := func(kind, ref string, inherited bool) (node, bool) {
		if it, ok := rows[kind+"|"+ref]; ok {
			return node{!it.Excluded, !it.Excluded && it.Auto}, true
		}
		if inherited {
			return node{true, true}, true
		}
		return node{}, false
	}
	var autoPlans, autoStages, autoReqs []string
	for _, it := range items {
		if it.Kind == ShareKindPlan && !it.Excluded {
			if err := s.db.QueryRowContext(ctx, `SELECT id FROM plans WHERE id = ?`, it.Ref).Scan(new(string)); err != nil {
				continue
			}
			sc.Plans[it.Ref] = true
			if it.Auto {
				autoPlans = append(autoPlans, it.Ref)
			}
		}
	}
	// 阶段：明确挂上的 + 开着 auto 的计划下面的
	stageIn := map[string]bool{} // 阶段 ID → 经由 auto 计划带进来
	if err := s.idPairs(ctx, `SELECT id, plan_id FROM plan_stages WHERE plan_id IN `, autoPlans, func(id, _ string) { stageIn[id] = true }); err != nil {
		return sc, err
	}
	for _, it := range items {
		if it.Kind == ShareKindStage {
			if err := s.db.QueryRowContext(ctx, `SELECT id FROM plan_stages WHERE id = ?`, it.Ref).Scan(new(string)); err == nil {
				if _, ok := stageIn[it.Ref]; !ok {
					stageIn[it.Ref] = false
				}
			}
		}
	}
	for id, inh := range stageIn {
		if n, ok := decide(ShareKindStage, id, inh); ok && n.in {
			sc.Stages[id] = true
			if n.auto {
				autoStages = append(autoStages, id)
			}
		}
	}
	// 需求：明确挂上的 + 开着 auto 的阶段里的
	reqIn := map[string]bool{}
	if err := s.idPairs(ctx, `SELECT id, stage_id FROM requirements WHERE stage_id IN `, autoStages, func(id, _ string) { reqIn[id] = true }); err != nil {
		return sc, err
	}
	for _, it := range items {
		if it.Kind == ShareKindReq {
			if err := s.db.QueryRowContext(ctx, `SELECT id FROM requirements WHERE id = ?`, it.Ref).Scan(new(string)); err == nil {
				if _, ok := reqIn[it.Ref]; !ok {
					reqIn[it.Ref] = false
				}
			}
		}
	}
	for id, inh := range reqIn {
		if n, ok := decide(ShareKindReq, id, inh); ok && n.in {
			sc.Reqs[id] = true
			if n.auto {
				autoReqs = append(autoReqs, id)
			}
		}
	}
	// 会话：明确挂上的 + 开着 auto 的需求关联的
	sessIn := map[string]bool{}
	if err := s.idPairs(ctx, `SELECT session_key, requirement_id FROM requirement_sessions WHERE requirement_id IN `, autoReqs, func(key, _ string) { sessIn[key] = true }); err != nil {
		return sc, err
	}
	for _, it := range items {
		if it.Kind == ShareKindSession {
			if err := s.db.QueryRowContext(ctx, `SELECT key FROM sessions WHERE key = ?`, it.Ref).Scan(new(string)); err == nil {
				if _, ok := sessIn[it.Ref]; !ok {
					sessIn[it.Ref] = false
				}
			}
		}
	}
	for key, inh := range sessIn {
		if n, ok := decide(ShareKindSession, key, inh); ok && n.in {
			sc.Sessions[key] = true
		}
	}
	return sc, nil
}

// idPairs 对 ids 分批跑 `... IN (?, ...)`，每行两列交给 fn。
func (s *Store) idPairs(ctx context.Context, q string, ids []string, fn func(a, b string)) error {
	for len(ids) > 0 {
		n := min(len(ids), 500)
		batch := ids[:n]
		ids = ids[n:]
		args := make([]any, len(batch))
		for i, id := range batch {
			args[i] = id
		}
		rows, err := s.db.QueryContext(ctx, q+`(?`+strings.Repeat(", ?", len(batch)-1)+`)`, args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var a, b sql.NullString
			if err := rows.Scan(&a, &b); err != nil {
				rows.Close()
				return err
			}
			fn(a.String, b.String)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
	}
	return nil
}

// ---------- 最近动态 ----------

// ShareActivity 是分享概览里「最近动态」的一条：只收有明确记录的事件（原则 11）。
type ShareActivity struct {
	At   time.Time `json:"at"`
	Kind string    `json:"kind" enum:"status,evidence,review,decision,answer,comment,link" doc:"status 改状态 / evidence 提交证据 / review 评审意见 / decision 提决策 / answer 决策作答 / comment 留言 / link 关联会话"`
	Req  string    `json:"req" doc:"需求编号 R-x"`
	Dec  string    `json:"dec,omitempty" doc:"决策 / 留言编号 D-x"`
	From string    `json:"from,omitempty" doc:"改状态：原状态"`
	To   string    `json:"to,omitempty" doc:"改状态：新状态"`
	// 谁做的：user 你 / agent 会话 / system 纸鸢
	Actor string `json:"actor" enum:"user,agent,system"`
	Role  string `json:"role,omitempty" doc:"会话当时的身份"`
	By    string `json:"by,omitempty" doc:"会话当时的标题快照"`
	// Session：做这件事的会话键；会话不在分享里时为空
	Session string `json:"session,omitempty"`
	Text    string `json:"text,omitempty" doc:"一句话：证据结论、决策问题、留言第一行（截断）"`
}

// ShareActivities 取几条需求最近的明确记录，新的在前。
func (s *Store) ShareActivities(ctx context.Context, reqIDs []string, limit int) ([]ShareActivity, error) {
	if len(reqIDs) == 0 {
		return []ShareActivity{}, nil
	}
	var out []ShareActivity
	in := func() (string, []any) {
		args := make([]any, len(reqIDs))
		for i, id := range reqIDs {
			args[i] = id
		}
		return `(?` + strings.Repeat(", ?", len(reqIDs)-1) + `)`, args
	}
	ph, args := in()
	lim := fmt.Sprintf(" ORDER BY at DESC LIMIT %d", limit)
	q := func(sqlq string, scan func(*sql.Rows) (ShareActivity, error)) error {
		rows, err := s.db.QueryContext(ctx, sqlq+lim, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			a, err := scan(rows)
			if err != nil {
				return err
			}
			out = append(out, a)
		}
		return rows.Err()
	}
	// 改状态
	if err := q(`SELECT c.at AS at, r.seq, c.from_status, c.to_status, c.actor, c.session_key, c.role, c.session_title FROM status_changes c JOIN requirements r ON r.id = c.requirement_id WHERE c.requirement_id IN `+ph,
		func(rows *sql.Rows) (ShareActivity, error) {
			var a ShareActivity
			var at string
			var seq int
			err := rows.Scan(&at, &seq, &a.From, &a.To, &a.Actor, &a.Session, &a.Role, &a.By)
			a.At, a.Kind, a.Req = parseTS(at), "status", reqCode(seq)
			return a, err
		}); err != nil {
		return nil, err
	}
	// 证据与评审意见（作废的不算）
	if err := q(`SELECT e.created_at AS at, r.seq, e.kind, COALESCE(e.session_key, ''), e.role, e.session_title, e.summary FROM evidence e JOIN requirements r ON r.id = e.requirement_id WHERE e.voided = 0 AND e.requirement_id IN `+ph,
		func(rows *sql.Rows) (ShareActivity, error) {
			var a ShareActivity
			var at, kind string
			var seq int
			err := rows.Scan(&at, &seq, &kind, &a.Session, &a.Role, &a.By, &a.Text)
			a.At, a.Kind, a.Req, a.Actor = parseTS(at), "evidence", reqCode(seq), "agent"
			if kind == "review" {
				a.Kind = "review"
			}
			if a.Session == "" {
				a.Actor = "user"
			}
			return a, err
		}); err != nil {
		return nil, err
	}
	// 决策：提出、作答；留言
	if err := q(`SELECT x.created_at AS at, r.seq, x.seq, x.kind, COALESCE(x.origin_session, ''), x.assigned_by, x.from_role, x.by_title, x.question, x.answer FROM decisions x JOIN requirements r ON r.id = x.requirement_id
		WHERE x.kind IN ('decision', 'comment') AND x.requirement_id IN `+ph,
		func(rows *sql.Rows) (ShareActivity, error) {
			var a ShareActivity
			var at, kind, origin, by, question, answer string
			var rseq, dseq int
			err := rows.Scan(&at, &rseq, &dseq, &kind, &origin, &by, &a.Role, &a.By, &question, &answer)
			a.At, a.Req, a.Dec = parseTS(at), reqCode(rseq), decCode(dseq)
			if kind == KindComment {
				a.Kind, a.Session, a.Text = "comment", by, answer
			} else {
				a.Kind, a.Session, a.Text = "decision", origin, question
			}
			a.Actor = "agent"
			if a.Session == "" {
				a.Actor = "user"
			}
			return a, err
		}); err != nil {
		return nil, err
	}
	if err := q(`SELECT x.answered_at AS at, r.seq, x.seq, x.proxy_by FROM decisions x JOIN requirements r ON r.id = x.requirement_id
		WHERE x.kind = 'decision' AND x.answered_at IS NOT NULL AND x.answered_at <> '' AND x.requirement_id IN `+ph,
		func(rows *sql.Rows) (ShareActivity, error) {
			var a ShareActivity
			var at, proxy string
			var rseq, dseq int
			err := rows.Scan(&at, &rseq, &dseq, &proxy)
			a.At, a.Kind, a.Req, a.Dec, a.Actor = parseTS(at), "answer", reqCode(rseq), decCode(dseq), "user"
			if proxy != "" {
				a.Actor, a.Session, a.Role = "agent", proxy, "aide"
			}
			return a, err
		}); err != nil {
		return nil, err
	}
	// 关联会话
	if err := q(`SELECT l.linked_at AS at, r.seq, l.session_key, COALESCE(x.role, ''), COALESCE(x.title, '') FROM requirement_sessions l JOIN requirements r ON r.id = l.requirement_id LEFT JOIN sessions x ON x.key = l.session_key WHERE l.requirement_id IN `+ph,
		func(rows *sql.Rows) (ShareActivity, error) {
			var a ShareActivity
			var at string
			var seq int
			err := rows.Scan(&at, &seq, &a.Session, &a.Role, &a.By)
			a.At, a.Kind, a.Req, a.Actor = parseTS(at), "link", reqCode(seq), "agent"
			return a, err
		}); err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	if len(out) > limit {
		out = out[:limit]
	}
	for i := range out {
		out[i].Text = shareLine(out[i].Text, 80)
	}
	if out == nil {
		out = []ShareActivity{}
	}
	return out, nil
}

// shareLine 取第一行并截到 n 个字。
func shareLine(s string, n int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	if r := []rune(s); len(r) > n {
		s = string(r[:n]) + "…"
	}
	return s
}

// ReqSessionLinks 返回几条需求关联的会话：会话键 → 需求 ID 列表。
func (s *Store) ReqSessionLinks(ctx context.Context, reqIDs []string) (map[string][]string, error) {
	out := map[string][]string{}
	err := s.idPairs(ctx, `SELECT session_key, requirement_id FROM requirement_sessions WHERE requirement_id IN `, reqIDs, func(key, rid string) {
		out[key] = append(out[key], rid)
	})
	return out, err
}

// GuestDecisionKinds 是访客在需求里看得到的决策类别：决策本身，以及需求时间线上的修改意见、留言、修改、放行、指派等记录。
// 团队消息、送达批次、身份变更这类会话之间的往来不给访客看。
var GuestDecisionKinds = []string{KindDecision, KindFeedback, KindNote, KindAssign, KindReviewTask, KindRelease, KindEdit, KindComment}

// ReqDecisions 列出需求上的决策（kind = decision），新的在前。
func (s *Store) ReqDecisions(ctx context.Context, reqID string) ([]Decision, error) {
	return s.queryDecs(ctx, decSelect+` WHERE x.requirement_id = ? AND x.kind = ? ORDER BY x.seq DESC`, reqID, KindDecision)
}

// FileOwner 是引用了某个附件的记录：需求（描述附图、证据）、决策（附图、表达块）或阶段（小结的表达块）。
type FileOwner struct {
	ReqID   string // 需求描述或证据里的
	DecReq  string // 决策所属的需求
	DecKind string
	StageID string
}

// FileOwners 找出引用了附件 id 的记录。附件 ID 是 ULID，按文字在 JSON 列里找。
func (s *Store) FileOwners(ctx context.Context, id string) ([]FileOwner, error) {
	if id == "" || strings.ContainsAny(id, `%_"\`) {
		return nil, nil
	}
	like := "%" + id + "%"
	var out []FileOwner
	scan := func(q string, args []any, fn func(a, b string) FileOwner) error {
		rows, err := s.db.QueryContext(ctx, q, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var a, b sql.NullString
			if err := rows.Scan(&a, &b); err != nil {
				return err
			}
			out = append(out, fn(a.String, b.String))
		}
		return rows.Err()
	}
	if err := scan(`SELECT id, '' FROM requirements WHERE files LIKE ?`, []any{like}, func(a, _ string) FileOwner { return FileOwner{ReqID: a} }); err != nil {
		return nil, err
	}
	if err := scan(`SELECT requirement_id, '' FROM evidence WHERE files LIKE ? OR payload LIKE ?`, []any{like, like}, func(a, _ string) FileOwner { return FileOwner{ReqID: a} }); err != nil {
		return nil, err
	}
	if err := scan(`SELECT requirement_id, kind FROM decisions WHERE requirement_id IS NOT NULL AND (context_files LIKE ? OR option_files LIKE ? OR answer_files LIKE ? OR blocks LIKE ? OR points LIKE ?)`,
		[]any{like, like, like, like, like}, func(a, b string) FileOwner { return FileOwner{DecReq: a, DecKind: b} }); err != nil {
		return nil, err
	}
	if err := scan(`SELECT id, '' FROM plan_stages WHERE summary_blocks LIKE ?`, []any{like}, func(a, _ string) FileOwner { return FileOwner{StageID: a} }); err != nil {
		return nil, err
	}
	return out, nil
}
