package e2e

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/client"
	"github.com/NekroAI/kitestring/internal/store"
)

func agentPlan(t *testing.T, key, title string) store.Plan {
	t.Helper()
	var x store.Plan
	err := client.New(10*time.Second).Do(context.Background(), "POST", "/agent/plans", map[string]any{
		"title": title, "session_key": key,
		"stages": []map[string]any{{"title": "一", "requirements": []map[string]any{{"title": title + "-需求"}}}},
	}, &x)
	if err != nil {
		t.Fatal(err)
	}
	return x
}

// R-99：顾问 / 审查员建的计划，负责会话改为项目 PM（没有就留空）；PM / 开发 / 无身份仍是调用者；
// 顾问和审查员不能当负责会话；已手动交给别人的需求，阶段解锁时不再推给负责会话。
func TestPlanOwnerRules(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	c := client.New(10 * time.Second)
	for _, id := range []string{"pm", "adv", "rev", "dev", "plain"} {
		hook(t, "claude", map[string]any{"session_id": id, "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "hi", "prompt_id": "p0"})
	}
	setRole := func(id, role string) {
		if err := c.Do(context.Background(), "POST", "/agent/sessions/role", map[string]any{"session_key": "claude:" + id, "role": role}, nil); err != nil {
			t.Fatal(err)
		}
	}

	// 没有 PM：顾问建的计划负责会话留空
	setRole("adv", "advisor")
	setRole("rev", "reviewer")
	if x := agentPlan(t, "claude:adv", "无 PM"); x.OwnerSession != "" {
		t.Fatalf("没有 PM 时负责会话应留空：%q", x.OwnerSession)
	}

	// 有 PM：顾问 / 审查员建的计划归 PM；开发、无身份保持调用者
	setRole("pm", "pm")
	setRole("dev", "dev")
	if x := agentPlan(t, "claude:adv", "顾问计划"); x.OwnerSession != "claude:pm" {
		t.Fatalf("顾问建的计划应归 PM：%q", x.OwnerSession)
	}
	if x := agentPlan(t, "claude:rev", "审查计划"); x.OwnerSession != "claude:pm" {
		t.Fatalf("审查员建的计划应归 PM：%q", x.OwnerSession)
	}
	if x := agentPlan(t, "claude:dev", "开发计划"); x.OwnerSession != "claude:dev" {
		t.Fatalf("开发建的计划应归自己：%q", x.OwnerSession)
	}
	if x := agentPlan(t, "claude:plain", "无身份计划"); x.OwnerSession != "claude:plain" {
		t.Fatalf("无身份建的计划应归自己：%q", x.OwnerSession)
	}

	// 网页 / 接口不能把顾问、审查员设为负责会话
	var p store.Plan
	e.call("POST", "/api/plans", map[string]any{"title": "网页计划", "owner_session": "claude:dev",
		"stages": []map[string]any{{"title": "一", "requirements": []map[string]any{{"title": "甲"}, {"title": "乙"}}}}}, &p)
	for _, k := range []string{"claude:adv", "claude:rev"} {
		if code, body := e.try("PUT", "/api/plans/"+p.Code, map[string]any{"title": "网页计划", "owner_session": k}); code != 400 {
			t.Fatalf("编辑为 %s 应被拒绝：%d %s", k, code, body)
		}
		if code, _ := e.try("POST", "/api/plans", map[string]any{"title": "x", "owner_session": k,
			"stages": []map[string]any{{"title": "一", "requirements": []map[string]any{{"title": "丙"}}}}}); code != 400 {
			t.Fatalf("新建时指定 %s 应被拒绝：%d", k, code)
		}
	}

	// 阶段内一条需求已手动交给别的会话（pm），开始计划时不再推给负责会话（dev）
	jia, yi := p.Stages[0].Requirements[0], p.Stages[0].Requirements[1]
	e.call("POST", "/api/requirements/"+jia.Code+"/assign", map[string]any{"session_key": "claude:pm"}, nil)
	e.call("POST", "/api/plans/"+p.Code+"/start", map[string]any{}, &p)
	var rj, ry store.Requirement
	e.call("GET", "/api/requirements/"+jia.Code, nil, &rj)
	e.call("GET", "/api/requirements/"+yi.Code, nil, &ry)
	if rj.OfferedTo != "claude:pm" || len(rj.Sessions) != 1 || rj.Sessions[0] != "claude:pm" {
		t.Fatalf("已交给 PM 的需求不应再推给负责会话：%+v", rj)
	}
	if ry.OfferedTo != "claude:dev" {
		t.Fatalf("另一条应交给负责会话：%+v", ry)
	}
}
