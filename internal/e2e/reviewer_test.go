package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/client"
	"github.com/NekroAI/kitestring/internal/daemon"
	"github.com/NekroAI/kitestring/internal/store"
)

// 审查员（R-88）：你把待验收的需求交给审查员 → 挂着 ks wait 的审查员被唤醒、收到审查任务（带“不要改代码、不要改状态”）
// → 交审查意见 → 需求状态不变、意见可读，和证据分开；非审查员不能交意见，也不能被选为审查员；验收照常。
func TestReviewer(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	c := client.New(30 * time.Second)
	ctx := context.Background()
	for _, sid := range []string{"dev", "rev"} {
		hook(t, "claude", map[string]any{"session_id": sid, "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "上线", "prompt_id": "p0"})
	}
	e.waitFor("会话入库", func() bool { return e.session("claude:rev").Key != "" })
	for k, r := range map[string]string{"claude:dev": "dev", "claude:rev": "reviewer"} {
		if err := c.Do(ctx, "POST", "/agent/sessions/role", map[string]any{"session_key": k, "role": r}, nil); err != nil {
			t.Fatal(err)
		}
	}

	var r store.Requirement
	if err := c.Do(ctx, "POST", "/agent/requirements", map[string]any{"title": "导出 CSV", "acceptance": "表头冻结", "session_key": "claude:dev"}, &r); err != nil {
		t.Fatal(err)
	}
	c.Do(ctx, "POST", "/agent/sessions/claude:dev/link", map[string]any{"requirement": r.Code}, nil)
	if err := c.Do(ctx, "POST", "/agent/requirements/"+r.Code+"/evidence", map[string]any{"kind": "test", "summary": "单测通过", "session_key": "claude:dev"}, nil); err != nil {
		t.Fatal(err)
	}
	c.Do(ctx, "POST", "/agent/requirements/"+r.Code+"/status", map[string]any{"status": "review", "session_key": "claude:dev"}, nil)

	// 非审查员：不能交审查意见，也不能用 evidence_attach 冒充（kind = review）
	if err := c.Do(ctx, "POST", "/agent/requirements/"+r.Code+"/reviews", map[string]any{"summary": "看着没问题", "session_key": "claude:dev"}, nil); err == nil || !strings.Contains(err.Error(), "Rev 会话") {
		t.Fatalf("非审查员交审查意见应被拒绝：%v", err)
	}
	if err := c.Do(ctx, "POST", "/agent/requirements/"+r.Code+"/evidence", map[string]any{"kind": "review", "summary": "冒充审查", "session_key": "claude:dev"}, nil); err == nil {
		t.Fatal("evidence_attach 不应接受 kind = review")
	}
	// 网页上只能交给审查员身份的会话
	if code := e.callStatus("POST", "/api/requirements/"+r.Code+"/reviewer", map[string]any{"session_key": "claude:dev"}); code != 400 {
		t.Fatalf("交给非审查员应返回 400，实际 %d", code)
	}

	// 交验收时项目里有在线评审：纸鸢自动派了一条验证任务（R-232），审查员挂上 ks wait 立刻收到
	wait := func() daemon.WaitResult {
		t.Helper()
		done := make(chan daemon.WaitResult, 1)
		go func() {
			var res daemon.WaitResult
			_ = c.Do(ctx, "GET", "/wait?session_key=claude:rev&timeout=20s", nil, &res)
			done <- res
		}()
		select {
		case res := <-done:
			return res
		case <-time.After(15 * time.Second):
			t.Fatal("审查员没有被唤醒")
		}
		return daemon.WaitResult{}
	}
	res := wait()
	for _, want := range []string{"审查", r.Code, "导出 CSV", "不要改代码", "不要改需求状态", "review_submit", "自动派给你验证", "表头冻结", "ks req review"} {
		if !strings.Contains(res.Text, want) {
			t.Fatalf("自动派的验证任务缺「%s」：%s", want, res.Text)
		}
	}

	// 你在网页上再把需求交给它（手动交给评审）
	var d store.Decision
	e.call("POST", "/api/requirements/"+r.Code+"/reviewer", map[string]any{"session_key": "claude:rev", "comment": "重点看空表"}, &d)
	if d.Kind != store.KindReviewTask {
		t.Fatalf("应记成审查任务：%+v", d)
	}
	res = wait()
	for _, want := range []string{"审查", r.Code, "导出 CSV", "不要改代码", "不要改需求状态", "review_submit", "重点看空表", "表头冻结"} {
		if !strings.Contains(res.Text, want) {
			t.Fatalf("审查员收到的内容缺「%s」：%s", want, res.Text)
		}
	}
	var got store.Requirement
	e.call("GET", "/api/requirements/"+r.Code, nil, &got)
	if got.Status != store.ReqReview {
		t.Fatalf("交给审查员不应改状态：%s", got.Status)
	}
	for _, k := range got.Sessions {
		if k == "claude:rev" {
			t.Fatal("交给审查员不应把它关联成需求的会话")
		}
	}

	// 审查员交意见：状态不变，意见可读，和证据分开
	if err := c.Do(ctx, "POST", "/agent/requirements/"+r.Code+"/reviews", map[string]any{"summary": "结论：空表时表头没冻结。", "session_key": "claude:rev"}, nil); err != nil {
		t.Fatal(err)
	}
	e.call("GET", "/api/requirements/"+r.Code, nil, &got)
	if got.Status != store.ReqReview {
		t.Fatalf("交审查意见不应改状态：%s", got.Status)
	}
	if len(got.Reviews) != 1 || got.Reviews[0].SessionKey != "claude:rev" || !strings.Contains(got.Reviews[0].Summary, "表头没冻结") {
		t.Fatalf("审查意见应挂在需求上：%+v", got.Reviews)
	}
	if len(got.Evidence) != 1 || got.Evidence[0].Kind != "test" {
		t.Fatalf("证据里不应混入审查意见：%+v", got.Evidence)
	}
	hasTask := false
	for _, f := range got.Feedback {
		hasTask = hasTask || (f.Kind == store.KindReviewTask && f.SessionKey == "claude:rev")
	}
	if !hasTask {
		t.Fatal("时间线里应有「交给审查员」")
	}

	// 不拦截验收：照常通过
	e.call("POST", "/api/reviews", map[string]any{"items": []map[string]any{{"ref": r.Code, "verdict": "accept"}}}, nil)
	e.call("GET", "/api/requirements/"+r.Code, nil, &got)
	if got.Status != store.ReqDone || len(got.Reviews) != 1 {
		t.Fatalf("验收应照常通过、意见保留：%s %d", got.Status, len(got.Reviews))
	}
}

// callStatus 调网页接口，只返回状态码（用于断言被拒绝）。
func (e *env) callStatus(method, path string, in any) int {
	e.t.Helper()
	b, _ := json.Marshal(in)
	req, _ := http.NewRequest(method, e.srv.URL+path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.web.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}
