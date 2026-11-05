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

// R-278：已指派、已送达的需求迟迟没开始处理——超时后再提醒一次（要动手，叫醒等待）、继续超时进「待处理」、
// 开始处理后清除（记录清空、还没送达的提醒关掉）；用户在网页上把已指派的需求改为进行中时叫醒执行方；
// 紧接着重挂等待时，新等待开头重列上一次等待送来、还没开始处理的指派。
func TestUnstartedNudge(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	c := client.New(30 * time.Second)
	ctx := context.Background()
	for _, sid := range []string{"us-pm", "us-dev"} {
		hook(t, "claude", map[string]any{"session_id": sid, "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "上线", "prompt_id": "p0"})
	}
	e.waitFor("会话入库", func() bool { return e.session("claude:us-dev").ProjectID != "" })
	for _, kr := range [][2]string{{"claude:us-pm", "pm"}, {"claude:us-dev", "dev"}} {
		if err := c.Do(ctx, "POST", "/agent/sessions/role", map[string]any{"session_key": kr[0], "role": kr[1]}, nil); err != nil {
			t.Fatal(err)
		}
	}
	pid := e.session("claude:us-dev").ProjectID
	newReq := func(title string) store.Requirement {
		t.Helper()
		var r store.Requirement
		e.call("POST", "/api/requirements", map[string]any{"title": title, "project_id": pid, "status": "draft"}, &r)
		return r
	}
	get := func(code string) store.Requirement {
		t.Helper()
		r, err := e.store.GetRequirement(ctx, code)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	nudges := func(key, code string) []store.Decision {
		var out []store.Decision
		for _, d := range tellsTo(e, key) {
			if d.Question == "指派未开始处理" && d.RequirementCode == code {
				out = append(out, d)
			}
		}
		return out
	}
	park := func(key string) chan struct{ Reason, Text string } {
		t.Helper()
		ch := make(chan struct{ Reason, Text string }, 1)
		w := parkWait(t, e, c, key)
		go func() { r := <-w; ch <- struct{ Reason, Text string }{r.Reason, r.Text} }()
		return ch
	}
	woke := func(ch chan struct{ Reason, Text string }, want, what string) string {
		t.Helper()
		select {
		case r := <-ch:
			if r.Reason != "delivered" || !strings.Contains(r.Text, want) {
				t.Fatalf("%s：等待应被叫醒并带上「%s」：%+v", what, want, r)
			}
			return r.Text
		case <-time.After(10 * time.Second):
			t.Fatalf("%s：等待应被叫醒", what)
		}
		return ""
	}

	// 1. 指派送达（挂着等待），超过 10 分钟（默认）仍待开始：再提醒一次，要动手档、叫醒等待
	r1 := newReq("未开始演示一")
	w := park("claude:us-dev")
	e.call("POST", "/api/requirements/"+r1.Code+"/assign", map[string]any{"session_key": "claude:us-dev"}, nil)
	woke(w, r1.Code, "指派送达")
	e.waitFor("送达记下", func() bool {
		d, err := e.store.OfferDelivery(ctx, get(r1.Code).ID, "claude:us-dev", *get(r1.Code).OfferedAt)
		return err == nil && d != nil
	})
	w = park("claude:us-dev")
	e.hub.StartSweep(ctx, time.Now().Add(5*time.Minute))
	if x := get(r1.Code); x.OfferDeliveredAt == nil || x.OfferNudgedAt != nil {
		t.Fatalf("送达 5 分钟：记下送达时间，还不提醒：%+v", x)
	}
	e.hub.StartSweep(ctx, time.Now().Add(11*time.Minute))
	text := woke(w, "仍未开始处理", "超时后再提醒")
	if !strings.Contains(text, r1.Code) || !strings.Contains(text, "session_link_req "+r1.Code) {
		t.Fatalf("提醒应写明哪条需求、怎么开始：%s", text)
	}
	if ns := nudges("claude:us-dev", r1.Code); len(ns) != 1 || ns[0].FYI {
		t.Fatalf("应有一条要动手的提醒：%+v", ns)
	}
	if x := get(r1.Code); x.OfferNudgedAt == nil || x.OfferOverdueAt != nil {
		t.Fatalf("提醒后记下提醒时间，还不进待处理：%+v", x)
	}
	e.hub.StartSweep(ctx, time.Now().Add(15*time.Minute))
	if ns := nudges("claude:us-dev", r1.Code); len(ns) != 1 {
		t.Fatalf("只提醒一次：%d", len(ns))
	}

	// 2. 提醒后再过 10 分钟仍没开始：进「待处理」（offer_overdue_at）
	e.hub.StartSweep(ctx, time.Now().Add(22*time.Minute))
	x := get(r1.Code)
	if x.OfferOverdueAt == nil || x.OfferedTo != "claude:us-dev" || x.Status != store.ReqReady {
		t.Fatalf("继续超时应进待处理：%+v", x)
	}
	var list struct {
		Requirements []store.Requirement `json:"requirements"`
	}
	e.call("GET", "/api/requirements?project_id="+pid, nil, &list)
	found := false
	for _, r := range list.Requirements {
		if r.Code == r1.Code && r.OfferOverdueAt != nil && r.OfferDeliveredAt != nil && r.OfferedTo == "claude:us-dev" {
			found = true
		}
	}
	if !found {
		t.Fatalf("需求列表应带上进待处理的记录（指派对象、送达时间）：%+v", list.Requirements)
	}

	// 3. 开始处理（关联需求 → 进行中）：记录清空
	if err := c.Do(ctx, "POST", "/agent/sessions/claude:us-dev/link", map[string]any{"requirement": r1.Code}, nil); err != nil {
		t.Fatal(err)
	}
	if x := get(r1.Code); x.Status != store.ReqInProgress || x.OfferOverdueAt != nil || x.OfferNudgedAt != nil || x.OfferDeliveredAt != nil {
		t.Fatalf("开始处理后记录应清空：%+v", x)
	}

	// 3b. 开发在运行中（没挂等待）时收到提醒、还没送达就开始处理：提醒关掉，不再送
	r2 := newReq("未开始演示二")
	w = park("claude:us-dev")
	e.call("POST", "/api/requirements/"+r2.Code+"/assign", map[string]any{"session_key": "claude:us-dev"}, nil)
	woke(w, r2.Code, "第二条指派送达")
	e.waitFor("第二条送达记下", func() bool {
		d, err := e.store.OfferDelivery(ctx, get(r2.Code).ID, "claude:us-dev", *get(r2.Code).OfferedAt)
		return err == nil && d != nil
	})
	hook(t, "claude", map[string]any{"session_id": "us-dev", "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "继续", "prompt_id": "p1"})
	e.waitFor("开发运行中", func() bool { return e.session("claude:us-dev").State == store.StateRunning })
	e.hub.StartSweep(ctx, time.Now().Add(11*time.Minute))
	ns := nudges("claude:us-dev", r2.Code)
	if len(ns) != 1 || ns[0].Status != store.DecAnswered {
		t.Fatalf("运行中时提醒应待送：%+v", ns)
	}
	if err := c.Do(ctx, "POST", "/agent/sessions/claude:us-dev/link", map[string]any{"requirement": r2.Code}, nil); err != nil {
		t.Fatal(err)
	}
	if d, _ := e.store.GetDecision(ctx, ns[0].ID); d.Status != store.DecClosed {
		t.Fatalf("开始处理后，还没送达的提醒应关掉：%+v", d)
	}

	// 4. 用户在网页上把已指派给主调度的需求从待开始改为进行中：叫醒主调度（不只是告知）
	r3 := newReq("未开始演示三")
	pw := park("claude:us-pm")
	woke(pw, "已上线待命", "先收掉开发上线的消息")
	pw = park("claude:us-pm")
	e.call("POST", "/api/requirements/"+r3.Code+"/assign", map[string]any{"session_key": "claude:us-pm"}, nil)
	woke(pw, r3.Code, "指派给主调度")
	pw = park("claude:us-pm")
	e.call("PUT", "/api/requirements/"+r3.Code, map[string]any{"title": r3.Title, "status": "in_progress", "project_id": pid}, nil)
	text = woke(pw, "用户在等你开始处理", "网页改为进行中")
	if !strings.Contains(text, r3.Code) {
		t.Fatalf("叫醒的消息应写明需求：%s", text)
	}

	// 5. 紧接着重挂等待：上一次等待送来、还没开始处理的指派，在新等待开头再列一次（只一次）；已开始处理的不列
	type checkOut struct {
		Replay string `json:"replay"`
	}
	check := func(key string) string {
		t.Helper()
		var out checkOut
		if err := c.Do(ctx, "GET", "/wait/check?session_key="+key, nil, &out); err != nil {
			t.Fatal(err)
		}
		return out.Replay
	}
	r4 := newReq("未开始演示四")
	check("claude:us-dev") // 这次等待开始
	w = park("claude:us-dev")
	e.call("POST", "/api/requirements/"+r4.Code+"/assign", map[string]any{"session_key": "claude:us-dev"}, nil)
	woke(w, r4.Code, "第四条指派送达")
	e.waitFor("第四条送达记下", func() bool {
		d, err := e.store.OfferDelivery(ctx, get(r4.Code).ID, "claude:us-dev", *get(r4.Code).OfferedAt)
		return err == nil && d != nil
	})
	rep := check("claude:us-dev") // 紧接着又挂新的等待
	if !strings.Contains(rep, r4.Code) || !strings.Contains(rep, "指派") || strings.Contains(rep, r2.Code) {
		t.Fatalf("新等待开头应重列还没开始处理的 %s（已开始处理的不列）：%q", r4.Code, rep)
	}
	if rep := check("claude:us-dev"); rep != "" {
		t.Fatalf("同一条只重列一次：%q", rep)
	}
}
