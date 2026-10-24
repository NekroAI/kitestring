package e2e

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/client"
	"github.com/NekroAI/kitestring/internal/daemon"
	"github.com/NekroAI/kitestring/internal/store"
)

// ks wait 开始前的自查（R-117）：名下没有等用户处理的事项时先提示——最后回复像在问用户时提示加重，
// 只是待命时轻提一句；有待答决策 / 待验收需求时不提示。可在会话面板上直接给它发一句话，会唤醒 ks wait。
func TestWaitCheck(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	c := client.New(30 * time.Second)
	key := "claude:wc-1"
	type check struct {
		Pending int      `json:"pending"`
		Items   []string `json:"items"`
		Asking  bool     `json:"asking"`
		Ask     string   `json:"ask"`
		Hint    string   `json:"hint"`
	}
	run := func() check {
		t.Helper()
		var out check
		if err := c.Do(context.Background(), "GET", "/wait/check?session_key="+url.QueryEscape(key), nil, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	turn := func(prompt, reply string, extra map[string]any) {
		t.Helper()
		hook(t, "claude", map[string]any{"session_id": "wc-1", "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": prompt})
		stop := map[string]any{"session_id": "wc-1", "cwd": cwd, "hook_event_name": "Stop", "last_assistant_message": reply}
		for k, v := range extra {
			stop[k] = v
		}
		hook(t, "claude", stop)
		e.waitFor("一轮结束", func() bool { s := e.session(key); return s.State == store.StateWaitReply })
	}

	// 1. 最后回复在问用户、名下没有正式事项 → 加重提示（摘录只给 ks wait 用，不给网页，R-239）
	turn("改登录页", "登录页改好了，测试都过了。\n\n要不要现在提交？", nil)
	got := run()
	if ask := got.Ask; !strings.Contains(ask, "要不要现在提交") || !strings.Contains(ask, "登录页改好了") {
		t.Fatalf("摘录应是最后一段（短时带上前一段）：%q", ask)
	}
	if got.Pending != 0 || !got.Asking || !strings.Contains(got.Hint, "像是在问用户") || !strings.Contains(got.Hint, "decision_raise") || !strings.Contains(got.Hint, "照常等待") {
		t.Fatalf("在问用户却没有正式事项，应加重提示：%+v", got)
	}

	// 2. 在工作台直接回复 → 不再算在等你回复；挂着的 ks wait 被唤醒并收到回复
	done := make(chan daemon.WaitResult, 1)
	go func() {
		var res daemon.WaitResult
		_ = c.Do(context.Background(), "GET", "/wait?session_key="+key+"&timeout=20s", nil, &res)
		done <- res
	}()
	e.waitFor("登记挂起", func() bool { return e.session(key).Parked })
	e.call("POST", "/api/sessions/"+url.PathEscape(key)+"/reply", map[string]any{"comment": "提交吧，记得写清楚改了什么"}, nil)
	select {
	case res := <-done:
		if res.Reason != "delivered" || !strings.Contains(res.Text, "工作台上回复了你") || !strings.Contains(res.Text, "提交吧，记得写清楚改了什么") {
			t.Fatalf("回复应唤醒 ks wait：%+v", res)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("回复后 ks wait 没有返回")
	}
	if got := run(); got.Asking {
		t.Fatalf("回复后不应再按上一轮的问句提醒：%+v", got)
	}

	// 3. 只是汇报、继续待命 → 轻提示，不说“像是在问用户”
	turn("继续", "已提交 a1b2c3。我继续待命，有新需求派给我就行。", nil)
	got = run()
	if got.Asking || got.Hint == "" || strings.Contains(got.Hint, "像是在问用户") || !strings.Contains(got.Hint, "只是待命") {
		t.Fatalf("单纯待命应轻提示：%+v", got)
	}

	// 4. 后台挂 ks wait 时这一轮还没结束：以对话记录里最新的回复为准
	tp := filepath.Join(t.TempDir(), "t.jsonl")
	lines := []map[string]any{
		{"type": "user", "message": map[string]any{"role": "user", "content": "合并吗"}},
		{"type": "assistant", "message": map[string]any{"role": "assistant", "content": []map[string]any{{"type": "text", "text": "分支已经准备好了，要我现在合并到 main 吗？"}}}},
		{"type": "assistant", "message": map[string]any{"role": "assistant", "content": []map[string]any{{"type": "tool_use", "name": "Bash", "input": map[string]any{"command": "ks wait"}}}}},
	}
	var b strings.Builder
	for _, l := range lines {
		raw, _ := json.Marshal(l)
		b.Write(raw)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(tp, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	hook(t, "claude", map[string]any{"session_id": "wc-1", "cwd": cwd, "hook_event_name": "PreToolUse", "tool_name": "Bash", "tool_input": map[string]any{"command": "ks wait"}, "transcript_path": tp})
	got = run()
	if !got.Asking || !strings.Contains(got.Ask, "要我现在合并到 main 吗") || !strings.Contains(got.Hint, "像是在问用户") {
		t.Fatalf("应按对话记录里最新的回复判断：%+v", got)
	}

	// 5. 提了决策 → 名下有待处理事项，不提示
	var dec store.Decision
	if err := c.Do(context.Background(), "POST", "/agent/decisions", map[string]any{"question": "现在合并到 main 吗？", "options": []string{"合并", "先不"}, "session_key": key}, &dec); err != nil {
		t.Fatal(err)
	}
	got = run()
	if got.Pending != 1 || got.Hint != "" || len(got.Items) != 1 || !strings.Contains(got.Items[0], dec.Code) {
		t.Fatalf("有待答决策时不应提示：%+v", got)
	}
	e.call("POST", "/api/decisions/"+dec.Code+"/close", map[string]any{}, nil)

	// 6. 关联的需求待验收 → 也算
	var r store.Requirement
	e.call("POST", "/api/requirements", map[string]any{"title": "登录页记住我", "status": "ready"}, &r)
	if err := c.Do(context.Background(), "POST", "/agent/sessions/"+key+"/link", map[string]any{"requirement": r.Code}, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.Do(context.Background(), "POST", "/agent/requirements/"+r.Code+"/status", map[string]any{"status": "review", "session_key": key}, nil); err != nil {
		t.Fatal(err)
	}
	got = run()
	if got.Pending != 1 || got.Hint != "" || !strings.Contains(got.Items[0], r.Code+" 待验收") {
		t.Fatalf("有待验收需求时不应提示：%+v", got)
	}
}

// AskTail 的判断：问句、征求意见的说法算；汇报、待命不算。
func TestAskTail(t *testing.T) {
	yes := []string{
		"改好了。\n\n要不要提交？",
		"都做完了，**你看可以吗**",
		"有两种做法：A 快、B 稳。你决定用哪种。",
		"Done. Should I open a PR?",
		"测试通过。要提交吗？我先挂着等你。",
	}
	no := []string{
		"已提交 a1b2c3，继续待命。",
		"R-12 已交验收，证据已附上。",
		"",
		"检查了是否有遗漏：没有。",
	}
	for _, s := range yes {
		if store.AskTail(s) == "" {
			t.Errorf("应判断为在问用户：%q", s)
		}
	}
	for _, s := range no {
		if store.AskTail(s) != "" {
			t.Errorf("不应判断为在问用户：%q", s)
		}
	}
}
