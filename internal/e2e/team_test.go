package e2e

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/client"
	"github.com/NekroAI/kitestring/internal/daemon"
	"github.com/NekroAI/kitestring/internal/store"
)

// 工人池（R-86）：只有 PM 能派活；指定开发 / auto 挑人（在线待命优先）；没有在线开发时不派；
// 派给待命的开发会立刻唤醒它，看板上需求交给了它。
func TestTeamAssign(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	c := client.New(30 * time.Second)
	ctx := context.Background()
	for _, sid := range []string{"pm", "dev-a", "dev-b"} {
		hook(t, "claude", map[string]any{"session_id": sid, "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "上线", "prompt_id": "p0"})
	}
	e.waitFor("会话入库", func() bool { return e.session("claude:dev-b").Key != "" })
	role := func(k, r string) {
		if err := c.Do(ctx, "POST", "/agent/sessions/role", map[string]any{"session_key": k, "role": r}, nil); err != nil {
			t.Fatal(err)
		}
	}
	role("claude:pm", "pm")
	e.allowRelease("claude:pm")
	role("claude:dev-a", "dev")
	role("claude:dev-b", "dev")

	var r1, r2, r3 store.Requirement
	for _, x := range []struct {
		r     *store.Requirement
		title string
	}{{&r1, "导出 CSV"}, {&r2, "优惠券入口"}, {&r3, "积分规则"}} {
		if err := c.Do(ctx, "POST", "/agent/requirements", map[string]any{"title": x.title, "session_key": "claude:pm"}, x.r); err != nil {
			t.Fatal(err)
		}
	}

	type assignOut struct {
		Assigned bool   `json:"assigned"`
		To       string `json:"to"`
		Message  string `json:"message"`
	}
	var out assignOut
	if err := c.Do(ctx, "POST", "/agent/requirements/"+r1.Code+"/assign", map[string]any{"session_key": "claude:dev-a", "to": "claude:dev-b"}, &out); err == nil {
		t.Fatal("非 PM 派活应被拒绝")
	}

	// dev-b 挂着 ks wait（在线待命），dev-a 在跑：auto 应挑 dev-b，并立刻唤醒它
	done := make(chan daemon.WaitResult, 1)
	go func() {
		var res daemon.WaitResult
		_ = c.Do(ctx, "GET", "/wait?session_key=claude:dev-b&timeout=20s", nil, &res)
		done <- res
	}()
	e.waitFor("dev-b 待命", func() bool { return e.session("claude:dev-b").Parked })
	var team struct {
		PM   *struct{ Key string } `json:"pm"`
		Devs []struct {
			Key      string `json:"key"`
			Presence string `json:"presence"`
		} `json:"devs"`
	}
	if err := c.Do(ctx, "GET", "/agent/team?session_key=claude:pm", nil, &team); err != nil {
		t.Fatal(err)
	}
	if team.PM == nil || team.PM.Key != "claude:pm" || len(team.Devs) != 2 || team.Devs[0].Key != "claude:dev-b" || team.Devs[0].Presence != "standby" || team.Devs[1].Presence != "working" {
		t.Fatalf("工人池不对：%+v", team)
	}
	if err := c.Do(ctx, "POST", "/agent/requirements/"+r1.Code+"/assign", map[string]any{"session_key": "claude:pm", "reason": "做法明确", "to": "auto", "comment": "和导入是一套"}, &out); err != nil {
		t.Fatal(err)
	}
	if !out.Assigned || out.To != "claude:dev-b" || !strings.Contains(out.Message, "马上开工") {
		t.Fatalf("auto 应派给待命的 dev-b：%+v", out)
	}
	select {
	case res := <-done:
		if res.Reason != "delivered" || !strings.Contains(res.Text, "导出 CSV") || !strings.Contains(res.Text, "和导入是一套") {
			t.Fatalf("dev-b 应收到需求和交代：%+v", res)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("dev-b 没有被唤醒")
	}
	var got store.Requirement
	e.call("GET", "/api/requirements/"+r1.Code, nil, &got)
	if got.OfferedTo != "claude:dev-b" || got.Status != store.ReqReady {
		t.Fatalf("看板应显示交给 dev-b、待开始：%s %s", got.OfferedTo, got.Status)
	}
	var decs struct {
		Decisions []store.Decision `json:"decisions"`
	}
	e.call("GET", "/api/decisions", nil, &decs)
	byPM := false
	for _, d := range decs.Decisions {
		byPM = byPM || (d.RequirementID == r1.ID && d.Kind == store.KindAssign && d.AssignedBy == "claude:pm")
	}
	if !byPM {
		t.Fatal("送达记录应写明由 PM 派活")
	}

	// dev-b 做完交验收，用户退回 → 意见送到 PM，由 PM 转派（R-85）
	c.Do(ctx, "POST", "/agent/sessions/claude:dev-b/link", map[string]any{"requirement": r1.Code}, nil)
	c.Do(ctx, "POST", "/agent/requirements/"+r1.Code+"/status", map[string]any{"status": "review", "session_key": "claude:dev-b"}, nil)
	var rv struct {
		Feedback []store.Decision `json:"feedback"`
	}
	e.call("POST", "/api/reviews", map[string]any{"items": []map[string]any{{"ref": r1.Code, "verdict": "revise", "comment": "表头要冻结"}}}, &rv)
	if len(rv.Feedback) != 1 || rv.Feedback[0].SessionKey != "claude:pm" {
		t.Fatalf("退回意见应送到 PM：%+v", rv.Feedback)
	}

	// 指定派给在忙的 dev-a
	if err := c.Do(ctx, "POST", "/agent/requirements/"+r2.Code+"/assign", map[string]any{"session_key": "claude:pm", "reason": "做法明确", "to": "claude:dev-a"}, &out); err != nil {
		t.Fatal(err)
	}
	if !out.Assigned || !strings.Contains(out.Message, "这一轮结束") {
		t.Fatalf("派给在忙的 dev-a：%+v", out)
	}
	if err := c.Do(ctx, "POST", "/agent/requirements/"+r2.Code+"/assign", map[string]any{"session_key": "claude:pm", "reason": "做法明确", "to": "claude:nobody"}, &out); err == nil {
		t.Fatal("派给不存在的会话应报错")
	}

	// 都离线：auto 不派
	for _, sid := range []string{"dev-a", "dev-b"} {
		hook(t, "claude", map[string]any{"session_id": sid, "cwd": cwd, "hook_event_name": "SessionEnd"})
	}
	e.waitFor("开发都结束", func() bool { return e.session("claude:dev-a").State == store.StateEnded && e.session("claude:dev-b").State == store.StateEnded })
	if err := c.Do(ctx, "POST", "/agent/requirements/"+r3.Code+"/assign", map[string]any{"session_key": "claude:pm", "reason": "做法明确", "to": "auto"}, &out); err != nil {
		t.Fatal(err)
	}
	if out.Assigned || !strings.Contains(out.Message, "没有在线的 Dev") {
		t.Fatalf("没有在线开发时不应派出：%+v", out)
	}
}
