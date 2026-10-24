package e2e

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/client"
	"github.com/NekroAI/kitestring/internal/store"
)

// R-239：「要你处理」和「等你」只认明确记录。会话只在对话里写问句，不记任何「在等你」的标记、不把原文给网页；
// 用提问工具在问 / 等授权时等待类型明确记成字段。原文取自 R-235 的两张截图和 R-239 的截图。
func TestExplicitWaiting(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	key := "claude:ex-1"
	ev := func(name string, extra map[string]any) {
		t.Helper()
		m := map[string]any{"session_id": "ex-1", "cwd": cwd, "hook_event_name": name}
		for k, v := range extra {
			m[k] = v
		}
		hook(t, "claude", m)
	}
	rawSessions := func() string {
		var raw map[string]any
		e.call("GET", "/api/sessions", nil, &raw)
		b, _ := json.Marshal(raw)
		return string(b)
	}

	replies := []string{
		// R-235 截图一：在对话里问「要不要记成需求草稿」
		"主调度正在部署这两条，部署时 hub 会重启约 1 分钟。\n\n失败提示改中文这件事，要不要我记成一条需求草稿？我在后台继续等新决策和交验收。",
		// R-235 截图二：只是报告排期
		"主调度排了下一批：R-233、R-234 先做。\n\nR-235 和 R-236 等你确认后再排。",
		// R-239 截图：泳道「等你」旁边的红字
		"确认「等你回复」卡片只截了会话最后两段（R-235）、提交失败的提示只写英文（R-236），草稿后再排期。",
	}
	for i, text := range replies {
		ev("UserPromptSubmit", map[string]any{"prompt": "继续"})
		ev("Stop", map[string]any{"last_assistant_message": text})
		e.waitFor("一轮结束", func() bool { return e.session(key).State == store.StateWaitReply })
		s := e.session(key)
		if s.WaitKind != "" {
			t.Fatalf("原文 %d 只是对话里的话，不应记等待类型：%q", i+1, s.WaitKind)
		}
		if raw := rawSessions(); strings.Contains(raw, "reply_ask") || strings.Contains(raw, "digest_ask") || strings.Contains(raw, "等你确认后再排") || strings.Contains(raw, "要不要我记成") {
			t.Fatalf("原文 %d 不应以「在等你回复」交给网页：%s", i+1, raw)
		}
		if s.PendingDecisions != 0 {
			t.Fatalf("原文 %d：没有提决策就没有待办：%+v", i+1, s.PendingDecisions)
		}
	}

	// 提问工具在问 → question；结束一轮回到空
	ev("UserPromptSubmit", map[string]any{"prompt": "选个方案"})
	ev("PreToolUse", map[string]any{"tool_name": "AskUserQuestion", "tool_input": map[string]any{"questions": []any{map[string]any{"question": "用哪个方案？"}}}})
	e.waitFor("提问工具", func() bool { return e.session(key).WaitKind == store.WaitQuestion })
	if s := e.session(key); s.State != store.StateWaitReply {
		t.Fatalf("提问工具在问时应是等你：%+v", s.State)
	}
	ev("PostToolUse", map[string]any{"tool_name": "AskUserQuestion"})
	e.waitFor("答完继续跑", func() bool { s := e.session(key); return s.State == store.StateRunning && s.WaitKind == "" })
	// 等授权 → approval
	ev("PermissionRequest", map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "rm -rf build"}})
	e.waitFor("等授权", func() bool { return e.session(key).WaitKind == store.WaitApproval })
	ev("PostToolUse", map[string]any{"tool_name": "Bash"})
	ev("Stop", map[string]any{"last_assistant_message": "删完了。要不要顺手清缓存？"})
	e.waitFor("一轮结束", func() bool { s := e.session(key); return s.State == store.StateWaitReply && s.WaitKind == "" })
}

// R-239：推荐选项存成字段——提问方明确给序号，或推荐文字与选项完全相同；不做模糊匹配。
func TestRecOption(t *testing.T) {
	e := setup(t)
	_ = e
	c := client.New(10 * time.Second)
	ctx := context.Background()
	raise := func(body map[string]any) store.Decision {
		t.Helper()
		var d store.Decision
		body["session_key"] = "claude:rec"
		if err := c.Do(ctx, "POST", "/agent/decisions", body, &d); err != nil {
			t.Fatal(err)
		}
		return d
	}
	opts := []string{"方案 A", "方案 B"}
	cases := []struct {
		name string
		body map[string]any
		want int
	}{
		{"推荐文字只是提到选项，不猜", map[string]any{"recommendation": "推荐「方案 B」，改动小"}, -1},
		{"推荐文字以选项开头，也不猜", map[string]any{"recommendation": "方案 A，因为稳"}, -1},
		{"推荐文字与选项完全相同", map[string]any{"recommendation": "方案 A"}, 0},
		{"明确给了序号", map[string]any{"recommendation": "推荐 B，改动小", "recommended_option": 1}, 1},
		{"序号越界不算", map[string]any{"recommendation": "推荐 B", "recommended_option": 5}, -1},
		{"没有推荐", map[string]any{}, -1},
	}
	for _, x := range cases {
		x.body["question"] = "用哪个方案？"
		x.body["options"] = opts
		if d := raise(x.body); d.RecOption != x.want {
			t.Errorf("%s：rec_option = %d，应为 %d", x.name, d.RecOption, x.want)
		}
	}
	if d := raise(map[string]any{"question": "批准这个方案？", "approval": true, "recommendation": "批准"}); d.RecOption != -1 {
		t.Errorf("批准类决策不标推荐选项：%d", d.RecOption)
	}
}
