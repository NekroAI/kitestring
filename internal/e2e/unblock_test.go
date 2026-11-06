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

// 受阻自动解除与开工转进行中（R-269）：
//  1. 等的需求验收完成 / 等的决策答复后，受阻的需求自动回到受阻前的状态，时间线记下原因，并通知设为受阻的会话；
//  2. 会话用 session_link_req 关联受阻的需求开工时转为进行中，时间线写明原来在等什么；
//  3. 受阻改回待开始不再报「只能放行草稿」。
func TestBlockedAutoUnblock(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	c := client.New(10 * time.Second)
	ctx := context.Background()
	hook(t, "claude", map[string]any{"session_id": "dev", "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "开工", "prompt_id": "p0"})
	hook(t, "claude", map[string]any{"session_id": "dev2", "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "开工", "prompt_id": "p0"})
	e.waitFor("会话入库", func() bool { return e.session("claude:dev").ProjectID != "" && e.session("claude:dev2").ProjectID != "" })
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
	get := func(ref string) store.Requirement {
		t.Helper()
		var r store.Requirement
		e.call("GET", "/api/requirements/"+ref, nil, &r)
		return r
	}
	must := func(r store.Requirement, err error) store.Requirement {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	last := func(r store.Requirement) store.StatusChange { return r.StatusChanges[len(r.StatusChanges)-1] }

	// 1. 等的需求验收完成：受阻的回到受阻前的进行中，通知设为受阻的会话
	front := create("前置需求")
	back := create("后续需求")
	must(status(back.Code, map[string]any{"status": "in_progress"}))
	must(status(back.Code, map[string]any{"status": "blocked", "waiting_on": front.Code, "reason": "等前置做完"}))
	must(status(front.Code, map[string]any{"status": "review"}))
	e.call("POST", "/api/reviews", map[string]any{"items": []map[string]any{{"ref": front.Code, "verdict": "accept"}}}, nil)
	e.waitFor("受阻自动解除", func() bool { return get(back.Code).Status == store.ReqInProgress })
	r := get(back.Code)
	if r.BlockedOn != "" || r.BlockedReason != "" {
		t.Fatalf("解除后应清空在等什么：%q %q", r.BlockedOn, r.BlockedReason)
	}
	if ch := last(r); ch.From != store.ReqBlocked || ch.To != store.ReqInProgress || ch.Via != "unblock" || ch.Ref != front.Code || !strings.Contains(ch.Reason, front.Code) || !strings.Contains(ch.Reason, "受阻前是进行中") {
		t.Fatalf("时间线应记下自动解除的原因：%+v", ch)
	}
	if got := unblockTells(e, "claude:dev", back.Code); got != 1 {
		t.Fatalf("应通知设为受阻的会话一次：%d", got)
	}

	// 1b. 等的决策答复：受阻前是待开始的，回到待开始
	var d store.Decision
	if err := c.Do(ctx, "POST", "/agent/decisions", map[string]any{"question": "导出用逗号还是分号", "session_key": "claude:dev", "requirement": "none"}, &d); err != nil {
		t.Fatal(err)
	}
	wait := create("等决策的需求")
	e.call("PUT", "/api/requirements/"+wait.Code, map[string]any{"title": wait.Title, "project_id": wait.ProjectID, "status": "ready"}, nil)
	must(status(wait.Code, map[string]any{"status": "blocked", "waiting_on": d.Code}))
	e.call("POST", "/api/decisions/answers", map[string]any{"answers": []map[string]any{{"ref": d.Code, "answer": "逗号"}}}, nil)
	e.waitFor("决策答复后解除", func() bool { return get(wait.Code).Status == store.ReqReady })
	if ch := last(get(wait.Code)); ch.Via != "unblock" || ch.Ref != d.Code || !strings.Contains(ch.Reason, "已答复") {
		t.Fatalf("时间线应记下决策答复解除：%+v", ch)
	}

	// 1c. 没在等它的受阻需求不受影响
	other := create("另一个受阻")
	must(status(other.Code, map[string]any{"status": "in_progress"}))
	must(status(other.Code, map[string]any{"status": "blocked", "reason": "没写等谁"}))
	still := create("别的前置")
	must(status(still.Code, map[string]any{"status": "review"}))
	e.call("POST", "/api/reviews", map[string]any{"items": []map[string]any{{"ref": still.Code, "verdict": "accept"}}}, nil)
	if get(other.Code).Status != store.ReqBlocked {
		t.Fatal("没指向完成需求的受阻不该被解除")
	}

	// 2. 会话关联受阻需求开工：转为进行中，时间线写明原来在等什么
	pre := create("还没做完的前置")
	blocked := create("被阻住的需求")
	must(status(blocked.Code, map[string]any{"status": "in_progress"}))
	must(status(blocked.Code, map[string]any{"status": "blocked", "waiting_on": pre.Code, "reason": "等前置"}))
	var out struct {
		Requirement store.Requirement `json:"requirement"`
		Notices     []string          `json:"notices"`
	}
	if err := c.Do(ctx, "POST", "/agent/sessions/claude:dev2/link", map[string]any{"requirement": blocked.Code}, &out); err != nil {
		t.Fatal(err)
	}
	r = get(blocked.Code)
	if r.Status != store.ReqInProgress || r.BlockedOn != "" {
		t.Fatalf("关联开工应转为进行中：%s on=%q", r.Status, r.BlockedOn)
	}
	if ch := last(r); ch.From != store.ReqBlocked || ch.Via != "link" || ch.Session != "claude:dev2" || ch.Ref != pre.Code || !strings.Contains(ch.Reason, "原来在等 "+pre.Code) || !strings.Contains(ch.Reason, "等前置") {
		t.Fatalf("时间线应写明原来在等什么：%+v", ch)
	}
	if len(out.Notices) == 0 {
		t.Fatal("应提示这条原来是受阻")
	}

	// 3. 受阻改回待开始：不是放行，不再报「只能放行草稿」
	must(status(blocked.Code, map[string]any{"status": "blocked", "reason": "又卡住了"}))
	if r, err := status(blocked.Code, map[string]any{"status": "ready"}); err != nil || r.Status != store.ReqReady {
		t.Fatalf("受阻改回待开始应当成功：%v %s", err, r.Status)
	}
}

// unblockTells 数发给 key、说某条需求已自动解除受阻的团队消息。
func unblockTells(e *env, key, code string) int {
	var out struct {
		Decisions []store.Decision `json:"decisions"`
	}
	e.call("GET", "/api/decisions", nil, &out)
	n := 0
	for _, d := range out.Decisions {
		if d.Kind == store.KindTell && d.SessionKey == key && strings.Contains(d.Answer, code) && strings.Contains(d.Answer, "已自动解除受阻") {
			n++
		}
	}
	return n
}
