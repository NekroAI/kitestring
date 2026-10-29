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

// 受阻指向（R-243）：设为受阻时可写在等的 R-x / D-x；校验编号存在、不指自己、不绕成圈；只在设为受阻时能写；
// 离开受阻时清空；时间线（状态变化）记下在等谁和原因。没写等什么时照常受阻（由界面把它放进「要你处理」）。
func TestBlockedWaitingOn(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	c := client.New(10 * time.Second)
	ctx := context.Background()
	hook(t, "claude", map[string]any{"session_id": "dev", "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "开工", "prompt_id": "p0"})
	e.waitFor("会话入库", func() bool { return e.session("claude:dev").ProjectID != "" })
	create := func(title string) store.Requirement {
		t.Helper()
		var r store.Requirement
		if err := c.Do(ctx, "POST", "/agent/requirements", map[string]any{"title": title, "session_key": "claude:dev"}, &r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	status := func(ref string, body map[string]any) (store.Requirement, error) {
		body["session_key"] = "claude:dev"
		var r store.Requirement
		err := c.Do(ctx, "POST", "/agent/requirements/"+ref+"/status", body, &r)
		return r, err
	}
	r210 := create("泳道时间线重做")
	r212 := create("报告面板原型") // 草稿：等它定稿
	var d store.Decision
	if err := c.Do(ctx, "POST", "/agent/decisions", map[string]any{"question": "导出用逗号还是分号", "session_key": "claude:dev", "requirement": "none"}, &d); err != nil {
		t.Fatal(err)
	}

	r, err := status(r210.Code, map[string]any{"status": "blocked", "waiting_on": strings.ToLower(r212.Code), "reason": "等报告面板定稿"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != store.ReqBlocked || r.BlockedOn != r212.Code || r.BlockedReason != "等报告面板定稿" {
		t.Fatalf("受阻指向没记下：%s %q %q", r.Status, r.BlockedOn, r.BlockedReason)
	}
	last := r.StatusChanges[len(r.StatusChanges)-1]
	if last.To != store.ReqBlocked || last.Ref != r212.Code || last.Reason != "等报告面板定稿" {
		t.Fatalf("时间线应记下在等谁和原因：%+v", last)
	}
	// 已经受阻时改指向：指向决策
	if r, err = status(r210.Code, map[string]any{"status": "blocked", "waiting_on": d.Code}); err != nil || r.BlockedOn != d.Code {
		t.Fatalf("受阻中改指向应生效：%v %q", err, r.BlockedOn)
	}
	for _, bad := range []map[string]any{
		{"status": "blocked", "waiting_on": "下周再说"},        // 不是编号
		{"status": "blocked", "waiting_on": "R-999"},       // 不存在
		{"status": "blocked", "waiting_on": "D-999"},       // 不存在
		{"status": "blocked", "waiting_on": r210.Code},     // 自己
		{"status": "in_progress", "waiting_on": r212.Code}, // 只在受阻时写
	} {
		if _, err := status(r210.Code, bad); err == nil {
			t.Fatalf("应拒收：%v", bad)
		}
	}
	// 互相等成圈：R-210 等 R-212 时，R-212 不能再等 R-210
	if _, err := status(r210.Code, map[string]any{"status": "blocked", "waiting_on": r212.Code}); err != nil {
		t.Fatal(err)
	}
	if _, err := status(r212.Code, map[string]any{"status": "blocked", "waiting_on": r210.Code}); err == nil || !strings.Contains(err.Error(), "圈") {
		t.Fatalf("互相等应拒收并说明：%v", err)
	}
	// 没写等什么：照常受阻，指向为空
	if r, err := status(r212.Code, map[string]any{"status": "blocked"}); err != nil || r.BlockedOn != "" {
		t.Fatalf("没写等什么也能受阻：%v %q", err, r.BlockedOn)
	}
	// 离开受阻：清空
	if r, err := status(r210.Code, map[string]any{"status": "in_progress"}); err != nil || r.BlockedOn != "" || r.BlockedReason != "" {
		t.Fatalf("离开受阻时应清空：%v %q %q", err, r.BlockedOn, r.BlockedReason)
	}
	// 网页上读到的也带字段（「要你处理」从这里算）
	var list struct {
		Requirements []store.Requirement `json:"requirements"`
	}
	if _, err := status(r210.Code, map[string]any{"status": "blocked", "waiting_on": r212.Code}); err != nil {
		t.Fatal(err)
	}
	e.call("GET", "/api/requirements", nil, &list)
	for _, x := range list.Requirements {
		if x.Code == r210.Code && x.BlockedOn != r212.Code {
			t.Fatalf("列表里应带受阻指向：%q", x.BlockedOn)
		}
	}
}

// 只告知的记录不进待转交（R-265）：负责会话已结束时，计划已完成的进展、被同一计划新进展取代的旧进展直接关掉；
// 进行中计划的最新进展留在待转交（要有人接着做）。待转交的记录在网页上点「关闭」后真的关闭。
func TestNoticeNotOrphan(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	ctx := context.Background()
	hook(t, "claude", map[string]any{"session_id": "old-pm", "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "建计划", "prompt_id": "p0"})
	e.waitFor("会话入库", func() bool { return e.session("claude:old-pm").ProjectID != "" })
	pid := e.session("claude:old-pm").ProjectID
	plan := func(title, status string) store.Plan {
		x, err := e.store.CreatePlan(ctx, store.Plan{ProjectID: pid, Title: title, OwnerSession: "claude:old-pm", Status: status}, []store.StageInput{{Title: "阶段 1", Requirements: []string{}}})
		if err != nil {
			t.Fatal(err)
		}
		return x
	}
	msg := func(x store.Plan, text string) store.Decision {
		d, err := e.store.CreateDecision(ctx, store.Decision{Kind: store.KindPlan, SessionKey: "claude:old-pm", ProjectID: pid, PlanID: x.ID, Question: x.Title, Answer: text})
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	done := plan("泳道时间线重做（方案 C 全套）", store.PlanDone)
	active := plan("报告面板", store.PlanActive)
	finished := msg(done, "阶段 3「缓存余温」已验收通过。计划全部完成。") // 像 D-964
	older := msg(active, "阶段 1 已验收通过，现在进入阶段 2。")
	latest := msg(active, "阶段 2 已验收通过，现在进入阶段 3，其中的需求已交给你。")
	other, err := e.store.CreateDecision(ctx, store.Decision{Kind: store.KindDecision, SessionKey: "claude:old-pm", ProjectID: pid, Question: "用哪个方案"})
	if err != nil {
		t.Fatal(err)
	}
	e.call("POST", "/api/decisions/answers", map[string]any{"answers": []map[string]any{{"ref": other.Code, "answer": "方案 B"}}}, nil)

	// 会话还在：都不动
	if n := e.hub.NoticeSweep(ctx); n != 0 {
		t.Fatalf("目标会话还在时不该关：%d", n)
	}
	hook(t, "claude", map[string]any{"session_id": "old-pm", "cwd": cwd, "hook_event_name": "SessionEnd", "prompt_id": "p1"})
	e.waitFor("会话结束", func() bool { return e.session("claude:old-pm").State == store.StateEnded })
	if n := e.hub.NoticeSweep(ctx); n != 2 {
		t.Fatalf("应关掉计划完成的进展和被取代的旧进展，共 2 条：%d", n)
	}
	get := func(code string) store.Decision {
		d, err := e.store.GetDecision(ctx, code)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	for _, code := range []string{finished.Code, older.Code} {
		if d := get(code); d.Status != store.DecClosed || !strings.Contains(d.CloseReason, "只告知") {
			t.Fatalf("%s 应关掉并写明原因：%s %q", code, d.Status, d.CloseReason)
		}
	}
	for _, code := range []string{latest.Code, other.Code} {
		if d := get(code); d.Status != store.DecAnswered {
			t.Fatalf("%s 还要有人接手，应留在待转交：%s", code, d.Status)
		}
	}
	// 待转交卡片点「关闭」：真的关闭
	e.call("POST", "/api/decisions/"+other.Code+"/close", map[string]any{}, nil)
	if d := get(other.Code); d.Status != store.DecClosed {
		t.Fatalf("点关闭后应关闭：%s", d.Status)
	}
}
