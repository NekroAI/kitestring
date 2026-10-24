package e2e

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/client"
	"github.com/NekroAI/kitestring/internal/store"
)

// 评审验证（R-232、R-237）：交验收时本项目有在线评审就自动派验证任务；评审交意见前需求处于「评审中」（不进要你处理）；
// 评审只验证、不能改状态和交验收证据；交了带 checklist 的意见后进验收卡，有不通过的用「要动手」那一档叫醒主调度；
// 派给开发的活里写明此刻有在线评审（开发减负）。
func TestReviewVerify(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	c := client.New(30 * time.Second)
	ctx := context.Background()
	for _, sid := range []string{"pm", "dev", "rev"} {
		hook(t, "claude", map[string]any{"session_id": sid, "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "上线", "prompt_id": "p0"})
	}
	e.waitFor("会话入库", func() bool { return e.session("claude:rev").ProjectID != "" })
	for _, kr := range [][2]string{{"claude:pm", "pm"}, {"claude:dev", "dev"}, {"claude:rev", "reviewer"}} {
		if err := c.Do(ctx, "POST", "/agent/sessions/role", map[string]any{"session_key": kr[0], "role": kr[1]}, nil); err != nil {
			t.Fatal(err)
		}
	}
	woke(t, parkWait(t, e, c, "claude:pm"), "已上线待命")

	var r store.Requirement
	if err := c.Do(ctx, "POST", "/agent/requirements", map[string]any{"title": "导出 CSV", "acceptance": "1. 表头冻结；2. 空表有提示", "session_key": "claude:pm"}, &r); err != nil {
		t.Fatal(err)
	}
	e.allowRelease("claude:pm")
	devW := parkWait(t, e, c, "claude:dev")
	if err := c.Do(ctx, "POST", "/agent/requirements/"+r.Code+"/assign", map[string]any{"session_key": "claude:pm", "to": "claude:dev", "reason": "做法明确"}, nil); err != nil {
		t.Fatal(err)
	}
	// 开发减负：派活内容写明此刻有在线评审，只保证编译、单测和一张主截图
	woke(t, devW, r.Code, "验证分工：本项目现在有在线评审", "只保证编译、单测通过")

	c.Do(ctx, "POST", "/agent/sessions/claude:dev/link", map[string]any{"requirement": r.Code}, nil)
	if err := c.Do(ctx, "POST", "/agent/requirements/"+r.Code+"/evidence", map[string]any{"kind": "test", "summary": "单测通过", "session_key": "claude:dev"}, nil); err != nil {
		t.Fatal(err)
	}
	pmW := parkWait(t, e, c, "claude:pm")
	if err := c.Do(ctx, "POST", "/agent/requirements/"+r.Code+"/status", map[string]any{"status": "review", "session_key": "claude:dev"}, nil); err != nil {
		t.Fatal(err)
	}
	// 主调度收到的交验收通知写明已自动交给评审
	woke(t, pmW, r.Code, "已交验收", "已自动交给评审", "不进用户的「要你处理」")

	// 评审中：需求详情和列表都带评审状态，写明谁在审
	var got store.Requirement
	e.call("GET", "/api/requirements/"+r.Code, nil, &got)
	if got.Review == nil || got.Review.State != store.ReviewReviewing || !got.Review.Auto || got.Review.SessionKey != "claude:rev" || got.Review.DueAt == nil {
		t.Fatalf("交验收后应自动交给评审、处于评审中：%+v", got.Review)
	}
	var list struct{ Requirements []store.Requirement }
	e.call("GET", "/api/requirements?status=review", nil, &list)
	if len(list.Requirements) != 1 || list.Requirements[0].Review == nil || list.Requirements[0].Review.State != store.ReviewReviewing {
		t.Fatalf("列表里也应带评审状态：%+v", list.Requirements)
	}
	if got.Status != store.ReqReview {
		t.Fatalf("自动派评审不改状态：%s", got.Status)
	}

	// 评审只验证：不能改需求状态、不能交验收证据
	if err := c.Do(ctx, "POST", "/agent/requirements/"+r.Code+"/status", map[string]any{"status": "in_progress", "session_key": "claude:rev"}, nil); err == nil || !strings.Contains(err.Error(), "不改需求状态") {
		t.Fatalf("评审改状态应被拒绝：%v", err)
	}
	if err := c.Do(ctx, "POST", "/agent/requirements/"+r.Code+"/evidence", map[string]any{"kind": "manual", "summary": "我测过了", "session_key": "claude:rev"}, nil); err == nil || !strings.Contains(err.Error(), "review_submit") {
		t.Fatalf("评审交验收证据应被拒绝：%v", err)
	}

	// 评审交验证结果：一句结论 + checklist（1 条不通过）；主调度被「要动手」那一档叫醒
	pmW = parkWait(t, e, c, "claude:pm")
	var out struct {
		store.Requirement
		ReviewID      string   `json:"review_id"`
		BlockWarnings []string `json:"block_warnings"`
	}
	err := c.Do(ctx, "POST", "/agent/requirements/"+r.Code+"/reviews", map[string]any{
		"session_key": "claude:rev",
		"verdict":     "2 条验收点 1 条不通过",
		"details":     "go test ./... 通过；演示 hub 7414",
		"blocks": []any{map[string]any{"type": "checklist", "items": []any{
			map[string]any{"criterion": 1, "text": "表头冻结", "result": "pass", "why": "滚动 200 行表头仍在"},
			map[string]any{"criterion": 2, "text": "空表有提示", "result": "fail", "why": "空表时一片空白"},
		}}},
	}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.BlockWarnings) > 0 || out.ReviewID == "" {
		t.Fatalf("清单应原样保存：%+v", out)
	}
	woke(t, pmW, "交了 "+r.Code, "不通过 1 条", "空表有提示", "退回给开发修")

	e.call("GET", "/api/requirements/"+r.Code, nil, &got)
	if got.Review == nil || got.Review.State != store.ReviewDone || got.Review.Fail != 1 || got.Review.Pass != 1 || got.Review.Total != 2 || got.Review.Verdict != "2 条验收点 1 条不通过" {
		t.Fatalf("评审交了意见后应带结论：%+v", got.Review)
	}
	if len(got.Reviews) != 1 || got.Reviews[0].Payload == nil || got.Reviews[0].Payload.Checklist() == nil {
		t.Fatalf("意见应带 checklist：%+v", got.Reviews)
	}
	if got.Status != store.ReqReview {
		t.Fatalf("评审交意见不改状态：%s", got.Status)
	}

	// 需求页上随时可以直接验收
	e.call("POST", "/api/reviews", map[string]any{"items": []map[string]any{{"ref": r.Code, "verdict": "accept"}}}, nil)
	got = store.Requirement{}
	e.call("GET", "/api/requirements/"+r.Code, nil, &got)
	if got.Status != store.ReqDone || got.Review != nil {
		t.Fatalf("验收应照常通过：%s %+v", got.Status, got.Review)
	}
}

// 没有在线评审时一切照旧（不派、不压）；交给评审 30 分钟没动静就照常弹给用户，标「评审未完成」；
// 用户也可以不等评审直接验收。
func TestReviewWaitLimit(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	c := client.New(30 * time.Second)
	ctx := context.Background()
	for _, sid := range []string{"dev", "rev"} {
		hook(t, "claude", map[string]any{"session_id": sid, "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "上线", "prompt_id": "p0"})
	}
	e.waitFor("会话入库", func() bool { return e.session("claude:rev").ProjectID != "" })
	if err := c.Do(ctx, "POST", "/agent/sessions/role", map[string]any{"session_key": "claude:dev", "role": "dev"}, nil); err != nil {
		t.Fatal(err)
	}
	submit := func(title string) store.Requirement {
		t.Helper()
		var r store.Requirement
		if err := c.Do(ctx, "POST", "/agent/requirements", map[string]any{"title": title, "session_key": "claude:dev"}, &r); err != nil {
			t.Fatal(err)
		}
		c.Do(ctx, "POST", "/agent/sessions/claude:dev/link", map[string]any{"requirement": r.Code}, nil)
		if err := c.Do(ctx, "POST", "/agent/requirements/"+r.Code+"/status", map[string]any{"status": "review", "session_key": "claude:dev"}, nil); err != nil {
			t.Fatal(err)
		}
		var got store.Requirement
		e.call("GET", "/api/requirements/"+r.Code, nil, &got)
		return got
	}

	// 1. 项目里还没有评审：照旧直接进验收，不带评审状态
	if r := submit("没有评审时"); r.Review != nil {
		t.Fatalf("没有评审时不应派验证任务：%+v", r.Review)
	}

	// 2. 有了评审：派出去，等满时限还没交意见 → 评审未完成（照常弹给用户）
	old := store.ReviewWaitLimit
	store.ReviewWaitLimit = 1500 * time.Millisecond
	t.Cleanup(func() { store.ReviewWaitLimit = old })
	if err := c.Do(ctx, "POST", "/agent/sessions/role", map[string]any{"session_key": "claude:rev", "role": "reviewer"}, nil); err != nil {
		t.Fatal(err)
	}
	r := submit("评审超时")
	if r.Review == nil || r.Review.State != store.ReviewReviewing {
		t.Fatalf("应处于评审中：%+v", r.Review)
	}
	e.waitFor("评审超时转为未完成", func() bool {
		var got store.Requirement
		e.call("GET", "/api/requirements/"+r.Code, nil, &got)
		return got.Review != nil && got.Review.State == store.ReviewOverdue
	})

	// 3. 评审中的需求，用户也能不等评审直接验收
	r = submit("直接验收")
	if !r.Review.Holding() {
		t.Fatalf("应处于评审中：%+v", r.Review)
	}
	e.call("POST", "/api/reviews", map[string]any{"items": []map[string]any{{"ref": r.Code, "verdict": "accept"}}}, nil)
	var got store.Requirement
	e.call("GET", "/api/requirements/"+r.Code, nil, &got)
	if got.Status != store.ReqDone {
		t.Fatalf("评审中也能直接验收：%s", got.Status)
	}
}
