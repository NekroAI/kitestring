package e2e

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/client"
	"github.com/NekroAI/kitestring/internal/store"
)

// R-167：验收退回意见不送顾问 / 审查员。有 PM 送 PM；没有 PM 送最近做这条需求的开发；
// 都没有就不送给任何会话（留在需求上，由你手动交出）。
func TestReviseFeedbackSkipsAdvisorReviewer(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	c := client.New(30 * time.Second)
	ctx := context.Background()
	for _, id := range []string{"pm", "adv", "rev", "dev"} {
		hook(t, "claude", map[string]any{"session_id": id, "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "上线", "prompt_id": "p0"})
	}
	e.waitFor("会话入库", func() bool { return e.session("claude:dev").Key != "" })
	setRole := func(id, role string) {
		if err := c.Do(ctx, "POST", "/agent/sessions/role", map[string]any{"session_key": "claude:" + id, "role": role}, nil); err != nil {
			t.Fatal(err)
		}
	}
	setRole("adv", "advisor")
	setRole("rev", "reviewer")
	setRole("dev", "dev")
	create := func(id, title string) store.Requirement {
		var r store.Requirement
		if err := c.Do(ctx, "POST", "/agent/requirements", map[string]any{"title": title, "session_key": "claude:" + id}, &r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	revise := func(r store.Requirement) store.Decision {
		e.call("PUT", "/api/requirements/"+r.Code, map[string]any{"title": r.Title, "status": "review", "project_id": r.ProjectID}, nil)
		var rv struct {
			Feedback []store.Decision `json:"feedback"`
		}
		e.call("POST", "/api/reviews", map[string]any{"items": []map[string]any{{"ref": r.Code, "verdict": "revise", "comment": "再改改"}}}, &rv)
		if len(rv.Feedback) != 1 {
			t.Fatalf("应有一条退回意见：%+v", rv)
		}
		return rv.Feedback[0]
	}

	// 1. 没有 PM，需求只关联顾问：意见不送给顾问，也不送给别人
	a := create("adv", "顾问记下的导出")
	if d := revise(a); d.SessionKey != "" {
		t.Fatalf("只关联顾问且没有 PM 时退回意见不应送给任何会话：%q", d.SessionKey)
	}
	// 2. 没有 PM，关联顾问和审查员 + 开发：送给开发
	b := create("rev", "审查员记下的空表")
	c.Do(ctx, "POST", "/agent/sessions/claude:adv/link", map[string]any{"requirement": b.Code}, nil)
	c.Do(ctx, "POST", "/agent/sessions/claude:dev/link", map[string]any{"requirement": b.Code}, nil)
	if d := revise(b); d.SessionKey != "claude:dev" {
		t.Fatalf("没有 PM 时退回意见应送给开发：%q", d.SessionKey)
	}
	// 3. 有 PM：一律送 PM
	setRole("pm", "pm")
	if d := revise(b); d.SessionKey != "claude:pm" {
		t.Fatalf("有 PM 时退回意见应送 PM：%q", d.SessionKey)
	}
	f := create("adv", "顾问记下的另一条")
	if d := revise(f); d.SessionKey != "claude:pm" {
		t.Fatalf("顾问的需求退回意见应送 PM：%q", d.SessionKey)
	}
}
