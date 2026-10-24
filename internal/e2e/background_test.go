package e2e

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/protocol"
	"github.com/NekroAI/kitestring/internal/store"
)

const ambientPrompt = "# Overview\n\nGenerate 0 to 3 hyperpersonalized suggestions for what this user can do with Codex in this local project: /Users/miose/Documents/Obsidian Vault\n\n\nGet an understanding of the user's intent …"

// R-179：Codex 桌面版在后台自动跑的「项目建议」任务，不当成用户会话——
// 不在默认的会话列表里（工作台的泳道、最近结束、各种计数都从它来），只能按「后台任务」找到；
// 同一个项目里用户自己的对话（检查空间占用）不受影响。
func TestBackgroundTasksFiltered(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	ev := func(sid, name string, extra map[string]any) {
		p := map[string]any{"session_id": sid, "cwd": cwd, "hook_event_name": name, "client": "app-server"}
		for k, v := range extra {
			p[k] = v
		}
		hook(t, "codex", p)
	}
	ev("amb-1", "SessionStart", nil)
	ev("amb-1", "UserPromptSubmit", map[string]any{"prompt": ambientPrompt, "turn_id": "t0"})
	ev("amb-1", "Stop", map[string]any{"turn_id": "t0", "last_assistant_message": `{"suggestions": []}`})
	ev("usr-1", "SessionStart", nil)
	ev("usr-1", "UserPromptSubmit", map[string]any{"prompt": "检查一下当前设备的空间占用情况", "turn_id": "t1"})
	e.waitFor("两个会话都入库", func() bool {
		a, _ := e.store.GetSession(context.Background(), "codex:amb-1")
		u, _ := e.store.GetSession(context.Background(), "codex:usr-1")
		return a.Turns == 1 && u.Turns == 1
	})

	list := func(query string) []store.Session {
		var out struct {
			Sessions []store.Session `json:"sessions"`
		}
		e.call("GET", "/api/sessions"+query, nil, &out)
		return out.Sessions
	}
	has := func(ss []store.Session, key string) bool {
		for _, s := range ss {
			if s.Key == key {
				return true
			}
		}
		return false
	}

	main := list("")
	if has(main, "codex:amb-1") || !has(main, "codex:usr-1") || len(main) != 1 {
		t.Fatalf("默认列表应只有用户自己的对话（会话数 1），得到 %d 个：%+v", len(main), main)
	}
	if main[0].Background {
		t.Fatal("用户自己的对话不应标为后台任务")
	}
	bg := list("?background=true")
	if len(bg) != 1 || bg[0].Key != "codex:amb-1" || !bg[0].Background {
		t.Fatalf("按后台任务筛选应只有项目建议任务，得到 %+v", bg)
	}

	// 后台任务没有身份，派活 / 自动送达（只找有身份的会话）里不会出现
	if rs, err := e.store.RoleSessions(context.Background(), bg[0].ProjectID); err != nil || len(rs) != 0 {
		t.Fatalf("后台任务不应参与派活：%v %v", rs, err)
	}
	// 详情仍能打开
	var d struct {
		Session store.Session `json:"session"`
	}
	e.call("GET", "/api/sessions/codex:amb-1", nil, &d)
	if !d.Session.Background {
		t.Fatal("详情里应带后台任务标记")
	}
}

// R-179：旧数据在启动时按同样的规则补标——看第一条用户提示词，不是最后一条。
func TestBackfillBackground(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	dev, err := e.store.ListDevices(ctx)
	if err != nil || len(dev) == 0 {
		t.Fatal(err)
	}
	t0 := time.Date(2026, 10, 24, 7, 25, 0, 0, time.UTC)
	old := func(sid, first, last string) {
		x := store.Session{Key: "codex:" + sid, Tool: "codex", SessionID: sid, DeviceID: dev[0].ID, State: store.StateEnded, LastPrompt: last, Turns: 1, StartedAt: t0, LastActiveAt: t0}
		if err := e.store.SaveSession(ctx, x); err != nil {
			t.Fatal(err)
		}
		if _, err := e.store.InsertEvent(ctx, dev[0].ID, protocol.Event{ID: "ev-" + sid, Tool: "codex", SessionID: sid, Name: "UserPromptSubmit", Prompt: first, Time: t0}); err != nil {
			t.Fatal(err)
		}
	}
	old("old-amb", ambientPrompt, ambientPrompt)
	old("old-usr", "检查一下当前设备的空间占用情况", "深度分析看看给我清理建议")
	n, err := e.store.BackfillBackground(ctx, protocol.IsBackgroundPrompt)
	if err != nil || n != 1 {
		t.Fatalf("应补标 1 个，得到 %d：%v", n, err)
	}
	a, _ := e.store.GetSession(ctx, "codex:old-amb")
	u, _ := e.store.GetSession(ctx, "codex:old-usr")
	if !a.Background || u.Background {
		t.Fatalf("补标结果不对：amb=%v usr=%v", a.Background, u.Background)
	}
}
