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

// 你在网页上新建、放到待开始的需求（没有关联会话）归项目的 PM，PM 待命时整批收到（R-85）。
func TestReadyGoesToPM(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	c := client.New(30 * time.Second)
	ctx := context.Background()
	hook(t, "claude", map[string]any{"session_id": "pm", "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "接管 PM", "prompt_id": "p0"})
	e.waitFor("会话入库", func() bool { return e.session("claude:pm").Key != "" })
	if err := c.Do(ctx, "POST", "/agent/sessions/role", map[string]any{"session_key": "claude:pm", "role": "pm"}, nil); err != nil {
		t.Fatal(err)
	}
	done := make(chan daemon.WaitResult, 1)
	go func() {
		var res daemon.WaitResult
		_ = c.Do(ctx, "GET", "/wait?session_key=claude:pm&timeout=20s", nil, &res)
		done <- res
	}()
	e.waitFor("PM 挂起", func() bool { return e.session("claude:pm").Parked })
	hook(t, "claude", map[string]any{"session_id": "pm", "cwd": cwd, "hook_event_name": "Stop"})
	e.waitFor("PM 待命", func() bool { return e.session("claude:pm").State == store.StateParked })
	pid := e.session("claude:pm").ProjectID
	for _, title := range []string{"导出 CSV", "暗色模式", "修复登录超时"} {
		e.call("POST", "/api/requirements", map[string]any{"title": title, "project_id": pid, "status": "ready"}, nil)
	}
	select {
	case res := <-done:
		if res.Reason != "delivered" || !strings.Contains(res.Text, "3 条") {
			t.Fatalf("PM 应整批收到 3 条：%+v", res)
		}
		for _, w := range []string{"导出 CSV", "暗色模式", "修复登录超时"} {
			if !strings.Contains(res.Text, w) {
				t.Fatalf("缺少 %s：%s", w, res.Text)
			}
		}
	case <-time.After(15 * time.Second):
		t.Fatal("PM 没有收到需求")
	}
	var list struct {
		Requirements []store.Requirement `json:"requirements"`
	}
	e.call("GET", "/api/requirements", nil, &list)
	for _, r := range list.Requirements {
		if r.OfferedTo != "claude:pm" {
			t.Fatalf("%s 应排给 PM：%q", r.Code, r.OfferedTo)
		}
	}
}
