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
	e.waitFor(key+" 挂起", func() bool { return len(done) > 0 || e.session(key).Parked }) // 已有消息时会立刻返回
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

	for _, kr := range [][2]string{{"claude:pm", "pm"}, {"claude:dev", "dev"}} {
		if err := c.Do(ctx, "POST", "/agent/sessions/role", map[string]any{"session_key": kr[0], "role": kr[1]}, nil); err != nil {
			t.Fatal(err)
		}
	}
	woke(t, parkWait(t, e, c, "claude:pm"), "已上线待命") // 先收掉开发上线的消息（R-116）
	var r store.Requirement
	if err := c.Do(ctx, "POST", "/agent/requirements", map[string]any{"title": "导出 CSV", "session_key": "claude:pm"}, &r); err != nil {
		t.Fatal(err)
	}
	e.allowRelease("claude:pm")
	if err := c.Do(ctx, "POST", "/agent/requirements/"+r.Code+"/assign", map[string]any{"session_key": "claude:pm", "reason": "做法明确", "to": "claude:dev"}, nil); err != nil {
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
		if d.Kind == store.KindTell && d.RequirementID != "" {
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

// tellCount 是送给 key 的团队消息条数。
func tellCount(e *env, key string) int {
	var out struct {
		Decisions []store.Decision `json:"decisions"`
	}
	e.call("GET", "/api/decisions", nil, &out)
	n := 0
	for _, d := range out.Decisions {
		if d.Kind == store.KindTell && d.SessionKey == key {
			n++
		}
	}
	return n
}

// 开发上线通知 PM（R-116）：PM 挂着 ks wait 时，新开发 ks role dev，PM 几秒内被唤醒；
// 同一次在线再挂 ks wait 不重复报；need_devs 提醒因开发够数被清除时一并告诉 PM；
// 开发离线后重新上线、第一次挂上 ks wait 时再报一次。
func TestDevOnlineTellsPM(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	c := client.New(30 * time.Second)
	ctx := context.Background()
	for _, sid := range []string{"pm", "dev"} {
		hook(t, "claude", map[string]any{"session_id": sid, "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "上线", "prompt_id": "p0"})
	}
	e.waitFor("会话入库", func() bool { return e.session("claude:dev").Key != "" })
	if err := c.Do(ctx, "POST", "/agent/sessions/role", map[string]any{"session_key": "claude:pm", "role": "pm"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.Do(ctx, "POST", "/agent/team/need", map[string]any{"session_key": "claude:pm", "count": 1, "reason": "R-1 等开发接手"}, nil); err != nil {
		t.Fatal(err)
	}
	before := pendingCount(e)

	// PM 挂着 ks wait；开发声明身份 → PM 被唤醒：开发上线 + 提醒已清除
	done := parkWait(t, e, c, "claude:pm")
	if err := c.Do(ctx, "POST", "/agent/sessions/role", map[string]any{"session_key": "claude:dev", "role": "dev"}, nil); err != nil {
		t.Fatal(err)
	}
	woke(t, done, "团队消息", "claude:dev", "已上线待命", "req_assign", "need_devs 要的 1 个，提醒已清除")
	if n := pendingCount(e); n != before {
		t.Fatalf("上线通知不应进用户收件箱：%d → %d", before, n)
	}

	// 同一次在线，开发挂上 ks wait：不重复报
	parkWait(t, e, c, "claude:dev")
	time.Sleep(300 * time.Millisecond)
	if n := tellCount(e, "claude:pm"); n != 1 {
		t.Fatalf("同一次在线只报一次，实际 %d 条", n)
	}

	// 开发离线后重新上线：第一次挂上 ks wait 时再报
	hook(t, "claude", map[string]any{"session_id": "dev", "cwd": cwd, "hook_event_name": "SessionEnd"})
	e.waitFor("开发离线", func() bool { return e.session("claude:dev").State == store.StateEnded })
	hook(t, "claude", map[string]any{"session_id": "dev", "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "继续", "prompt_id": "p1"})
	e.waitFor("开发回来", func() bool { return e.session("claude:dev").State == store.StateRunning })
	done = parkWait(t, e, c, "claude:pm")
	parkWait(t, e, c, "claude:dev")
	woke(t, done, "已上线待命", "claude:dev")
	if n := tellCount(e, "claude:pm"); n != 2 {
		t.Fatalf("重新上线应再报一次，实际 %d 条", n)
	}
}
