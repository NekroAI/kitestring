package e2e

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/client"
	"github.com/NekroAI/kitestring/internal/daemon"
)

// 会话身份（R-82）：声明身份后 Hub 记下并返回提示词；压缩后由 hook 补发；同一项目只认最新的 PM，
// 旧 PM 降为无身份并收到通知（挂着 ks wait 时立刻被唤醒）。
func TestRoles(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	for _, sid := range []string{"pm-1", "pm-2"} {
		hook(t, "claude", map[string]any{"session_id": sid, "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "接管 PM", "prompt_id": "p0"})
	}
	e.waitFor("两个会话入库", func() bool { return e.session("claude:pm-2").Key != "" && e.session("claude:pm-1").Key != "" })
	c := client.New(30 * time.Second)
	ctx := context.Background()
	type roleOut struct {
		Role    string   `json:"role"`
		Prompt  string   `json:"prompt"`
		Demoted []string `json:"demoted"`
	}
	var out roleOut
	if err := c.Do(ctx, "POST", "/agent/sessions/role", map[string]any{"session_key": "claude:pm-1", "role": "nobody"}, &out); err == nil {
		t.Fatal("未知身份应报错")
	}
	if err := c.Do(ctx, "POST", "/agent/sessions/role", map[string]any{"session_key": "claude:pm-1", "role": "pm"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Prompt, "PM") || len(out.Demoted) != 0 {
		t.Fatalf("设为 PM 应返回提示词且无人降级：%+v", out)
	}
	if r := e.session("claude:pm-1").Role; r != "pm" {
		t.Fatalf("网页应看到身份 pm，得到 %q", r)
	}

	// 压缩后补发身份提示词；普通的新一轮不重复注入
	e.waitFor("daemon 收到身份", func() bool {
		return strings.Contains(hook(t, "claude", map[string]any{"session_id": "pm-1", "cwd": cwd, "hook_event_name": "SessionStart", "source": "compact"}), "你的会话身份：主调度（PM）")
	})
	if got := hook(t, "claude", map[string]any{"session_id": "pm-1", "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "继续", "prompt_id": "p1"}); strings.Contains(got, "会话身份") {
		t.Fatalf("普通一轮不应重复注入身份：%s", got)
	}

	// 旧 PM 挂着 ks wait；第二个会话接管 PM → 旧 PM 被唤醒并收到降级通知
	done := make(chan daemon.WaitResult, 1)
	go func() {
		var res daemon.WaitResult
		_ = c.Do(ctx, "GET", "/wait?session_key=claude:pm-1&timeout=20s", nil, &res)
		done <- res
	}()
	e.waitFor("旧 PM 挂起", func() bool { return e.session("claude:pm-1").Parked })
	if err := c.Do(ctx, "POST", "/agent/sessions/role", map[string]any{"session_key": "claude:pm-2", "role": "pm"}, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Demoted) != 1 || out.Demoted[0] != "claude:pm-1" {
		t.Fatalf("应降级旧 PM：%+v", out)
	}
	select {
	case res := <-done:
		if res.Reason != "delivered" || !strings.Contains(res.Text, "主调度（PM）身份已由") {
			t.Fatalf("旧 PM 应收到降级通知：%+v", res)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("旧 PM 没有被唤醒")
	}
	if a, b := e.session("claude:pm-1").Role, e.session("claude:pm-2").Role; a != "" || b != "pm" {
		t.Fatalf("身份应为 旧=空 新=pm，得到 %q %q", a, b)
	}
	// 降级后压缩不再补发 PM 提示词
	e.waitFor("daemon 更新身份", func() bool {
		return !strings.Contains(hook(t, "claude", map[string]any{"session_id": "pm-1", "cwd": cwd, "hook_event_name": "SessionStart", "source": "compact"}), "你的会话身份")
	})

	// 清除身份
	if err := c.Do(ctx, "POST", "/agent/sessions/role", map[string]any{"session_key": "claude:pm-2", "role": ""}, &out); err != nil {
		t.Fatal(err)
	}
	if r := e.session("claude:pm-2").Role; r != "" {
		t.Fatalf("应已清除身份，得到 %q", r)
	}
}
