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

// R-242：按主助理的建议退回——附言可以留空，送给接手会话的修改意见里带建议原文并标明出处；
// req_get 的数据里能看到验收建议；没有建议时附言照旧必填。
func TestReviseCarriesAideAdvice(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	c := client.New(30 * time.Second)
	ctx := context.Background()
	hook(t, "claude", map[string]any{"session_id": "dev", "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "上线", "prompt_id": "p0"})
	e.waitFor("会话入库", func() bool { return e.session("claude:dev").Key != "" })
	var r store.Requirement
	if err := c.Do(ctx, "POST", "/agent/requirements", map[string]any{"title": "导出", "session_key": "claude:dev"}, &r); err != nil {
		t.Fatal(err)
	}
	toReview := func() {
		e.call("PUT", "/api/requirements/"+r.Code, map[string]any{"title": r.Title, "status": "review", "project_id": r.ProjectID}, nil)
	}
	type revOut struct {
		Feedback []store.Decision `json:"feedback"`
	}
	revise := func(comment string) revOut {
		var rv revOut
		e.call("POST", "/api/reviews", map[string]any{"items": []map[string]any{{"ref": r.Code, "verdict": "revise", "comment": comment}}}, &rv)
		return rv
	}
	emptyRevise := func() int {
		return e.callStatus("POST", "/api/reviews", map[string]any{"items": []map[string]any{{"ref": r.Code, "verdict": "revise"}}})
	}

	// 没有建议、没附言：照旧不让退回
	toReview()
	if code := emptyRevise(); code != 400 {
		t.Fatalf("没有建议时附言必填，应 400：%d", code)
	}
	// 建议通过时留空也不行
	if err := e.store.SetAdvice(ctx, r.ID, "pass", "都达标", "claude:dev"); err != nil {
		t.Fatal(err)
	}
	if code := emptyRevise(); code != 400 {
		t.Fatalf("建议通过时留空仍应 400：%d", code)
	}

	// 建议退回 + 附言为空：能退回，建议随修改意见送出，req_get 能看到
	if err := e.store.SetAdvice(ctx, r.ID, "return", "第 2 条验收没达标", "claude:dev"); err != nil {
		t.Fatal(err)
	}
	got, _ := e.store.GetRequirement(ctx, r.Code)
	if got.Advice == nil || got.Advice.Verdict != "return" || got.Advice.Reason != "第 2 条验收没达标" {
		t.Fatalf("待验收时需求应带当前验收建议：%+v", got.Advice)
	}
	rv := revise("")
	if len(rv.Feedback) != 1 {
		t.Fatalf("应有一条修改意见：%+v", rv)
	}
	d := rv.Feedback[0]
	for _, w := range []string{"主助理", "不是用户原话", "建议退回", "第 2 条验收没达标"} {
		if !strings.Contains(d.Context, w) {
			t.Fatalf("修改意见应带标明出处的建议原文，缺 %q：%q", w, d.Context)
		}
	}
	if !strings.Contains(d.Answer, "按主助理建议退回") {
		t.Fatalf("附言为空时修改意见应写明按建议退回：%q", d.Answer)
	}
	if got, _ := e.store.GetRequirement(ctx, r.Code); got.Advice != nil || len(got.Feedback) == 0 || got.Feedback[0].Context != d.Context {
		t.Fatalf("退回后建议随修改意见留在需求上：%+v", got.Feedback)
	}

	// 带附言：附言和建议分开
	toReview()
	if err := e.store.SetAdvice(ctx, r.ID, "return", "边界没测", "claude:dev"); err != nil {
		t.Fatal(err)
	}
	d = revise("我也觉得要补边界").Feedback[0]
	if d.Answer != "我也觉得要补边界" || !strings.Contains(d.Context, "边界没测") {
		t.Fatalf("附言与建议应分开保存：%q | %q", d.Answer, d.Context)
	}
}
