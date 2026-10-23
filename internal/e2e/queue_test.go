package e2e

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/client"
	"github.com/NekroAI/kitestring/internal/hub"
	"github.com/NekroAI/kitestring/internal/store"
)

func TestQueueDispatch(t *testing.T) {
	hub.SetKickDelay(300 * time.Millisecond)
	e := setup(t)
	cwd, _ := os.Getwd()
	key := "claude:qu-1"
	ev := func(name string, extra map[string]any) string {
		p := map[string]any{"session_id": "qu-1", "cwd": cwd, "hook_event_name": name}
		for k, v := range extra {
			p[k] = v
		}
		return hook(t, "claude", p)
	}
	prompt := func(turn string) string { return ev("UserPromptSubmit", map[string]any{"prompt": "继续", "prompt_id": turn}) }
	prompt("p0")
	c := client.New(10 * time.Second)
	create := func(title, prio string) store.Requirement {
		var r store.Requirement
		if err := c.Do(context.Background(), "POST", "/agent/requirements", map[string]any{"title": title, "priority": prio, "status": "ready", "session_key": key}, &r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	ready := func(r store.Requirement) {
		e.call("PUT", "/api/requirements/"+r.Code, map[string]any{"title": r.Title, "priority": r.Priority, "status": "ready", "project_id": r.ProjectID}, nil)
	}
	get := func(code string) store.Requirement {
		var r store.Requirement
		e.call("GET", "/api/requirements/"+code, nil, &r)
		return r
	}

	// Agent 记下的需求停在草稿；Agent 也不能自己改成待开始
	a := create("导出 CSV", "P2")
	if a.Status != store.ReqDraft {
		t.Fatalf("Agent 记下的需求应为草稿：%s", a.Status)
	}
	if err := c.Do(context.Background(), "POST", "/agent/requirements/"+a.Code+"/status", map[string]any{"status": "ready", "session_key": key}, nil); err == nil {
		t.Fatal("Agent 不能把需求改为待开始")
	}

	// 你确认（改为待开始）时会话还在运行：不打断，这一轮结束后送达，下次对话时注入
	ready(a)
	time.Sleep(600 * time.Millisecond)
	if get(a.Code).OfferedTo != "" {
		t.Fatal("运行中的会话不应被打断")
	}
	ev("Stop", map[string]any{"last_assistant_message": "好了"})
	e.waitFor("一轮结束后排队送达", func() bool { return get(a.Code).OfferedTo == key })
	if out := prompt("p1"); !strings.Contains(out, "排给你的待开始需求（1 条") || !strings.Contains(out, a.Code+"「导出 CSV」P2") {
		t.Fatalf("下次对话时应注入排队的需求：%s", out)
	}

	// 挂起待命时你连着确认几条：攒成一批，一次叫醒，按优先级排
	b, d := create("修复登录跳转", "P2"), create("首页加载慢", "P1")
	ev("Stop", map[string]any{"last_assistant_message": "好了"})
	e.waitFor("会话停下", func() bool { return e.session(key).State == store.StateWaitReply })
	w := waitOnce(key)
	e.waitFor("登记挂起", func() bool { return e.session(key).Parked })
	ready(b)
	ready(d)
	text := recv(t, w, "整批送达")
	if !strings.Contains(text, "（2 条") || strings.Index(text, d.Code) > strings.Index(text, b.Code) || !strings.Contains(text, "重复的设为 shelved") || !strings.Contains(text, "plan_create") {
		t.Fatalf("应一次送达两条、P1 在前，并说明合并 / 去重 / 依赖的处理方式：%s", text)
	}

	// 一次最多 10 条，其余下次空闲时送
	var many []store.Requirement
	for i := 0; i < 12; i++ {
		r := create(fmt.Sprintf("批量项 %d", i+1), "P3")
		many = append(many, r)
	}
	prompt("p2")
	for _, r := range many {
		ready(r)
	}
	ev("Stop", map[string]any{"last_assistant_message": "好了"})
	offered := func() int {
		n := 0
		for _, r := range many {
			if get(r.Code).OfferedTo != "" {
				n++
			}
		}
		return n
	}
	e.waitFor("送出第一批", func() bool { return offered() > 0 })
	time.Sleep(300 * time.Millisecond)
	if n := offered(); n != 10 {
		t.Fatalf("一次最多送 10 条，实际 %d", n)
	}
	prompt("p3")
	ev("Stop", map[string]any{"last_assistant_message": "好了"})
	e.waitFor("剩下的下次空闲时送达", func() bool { return offered() == 12 })

	// 开始做（状态变了）就不再算已交出；再回到待开始会重新排队
	if err := c.Do(context.Background(), "POST", "/agent/sessions/"+key+"/link", map[string]any{"requirement": a.Code}, nil); err != nil {
		t.Fatal(err)
	}
	if r := get(a.Code); r.Status != store.ReqInProgress || r.OfferedTo != "" {
		t.Fatalf("开始做后应清掉交出记录：%s %q", r.Status, r.OfferedTo)
	}

	// 项目关掉自动送达：不再排队送
	var projs struct {
		Projects []store.Project `json:"projects"`
	}
	e.call("GET", "/api/projects", nil, &projs)
	p := projs.Projects[0]
	off := false
	e.call("PUT", "/api/projects/"+p.ID, map[string]any{"name": p.Name, "doc_root": p.DocRoot, "auto_dispatch": off}, nil)
	x := create("关掉后的需求", "P2")
	ready(x)
	prompt("p4")
	ev("Stop", map[string]any{"last_assistant_message": "好了"})
	e.waitFor("会话停下", func() bool { return e.session(key).State == store.StateWaitReply })
	time.Sleep(600 * time.Millisecond)
	if get(x.Code).OfferedTo != "" {
		t.Fatal("关掉自动送达后不应排队送达")
	}
	on := true
	e.call("PUT", "/api/projects/"+p.ID, map[string]any{"name": p.Name, "doc_root": p.DocRoot, "auto_dispatch": on}, nil)

	// 会话结束：没人接手，不送
	ev("SessionEnd", map[string]any{"reason": "exit"})
	e.waitFor("会话结束", func() bool { return e.session(key).State == store.StateEnded })
	y := create("结束后的需求", "P2")
	ready(y)
	time.Sleep(600 * time.Millisecond)
	if get(y.Code).OfferedTo != "" {
		t.Fatal("会话已结束时不应送达")
	}
}
