package e2e

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/client"
	"github.com/NekroAI/kitestring/internal/daemon"
	"github.com/NekroAI/kitestring/internal/hub"
	"github.com/NekroAI/kitestring/internal/store"
)

// R-150：顾问 / 审查员记下的需求被你改为待开始后，不排给它们自己，改送项目 PM；
// 没有 PM 时停在看板上“没人接手”，不送给顾问；你手动交给审查员 / 交给顾问照旧。
func TestReadyFromAdvisorGoesToPM(t *testing.T) {
	hub.SetKickDelay(300 * time.Millisecond)
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
	// park 让会话挂上 ks wait 并结束这一轮，返回 wait 的结果通道
	wait := func(id string, timeout string) chan daemon.WaitResult {
		done := make(chan daemon.WaitResult, 1)
		go func() {
			var res daemon.WaitResult
			if err := c.Do(ctx, "GET", "/wait?session_key=claude:"+id+"&timeout="+timeout, nil, &res); err != nil {
				res.Reason = "err: " + err.Error()
			}
			done <- res
		}()
		return done
	}
	park := func(id string, timeout string) chan daemon.WaitResult {
		done := wait(id, timeout)
		e.waitFor(id+" 挂起", func() bool { return e.session("claude:"+id).Parked })
		hook(t, "claude", map[string]any{"session_id": id, "cwd": cwd, "hook_event_name": "Stop"})
		e.waitFor(id+" 待命", func() bool { return e.session("claude:"+id).State == store.StateParked })
		return done
	}
	create := func(id, title string) store.Requirement {
		var r store.Requirement
		if err := c.Do(ctx, "POST", "/agent/requirements", map[string]any{"title": title, "session_key": "claude:" + id}, &r); err != nil {
			t.Fatal(err)
		}
		if r.Status != store.ReqDraft || len(r.Sessions) != 1 || r.Sessions[0] != "claude:"+id {
			t.Fatalf("Agent 记下的需求应是草稿、关联创建者：%+v", r)
		}
		return r
	}
	ready := func(r store.Requirement) {
		e.call("PUT", "/api/requirements/"+r.Code, map[string]any{"title": r.Title, "status": "ready", "project_id": r.ProjectID}, nil)
	}
	get := func(code string) store.Requirement {
		var r store.Requirement
		e.call("GET", "/api/requirements/"+code, nil, &r)
		return r
	}

	// 顾问第一次挂 ks wait、项目还没有主助理：先收到一次身份调整提示（R-190），收掉再测排队
	if res := <-wait("adv", "5s"); !strings.Contains(res.Text, "ks role aide") {
		t.Fatalf("顾问常驻应先收到身份调整提示：%+v", res)
	}

	// 1. 没有 PM：顾问记下的需求改为待开始后不送给顾问，停在“没人接手”
	advDone := park("adv", "3s")
	a := create("adv", "顾问记下的导出 CSV")
	ready(a)
	select {
	case res := <-advDone:
		if res.Reason == "delivered" || strings.Contains(res.Text, a.Title) {
			t.Fatalf("没有 PM 时不应送给顾问：%+v", res)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("顾问的 ks wait 没有按时结束")
	}
	if got := get(a.Code); got.OfferedTo != "" || got.Status != store.ReqReady {
		t.Fatalf("没有 PM 时应停在待开始、没人接手：%q %s", got.OfferedTo, got.Status)
	}

	// 2. 有了 PM：PM 挂上 ks wait 就收到顾问那条
	setRole("pm", "pm")
	pmDone := wait("pm", "20s")
	select {
	case res := <-pmDone:
		if res.Reason != "delivered" || !strings.Contains(res.Text, a.Title) {
			t.Fatalf("PM 应收到顾问记下的需求：%+v", res)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("PM 没有收到顾问记下的需求")
	}
	if got := get(a.Code); got.OfferedTo != "claude:pm" {
		t.Fatalf("顾问记下的需求应排给 PM：%q", got.OfferedTo)
	}

	// 3. 审查员记下的需求同理，送给 PM，不送审查员
	hook(t, "claude", map[string]any{"session_id": "pm", "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "继续", "prompt_id": "p1"})
	pmDone = park("pm", "20s")
	revDone := park("rev", "3s")
	b := create("rev", "审查员记下的空表问题")
	ready(b)
	select {
	case res := <-pmDone:
		if res.Reason != "delivered" || !strings.Contains(res.Text, b.Title) {
			t.Fatalf("PM 应收到审查员记下的需求：%+v", res)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("PM 没有收到审查员记下的需求")
	}
	if res := <-revDone; res.Reason == "delivered" || strings.Contains(res.Text, b.Title) {
		t.Fatalf("不应送给审查员：%+v", res)
	}
	if got := get(b.Code); got.OfferedTo != "claude:pm" {
		t.Fatalf("审查员记下的需求应排给 PM：%q", got.OfferedTo)
	}

	// 4. 开发记下的需求仍归开发自己
	devDone := park("dev", "20s")
	d := create("dev", "开发记下的登录超时")
	ready(d)
	select {
	case res := <-devDone:
		if res.Reason != "delivered" || !strings.Contains(res.Text, d.Title) {
			t.Fatalf("开发应收到自己记下的需求：%+v", res)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("开发没有收到自己记下的需求")
	}
	if got := get(d.Code); got.OfferedTo != "claude:dev" {
		t.Fatalf("开发记下的需求应排给开发：%q", got.OfferedTo)
	}

	// 5. 你手动指定照旧：交给顾问、交给审查员都送到它们
	x := create("dev", "请顾问看看方案")
	e.call("POST", "/api/requirements/"+x.Code+"/assign", map[string]any{"session_key": "claude:adv"}, nil)
	if got := get(x.Code); got.OfferedTo != "claude:adv" {
		t.Fatalf("手动交给顾问应照旧：%q", got.OfferedTo)
	}
	c.Do(ctx, "POST", "/agent/sessions/claude:dev/link", map[string]any{"requirement": d.Code}, nil)
	if err := c.Do(ctx, "POST", "/agent/requirements/"+d.Code+"/evidence", map[string]any{"kind": "test", "summary": "单测通过", "session_key": "claude:dev"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.Do(ctx, "POST", "/agent/requirements/"+d.Code+"/status", map[string]any{"status": "review", "session_key": "claude:dev"}, nil); err != nil {
		t.Fatal(err)
	}
	var task store.Decision
	e.call("POST", "/api/requirements/"+d.Code+"/reviewer", map[string]any{"session_key": "claude:rev"}, &task)
	if task.Kind != store.KindReviewTask || task.SessionKey != "claude:rev" {
		t.Fatalf("交给审查员应照旧：%+v", task)
	}
}
