package store

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/protocol"
)

// R-382：列表接口改成批量查询后，结果必须与原来逐条查询一致，查库次数不随条数增长。
// 下面的 ref… 是改动前逐条查询的原样实现，只留在测试里做对照。

func (s *Store) refFillReviewState(ctx context.Context, r *Requirement) error {
	if r.Status != ReqReview {
		return nil
	}
	full := *r
	var err error
	if full.StatusChanges, err = s.ListStatusChanges(ctx, StatusQuery{RequirementID: r.ID}); err != nil {
		return err
	}
	if full.Reviews, err = s.ListReviews(ctx, r.ID); err != nil {
		return err
	}
	if full.Feedback, err = s.queryDecs(ctx, decSelect+` WHERE x.requirement_id = ? AND x.kind = ? ORDER BY x.seq DESC`, r.ID, KindReviewTask); err != nil {
		return err
	}
	r.Review = s.ReviewOf(ctx, full, time.Now())
	return nil
}

func (s *Store) refFillActivity(ctx context.Context, x *Session) error {
	x.Activity = make([]int, activityBuckets)
	now := time.Now()
	from := now.Add(-activityBuckets * activityStep)
	if x.LastActiveAt.Before(from) {
		return nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT time FROM events WHERE session_key = ? AND time >= ? AND name <> 'KsTasks'`, x.Key, ts(from))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return err
		}
		i := int(parseTS(t).Sub(from) / activityStep)
		if i >= 0 && i < activityBuckets {
			x.Activity[i]++
		}
	}
	return rows.Err()
}

type batchFix struct {
	s        *Store
	p, q     string
	keys     []string
	reqs     []string
	reviewed []string
}

// batchFixture：两个项目、6 个会话（在跑的、一小时前的、已结束的）、20 条需求（4 条待验收，带审查任务、审查意见），
// 每条需求关联 0～3 个会话（关联先后与会话键顺序不同），会话有待答决策和近一小时的事件，外加计划、凭据、代答和授权窗口。
func batchFixture(t *testing.T) batchFix {
	t.Helper()
	ctx := context.Background()
	s := openTest(t)
	f := batchFix{s: s, p: testProject(t, s, "纸鸢"), q: testProject(t, s, "别的项目")}
	db := s.DB()
	if _, err := db.Exec(`INSERT INTO devices(id, name, token_hash, created_at) VALUES('dev1', 'mini', 'h', ?)`, now()); err != nil {
		t.Fatal(err)
	}
	nowT := time.Now()
	for i := 0; i < 6; i++ {
		k := fmt.Sprintf("claude:s%d", i)
		state, last := StateRunning, nowT.Add(-time.Duration(i)*time.Minute)
		switch i {
		case 4:
			state, last = StateEnded, nowT.Add(-2*time.Hour)
		case 5:
			state = StateEnded
		}
		pid := f.p
		if i%2 == 1 {
			pid = f.q
		}
		if _, err := db.Exec(`INSERT INTO sessions(key, tool, session_id, device_id, project_id, title, state, started_at, last_active_at) VALUES(?, 'claude', ?, 'dev1', ?, ?, ?, ?, ?)`,
			k, fmt.Sprintf("s%d", i), pid, "会话"+k, state, ts(last.Add(-time.Hour)), ts(last)); err != nil {
			t.Fatal(err)
		}
		f.keys = append(f.keys, k)
		// 近一小时的事件（含不计入活跃度的 KsTasks）和工具调用
		for j := 0; j < 8; j++ {
			name := []string{"PreToolUse", "PostToolUse", "KsTasks", "Stop"}[j%4]
			ev := protocol.Event{ID: protocol.NewID(), Tool: "claude", SessionID: fmt.Sprintf("s%d", i), Name: name, Time: last.Add(-time.Duration(j*7) * time.Minute),
				ToolName: "Bash", ToolUseID: fmt.Sprintf("toolu_%d_%d", i, j/2)}
			if _, err := s.InsertEvent(ctx, "dev1", ev); err != nil {
				t.Fatal(err)
			}
		}
		for j := 0; j < i%3; j++ {
			if _, err := s.CreateDecision(ctx, Decision{SessionKey: k, ProjectID: pid, Question: fmt.Sprintf("要不要 %d", j)}); err != nil {
				t.Fatal(err)
			}
		}
	}
	for i := 0; i < 20; i++ {
		pid := f.p
		if i%3 == 0 {
			pid = f.q
		}
		r, err := s.CreateRequirement(ctx, Requirement{ProjectID: pid, Title: fmt.Sprintf("需求 %d", i), Priority: []string{"P1", "P2"}[i%2]})
		if err != nil {
			t.Fatal(err)
		}
		f.reqs = append(f.reqs, r.ID)
		for j := 0; j < i%4; j++ {
			k := f.keys[(i+5*j)%len(f.keys)]
			at := nowT.Add(-time.Duration(10-j) * time.Minute).Add(time.Duration(i) * time.Second)
			if _, err := db.Exec(`INSERT INTO requirement_sessions(requirement_id, session_key, linked_at) VALUES(?, ?, ?) ON CONFLICT DO NOTHING`, r.ID, k, ts(at)); err != nil {
				t.Fatal(err)
			}
		}
		if i%5 == 2 {
			if _, err := s.SetRequirementStatus(ctx, r.ID, ReqReview); err != nil {
				t.Fatal(err)
			}
			f.reviewed = append(f.reviewed, r.ID)
			rev := f.keys[i%len(f.keys)]
			if i != 12 { // 一条只有审查意见、没有审查任务
				if _, err := s.CreateDecision(ctx, Decision{Kind: KindReviewTask, SessionKey: rev, ProjectID: pid, RequirementID: r.ID, Question: "审查", AssignedBy: ReviewAuto}); err != nil {
					t.Fatal(err)
				}
			}
			if i != 7 { // 一条还在评审中
				if _, err := s.AddEvidence(ctx, Evidence{RequirementID: r.ID, SessionKey: rev, Kind: EvidenceReview, Summary: "## 结论\n可以通过"}); err != nil {
					t.Fatal(err)
				}
			}
		}
		if i%4 == 1 {
			if _, err := s.AddEvidence(ctx, Evidence{RequirementID: r.ID, Kind: "test", Summary: fmt.Sprintf("测试通过 %d", i)}); err != nil {
				t.Fatal(err)
			}
		}
		if i%6 == 0 && i%5 != 2 {
			if _, err := s.SetRequirementStatus(ctx, r.ID, ReqBlocked); err != nil {
				t.Fatal(err)
			}
		}
	}
	// 计划：两个项目各一个，阶段里挂需求
	for n, pid := range []string{f.p, f.q} {
		if _, err := s.CreatePlan(ctx, Plan{ProjectID: pid, Title: fmt.Sprintf("计划 %d", n)}, []StageInput{
			{Title: "一", Requirements: []string{f.reqs[n], f.reqs[n+2]}},
			{Title: "二", Requirements: []string{f.reqs[n+4]}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	// 代答、发布、团队消息
	for i, k := range []string{KindTell, KindRelease, KindTell} {
		pid := []string{f.p, f.q}[i%2]
		if _, err := s.CreateDecision(ctx, Decision{Kind: k, SessionKey: f.keys[i], ProjectID: pid, RequirementID: f.reqs[i], Question: "消息", Answer: "好", AssignedBy: f.keys[0]}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`UPDATE decisions SET proxy_by = ?, proxy_at = ? WHERE kind = 'decision' AND seq % 2 = 0`, f.keys[1], now()); err != nil {
		t.Fatal(err)
	}
	// 一条审查意见没有会话标题快照（老数据）：评审验证情况要去会话上取标题
	if _, err := db.Exec(`UPDATE evidence SET session_title = '' WHERE kind = ? AND requirement_id = ?`, EvidenceReview, f.reqs[17]); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateGrant(ctx, f.p, f.keys[1], RiskLow, nowT.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.SetMorning(ctx, f.p, "总述", f.keys[1], "一句话", []string{"R-1"}); err != nil {
		t.Fatal(err)
	}
	return f
}

func sameJSON(t *testing.T, what string, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s 与逐条查询不一致：\n批量：%+v\n逐条：%+v", what, got, want)
	}
}

func countQueries(t *testing.T, what string, max int, f func(ctx context.Context) error) {
	t.Helper()
	ctx, n := WithQueryCount(context.Background())
	if err := f(ctx); err != nil {
		t.Fatal(err)
	}
	if n.Load() > int64(max) {
		t.Fatalf("%s 查了 %d 次库，应不超过 %d 次（逐条查库）", what, n.Load(), max)
	}
}

func TestListRequirementsBatchMatchesPerItem(t *testing.T) {
	f := batchFixture(t)
	ctx := context.Background()
	for _, c := range []struct {
		project  string
		statuses []string
	}{{"", nil}, {f.p, nil}, {"", []string{ReqReview, ReqBlocked}}} {
		list, err := f.s.ListRequirements(ctx, c.project, c.statuses)
		if err != nil {
			t.Fatal(err)
		}
		if len(list) == 0 {
			t.Fatal("没有需求")
		}
		reviews := 0
		for _, r := range list {
			ref := r
			ref.Sessions, ref.Review = []string{}, nil
			if err := f.s.fillReqSessions(ctx, &ref); err != nil {
				t.Fatal(err)
			}
			if err := f.s.refFillReviewState(ctx, &ref); err != nil {
				t.Fatal(err)
			}
			sameJSON(t, r.Code+" 关联会话", r.Sessions, ref.Sessions)
			sameJSON(t, r.Code+" 评审验证", r.Review, ref.Review)
			if r.Review != nil {
				reviews++
			}
		}
		if c.project == "" && reviews != len(f.reviewed) {
			t.Fatalf("待验收的 %d 条应都有评审验证情况，实际 %d 条", len(f.reviewed), reviews)
		}
	}
	countQueries(t, "需求列表", 6, func(ctx context.Context) error {
		_, err := f.s.ListRequirements(ctx, "", nil)
		return err
	})
}

func TestListSessionsBatchMatchesPerItem(t *testing.T) {
	f := batchFixture(t)
	ctx := context.Background()
	list, err := f.s.ListSessions(ctx, 24*time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != len(f.keys) {
		t.Fatalf("会话 %d 个，应为 %d 个", len(list), len(f.keys))
	}
	active := 0
	for _, x := range list {
		ref := x
		ref.Requirements, ref.PendingDecisions = []RequirementRef{}, -1
		if err := f.s.fillReqRefs(ctx, &ref); err != nil {
			t.Fatal(err)
		}
		if err := f.s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM decisions WHERE session_key = ? AND status = 'pending'`, x.Key).Scan(&ref.PendingDecisions); err != nil {
			t.Fatal(err)
		}
		if err := f.s.refFillActivity(ctx, &ref); err != nil {
			t.Fatal(err)
		}
		sameJSON(t, x.Key+" 关联需求", x.Requirements, ref.Requirements)
		sameJSON(t, x.Key+" 待答决策数", x.PendingDecisions, ref.PendingDecisions)
		sameJSON(t, x.Key+" 活跃度", x.Activity, ref.Activity)
		for _, n := range x.Activity {
			active += n
		}
		// 单个读取（GetSession）与列表里的一致
		one, err := f.s.GetSession(ctx, x.Key)
		if err != nil {
			t.Fatal(err)
		}
		sameJSON(t, x.Key+" 单个读取", one, x)
	}
	if active == 0 {
		t.Fatal("夹具里应有近一小时的事件")
	}
	got, err := f.s.GetSessions(ctx, append([]string{"claude:nobody"}, f.keys...))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(f.keys) || got["claude:nobody"] != nil {
		t.Fatalf("GetSessions 应只返回存在的会话：%d", len(got))
	}
	for _, x := range list {
		sameJSON(t, x.Key+" 批量读取", *got[x.Key], x)
	}
	// 列表 1 次 + 关联需求、待答数、活跃度各 1 次；只有没结束的会话（这里 4 个）还要各查最近动作和刻度
	countQueries(t, "会话列表", 4+2*4, func(ctx context.Context) error {
		_, err := f.s.ListSessions(ctx, 24*time.Hour, false)
		return err
	})
}

func TestListPlansBatchMatchesGetPlan(t *testing.T) {
	f := batchFixture(t)
	ctx := context.Background()
	plans, err := f.s.ListPlans(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 2 {
		t.Fatalf("计划 %d 个", len(plans))
	}
	for _, x := range plans {
		one, err := f.s.GetPlan(ctx, x.ID)
		if err != nil {
			t.Fatal(err)
		}
		sameJSON(t, x.Code, x, one)
		if len(x.Stages) != 2 || len(x.Stages[0].Requirements) != 2 {
			t.Fatalf("%s 的阶段或需求没补上：%+v", x.Code, x.Stages)
		}
	}
	countQueries(t, "计划列表", 3, func(ctx context.Context) error {
		_, err := f.s.ListPlans(ctx, "")
		return err
	})
}

func TestMorningBatchMatchesPerProject(t *testing.T) {
	f := batchFixture(t)
	ctx := context.Background()
	since := time.Now().Add(-24 * time.Hour)
	notes, err := f.s.MorningNotes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	latest, err := f.s.LatestGrants(ctx)
	if err != nil {
		t.Fatal(err)
	}
	active, err := f.s.ActiveGrants(ctx)
	if err != nil {
		t.Fatal(err)
	}
	evs, err := f.s.EvidenceSinceAll(ctx, since)
	if err != nil {
		t.Fatal(err)
	}
	tells, err := f.s.ProjectDecisionsSinceAll(ctx, KindTell, since, 1) // 每个项目只取最新一条：核对按项目的条数上限
	if err != nil {
		t.Fatal(err)
	}
	rels, err := f.s.ProjectDecisionsSinceAll(ctx, KindRelease, since, 100)
	if err != nil {
		t.Fatal(err)
	}
	proxied, err := f.s.ProxiedSinceAll(ctx, since)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := f.s.PendingDecisionsAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	nonEmpty := 0
	for _, pid := range []string{f.p, f.q} {
		if n, err := f.s.GetMorning(ctx, pid); err == nil {
			sameJSON(t, "总述", notes[pid], n)
			nonEmpty++
		} else if _, ok := notes[pid]; ok {
			t.Fatal("没有总述的项目不应出现")
		}
		gs, _ := f.s.ListGrants(ctx, pid, 1)
		if len(gs) > 0 {
			sameJSON(t, "最近窗口", latest[pid], gs[0])
			nonEmpty++
		} else if _, ok := latest[pid]; ok {
			t.Fatal("没有窗口的项目不应出现")
		}
		if g, err := f.s.ActiveGrant(ctx, pid); err == nil {
			sameJSON(t, "生效窗口", active[pid], g)
		} else if _, ok := active[pid]; ok {
			t.Fatal("没有生效窗口的项目不应出现")
		}
		e1, _ := f.s.EvidenceSince(ctx, pid, since)
		sameJSON(t, "期间证据", orEmpty(evs[pid]), e1)
		t1, _ := f.s.ProjectDecisionsSince(ctx, pid, KindTell, since, 1)
		sameJSON(t, "团队消息", orEmpty(tells[pid]), t1)
		r1, _ := f.s.ProjectDecisionsSince(ctx, pid, KindRelease, since, 100)
		sameJSON(t, "放行", orEmpty(rels[pid]), r1)
		x1, _ := f.s.ProxiedSince(ctx, pid, since)
		sameJSON(t, "代答", orEmpty(proxied[pid]), x1)
		p1, _ := f.s.PendingDecisions(ctx, pid)
		sameJSON(t, "待答", orEmpty(pending[pid]), p1)
		nonEmpty += len(e1) + len(t1) + len(r1) + len(x1) + len(p1)
	}
	if nonEmpty < 8 {
		t.Fatalf("夹具数据太少，比不出差别：%d", nonEmpty)
	}
}

func TestDecisionSweepQueries(t *testing.T) {
	f := batchFixture(t)
	ctx := context.Background()
	for _, kind := range []string{KindTell, KindReviewTask, KindPlan} {
		for _, limit := range []int{-1, 3} {
			all, err := f.s.ListDecisions(ctx, []string{DecAnswered, DecPending}, limit)
			if err != nil {
				t.Fatal(err)
			}
			want := []Decision{}
			for _, d := range all {
				if d.Kind == kind {
					want = append(want, d)
				}
			}
			got, err := f.s.ListDecisionsKind(ctx, []string{DecAnswered, DecPending}, limit, kind)
			if err != nil {
				t.Fatal(err)
			}
			sameJSON(t, fmt.Sprintf("%s 前 %d 条", kind, limit), got, want)
		}
	}
	// 计划进展：每个计划最新一条
	plans, _ := f.s.ListPlans(ctx, "")
	for i := 0; i < 3; i++ {
		if _, err := f.s.CreateDecision(ctx, Decision{Kind: KindPlan, PlanID: plans[i%2].ID, ProjectID: plans[i%2].ProjectID, Question: "进展", Answer: "阶段一完成"}); err != nil {
			t.Fatal(err)
		}
	}
	all, _ := f.s.ListDecisions(ctx, []string{DecAnswered, DecDelivered}, 2000)
	want := map[string]int{}
	for _, d := range all {
		if d.Kind == KindPlan && d.PlanID != "" {
			want[d.PlanID] = max(want[d.PlanID], seqOfCode(d.Code))
		}
	}
	got, err := f.s.LatestPlanNotes(ctx, []string{DecAnswered, DecDelivered}, 2000)
	if err != nil {
		t.Fatal(err)
	}
	if len(want) != 2 {
		t.Fatalf("夹具应有两个计划的进展：%v", want)
	}
	sameJSON(t, "计划最新进展", got, want)
}

func seqOfCode(code string) int {
	var n int
	fmt.Sscanf(strings.TrimPrefix(code, "D-"), "%d", &n)
	return n
}

func TestLastSecretUsesMatchesPerItem(t *testing.T) {
	f := batchFixture(t)
	ctx := context.Background()
	var ids []string
	for i := 0; i < 3; i++ {
		x, err := f.s.CreateSecret(ctx, Secret{Name: fmt.Sprintf("凭据%d", i), Projects: []string{f.p}})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, x.ID)
		at := time.Now().Add(-time.Hour)
		for j := 0; j < i; j++ { // 第一个没用过；同一时刻的两条按写入先后取后一条
			for _, kind := range []string{SecretLogUse, SecretLogWrite, "grant"} {
				if _, err := f.s.AddSecretLog(ctx, SecretLog{SecretID: x.ID, Kind: kind, At: at.Add(time.Duration(j) * time.Minute), Command: fmt.Sprintf("%s-%d", kind, j)}); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	got, err := f.s.LastSecretUses(ctx, ids)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		want, err := f.s.LastSecretUse(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		sameJSON(t, "最近一次使用", got[id], want)
	}
	if len(got) != 2 {
		t.Fatalf("用过的凭据 2 个，实际 %d 个", len(got))
	}
}
