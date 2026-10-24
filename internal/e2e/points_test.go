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

// TestDecisionPoints：一个决策有两个要拍板的点，分别作答后逐点注入 Agent 的上下文（R-112）；
// 批准类可整体拒绝；没选全时拒绝提交；旧的单选决策照常作答。
func TestDecisionPoints(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	prompt := func(turn string) string {
		return hook(t, "claude", map[string]any{"session_id": "pt-1", "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "继续", "prompt_id": turn})
	}
	prompt("p0")
	c := client.New(5 * time.Second)
	ctx := context.Background()
	key := "claude:pt-1"

	var d struct {
		store.Decision
		BlockWarnings []string `json:"block_warnings"`
	}
	err := c.Do(ctx, "POST", "/agent/decisions", map[string]any{
		"question": "阶段 1 按这个方案做吗？", "approval": true, "session_key": key,
		"summary": []string{"R-82 新命令 ks role，提示词压缩后由 hook 补发", "R-83 没挂 ks wait 就拦下结束"},
		"points": []any{
			map[string]any{"question": "旧 PM 被接管后变成什么？", "options": []any{
				map[string]any{"label": "无身份", "desc": "交接干净", "recommended": true},
				map[string]any{"label": "降为开发", "desc": "工人池会变大"}}},
			map[string]any{"question": "每轮拦几次就放行？", "options": []any{
				map[string]any{"label": "2 次", "recommended": true},
				map[string]any{"label": "不限"}}},
			map[string]any{"question": "只有一个选项", "options": []any{map[string]any{"label": "唯一"}}},
		},
	}, &d)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Points) != 2 || d.Points[0].Title != "旧 PM 被接管后变成什么？" || !d.Points[0].Items[0].Recommended || len(d.Summary) != 2 || len(d.BlockWarnings) != 1 {
		t.Fatalf("要拍板的点保存不对：%+v %v", d.Points, d.BlockWarnings)
	}

	// 没选全：拒绝提交并说明
	if code, body := e.try("POST", "/api/decisions/answers", map[string]any{"answers": []map[string]any{{"ref": d.Code, "answer": "", "choices": []int{0, -1}}}}); code != 400 || !strings.Contains(body, "还没选") {
		t.Fatalf("没选全应被拒绝：%d %s", code, body)
	}
	// 逐点作答：第 1 点选第二个（不按推荐），第 2 点选推荐，附言
	e.call("POST", "/api/decisions/answers", map[string]any{"answers": []map[string]any{{"ref": d.Code, "answer": "其余按方案来", "choices": []int{1, 0}}}}, nil)
	var out string
	e.waitFor("多点答复送达", func() bool { out = prompt("p1"); return strings.Contains(out, d.Code) })
	for _, want := range []string{"用户答复：批准，按以下选择：", "    1. 旧 PM 被接管后变成什么？ → 降为开发", "    2. 每轮拦几次就放行？ → 2 次", "    附言：其余按方案来"} {
		if !strings.Contains(out, want) {
			t.Fatalf("注入里缺少「%s」：\n%s", want, out)
		}
	}
	var got store.Decision
	if err := c.Do(ctx, "GET", "/agent/decisions/"+d.Code, nil, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Choices) != 2 || got.Choices[0] != 1 || got.Choices[1] != 0 {
		t.Fatalf("逐点选择应保存：%v", got.Choices)
	}

	// 批准类多点决策可以整体拒绝
	var d2 store.Decision
	if err := c.Do(ctx, "POST", "/agent/decisions", map[string]any{"question": "按这个方案做吗？", "approval": true, "session_key": key,
		"points": []any{map[string]any{"question": "用哪个？", "options": []any{map[string]any{"label": "甲"}, map[string]any{"label": "乙"}}}}}, &d2); err != nil {
		t.Fatal(err)
	}
	e.call("POST", "/api/decisions/answers", map[string]any{"answers": []map[string]any{{"ref": d2.Code, "answer": "方向不对", "reject": true}}}, nil)
	e.waitFor("拒绝送达", func() bool { out = prompt("p2"); return strings.Contains(out, d2.Code) })
	if !strings.Contains(out, "用户答复：拒绝（用户明确拒绝了这个请求）。附言：方向不对") {
		t.Fatalf("拒绝应注入：\n%s", out)
	}

	// 批准类单点决策（R-176）：网页发「拒绝；附言」，送给 Agent 时写明是用户明确拒绝，不是退回修改、不是超时
	var d4 store.Decision
	if err := c.Do(ctx, "POST", "/agent/decisions", map[string]any{"question": "开一个独立开发会话做这件事吗？", "approval": true, "session_key": key}, &d4); err != nil {
		t.Fatal(err)
	}
	e.call("POST", "/api/decisions/answers", map[string]any{"answers": []map[string]any{{"ref": d4.Code, "answer": "拒绝；先在当前会话做"}}}, nil)
	e.waitFor("单点拒绝送达", func() bool { out = prompt("p4"); return strings.Contains(out, d4.Code) })
	if !strings.Contains(out, "用户答复：拒绝（用户明确拒绝了这个请求）。附言：先在当前会话做") || strings.Contains(out, "超时") {
		t.Fatalf("单点拒绝应写明是用户拒绝：\n%s", out)
	}

	// 旧的单选决策：照常按文字作答（带 choices 也忽略）
	var d3 store.Decision
	if err := c.Do(ctx, "POST", "/agent/decisions", map[string]any{"question": "用哪种颜色？", "options": []string{"红", "蓝"}, "session_key": key}, &d3); err != nil {
		t.Fatal(err)
	}
	e.call("POST", "/api/decisions/answers", map[string]any{"answers": []map[string]any{{"ref": d3.Code, "answer": "蓝", "choices": []int{0}}}}, nil)
	e.waitFor("单选送达", func() bool { out = prompt("p3"); return strings.Contains(out, d3.Code) })
	if !strings.Contains(out, "用户答复：蓝") {
		t.Fatalf("旧决策应照常注入：\n%s", out)
	}
}
