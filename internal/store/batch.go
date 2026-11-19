package store

import (
	"context"
	"strings"
	"time"

	"github.com/NekroAI/kitestring/internal/blocks"
)

// 列表接口批量补关联数据（R-382）：原来逐条需求 / 逐个会话各查几次库（N+1），需求列表一次请求查库近 400 次，
// 网页一打开并发七八个接口，只读连接上排长队。这里改成按编号一次（或按批）查回来，在内存里按编号分组。
// 每个批量函数与原来的逐条查询用同样的条件和排序，结果一致；对比测试见 batch_test.go。

// inBatch 是一条语句里 IN (…) 最多放多少个参数：SQLite 单条语句的参数个数有上限。
const inBatch = 500

// eachChunk 把 ids 按 inBatch 一批拆开，逐批调用 fn：ph 是 "?, ?, …"，args 是对应的参数。
func eachChunk(ids []string, fn func(ph string, args []any) error) error {
	for len(ids) > 0 {
		n := min(len(ids), inBatch)
		args := make([]any, n)
		for i, id := range ids[:n] {
			args[i] = id
		}
		if err := fn(strings.TrimSuffix(strings.Repeat("?, ", n), ", "), args); err != nil {
			return err
		}
		ids = ids[n:]
	}
	return nil
}

// reqSessionsOf：需求编号 → 关联的会话键（按关联先后），对应 fillReqSessions。
func (s *Store) reqSessionsOf(ctx context.Context, ids []string) (map[string][]string, error) {
	out := map[string][]string{}
	err := eachChunk(ids, func(ph string, args []any) error {
		rows, err := s.db.QueryContext(ctx, `SELECT requirement_id, session_key FROM requirement_sessions WHERE requirement_id IN (`+ph+`) ORDER BY linked_at`, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id, k string
			if err := rows.Scan(&id, &k); err != nil {
				return err
			}
			out[id] = append(out[id], k)
		}
		return rows.Err()
	})
	return out, err
}

// fillReviewStates 批量给待验收的需求补评审验证情况，对应逐条的 fillReviewState：
// 状态变化、审查意见、审查任务各一次查回来（不再每条需求三次）。
func (s *Store) fillReviewStates(ctx context.Context, list []Requirement) error {
	var ids []string
	for _, r := range list {
		if r.Status == ReqReview {
			ids = append(ids, r.ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	changes := map[string][]StatusChange{}
	reviews := map[string][]Evidence{}
	tasks := map[string][]Decision{}
	err := eachChunk(ids, func(ph string, args []any) error {
		cs, err := s.statusChangesOf(ctx, ph, args)
		if err != nil {
			return err
		}
		for _, c := range cs {
			changes[c.RequirementID] = append(changes[c.RequirementID], c)
		}
		es, err := s.evidenceRows(ctx, `SELECT `+evidenceCols+` FROM evidence WHERE requirement_id IN (`+ph+`) AND kind = ? ORDER BY created_at`, append(args, EvidenceReview)...)
		if err != nil {
			return err
		}
		for _, e := range es {
			reviews[e.RequirementID] = append(reviews[e.RequirementID], e)
		}
		ds, err := s.queryDecs(ctx, decSelect+` WHERE x.requirement_id IN (`+ph+`) AND x.kind = ? ORDER BY x.seq DESC`, append(args, KindReviewTask)...)
		if err != nil {
			return err
		}
		for _, d := range ds {
			tasks[d.RequirementID] = append(tasks[d.RequirementID], d)
		}
		return nil
	})
	if err != nil {
		return err
	}
	// 评审会话的标题（意见里没带标题快照时显示它）：涉及的会话一次查回来；与 GetSession 同样要求会话的设备还在
	seen := map[string]bool{}
	var keys []string
	add := func(k string) {
		if k != "" && !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	for _, ds := range tasks {
		for _, d := range ds {
			add(d.SessionKey)
		}
	}
	for _, es := range reviews {
		for _, e := range es {
			add(e.SessionKey)
		}
	}
	titles := map[string]string{}
	err = eachChunk(keys, func(ph string, args []any) error {
		rows, err := s.db.QueryContext(ctx, `SELECT s.key, s.title FROM sessions s JOIN devices d ON d.id = s.device_id WHERE s.key IN (`+ph+`)`, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var k, t string
			if err := rows.Scan(&k, &t); err != nil {
				return err
			}
			titles[k] = t
		}
		return rows.Err()
	})
	if err != nil {
		return err
	}
	now := time.Now()
	for i := range list {
		r := &list[i]
		if r.Status != ReqReview {
			continue
		}
		full := *r
		// 与逐条查询一致：没有记录时是空切片而不是 nil
		full.StatusChanges, full.Reviews, full.Feedback = orEmpty(changes[r.ID]), orEmpty(reviews[r.ID]), orEmpty(tasks[r.ID])
		r.Review = reviewOf(full, now, func(key string) string { return titles[key] })
	}
	return nil
}

func orEmpty[T any](v []T) []T {
	if v == nil {
		return []T{}
	}
	return v
}

// statusChangesOf：一批需求的状态变化，按时间正序（同 ListStatusChanges 的排序）。
func (s *Store) statusChangesOf(ctx context.Context, ph string, args []any) ([]StatusChange, error) {
	rows, err := s.db.QueryContext(ctx, statusChangeSelect+` AND c.requirement_id IN (`+ph+`) ORDER BY c.at, c.rowid`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []StatusChange{}
	for rows.Next() {
		c, err := scanStatusChange(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// MorningNotes 一次取回全部项目的小结总述（项目 → 总述），对应逐个项目的 GetMorning。
func (s *Store) MorningNotes(ctx context.Context) (map[string]MorningNote, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT project_id, text, by_session, at, headline, look FROM morning_notes`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]MorningNote{}
	for rows.Next() {
		var id, at, look string
		var n MorningNote
		if err := rows.Scan(&id, &n.Text, &n.By, &at, &n.Headline, &look); err != nil {
			return nil, err
		}
		if v := blocks.Decode[[]string](look); v != nil {
			n.Look = *v
		}
		n.At = parseTS(at)
		out[id] = n
	}
	return out, rows.Err()
}

// LatestGrants 一次取回每个项目最近一个授权窗口（项目 → 窗口），对应逐个项目的 ListGrants(projectID, 1)。
func (s *Store) LatestGrants(ctx context.Context) (map[string]Grant, error) {
	return s.grantsByProject(ctx, grantSelect+` WHERE g.seq IN (SELECT MAX(seq) FROM grants GROUP BY project_id)`)
}

// ActiveGrants 一次取回每个项目当前生效中的窗口（项目 → 窗口），对应逐个项目的 ActiveGrant。
func (s *Store) ActiveGrants(ctx context.Context) (map[string]Grant, error) {
	return s.grantsByProject(ctx, grantSelect+` WHERE g.seq IN (SELECT MAX(seq) FROM grants WHERE revoked_at = '' AND expires_at > ? GROUP BY project_id)`, now())
}

func (s *Store) grantsByProject(ctx context.Context, q string, args ...any) (map[string]Grant, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Grant{}
	at := time.Now()
	for rows.Next() {
		g, err := scanGrant(rows, at)
		if err != nil {
			return nil, err
		}
		out[g.ProjectID] = g
	}
	return out, rows.Err()
}

// evidenceRows 执行一条取证据的查询（列为 evidenceCols），不补原型的最新版本。
func (s *Store) evidenceRows(ctx context.Context, q string, args ...any) ([]Evidence, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
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
