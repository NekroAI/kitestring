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

// tellsTo 列出发给 key 的团队消息（含只告知的）。
func tellsTo(e *env, key string) []store.Decision {
	var out struct {
		Decisions []store.Decision `json:"decisions"`
	}
	e.call("GET", "/api/decisions", nil, &out)
	var got []store.Decision
	for _, d := range out.Decisions {
		if d.Kind == store.KindTell && d.SessionKey == key {
			got = append(got, d)
		}
	}
	return got
}

// R-229：开发会话待命超过项目设置的时长、手上没有排给它的需求 → 记下待命超时、告诉主调度（只告知），之后订阅事件不再送；
// 时长按项目设置；点名给它的（派活）照常叫醒并清除待命超时；有排给它的需求时巡查也清除。
func TestDevIdle(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	c := client.New(30 * time.Second)
	ctx := context.Background()
	for _, sid := range []string{"idle-pm", "idle-dev", "idle-x"} {
		hook(t, "claude", map[string]any{"session_id": sid, "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "上线", "prompt_id": "p0"})
	}
	e.waitFor("会话入库", func() bool { return e.session("claude:idle-x").ProjectID != "" })
	for _, kr := range [][2]string{{"claude:idle-pm", "pm"}, {"claude:idle-dev", "dev"}} {
		if err := c.Do(ctx, "POST", "/agent/sessions/role", map[string]any{"session_key": kr[0], "role": kr[1]}, nil); err != nil {
			t.Fatal(err)
		}
	}
	pid := e.session("claude:idle-dev").ProjectID

	// 开发订阅 decision 后挂起待命（一轮结束：Stop 后进入待命）
	park := func() chan struct{ Reason, Text string } {
		t.Helper()
		ch := make(chan struct{ Reason, Text string }, 1)
		w := parkWaitOn(t, e, c, "claude:idle-dev", "decision")
		go func() { r := <-w; ch <- struct{ Reason, Text string }{r.Reason, r.Text} }()
		hook(t, "claude", map[string]any{"session_id": "idle-dev", "cwd": cwd, "hook_event_name": "Stop"})
		e.waitFor("开发进入待命", func() bool { return e.session("claude:idle-dev").State == store.StateParked })
		return ch
	}
	devW := park()
	raise := func(q string) store.Decision {
		t.Helper()
		var d store.Decision
		if err := c.Do(ctx, "POST", "/agent/decisions", map[string]any{"session_key": "claude:idle-x", "question": q, "options": []string{"甲", "乙"}}, &d); err != nil {
			t.Fatal(err)
		}
		return d
	}
	// 对照：没超时时，订阅的 decision 事件照常叫醒它
	raise("没超时时提的决策")
	select {
	case r := <-devW:
		if r.Reason != "delivered" || !strings.Contains(r.Text, "没超时时提的决策") {
			t.Fatalf("没超时时应收到订阅事件：%+v", r)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("没超时时订阅事件应叫醒开发")
	}
	devW = park()

	// 项目设置改成 60 分钟：31 分钟时不算超时，61 分钟时算
	p, err := e.store.GetProject(ctx, pid)
	if err != nil {
		t.Fatal(err)
	}
	e.call("PUT", "/api/projects/"+pid, map[string]any{"name": p.Name, "doc_root": p.DocRoot, "dev_idle_minutes": 60}, nil)
	if p, _ = e.store.GetProject(ctx, pid); p.DevIdleMinutes != 60 {
		t.Fatalf("项目设置应存下 60 分钟：%d", p.DevIdleMinutes)
	}
	pmBefore := len(tellsTo(e, "claude:idle-pm"))
	e.hub.DevIdleSweep(ctx, time.Now().Add(31*time.Minute))
	if e.session("claude:idle-dev").IdleAt != nil {
		t.Fatal("没到项目设置的 60 分钟不应算待命超时")
	}
	e.hub.DevIdleSweep(ctx, time.Now().Add(61*time.Minute))
	if e.session("claude:idle-dev").IdleAt == nil {
		t.Fatal("待命超过 60 分钟、没有排给它的需求，应记下待命超时")
	}
	tells := tellsTo(e, "claude:idle-pm")
	if len(tells) != pmBefore+1 || !strings.Contains(tells[0].Answer+tells[len(tells)-1].Answer, "泳道上标了「待命超时」") {
		t.Fatalf("主调度应收到一条待命超时的说明：%+v", tells)
	}
	for _, d := range tells {
		if strings.Contains(d.Answer, "待命超时") && d.Question != "团队消息（不唤醒）" {
			t.Fatalf("给主调度的待命超时说明应是只告知：%+v", d)
		}
	}
	e.hub.DevIdleSweep(ctx, time.Now().Add(120*time.Minute))
	if n := len(tellsTo(e, "claude:idle-pm")); n != pmBefore+1 {
		t.Fatalf("同一次待命超时只告诉主调度一次：%d", n)
	}

	// 待命超时后：订阅的 decision 事件不再送（不叫醒、也不入队）
	devTells := len(tellsTo(e, "claude:idle-dev"))
	raise("超时后提的决策")
	select {
	case r := <-devW:
		t.Fatalf("待命超时的开发不应被订阅事件叫醒：%+v", r)
	case <-time.After(800 * time.Millisecond):
	}
	if n := len(tellsTo(e, "claude:idle-dev")); n != devTells {
		t.Fatalf("待命超时的开发不应再收到订阅事件：%d → %d", devTells, n)
	}

	// 派活：照常叫醒，并清除待命超时
	var r store.Requirement
	if err := c.Do(ctx, "POST", "/agent/requirements", map[string]any{"title": "派给待命开发的活", "session_key": "claude:idle-pm"}, &r); err != nil {
		t.Fatal(err)
	}
	e.call("PUT", "/api/requirements/"+r.Code, map[string]any{"title": r.Title, "status": "ready", "project_id": r.ProjectID}, nil)
	if err := c.Do(ctx, "POST", "/agent/requirements/"+r.Code+"/assign", map[string]any{"session_key": "claude:idle-pm", "to": "claude:idle-dev"}, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-devW:
		if got.Reason != "delivered" || !strings.Contains(got.Text, r.Code) {
			t.Fatalf("派活应叫醒开发：%+v", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("派活应叫醒待命超时的开发")
	}
	if e.session("claude:idle-dev").IdleAt != nil {
		t.Fatal("派活后应清除待命超时")
	}

	// 手上有排给它的需求：再久也不算待命超时
	devW = park()
	e.hub.DevIdleSweep(ctx, time.Now().Add(5*time.Hour))
	if e.session("claude:idle-dev").IdleAt != nil {
		t.Fatal("有排给它的需求时不应算待命超时")
	}
	// 恢复后订阅事件照常送到
	raise("恢复后提的决策")
	select {
	case got := <-devW:
		if got.Reason != "delivered" || !strings.Contains(got.Text, "恢复后提的决策") {
			t.Fatalf("恢复后应收到订阅事件：%+v", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("恢复后订阅事件应叫醒开发")
	}

	// 交了验收（手上不再有排给它的活）后又待命超时：用户答复它提的决策照常叫醒，并清除待命超时
	var mine store.Decision
	if err := c.Do(ctx, "POST", "/agent/decisions", map[string]any{"session_key": "claude:idle-dev", "question": "开发在等的决策", "options": []string{"甲", "乙"}}, &mine); err != nil {
		t.Fatal(err)
	}
	c.Do(ctx, "POST", "/agent/sessions/claude:idle-dev/link", map[string]any{"requirement": r.Code}, nil)
	if err := c.Do(ctx, "POST", "/agent/requirements/"+r.Code+"/status", map[string]any{"status": "review", "session_key": "claude:idle-dev"}, nil); err != nil {
		t.Fatal(err)
	}
	devW = park()
	e.hub.DevIdleSweep(ctx, time.Now().Add(5*time.Hour))
	if e.session("claude:idle-dev").IdleAt == nil {
		t.Fatal("手上只有待验收的需求时应算待命超时")
	}
	e.call("POST", "/api/decisions/answers", map[string]any{"answers": []map[string]any{{"ref": mine.Code, "answer": "甲"}}}, nil)
	select {
	case got := <-devW:
		if got.Reason != "delivered" || !strings.Contains(got.Text, mine.Code) {
			t.Fatalf("用户答复应叫醒提问的开发：%+v", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("用户答复应叫醒待命超时的开发")
	}
	if e.session("claude:idle-dev").IdleAt != nil {
		t.Fatal("用户答复它提的决策后应清除待命超时")
	}

	// 你在终端给它发消息：同样清除
	devW = park()
	e.hub.DevIdleSweep(ctx, time.Now().Add(5*time.Hour))
	if e.session("claude:idle-dev").IdleAt == nil {
		t.Fatal("应再次待命超时")
	}
	hook(t, "claude", map[string]any{"session_id": "idle-dev", "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "还在吗", "prompt_id": "p9"})
	e.waitFor("用户发消息后清除待命超时", func() bool { return e.session("claude:idle-dev").IdleAt == nil })
	_ = devW
}

// R-229：没写交接、直接 ks role 接任主调度时，旧会话同样卸任（与 R-224 交接卸任同一处标记）：
// 收到最后一条卸任通知（列出它提的待答决策），之后 ks wait 直接返回、订阅事件不再送；重新声明身份恢复。
func TestTakeoverRetires(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	c := client.New(30 * time.Second)
	ctx := context.Background()
	for _, sid := range []string{"tr-old", "tr-new", "tr-dev"} {
		hook(t, "claude", map[string]any{"session_id": sid, "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "上线", "prompt_id": "p0"})
	}
	e.waitFor("会话入库", func() bool { return e.session("claude:tr-dev").ProjectID != "" })
	for _, kr := range [][2]string{{"claude:tr-old", "pm"}, {"claude:tr-dev", "dev"}} {
		if err := c.Do(ctx, "POST", "/agent/sessions/role", map[string]any{"session_key": kr[0], "role": kr[1]}, nil); err != nil {
			t.Fatal(err)
		}
	}
	woke(t, parkWait(t, e, c, "claude:tr-old"), "已上线待命")
	var d store.Decision
	if err := c.Do(ctx, "POST", "/agent/decisions", map[string]any{"session_key": "claude:tr-old", "question": "旧主调度在等的决策", "options": []string{"甲", "乙"}}, &d); err != nil {
		t.Fatal(err)
	}

	// 旧主调度显式订阅 blocked（wait_on 存着），挂起；新会话直接 ks role pm（没有交接）
	oldW := parkWaitOn(t, e, c, "claude:tr-old", "blocked,review")
	var out struct {
		Demoted []string `json:"demoted"`
		Handoff bool     `json:"handoff"`
	}
	if err := c.Do(ctx, "POST", "/agent/sessions/role", map[string]any{"session_key": "claude:tr-new", "role": "pm"}, &out); err != nil {
		t.Fatal(err)
	}
	if out.Handoff || len(out.Demoted) != 1 || out.Demoted[0] != "claude:tr-old" {
		t.Fatalf("应直接接任、卸下旧主调度：%+v", out)
	}
	woke(t, oldW, "不要再挂 ks wait", "不会再唤醒你", d.Code, "下次对话时送达", "ks role")
	old := e.session("claude:tr-old")
	if old.Role != "" || old.RetiredAt == nil {
		t.Fatalf("没写交接的接任也应卸任：role=%q retired=%v", old.Role, old.RetiredAt)
	}
	var chk struct {
		Retired bool   `json:"retired"`
		Hint    string `json:"hint"`
	}
	if err := c.Do(ctx, "GET", "/wait/check?session_key=claude:tr-old", nil, &chk); err != nil || !chk.Retired {
		t.Fatalf("卸任的会话 ks wait 应直接返回：%+v %v", chk, err)
	}

	// 卸任后：它存着的订阅（blocked）不再送——开发把需求设为受阻，旧会话收不到
	var r store.Requirement
	if err := c.Do(ctx, "POST", "/agent/requirements", map[string]any{"title": "受阻演示", "session_key": "claude:tr-dev"}, &r); err != nil {
		t.Fatal(err)
	}
	before := len(tellsTo(e, "claude:tr-old"))
	for _, st := range []string{"in_progress", "blocked"} {
		if err := c.Do(ctx, "POST", "/agent/requirements/"+r.Code+"/status", map[string]any{"status": st, "session_key": "claude:tr-dev"}, nil); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(300 * time.Millisecond)
	if n := len(tellsTo(e, "claude:tr-old")); n != before {
		t.Fatalf("卸任的会话不应再收到订阅事件：%d → %d", before, n)
	}
	if n := len(tellsTo(e, "claude:tr-new")); n == 0 {
		t.Fatal("新主调度应收到受阻通知")
	}

	// 重新声明身份：恢复正常
	if err := c.Do(ctx, "POST", "/agent/sessions/role", map[string]any{"session_key": "claude:tr-old", "role": "advisor"}, nil); err != nil {
		t.Fatal(err)
	}
	if e.session("claude:tr-old").RetiredAt != nil {
		t.Fatal("重新声明身份后应清除卸任")
	}
}
