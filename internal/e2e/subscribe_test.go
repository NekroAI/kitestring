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

// parkWaitOn 让会话挂上 ks wait --on on（on 为空时不带 --on）。
func parkWaitOn(t *testing.T, e *env, c *client.Client, key, on string) chan daemon.WaitResult {
	t.Helper()
	if on == "" {
		return parkWait(t, e, c, key)
	}
	done := make(chan daemon.WaitResult, 1)
	go func() {
		var res daemon.WaitResult
		_ = c.Do(context.Background(), "GET", "/wait?session_key="+key+"&timeout=20s&on="+on, nil, &res)
		done <- res
	}()
	e.waitFor(key+" 挂起", func() bool { return len(done) > 0 || e.session(key).Parked })
	return done
}

// asleep：等待中的会话在一小段时间内没有被唤醒。
func asleep(t *testing.T, done chan daemon.WaitResult, who string) {
	t.Helper()
	select {
	case res := <-done:
		t.Fatalf("%s 不应被唤醒：%+v", who, res)
	case <-time.After(500 * time.Millisecond):
	}
}

// drainQuiet（R-227）：断言 done 的 ks wait 没被只告知的消息叫醒，再模拟用户发消息（下一轮开始），
// 取出攒着的只告知消息，并检查其中包含 want；返回注入的全文。
func drainQuiet(t *testing.T, done chan daemon.WaitResult, who, sid string, want ...string) string {
	t.Helper()
	asleep(t, done, who)
	cwd, _ := os.Getwd()
	out := hook(t, "claude", map[string]any{"session_id": sid, "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "继续", "prompt_id": "q-" + time.Now().Format("150405.000000")})
	if !strings.Contains(out, "只告知") {
		t.Fatalf("%s 下一轮开始时应附上只告知的团队消息：%s", who, out)
	}
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Fatalf("%s 下一轮的注入应包含 %q：%s", who, w, out)
		}
	}
	return out
}

// 事件订阅（R-164）：Adv 默认订阅 decision / review，PM 默认订阅 review / blocked；
// 别的会话提决策、交验收、设为受阻时，订阅了的等待中会话被唤醒并收到一句说明；没订阅的（无身份、Dev）不被唤醒；
// 交验收不给已收到派活通知的 PM 重复送；--on 改订阅、不填沿用上次、none 不订阅；不进用户收件箱。
func TestWaitSubscribe(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	c := client.New(30 * time.Second)
	ctx := context.Background()
	for _, sid := range []string{"pm", "adv", "dev", "plain"} {
		hook(t, "claude", map[string]any{"session_id": sid, "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "上线", "prompt_id": "p0"})
	}
	e.waitFor("会话入库", func() bool { return e.session("claude:plain").Key != "" })
	for _, kr := range [][2]string{{"claude:pm", "pm"}, {"claude:adv", "aide"}, {"claude:dev", "dev"}} {
		if err := c.Do(ctx, "POST", "/agent/sessions/role", map[string]any{"session_key": kr[0], "role": kr[1]}, nil); err != nil {
			t.Fatal(err)
		}
	}
	woke(t, parkWait(t, e, c, "claude:pm"), "已上线待命") // 先收掉开发上线的消息（R-116）

	// 开发向用户提决策 → Adv（默认订阅 decision）被唤醒；PM、无身份会话不被唤醒
	pmW, advW, plainW := parkWait(t, e, c, "claude:pm"), parkWait(t, e, c, "claude:adv"), parkWait(t, e, c, "claude:plain")
	before := pendingCount(e)
	var d store.Decision
	if err := c.Do(ctx, "POST", "/agent/decisions", map[string]any{"session_key": "claude:dev", "question": "导出用 CSV 还是 Excel？", "options": []string{"CSV", "Excel"}}, &d); err != nil {
		t.Fatal(err)
	}
	woke(t, advW, "团队消息", "订阅的事件", "decision", d.Code, "导出用 CSV 还是 Excel", "claude:dev", "不是派给你的活")
	asleep(t, pmW, "PM（没订阅 decision）")
	asleep(t, plainW, "无身份会话")
	if n := pendingCount(e); n != before+1 {
		t.Fatalf("用户收件箱只应多出开发提的那条决策：%d → %d", before, n)
	}

	// 开发把需求设为受阻 → PM（默认订阅 blocked）被唤醒；Adv 不被唤醒
	var r store.Requirement
	if err := c.Do(ctx, "POST", "/agent/requirements", map[string]any{"title": "导出 CSV", "session_key": "claude:dev"}, &r); err != nil {
		t.Fatal(err)
	}
	advW = parkWait(t, e, c, "claude:adv")
	advTells := tellCount(e, "claude:adv")
	if err := c.Do(ctx, "POST", "/agent/requirements/"+r.Code+"/status", map[string]any{"status": "blocked", "session_key": "claude:dev"}, nil); err != nil {
		t.Fatal(err)
	}
	woke(t, pmW, "订阅的事件", "blocked", r.Code, "受阻", "claude:dev")
	asleep(t, advW, "Adv（没订阅 blocked）")
	if n := tellCount(e, "claude:adv"); n != advTells {
		t.Fatalf("Adv 不应收到受阻消息：%d → %d", advTells, n)
	}

	// 开发交验收 → PM 只收到一条（派活通知，不重复），Adv 收到订阅消息；无身份会话不被唤醒
	pmW = parkWait(t, e, c, "claude:pm")
	pmTells := tellCount(e, "claude:pm")
	if err := c.Do(ctx, "POST", "/agent/requirements/"+r.Code+"/status", map[string]any{"status": "review", "session_key": "claude:dev"}, nil); err != nil {
		t.Fatal(err)
	}
	woke(t, pmW, "已交验收", r.Code)
	woke(t, advW, "订阅的事件", "review", r.Code, "已交验收")
	asleep(t, plainW, "无身份会话")
	time.Sleep(200 * time.Millisecond)
	if n := tellCount(e, "claude:pm"); n != pmTells+1 {
		t.Fatalf("交验收 PM 只应收到一条：%d → %d", pmTells, n)
	}

	// 无身份会话用 --on blocked 订阅 → 被唤醒；再挂 ks wait 不带 --on 沿用上次；--on none 后不再被唤醒
	blocked := func(ref string) {
		t.Helper()
		for _, st := range []string{"in_progress", "blocked"} {
			if err := c.Do(ctx, "POST", "/agent/requirements/"+ref+"/status", map[string]any{"status": st, "session_key": "claude:dev"}, nil); err != nil {
				t.Fatal(err)
			}
		}
	}
	e.call("POST", "/api/sessions/claude:plain/reply", map[string]any{"comment": "换个订阅"}, nil) // 先把上一次等待唤醒，下一次挂起看得出来
	woke(t, plainW, "换个订阅")
	plainW = parkWaitOn(t, e, c, "claude:plain", "blocked")
	blocked(r.Code)
	woke(t, plainW, "订阅的事件", "blocked", r.Code)
	plainW = parkWait(t, e, c, "claude:plain")
	blocked(r.Code)
	woke(t, plainW, "blocked", r.Code)
	plainW = parkWaitOn(t, e, c, "claude:plain", "none")
	n0 := tellCount(e, "claude:plain")
	blocked(r.Code)
	asleep(t, plainW, "--on none 的会话")
	if n := tellCount(e, "claude:plain"); n != n0 {
		t.Fatalf("--on none 后不应再收到：%d → %d", n0, n)
	}

	// 开发（默认不订阅）不被别人的事件唤醒：PM 提决策，挂着 ks wait 的开发不醒
	devW := parkWait(t, e, c, "claude:dev")
	if err := c.Do(ctx, "POST", "/agent/decisions", map[string]any{"session_key": "claude:pm", "question": "要不要加分页？", "options": []string{"要", "不要"}}, nil); err != nil {
		t.Fatal(err)
	}
	asleep(t, devW, "Dev（默认不订阅）")
	if n := pendingCount(e); n != before+2 {
		t.Fatalf("订阅消息不应进用户收件箱：%d → %d", before, n)
	}
}

// 派活关系保证通知（R-160）：开发把派给它（PM 派活或项目 PM）的需求设为受阻、或针对这条需求提决策，
// 不管 PM 订阅了什么都告诉 PM（挂着 ks wait 的立刻被唤醒）；订阅了的 PM 只收一条；没关联需求的决策不打扰；用户收件箱不多出东西。
func TestAssignerNotifiedOnBlockedAndDecision(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	c := client.New(30 * time.Second)
	ctx := context.Background()
	for _, sid := range []string{"pm", "dev"} {
		hook(t, "claude", map[string]any{"session_id": sid, "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "上线", "prompt_id": "p0"})
	}
	e.waitFor("会话入库", func() bool { return e.session("claude:dev").Key != "" })
	for _, kr := range [][2]string{{"claude:pm", "pm"}, {"claude:dev", "dev"}} {
		if err := c.Do(ctx, "POST", "/agent/sessions/role", map[string]any{"session_key": kr[0], "role": kr[1]}, nil); err != nil {
			t.Fatal(err)
		}
	}
	woke(t, parkWait(t, e, c, "claude:pm"), "已上线待命")
	var r store.Requirement
	if err := c.Do(ctx, "POST", "/agent/requirements", map[string]any{"title": "导出 CSV", "session_key": "claude:dev"}, &r); err != nil {
		t.Fatal(err)
	}
	setStatus := func(st string) {
		t.Helper()
		if err := c.Do(ctx, "POST", "/agent/requirements/"+r.Code+"/status", map[string]any{"status": st, "session_key": "claude:dev"}, nil); err != nil {
			t.Fatal(err)
		}
	}

	// PM 默认订阅 blocked：受阻只收一条
	pmW := parkWait(t, e, c, "claude:pm")
	n0 := tellCount(e, "claude:pm")
	setStatus("blocked")
	woke(t, pmW, "受阻", r.Code)
	time.Sleep(200 * time.Millisecond)
	if n := tellCount(e, "claude:pm"); n != n0+1 {
		t.Fatalf("受阻 PM 只应收到一条：%d → %d", n0, n)
	}
	setStatus("in_progress")

	// PM 订阅 none：受阻、针对需求的决策仍然送到；没关联需求的决策不送
	pmW = parkWaitOn(t, e, c, "claude:pm", "none")
	n0 = tellCount(e, "claude:pm")
	setStatus("blocked")
	woke(t, pmW, "受阻", r.Code)
	pmW = parkWaitOn(t, e, c, "claude:pm", "none")
	before := pendingCount(e)
	if err := c.Do(ctx, "POST", "/agent/decisions", map[string]any{"session_key": "claude:dev", "question": "无关的问题？", "options": []string{"是", "否"}, "requirement": "none"}, nil); err != nil {
		t.Fatal(err)
	}
	asleep(t, pmW, "PM（决策没关联需求）")
	var d store.Decision
	if err := c.Do(ctx, "POST", "/agent/decisions", map[string]any{"session_key": "claude:dev", "question": "导出编码用哪个？", "options": []string{"UTF-8", "GBK"}, "requirement": r.Code}, &d); err != nil {
		t.Fatal(err)
	}
	woke(t, pmW, "这条需求是你派的", d.Code, r.Code, "导出编码用哪个")
	time.Sleep(200 * time.Millisecond)
	if n := tellCount(e, "claude:pm"); n != n0+2 {
		t.Fatalf("PM 应收到受阻和需求上的决策共两条：%d → %d", n0, n)
	}
	if n := pendingCount(e); n != before+2 {
		t.Fatalf("用户收件箱只应多出两条决策：%d → %d", before, n)
	}
}

// 用户在网页上做的事通知相关会话（R-171）：改放行开关、开 / 撤参谋授权窗口，PM 和 Adv（默认订阅 settings）被唤醒并看到改了什么，
// 没订阅的会话不被打扰，值没变不重复送；被授权的 Adv 已有授权通知，不再多收一条；
// 用户在网页改需求状态，派活的 PM 和关联的在线会话收到「从 x 改为 y」，订阅了 status 的也收到，各只一条；不进用户收件箱。
func TestUserWebChangesNotify(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	c := client.New(30 * time.Second)
	ctx := context.Background()
	for _, sid := range []string{"pm", "adv", "dev", "plain"} {
		hook(t, "claude", map[string]any{"session_id": sid, "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "上线", "prompt_id": "p0"})
	}
	e.waitFor("会话入库", func() bool { return e.session("claude:plain").ProjectID != "" })
	for _, kr := range [][2]string{{"claude:pm", "pm"}, {"claude:adv", "aide"}, {"claude:dev", "dev"}} {
		if err := c.Do(ctx, "POST", "/agent/sessions/role", map[string]any{"session_key": kr[0], "role": kr[1]}, nil); err != nil {
			t.Fatal(err)
		}
	}
	woke(t, parkWait(t, e, c, "claude:pm"), "已上线待命")
	project := e.session("claude:dev").ProjectID
	var p store.Project
	var plist struct {
		Projects []store.Project `json:"projects"`
	}
	e.call("GET", "/api/projects", nil, &plist)
	for _, x := range plist.Projects {
		if x.ID == project {
			p = x
		}
	}
	setProject := func(fields map[string]any) {
		t.Helper()
		fields["name"], fields["doc_root"] = p.Name, p.DocRoot
		e.call("PUT", "/api/projects/"+project, fields, nil)
	}
	before := pendingCount(e)

	// 1. 开放行开关：PM、Adv 被唤醒，无身份会话和 Dev 不被打扰；值没变不重复送
	pmW, advW, plainW := parkWait(t, e, c, "claude:pm"), parkWait(t, e, c, "claude:adv"), parkWait(t, e, c, "claude:plain")
	devTells := tellCount(e, "claude:dev")
	setProject(map[string]any{"agent_release": true})
	drainQuiet(t, pmW, "PM（改设置只告知）", "pm", "订阅的事件", "settings", "用户打开了项目", p.Name, "放行", "关 → 开")
	drainQuiet(t, advW, "Adv（改设置只告知）", "adv", "用户打开了项目", "放行")
	asleep(t, plainW, "无身份会话")
	pmW, advW = parkWait(t, e, c, "claude:pm"), parkWait(t, e, c, "claude:adv")
	pmTells, advTells := tellCount(e, "claude:pm"), tellCount(e, "claude:adv")
	setProject(map[string]any{"agent_release": true})
	asleep(t, pmW, "PM（放行开关没变）")
	if tellCount(e, "claude:pm") != pmTells || tellCount(e, "claude:adv") != advTells {
		t.Fatal("设置没变不应再送")
	}

	// 2. 关自动派发：只改了它，消息只写它
	setProject(map[string]any{"agent_release": true, "auto_dispatch": false})
	drainQuiet(t, pmW, "PM（改设置只告知）", "pm", "用户关闭了项目", "自动派发", "开 → 关")
	drainQuiet(t, advW, "Adv（改设置只告知）", "adv", "自动派发")

	// 3. 开授权窗口：被授权的 Adv 只收授权通知一条，PM 收到设置变化；撤销同理
	pmW, advW = parkWait(t, e, c, "claude:pm"), parkWait(t, e, c, "claude:adv")
	pmTells, advTells = tellCount(e, "claude:pm"), tellCount(e, "claude:adv")
	var g store.Grant
	e.call("POST", "/api/grants", map[string]any{"project_id": project, "session_key": "claude:adv", "max_risk": "low", "expires_at": time.Now().Add(time.Hour).Format(time.RFC3339Nano)}, &g)
	drainQuiet(t, pmW, "PM（改设置只告知）", "pm", "settings", "用户开了参谋授权窗口", g.Code)
	drainQuiet(t, advW, "Adv（授权变化只告知）", "adv", "用户开了参谋授权窗口", g.Code)
	time.Sleep(200 * time.Millisecond)
	if n := tellCount(e, "claude:adv"); n != advTells+1 {
		t.Fatalf("被授权的 Adv 只应收到一条授权通知：%d → %d", advTells, n)
	}
	if n := tellCount(e, "claude:pm"); n != pmTells+1 {
		t.Fatalf("PM 只应收到一条：%d → %d", pmTells, n)
	}
	pmW, advW = parkWait(t, e, c, "claude:pm"), parkWait(t, e, c, "claude:adv")
	advTells = tellCount(e, "claude:adv")
	e.call("POST", "/api/grants/"+g.Code+"/revoke", nil, nil)
	drainQuiet(t, pmW, "PM（改设置只告知）", "pm", "用户撤销了参谋授权窗口", g.Code)
	drainQuiet(t, advW, "Adv（授权变化只告知）", "adv", "用户撤销了参谋授权窗口", g.Code)
	time.Sleep(200 * time.Millisecond)
	if n := tellCount(e, "claude:adv"); n != advTells+1 {
		t.Fatalf("撤销时被授权的 Adv 只应收到一条：%d → %d", advTells, n)
	}

	// 4. 网页改需求状态：派活的 PM 和关联的 Dev 各收一条；没订阅 status 的 Adv 不被打扰
	var r store.Requirement
	if err := c.Do(ctx, "POST", "/agent/requirements", map[string]any{"title": "导出 CSV", "session_key": "claude:dev"}, &r); err != nil {
		t.Fatal(err)
	}
	var got store.Requirement
	e.call("GET", "/api/requirements/"+r.Code, nil, &got)
	if len(got.Sessions) == 0 {
		t.Fatalf("开发应已关联这条需求：%+v", got.Sessions)
	}
	setStatus := func(st string) {
		t.Helper()
		e.call("PUT", "/api/requirements/"+r.Code, map[string]any{"title": r.Title, "status": st, "project_id": project}, nil)
	}
	pmW, advW = parkWait(t, e, c, "claude:pm"), parkWait(t, e, c, "claude:adv")
	pmTells, advTells, devTells = tellCount(e, "claude:pm"), tellCount(e, "claude:adv"), tellCount(e, "claude:dev")
	setStatus("shelved")
	drainQuiet(t, pmW, "PM（状态变化只告知）", "pm", "这条需求是你派的", r.Code, "用户把", "从草稿改为已搁置")
	asleep(t, advW, "Adv（没订阅 status）")
	time.Sleep(200 * time.Millisecond)
	if n := tellCount(e, "claude:pm"); n != pmTells+1 {
		t.Fatalf("PM 只应收到一条：%d → %d", pmTells, n)
	}
	if n := tellCount(e, "claude:dev"); n != devTells+1 {
		t.Fatalf("关联的 Dev 应收到一条：%d → %d", devTells, n)
	}
	if n := tellCount(e, "claude:adv"); n != advTells {
		t.Fatalf("Adv 不应收到：%d → %d", advTells, n)
	}

	// 5. Adv 用 --on status 订阅后也收到；状态没变（只改了别的）不送
	advW = parkWaitOn(t, e, c, "claude:adv", "status")
	pmTells = tellCount(e, "claude:pm")
	e.call("PUT", "/api/requirements/"+r.Code, map[string]any{"title": "导出 CSV（改名）", "status": "shelved", "project_id": project}, nil)
	asleep(t, advW, "Adv（状态没变）")
	setStatus("draft")
	drainQuiet(t, advW, "Adv（状态变化只告知）", "adv", "订阅的事件", "status", r.Code, "从已搁置改为草稿")
	time.Sleep(200 * time.Millisecond)
	if n := tellCount(e, "claude:pm"); n != pmTells+1 {
		t.Fatalf("PM 只应收到一条：%d → %d", pmTells, n)
	}

	if n := pendingCount(e); n != before {
		t.Fatalf("这些通知不应进用户收件箱：%d → %d", before, n)
	}
}

// 用户在网页上验收 / 退回、批准 / 退回计划、通过 / 退回阶段，以及参谋授权窗口到期也通知相关会话（R-171）：
// 已经收到同一件事的会话不重复送——退回意见送给 PM 的，PM 不再多收一条状态通知；计划进展送给负责会话的，负责会话不再多收；
// 订阅了 status 的 Adv 只补收没收到的；授权窗口到期，被授权的 Adv 和 PM 各收一条。
func TestUserReviewPlanGrantNotify(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	c := client.New(30 * time.Second)
	ctx := context.Background()
	for _, sid := range []string{"pm", "adv", "dev"} {
		hook(t, "claude", map[string]any{"session_id": sid, "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "上线", "prompt_id": "p0"})
	}
	e.waitFor("会话入库", func() bool { return e.session("claude:dev").ProjectID != "" })
	for _, kr := range [][2]string{{"claude:pm", "pm"}, {"claude:adv", "aide"}, {"claude:dev", "dev"}} {
		if err := c.Do(ctx, "POST", "/agent/sessions/role", map[string]any{"session_key": kr[0], "role": kr[1]}, nil); err != nil {
			t.Fatal(err)
		}
	}
	woke(t, parkWait(t, e, c, "claude:pm"), "已上线待命")
	project := e.session("claude:dev").ProjectID
	before := pendingCount(e)

	// 开发记一条需求并交验收；Adv 先收掉「已交验收」，并订阅 status
	newReview := func(title string) store.Requirement {
		t.Helper()
		var r store.Requirement
		if err := c.Do(ctx, "POST", "/agent/requirements", map[string]any{"title": title, "session_key": "claude:dev"}, &r); err != nil {
			t.Fatal(err)
		}
		advW := parkWaitOn(t, e, c, "claude:adv", "decision,review,settings,status")
		pmW := parkWait(t, e, c, "claude:pm")
		if err := c.Do(ctx, "POST", "/agent/requirements/"+r.Code+"/status", map[string]any{"status": "review", "session_key": "claude:dev"}, nil); err != nil {
			t.Fatal(err)
		}
		woke(t, advW, "已交验收", r.Code)
		woke(t, pmW, "已交验收", r.Code)
		return r
	}
	review := func(ref, verdict, comment string) {
		t.Helper()
		e.call("POST", "/api/reviews", map[string]any{"items": []map[string]any{{"ref": ref, "verdict": verdict, "comment": comment}}}, nil)
	}

	// 1. 无附言验收通过：派活的 PM、关联的 Dev 各收一条状态通知，订阅了 status 的 Adv 也收到
	r1 := newReview("导出 CSV")
	pmW, advW := parkWait(t, e, c, "claude:pm"), parkWait(t, e, c, "claude:adv")
	pmTells, devTells, advTells := tellCount(e, "claude:pm"), tellCount(e, "claude:dev"), tellCount(e, "claude:adv")
	review(r1.Code, "accept", "")
	drainQuiet(t, pmW, "PM（状态变化只告知）", "pm", r1.Code, "从待验收改为已完成")
	drainQuiet(t, advW, "Adv（状态变化只告知）", "adv", "订阅的事件", "status", r1.Code, "从待验收改为已完成")
	time.Sleep(200 * time.Millisecond)
	if n := tellCount(e, "claude:pm"); n != pmTells+1 {
		t.Fatalf("验收通过 PM 只应收到一条：%d → %d", pmTells, n)
	}
	if n := tellCount(e, "claude:dev"); n != devTells+1 {
		t.Fatalf("验收通过关联的 Dev 应收到一条：%d → %d", devTells, n)
	}
	if n := tellCount(e, "claude:adv"); n != advTells+1 {
		t.Fatalf("验收通过 Adv 只应收到一条：%d → %d", advTells, n)
	}

	// 2. 退回（带意见）：意见已送给 PM，PM 不再多收状态通知；Dev、Adv 各收一条
	r2 := newReview("导出 Excel")
	pmW, advW = parkWait(t, e, c, "claude:pm"), parkWait(t, e, c, "claude:adv")
	pmTells, devTells = tellCount(e, "claude:pm"), tellCount(e, "claude:dev")
	review(r2.Code, "revise", "表头缺失")
	woke(t, pmW, r2.Code, "表头缺失")
	drainQuiet(t, advW, "Adv（状态变化只告知）", "adv", "订阅的事件", "status", r2.Code, "从待验收改为进行中")
	time.Sleep(200 * time.Millisecond)
	if n := tellCount(e, "claude:pm"); n != pmTells {
		t.Fatalf("退回意见已送给 PM，不应再多一条状态通知：%d → %d", pmTells, n)
	}
	if n := tellCount(e, "claude:dev"); n != devTells+1 {
		t.Fatalf("退回时关联的 Dev 应收到一条状态通知：%d → %d", devTells, n)
	}

	// 3. 计划：负责会话 Dev 收到的是计划进展，不再多收状态通知；Adv 收到批准 / 退回阶段 / 通过阶段 / 不批准
	var a, b store.Requirement
	e.call("POST", "/api/requirements", map[string]any{"title": "计划项甲", "project_id": project}, &a)
	e.call("POST", "/api/requirements", map[string]any{"title": "计划项乙", "project_id": project}, &b)
	var x, y store.Plan
	e.call("POST", "/api/plans", map[string]any{"title": "导出计划", "project_id": project, "owner_session": "claude:dev",
		"stages": []map[string]any{{"title": "后端", "requirements": []map[string]any{{"ref": a.Code}}}, {"title": "网页", "requirements": []map[string]any{{"ref": b.Code}}}}}, &x)
	e.call("POST", "/api/plans", map[string]any{"title": "另一个计划", "project_id": project, "owner_session": "claude:dev",
		"stages": []map[string]any{{"title": "只做一段", "requirements": []map[string]any{{"title": "丙"}}}}}, &y)
	advW = parkWait(t, e, c, "claude:adv")
	devTells = tellCount(e, "claude:dev")
	e.call("POST", "/api/plans/"+x.Code+"/start", map[string]any{}, nil)
	drainQuiet(t, advW, "Adv（计划进展只告知）", "adv", "status", "用户批准了计划", x.Code, "阶段 1")
	advW = parkWait(t, e, c, "claude:adv")
	e.call("POST", "/api/plans/"+x.Code+"/stages/1/revise", map[string]any{"comment": "接口缺分页"}, nil)
	drainQuiet(t, advW, "Adv（计划进展只告知）", "adv", "用户退回了计划", x.Code, "阶段 1")
	e.call("PUT", "/api/requirements/"+a.Code, map[string]any{"title": a.Title, "status": "done", "project_id": project}, nil)
	drainQuiet(t, parkWait(t, e, c, "claude:adv"), "Adv（状态变化只告知）", "adv", "status", a.Code, "已完成") // 单条改状态那条
	advW = parkWait(t, e, c, "claude:adv")
	e.call("POST", "/api/plans/"+x.Code+"/stages/1/pass", map[string]any{}, nil)
	drainQuiet(t, advW, "Adv（计划进展只告知）", "adv", "用户验收通过了计划", x.Code, "阶段 2「网页」已解锁")
	advW = parkWait(t, e, c, "claude:adv")
	e.call("POST", "/api/plans/"+y.Code+"/reject", map[string]any{"comment": "先别做"}, nil)
	drainQuiet(t, advW, "Adv（计划进展只告知）", "adv", "用户退回了计划", y.Code)
	time.Sleep(200 * time.Millisecond)
	// 负责会话收到的是计划进展 / 派活，不是状态通知；除单条改状态那一条外没有多出 tell
	if n := tellCount(e, "claude:dev"); n > devTells+1 {
		t.Fatalf("计划事件不应给负责会话多送状态通知：%d → %d", devTells, n)
	}

	// 4. 授权窗口到期：被授权的 Adv 和 PM 各收一条「已到期」
	pmW, advW = parkWait(t, e, c, "claude:pm"), parkWait(t, e, c, "claude:adv")
	var g store.Grant
	e.call("POST", "/api/grants", map[string]any{"project_id": project, "session_key": "claude:adv", "max_risk": "low", "expires_at": time.Now().Add(2 * time.Second).Format(time.RFC3339Nano)}, &g)
	drainQuiet(t, pmW, "PM（改设置只告知）", "pm", "用户开了参谋授权窗口", g.Code)
	drainQuiet(t, advW, "Adv（授权变化只告知）", "adv", "用户开了参谋授权窗口", g.Code)
	pmW, advW = parkWait(t, e, c, "claude:pm"), parkWait(t, e, c, "claude:adv")
	pmTells, advTells = tellCount(e, "claude:pm"), tellCount(e, "claude:adv")
	time.Sleep(2500 * time.Millisecond) // 到期通知也只告知，不叫醒
	drainQuiet(t, pmW, "PM（到期只告知）", "pm", g.Code, "授权窗口", "已到期")
	drainQuiet(t, advW, "Adv（到期只告知）", "adv", g.Code, "授权窗口", "已到期")
	time.Sleep(300 * time.Millisecond)
	if tellCount(e, "claude:pm") != pmTells+1 || tellCount(e, "claude:adv") != advTells+1 {
		t.Fatal("到期通知每人只应一条")
	}

	// 5. 撤销过的窗口到点不再通知
	pmW = parkWait(t, e, c, "claude:pm")
	e.call("POST", "/api/grants", map[string]any{"project_id": project, "session_key": "claude:adv", "max_risk": "low", "expires_at": time.Now().Add(1500 * time.Millisecond).Format(time.RFC3339Nano)}, &g)
	drainQuiet(t, pmW, "PM（改设置只告知）", "pm", "用户开了参谋授权窗口", g.Code)
	pmW = parkWait(t, e, c, "claude:pm")
	e.call("POST", "/api/grants/"+g.Code+"/revoke", nil, nil)
	drainQuiet(t, pmW, "PM（改设置只告知）", "pm", "用户撤销了参谋授权窗口", g.Code)
	pmW = parkWait(t, e, c, "claude:pm")
	asleep(t, pmW, "PM（窗口已撤销，到点不再通知）")
	time.Sleep(1500 * time.Millisecond)
	asleep(t, pmW, "PM（窗口已撤销，到点不再通知）")

	if n := pendingCount(e); n != before {
		t.Fatalf("这些通知不应进用户收件箱：%d → %d", before, n)
	}
}
