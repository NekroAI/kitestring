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

// 验收通过后自动提交（R-192）：项目开关默认关；打开后需求验收通过，负责的主调度收到一条合成消息
// （状态变化 + 「R-x 已验收，按项目设置提交属于它的改动（不推送）」），不再另收 status 通知；
// 被退回的不提示；关闭时验收通过只收到原来的状态通知、没有提交提示。
func TestCommitOnAccept(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	c := client.New(30 * time.Second)
	ctx := context.Background()
	for _, sid := range []string{"pm", "dev"} {
		hook(t, "claude", map[string]any{"session_id": sid, "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "上线", "prompt_id": "p0"})
	}
	e.waitFor("会话入库", func() bool { return e.session("claude:dev").ProjectID != "" })
	for _, kr := range [][2]string{{"claude:pm", "pm"}, {"claude:dev", "dev"}} {
		if err := c.Do(ctx, "POST", "/agent/sessions/role", map[string]any{"session_key": kr[0], "role": kr[1]}, nil); err != nil {
			t.Fatal(err)
		}
	}
	woke(t, parkWait(t, e, c, "claude:pm"), "已上线待命")
	project := e.session("claude:dev").ProjectID

	var projs struct {
		Projects []store.Project `json:"projects"`
	}
	getProject := func() store.Project {
		e.call("GET", "/api/projects", nil, &projs)
		for _, p := range projs.Projects {
			if p.ID == project {
				return p
			}
		}
		t.Fatal("找不到项目")
		return store.Project{}
	}
	setCommit := func(on bool) {
		p := getProject()
		e.call("PUT", "/api/projects/"+p.ID, map[string]any{"name": p.Name, "doc_root": p.DocRoot, "commit_on_accept": on}, nil)
	}
	newReview := func(title string) store.Requirement {
		t.Helper()
		var r store.Requirement
		if err := c.Do(ctx, "POST", "/agent/requirements", map[string]any{"title": title, "session_key": "claude:dev"}, &r); err != nil {
			t.Fatal(err)
		}
		pmW := parkWait(t, e, c, "claude:pm")
		if err := c.Do(ctx, "POST", "/agent/requirements/"+r.Code+"/status", map[string]any{"status": "review", "session_key": "claude:dev"}, nil); err != nil {
			t.Fatal(err)
		}
		woke(t, pmW, "已交验收", r.Code)
		return r
	}
	review := func(ref, verdict, comment string) {
		t.Helper()
		e.call("POST", "/api/reviews", map[string]any{"items": []map[string]any{{"ref": ref, "verdict": verdict, "comment": comment}}}, nil)
	}

	// 1. 默认关闭；关闭时验收通过只有原来的状态通知，没有提交提示
	if getProject().CommitOnAccept {
		t.Fatal("「验收通过后自动提交」默认应关闭")
	}
	r1 := newReview("导出 CSV")
	pmW := parkWait(t, e, c, "claude:pm")
	n := tellCount(e, "claude:pm")
	review(r1.Code, "accept", "")
	text := woke(t, pmW, r1.Code, "从待验收改为已完成")
	if strings.Contains(text, "提交") {
		t.Fatalf("关闭时不应有提交提示：%s", text)
	}
	time.Sleep(200 * time.Millisecond)
	if got := tellCount(e, "claude:pm"); got != n+1 {
		t.Fatalf("关闭时 PM 只应收到一条状态通知：%d → %d", n, got)
	}

	// 2. 打开：改设置通知 PM（settings 订阅）；验收通过 PM 只收到一条合成消息
	pmW = parkWait(t, e, c, "claude:pm")
	setCommit(true)
	woke(t, pmW, "验收通过后自动提交", "关 → 开")
	if !getProject().CommitOnAccept {
		t.Fatal("开关应已打开")
	}
	r2 := newReview("导出 Excel")
	pmW = parkWait(t, e, c, "claude:pm")
	n = tellCount(e, "claude:pm")
	review(r2.Code, "accept", "")
	text = woke(t, pmW, r2.Code+" 已验收，按项目设置提交属于它的改动（不推送）", "从待验收改为已完成", "中文", "需求编号")
	time.Sleep(200 * time.Millisecond)
	if got := tellCount(e, "claude:pm"); got != n+1 {
		t.Fatalf("验收通过 PM 只应收到一条（提交提示与状态通知合成）：%d → %d", n, got)
	}

	// 3. 退回的不提示
	r3 := newReview("导出 PDF")
	pmW = parkWait(t, e, c, "claude:pm")
	review(r3.Code, "revise", "页边距不对")
	text = woke(t, pmW, r3.Code, "页边距不对")
	if strings.Contains(text, "按项目设置提交") {
		t.Fatalf("退回不应有提交提示：%s", text)
	}

	// 4. 再关闭：回到只有状态通知
	pmW = parkWait(t, e, c, "claude:pm")
	setCommit(false)
	woke(t, pmW, "验收通过后自动提交", "开 → 关")
	r4 := newReview("导出 JSON")
	pmW = parkWait(t, e, c, "claude:pm")
	review(r4.Code, "accept", "")
	text = woke(t, pmW, r4.Code, "从待验收改为已完成")
	if strings.Contains(text, "提交") {
		t.Fatalf("重新关闭后不应有提交提示：%s", text)
	}
}
