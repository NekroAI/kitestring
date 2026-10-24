package e2e

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/client"
)

// 团队消息分两档（R-227）：要对方动手的立刻叫醒；标明「只告知」的（fyi，部署 / 重启通知这类）进待送队列，
// 不结束 ks wait，等下次因要动手的事被叫醒、或用户发消息时一并附上，不丢。
func TestFYITellDoesNotWake(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	c := client.New(30 * time.Second)
	ctx := context.Background()
	for _, sid := range []string{"aide", "pm", "dev"} {
		hook(t, "claude", map[string]any{"session_id": sid, "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "上线", "prompt_id": "p0"})
	}
	e.waitFor("会话入库", func() bool { return e.session("claude:dev").ProjectID != "" })
	for _, kr := range [][2]string{{"claude:aide", "aide"}, {"claude:pm", "pm"}, {"claude:dev", "dev"}} {
		if err := c.Do(ctx, "POST", "/agent/sessions/role", map[string]any{"session_key": kr[0], "role": kr[1]}, nil); err != nil {
			t.Fatal(err)
		}
	}
	woke(t, parkWait(t, e, c, "claude:pm"), "已上线待命")
	tell := func(from string, body map[string]any) string {
		t.Helper()
		body["session_key"] = from
		var out struct {
			Delivered bool   `json:"delivered"`
			Message   string `json:"message"`
		}
		if err := c.Do(ctx, "POST", "/agent/team/tell", body, &out); err != nil || !out.Delivered {
			t.Fatalf("发团队消息失败：%+v %v", out, err)
		}
		return out.Message
	}

	// 1. 只告知：主助理挂着 ks wait，不被叫醒；返回说明写明不唤醒
	aideW := parkWait(t, e, c, "claude:aide")
	msg := tell("claude:pm", map[string]any{"to": "aide", "text": "已部署 v1.2，daemon 已重启", "fyi": true})
	if !strings.Contains(msg, "只告知") || !strings.Contains(msg, "不唤醒") {
		t.Fatalf("返回应说明按只告知送出、不唤醒：%s", msg)
	}
	tell("claude:dev", map[string]any{"to": "aide", "text": "R-9 进展：接口已通", "fyi": true})
	asleep(t, aideW, "主助理（只告知）")

	// 2. 下次因要动手的事被叫醒（开发求裁定，默认档）：只告知的一起附上，分开标注，不丢
	tell("claude:dev", map[string]any{"to": "aide", "text": "分页大小 20 还是 50？写清选项和倾向"})
	text := woke(t, aideW, "分页大小 20 还是 50", "只告知", "已部署 v1.2", "R-9 进展")
	if i, j := strings.Index(text, "分页大小"), strings.Index(text, "只告知"); i < 0 || j < i {
		t.Fatalf("要动手的在前，只告知的单列在后：%s", text)
	}

	// 3. 用户发消息（下一轮开始）时一并附上，且只送一次
	aideW = parkWait(t, e, c, "claude:aide")
	tell("claude:pm", map[string]any{"to": "aide", "text": "重启完成，一切正常", "fyi": true})
	asleep(t, aideW, "主助理（只告知）")
	out := hook(t, "claude", map[string]any{"session_id": "aide", "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "最近怎么样", "prompt_id": "p1"})
	if !strings.Contains(out, "重启完成，一切正常") || !strings.Contains(out, "只告知") {
		t.Fatalf("用户发消息时应附上攒着的只告知消息：%s", out)
	}
	out = hook(t, "claude", map[string]any{"session_id": "aide", "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "继续", "prompt_id": "p2"})
	if strings.Contains(out, "重启完成") {
		t.Fatalf("送达过的不再补发：%s", out)
	}

	// 4. 默认档：不标 fyi 照常立刻叫醒
	aideW = parkWait(t, e, c, "claude:aide")
	tell("claude:pm", map[string]any{"to": "aide", "text": "请裁定：按钮放左还是右"})
	woke(t, aideW, "请裁定：按钮放左还是右")
	if strings.Contains(tell("claude:pm", map[string]any{"to": "aide", "text": "知道了", "fyi": true}), "立刻收到") {
		t.Fatal("只告知的返回不应写「立刻收到」")
	}
}

// 订阅事件同样分档（R-227）：交验收、有人提决策、受阻要动手，立刻叫醒；用户改需求状态、改设置只是告知，攒着不叫醒。
func TestEventTiers(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	c := client.New(30 * time.Second)
	ctx := context.Background()
	for _, sid := range []string{"aide", "dev"} {
		hook(t, "claude", map[string]any{"session_id": sid, "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "上线", "prompt_id": "p0"})
	}
	e.waitFor("会话入库", func() bool { return e.session("claude:dev").ProjectID != "" })
	for _, kr := range [][2]string{{"claude:aide", "aide"}, {"claude:dev", "dev"}} {
		if err := c.Do(ctx, "POST", "/agent/sessions/role", map[string]any{"session_key": kr[0], "role": kr[1]}, nil); err != nil {
			t.Fatal(err)
		}
	}
	project := e.session("claude:dev").ProjectID
	var r struct{ Code, Title string }
	if err := c.Do(ctx, "POST", "/agent/requirements", map[string]any{"title": "导出 CSV", "session_key": "claude:dev"}, &r); err != nil {
		t.Fatal(err)
	}
	// 用户改状态、改设置：主助理订阅了 status / settings 也不叫醒
	aideW := parkWaitOn(t, e, c, "claude:aide", "decision,review,settings,status")
	e.call("PUT", "/api/requirements/"+r.Code, map[string]any{"title": r.Title, "status": "ready", "project_id": project}, nil)
	asleep(t, aideW, "主助理（状态变化只告知）")
	// 有人提决策（要查冲突）：照常叫醒，攒着的状态变化一并附上
	if err := c.Do(ctx, "POST", "/agent/decisions", map[string]any{"session_key": "claude:dev", "question": "按钮放哪", "requirement": r.Code}, nil); err != nil {
		t.Fatal(err)
	}
	woke(t, aideW, "按钮放哪", "只告知", r.Code, "改为待开始")
	// 交验收：要写建议，照常叫醒
	aideW = parkWaitOn(t, e, c, "claude:aide", "decision,review,settings,status")
	if err := c.Do(ctx, "POST", "/agent/requirements/"+r.Code+"/status", map[string]any{"status": "review", "session_key": "claude:dev"}, nil); err != nil {
		t.Fatal(err)
	}
	woke(t, aideW, "已交验收", r.Code)
}
