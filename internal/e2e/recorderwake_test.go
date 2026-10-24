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

// 记下需求的主助理 / 顾问不因用户改状态被唤醒（R-198）：它们记需求时自动关联，但不是执行方。
// 没订阅 status 的 ks wait 不退出，状态通知照旧入队，下次因别的消息唤醒时一并带上；订阅了 status 的照常唤醒、只收一条；
// 状态变化现在整体只告知（R-227）：执行方 Dev、派活的 PM 也不被唤醒，只有需求被撤回草稿 / 搁置（要停手）才叫醒 Dev。
func TestRecorderNotWokenByStatus(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	c := client.New(30 * time.Second)
	ctx := context.Background()
	for _, sid := range []string{"pm", "aide", "adv", "dev"} {
		hook(t, "claude", map[string]any{"session_id": sid, "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "上线", "prompt_id": "p0"})
	}
	e.waitFor("会话入库", func() bool { return e.session("claude:dev").ProjectID != "" })
	for _, kr := range [][2]string{{"claude:pm", "pm"}, {"claude:aide", "aide"}, {"claude:adv", "advisor"}, {"claude:dev", "dev"}} {
		if err := c.Do(ctx, "POST", "/agent/sessions/role", map[string]any{"session_key": kr[0], "role": kr[1]}, nil); err != nil {
			t.Fatal(err)
		}
	}
	woke(t, parkWait(t, e, c, "claude:pm"), "已上线待命")
	project := e.session("claude:dev").ProjectID

	// 主助理、顾问各记一条需求（自动关联），开发关联上去做
	record := func(by, title string) store.Requirement {
		t.Helper()
		var r store.Requirement
		if err := c.Do(ctx, "POST", "/agent/requirements", map[string]any{"title": title, "session_key": by}, &r); err != nil {
			t.Fatal(err)
		}
		if err := c.Do(ctx, "POST", "/agent/sessions/claude:dev/link", map[string]any{"requirement": r.Code}, nil); err != nil {
			t.Fatal(err)
		}
		var got store.Requirement
		e.call("GET", "/api/requirements/"+r.Code, nil, &got)
		if len(got.Sessions) < 2 {
			t.Fatalf("%s 和开发都应已关联：%+v", by, got.Sessions)
		}
		return got
	}
	ra := record("claude:aide", "导出 CSV")
	rb := record("claude:adv", "导出 Excel")
	setStatus := func(r store.Requirement, st string) {
		t.Helper()
		e.call("PUT", "/api/requirements/"+r.Code, map[string]any{"title": r.Title, "status": st, "project_id": project}, nil)
	}

	// 1. 主助理挂着不含 status 的订阅：自己记下的需求被改状态（验收为已完成）不唤醒，消息照旧入队；Dev、PM 照旧被唤醒
	aideW := parkWaitOn(t, e, c, "claude:aide", "decision,review,blocked,stage")
	devW, pmW := parkWait(t, e, c, "claude:dev"), parkWait(t, e, c, "claude:pm")
	aideTells := tellCount(e, "claude:aide")
	setStatus(ra, "done")
	drainQuiet(t, devW, "Dev（状态变化只告知）", "dev", "你关联的需求有变化", ra.Code, "改为已完成")
	drainQuiet(t, pmW, "PM（状态变化只告知）", "pm", ra.Code, "改为已完成")
	asleep(t, aideW, "主助理（没订阅 status）")
	if n := tellCount(e, "claude:aide"); n != aideTells+1 {
		t.Fatalf("主助理的状态通知应照旧入队一条：%d → %d", aideTells, n)
	}

	// 2. 顾问（不订阅事件）同理：不唤醒
	advW := parkWait(t, e, c, "claude:adv")
	devW = parkWait(t, e, c, "claude:dev")
	setStatus(rb, "shelved")
	woke(t, devW, rb.Code, "改为已搁置")
	asleep(t, advW, "顾问（没订阅 status）")

	// 3. 主助理因订阅的事件（开发提决策）被唤醒时，入队的状态通知一并带上
	if err := c.Do(ctx, "POST", "/agent/decisions", map[string]any{"session_key": "claude:dev", "question": "按钮放哪", "requirement": rb.Code}, nil); err != nil {
		t.Fatal(err)
	}
	woke(t, aideW, "按钮放哪", ra.Code, "改为已完成")
	asleep(t, advW, "顾问（没订阅 decision）")

	// 4. 顾问下一轮开始时（用户发消息），入队的状态通知随注入带上
	out := hook(t, "claude", map[string]any{"session_id": "adv", "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "继续", "prompt_id": "p1"})
	for _, w := range []string{"你关联的需求有变化", rb.Code, "改为已搁置"} {
		if !strings.Contains(out, w) {
			t.Fatalf("顾问下一轮的注入应包含 %q：%s", w, out)
		}
	}

	// 5. 主助理订阅了 status：照常被唤醒，只收一条（不因关联再多一条）
	aideW = parkWaitOn(t, e, c, "claude:aide", "decision,review,blocked,stage,status")
	aideTells = tellCount(e, "claude:aide")
	setStatus(ra, "in_progress")
	drainQuiet(t, aideW, "主助理（状态变化只告知）", "aide", "订阅的事件", "status", ra.Code, "改为进行中")
	time.Sleep(200 * time.Millisecond)
	if n := tellCount(e, "claude:aide"); n != aideTells+1 {
		t.Fatalf("订阅了 status 的主助理只应收到一条：%d → %d", aideTells, n)
	}
}
