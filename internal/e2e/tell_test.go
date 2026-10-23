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

// parkWait 让会话挂上 ks wait，返回结果通道。
func parkWait(t *testing.T, e *env, c *client.Client, key string) chan daemon.WaitResult {
	t.Helper()
	done := make(chan daemon.WaitResult, 1)
	go func() {
		var res daemon.WaitResult
		_ = c.Do(context.Background(), "GET", "/wait?session_key="+key+"&timeout=20s", nil, &res)
		done <- res
	}()
	e.waitFor(key+" 挂起", func() bool { return e.session(key).Parked })
	return done
}

func woke(t *testing.T, done chan daemon.WaitResult, want ...string) string {
	t.Helper()
	select {
	case res := <-done:
		if res.Reason != "delivered" {
			t.Fatalf("应被消息唤醒：%+v", res)
		}
		for _, w := range want {
			if !strings.Contains(res.Text, w) {
				t.Fatalf("唤醒内容应包含 %q：%s", w, res.Text)
			}
		}
		return res.Text
	case <-time.After(10 * time.Second):
		t.Fatal("没有被唤醒")
	}
	return ""
}

// pendingCount 是用户收件箱里待决策的条数。
func pendingCount(e *env) int {
	var out struct {
		Decisions []store.Decision `json:"decisions"`
	}
	e.call("GET", "/api/decisions", nil, &out)
	n := 0
	for _, d := range out.Decisions {
		if d.Status == store.DecPending {
			n++
		}
	}
	return n
}

// 团队消息（R-135）：开发把 PM 派的需求设为待验收，挂着 ks wait 的 PM 立刻被唤醒，收到结论和达标数；
// 开发用 ks team tell 给 PM 留言，PM 收到；用户收件箱不多出决策；没有 PM 的项目不报错。
func TestTellPM(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	c := client.New(30 * time.Second)
	ctx := context.Background()
	for _, sid := range []string{"pm", "dev"} {
		hook(t, "claude", map[string]any{"session_id": sid, "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "上线", "prompt_id": "p0"})
	}
	e.waitFor("会话入库", func() bool { return e.session("claude:dev").Key != "" })

	// 还没有 PM：交验收、留言都不报错，也不送
	var r0 store.Requirement
	if err := c.Do(ctx, "POST", "/agent/requirements", map[string]any{"title": "没 PM 的需求", "session_key": "claude:dev"}, &r0); err != nil {
		t.Fatal(err)
	}
	if err := c.Do(ctx, "POST", "/agent/requirements/"+r0.Code+"/status", map[string]any{"status": "review", "session_key": "claude:dev"}, nil); err != nil {
		t.Fatalf("没有 PM 时交验收不应报错：%v", err)
	}
	var tell struct {
		Delivered bool   `json:"delivered"`
		Message   string `json:"message"`
	}
	if err := c.Do(ctx, "POST", "/agent/team/tell", map[string]any{"session_key": "claude:dev", "text": "在吗"}, &tell); err != nil {
		t.Fatalf("没有 PM 时留言不应报错：%v", err)
	}
	if tell.Delivered || !strings.Contains(tell.Message, "没有 PM") {
		t.Fatalf("没有 PM 时应说明没送出：%+v", tell)
	}

	for k, r := range map[string]string{"claude:pm": "pm", "claude:dev": "dev"} {
		if err := c.Do(ctx, "POST", "/agent/sessions/role", map[string]any{"session_key": k, "role": r}, nil); err != nil {
			t.Fatal(err)
		}
	}
	var r store.Requirement
	if err := c.Do(ctx, "POST", "/agent/requirements", map[string]any{"title": "导出 CSV", "session_key": "claude:pm"}, &r); err != nil {
		t.Fatal(err)
	}
	if err := c.Do(ctx, "POST", "/agent/requirements/"+r.Code+"/assign", map[string]any{"session_key": "claude:pm", "to": "claude:dev"}, nil); err != nil {
		t.Fatal(err)
	}
	c.Do(ctx, "POST", "/agent/sessions/claude:dev/link", map[string]any{"requirement": r.Code}, nil)
	if err := c.Do(ctx, "POST", "/agent/requirements/"+r.Code+"/evidence", map[string]any{"kind": "test", "session_key": "claude:dev", "verdict": "列表页能导出 CSV",
		"blocks": []any{map[string]any{"type": "checklist", "items": []any{
			map[string]any{"criterion": 1, "text": "能导出", "result": "pass"},
			map[string]any{"criterion": 2, "text": "表头冻结", "result": "pass"},
			map[string]any{"criterion": 3, "text": "大文件", "result": "ask"},
		}}}}, nil); err != nil {
		t.Fatal(err)
	}
	before := pendingCount(e)

	// PM 挂着 ks wait；开发交验收 → PM 被唤醒，看到结论和达标数
	done := parkWait(t, e, c, "claude:pm")
	if err := c.Do(ctx, "POST", "/agent/requirements/"+r.Code+"/status", map[string]any{"status": "review", "session_key": "claude:dev"}, nil); err != nil {
		t.Fatal(err)
	}
	text := woke(t, done, "团队消息", r.Code, "已交验收", "列表页能导出 CSV", "达标 2/3")
	if strings.Contains(text, "用户已对你之前提出的决策") {
		t.Fatalf("团队消息不应写成用户答复：%s", text)
	}
	if n := pendingCount(e); n != before {
		t.Fatalf("用户收件箱不应多出决策：%d → %d", before, n)
	}

	// 开发给 PM 留言 → PM 被唤醒；用户收不到
	done = parkWait(t, e, c, "claude:pm")
	if err := c.Do(ctx, "POST", "/agent/team/tell", map[string]any{"session_key": "claude:dev", "text": "接口文档没写分页，先按 50 条做了", "requirement": r.Code}, &tell); err != nil {
		t.Fatal(err)
	}
	if !tell.Delivered {
		t.Fatalf("应送给 PM：%+v", tell)
	}
	woke(t, done, "团队消息", "Dev", "claude:dev", r.Code, "先按 50 条做了")
	if n := pendingCount(e); n != before {
		t.Fatalf("留言不应进用户收件箱：%d → %d", before, n)
	}
	var decs struct {
		Decisions []store.Decision `json:"decisions"`
	}
	e.call("GET", "/api/decisions", nil, &decs)
	tells := 0
	for _, d := range decs.Decisions {
		if d.Kind == store.KindTell {
			tells++
			if d.SessionKey != "claude:pm" {
				t.Fatalf("团队消息只送给 PM：%+v", d)
			}
		}
	}
	if tells != 2 {
		t.Fatalf("应有 2 条团队消息，实际 %d", tells)
	}

	// PM 自己交验收不给自己发
	var r2 store.Requirement
	c.Do(ctx, "POST", "/agent/requirements", map[string]any{"title": "PM 自己做的", "session_key": "claude:pm"}, &r2)
	c.Do(ctx, "POST", "/agent/requirements/"+r2.Code+"/status", map[string]any{"status": "review", "session_key": "claude:pm"}, nil)
	e.call("GET", "/api/decisions", nil, &decs)
	for _, d := range decs.Decisions {
		if d.Kind == store.KindTell && d.RequirementID == r2.ID {
			t.Fatal("PM 自己交验收不应给自己发团队消息")
		}
	}
}
