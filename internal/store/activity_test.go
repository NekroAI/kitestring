package store

import (
	"context"
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/blocks"
)

// 最近动态（R-296）：各类记录都进时间线，主语写清是你、哪个会话（身份 + 标题）还是纸鸢；能按项目过滤、只看你做的。
func TestListActivity(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	p := testProject(t, s, "纸鸢")
	q := testProject(t, s, "别的项目")
	db := s.DB()
	if _, err := db.Exec(`INSERT INTO devices(id, name, token_hash, created_at) VALUES('dev1', 'mini', 'h', ?)`, now()); err != nil {
		t.Fatal(err)
	}
	for _, x := range [][3]string{{"claude:pm", "派活与合并", "pm"}, {"claude:dev", "实现动态接口", "dev"}, {"claude:aide", "常驻主助理", "aide"}, {"claude:rev", "审查动态接口", "reviewer"}} {
		if _, err := db.Exec(`INSERT INTO sessions(key, tool, session_id, device_id, project_id, title, role, state, started_at, last_active_at) VALUES(?, 'claude', ?, 'dev1', ?, ?, ?, 'idle', ?, ?)`, x[0], x[0], p, x[1], x[2], now(), now()); err != nil {
			t.Fatal(err)
		}
	}
	mk := func(proj, title, status string) Requirement {
		r, err := s.CreateRequirement(ctx, Requirement{ProjectID: proj, Title: title, Status: status, Priority: "P2"})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	agent := func(key string) context.Context { return WithActor(ctx, Actor{Kind: ActorAgent, Session: key}) }
	user := WithActor(ctx, Actor{Kind: ActorUser})
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	setSt := func(c context.Context, r Requirement, st string) {
		t.Helper()
		_, err := s.SetRequirementStatus(c, r.ID, st)
		must(err)
	}
	dec := func(d Decision) Decision {
		t.Helper()
		x, err := s.CreateDecision(ctx, d)
		must(err)
		return x
	}

	a := mk(p, "动态接口", ReqReady)
	b := mk(p, "等决策的", ReqInProgress)
	other := mk(q, "别的项目的需求", ReqInProgress)

	// 你指派给开发；主调度指派；开发交证据并提交验收（合成一条）；评审由纸鸢自动提交，评审交意见
	dec(Decision{Kind: KindAssign, RequirementID: a.ID, ProjectID: p, SessionKey: "claude:dev", Question: a.Title})
	dec(Decision{Kind: KindAssign, RequirementID: b.ID, ProjectID: p, SessionKey: "claude:dev", AssignedBy: "claude:pm", Question: b.Title})
	setSt(agent("claude:dev"), a, ReqInProgress)
	_, err := s.AddEvidence(ctx, Evidence{RequirementID: a.ID, SessionKey: "claude:dev", Kind: "text", Summary: "做完了"})
	must(err)
	setSt(agent("claude:dev"), a, ReqReview)
	dec(Decision{Kind: KindReviewTask, RequirementID: a.ID, ProjectID: p, SessionKey: "claude:rev", AssignedBy: ReviewAuto, Question: a.Title})
	_, err = s.AddEvidence(ctx, Evidence{RequirementID: a.ID, SessionKey: "claude:rev", Kind: EvidenceReview, Summary: "## 结论\n可以通过", Payload: &blocks.Payload{Verdict: "可以通过，清单全过"}})
	must(err)
	// 主调度提请求批准，设 b 受阻等它；你批准后纸鸢自动解除受阻
	d := dec(Decision{Kind: KindDecision, RequirementID: b.ID, ProjectID: p, OriginSession: "claude:pm", SessionKey: "claude:pm", FromRole: "pm", Approval: true, Question: "要不要加索引"})
	setSt(WithActor(agent("claude:pm"), Actor{Ref: d.Code, Reason: "等用户定索引"}), b, ReqBlocked)
	_, err = s.AnswerDecision(ctx, d.Code, "批准", nil)
	must(err)
	setSt(WithActor(ctx, Actor{Kind: ActorSystem, Via: "unblock", Ref: d.Code}), b, ReqInProgress)
	// 留言：会话留言、你留言；你退回；主助理放行；你验收通过
	_, err = s.CreateComment(ctx, b, Comment{By: "claude:aide", Role: "aide", Text: "  细节见下  "})
	must(err)
	dec(Decision{Kind: KindNote, RequirementID: b.ID, ProjectID: p, Question: b.Title, Answer: "顺便看下手机宽度"})
	dec(Decision{Kind: KindFeedback, RequirementID: b.ID, ProjectID: p, Question: b.Title, Answer: "截图缺深色"})
	_, err = s.CreateRelease(ctx, b, "claude:aide", "aide", "做法清楚")
	must(err)
	setSt(WithActor(user, Actor{Via: "accept"}), a, ReqDone)
	// 主助理代答一条选项类决策
	d2 := dec(Decision{Kind: KindDecision, RequirementID: b.ID, ProjectID: p, OriginSession: "claude:dev", SessionKey: "claude:dev", FromRole: "dev", Question: "用哪种排序", Options: []string{"时间", "编号"}})
	_, err = s.AnswerDecision(ctx, d2.Code, "时间", nil)
	must(err)
	_, err = db.Exec(`UPDATE decisions SET proxy_by = 'claude:aide' WHERE id = ?`, d2.ID)
	must(err)
	// 别的项目
	_, err = s.AddEvidence(ctx, Evidence{RequirementID: other.ID, SessionKey: "claude:dev", Kind: "text", Summary: "别处"})
	must(err)
	// 计划：你通过阶段 1，阶段 2 随之自动通过
	at := time.Now().UTC()
	_, err = db.Exec(`INSERT INTO plans(id, seq, project_id, title, status, created_at, updated_at) VALUES('pl1', 1, ?, '计划', 'done', ?, ?)`, p, ts(at), ts(at))
	must(err)
	_, err = db.Exec(`INSERT INTO plan_stages(id, plan_id, seq, title, status, passed_at) VALUES('s1', 'pl1', 1, '后端', 'passed', ?), ('s2', 'pl1', 2, '前端', 'passed', ?)`, ts(at), ts(at.Add(500*time.Millisecond)))
	must(err)

	all, err := s.ListActivity(ctx, ActivityQuery{ProjectID: p, Limit: 100})
	must(err)
	for i := 1; i < len(all); i++ {
		if all[i].At.After(all[i-1].At) {
			t.Fatalf("没按时间倒序：%+v", all)
		}
	}
	find := func(xs []Activity, kind, code string) *Activity {
		for i := range xs {
			if xs[i].Kind == kind && xs[i].Code == code {
				return &xs[i]
			}
		}
		return nil
	}
	check := func(kind, code, actor, role, title string) *Activity {
		t.Helper()
		x := find(all, kind, code)
		if x == nil {
			t.Fatalf("缺 %s %s：%+v", kind, code, all)
		}
		if x.Actor != actor || x.Role != role || x.SessionTitle != title {
			t.Fatalf("%s %s 主语不对：%+v", kind, code, *x)
		}
		return x
	}
	if x := check(FeedAssign, a.Code, ActorUser, "", ""); x.TargetRole != "dev" || x.TargetTitle != "实现动态接口" {
		t.Fatalf("你指派的接手会话不对：%+v", *x)
	}
	if x := check(FeedAssign, b.Code, ActorAgent, "pm", "派活与合并"); x.Session != "claude:pm" || x.TargetTitle != "实现动态接口" {
		t.Fatalf("主调度指派不对：%+v", *x)
	}
	if x := check(FeedSubmit, a.Code, ActorAgent, "dev", "实现动态接口"); !x.Evidence {
		t.Fatalf("交证据后紧接着提交验收应合成一条：%+v", *x)
	}
	if find(all, FeedEvidence, a.Code) != nil {
		t.Fatal("合并后不应再单列交证据")
	}
	if x := check(FeedReviewTask, a.Code, ActorSystem, "", ""); x.TargetRole != "reviewer" || x.TargetTitle != "审查动态接口" {
		t.Fatalf("纸鸢自动提交评审不对：%+v", *x)
	}
	if x := check(FeedReview, a.Code, ActorAgent, "reviewer", "审查动态接口"); x.Text != "可以通过，清单全过" {
		t.Fatalf("审查意见结论不对：%+v", *x)
	}
	if x := check(FeedRaise, d.Code, ActorAgent, "pm", "派活与合并"); !x.Approval || x.ReqCode != b.Code {
		t.Fatalf("请求批准不对：%+v", *x)
	}
	if x := check(FeedBlocked, b.Code, ActorAgent, "pm", "派活与合并"); x.Ref != d.Code || x.Text != "等用户定索引" {
		t.Fatalf("受阻没写在等什么：%+v", *x)
	}
	if x := check(FeedAnswer, d.Code, ActorUser, "", ""); x.Verdict != "approved" {
		t.Fatalf("你批准的结论不对：%+v", *x)
	}
	if x := check(FeedUnblock, b.Code, ActorSystem, "", ""); x.Ref != d.Code {
		t.Fatalf("自动解除受阻不对：%+v", *x)
	}
	if x := check(FeedComment, b.Code, ActorAgent, "aide", "常驻主助理"); x.Text != "细节见下" {
		t.Fatalf("会话留言不对：%+v", *x)
	}
	if x := check(FeedNote, b.Code, ActorUser, "", ""); x.Text != "顺便看下手机宽度" {
		t.Fatalf("你留言不对：%+v", *x)
	}
	check(FeedRevise, b.Code, ActorUser, "", "")
	check(FeedRelease, b.Code, ActorAgent, "aide", "常驻主助理")
	check(FeedDone, a.Code, ActorUser, "", "")
	if x := check(FeedAnswer, d2.Code, ActorAgent, "aide", "常驻主助理"); x.Verdict != "proxy" {
		t.Fatalf("代答不对：%+v", *x)
	}
	check(FeedRaise, d2.Code, ActorAgent, "dev", "实现动态接口")
	var stages []Activity
	for _, x := range all {
		if x.Kind == FeedStage {
			stages = append(stages, x)
		}
	}
	if len(stages) != 2 || stages[0].StageSeq != 2 || stages[0].StageWith != 1 || stages[0].Actor != ActorSystem || stages[1].StageWith != 0 || stages[1].Actor != ActorUser {
		t.Fatalf("阶段验收不对：%+v", stages)
	}
	// 普通流转（关联开工）不进动态；别的项目的不混进来
	for _, x := range all {
		if x.Code == other.Code {
			t.Fatalf("混进了别的项目：%+v", x)
		}
	}
	if find(all, FeedSubmit, b.Code) != nil {
		t.Fatal("b 没提交验收")
	}

	// 全部项目
	every, err := s.ListActivity(ctx, ActivityQuery{Limit: 100})
	must(err)
	if find(every, FeedEvidence, other.Code) == nil {
		t.Fatal("全部项目时应有别的项目的证据")
	}
	// 只看你做的：只剩你的指派、批准、留言、退回、通过、你通过的阶段
	mine, err := s.ListActivity(ctx, ActivityQuery{ProjectID: p, Mine: true, Limit: 100})
	must(err)
	want := map[string]bool{FeedAssign + a.Code: true, FeedAnswer + d.Code: true, FeedNote + b.Code: true, FeedRevise + b.Code: true, FeedDone + a.Code: true, FeedStage + "P-1": true}
	if len(mine) != len(want) {
		t.Fatalf("只看我的条数不对：%+v", mine)
	}
	for _, x := range mine {
		if x.Actor != ActorUser || !want[x.Kind+x.Code] {
			t.Fatalf("只看我的混进了别人的：%+v", x)
		}
	}
	// 条数限制
	few, err := s.ListActivity(ctx, ActivityQuery{ProjectID: p, Limit: 3})
	must(err)
	if len(few) != 3 || !few[0].At.Equal(all[0].At) {
		t.Fatalf("条数限制不对：%+v", few)
	}
}
