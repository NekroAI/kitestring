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

// 防重复需求（R-128）：Agent 建计划时新需求与同项目未关闭需求同名，直接关联已有需求并说明；
// req_create 遇同名未关闭需求给提示但照常新建；已搁置 / 已完成的同名不算。
func TestPlanAndReqDedupe(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	c := client.New(10 * time.Second)
	ctx := context.Background()
	hook(t, "claude", map[string]any{"session_id": "pm", "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "hi", "prompt_id": "p0"})
	key := "claude:pm"
	e.waitFor("会话入库", func() bool { return e.session(key).ProjectID != "" })

	type created struct {
		store.Requirement
		Notices []string `json:"notices"`
	}
	mk := func(title string) created {
		var r created
		if err := c.Do(ctx, "POST", "/agent/requirements", map[string]any{"title": title, "session_key": key}, &r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	first := mk("导出 CSV")
	if len(first.Notices) != 0 {
		t.Fatalf("没有同名时不应有提示：%v", first.Notices)
	}
	dup := mk("  导出  csv ")
	if dup.Code == first.Code || len(dup.Notices) != 1 || !strings.Contains(dup.Notices[0], first.Code) {
		t.Fatalf("同名未关闭需求应提示且仍新建：%+v", dup)
	}
	// 搁置后不再算同名
	if err := c.Do(ctx, "POST", "/agent/requirements/"+dup.Code+"/status", map[string]any{"status": "shelved", "session_key": key}, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.Do(ctx, "POST", "/agent/requirements/"+first.Code+"/status", map[string]any{"status": "shelved", "session_key": key}, nil); err != nil {
		t.Fatal(err)
	}
	if again := mk("导出 CSV"); len(again.Notices) != 0 {
		t.Fatalf("已搁置的同名不应提示：%v", again.Notices)
	}

	// 建计划：同名未关闭 -> 直接关联；不同名 -> 新建
	exist := mk("做搜索")
	var plan struct {
		store.Plan
		Notices []string `json:"notices"`
	}
	if err := c.Do(ctx, "POST", "/agent/plans", map[string]any{"title": "计划", "session_key": key,
		"stages": []map[string]any{{"title": "一", "requirements": []map[string]any{{"title": "做搜索"}, {"title": "做导出"}}}}}, &plan); err != nil {
		t.Fatal(err)
	}
	if len(plan.Notices) != 1 || !strings.Contains(plan.Notices[0], exist.Code) {
		t.Fatalf("同名需求应关联已有并说明：%v", plan.Notices)
	}
	got := plan.Stages[0].Requirements
	if len(got) != 2 {
		t.Fatalf("阶段应有 2 条需求：%+v", got)
	}
	codes := map[string]bool{got[0].Code: true, got[1].Code: true}
	if !codes[exist.Code] {
		t.Fatalf("阶段里应是已有需求 %s：%v", exist.Code, codes)
	}
	// 已在别的计划里的同名需求不动：仍新建并提示
	var plan2 struct {
		store.Plan
		Notices []string `json:"notices"`
	}
	if err := c.Do(ctx, "POST", "/agent/plans", map[string]any{"title": "计划二", "session_key": key,
		"stages": []map[string]any{{"title": "一", "requirements": []map[string]any{{"title": "做搜索"}}}}}, &plan2); err != nil {
		t.Fatal(err)
	}
	if len(plan2.Notices) != 1 || !strings.Contains(plan2.Notices[0], "已在计划") {
		t.Fatalf("同名需求已在别的计划时应提示：%v", plan2.Notices)
	}
	if plan2.Stages[0].Requirements[0].Code == exist.Code {
		t.Fatal("已在别的计划里的需求不应被抢走")
	}
}
