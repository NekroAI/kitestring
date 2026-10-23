package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/client"
	"github.com/NekroAI/kitestring/internal/daemon"
	"github.com/NekroAI/kitestring/internal/store"
)

// try 调用 Web 接口并返回状态码与响应体，用于验证应被拒绝的操作。
func (e *env) try(method, path string, in any) (int, string) {
	e.t.Helper()
	b, _ := json.Marshal(in)
	req, _ := http.NewRequest(method, e.srv.URL+path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.web.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	buf.ReadFrom(resp.Body)
	return resp.StatusCode, buf.String()
}

// waitOnce 挂一次 ks wait，返回结果通道。
func waitOnce(key string) chan daemon.WaitResult {
	ch := make(chan daemon.WaitResult, 1)
	go func() {
		var res daemon.WaitResult
		_ = client.New(30*time.Second).Do(context.Background(), "GET", "/wait?session_key="+key+"&timeout=20s", nil, &res)
		ch <- res
	}()
	return ch
}

func recv(t *testing.T, ch chan daemon.WaitResult, what string) string {
	t.Helper()
	select {
	case res := <-ch:
		if res.Reason != "delivered" {
			t.Fatalf("%s：等待没有收到送达：%+v", what, res)
		}
		return res.Text
	case <-time.After(15 * time.Second):
		t.Fatalf("%s：等待超时", what)
	}
	return ""
}

func TestPlanStagesAdvance(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	hook(t, "claude", map[string]any{"session_id": "pl-1", "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "待命", "prompt_id": "p0"})
	c := client.New(10 * time.Second)
	key := "claude:pl-1"
	setStatus := func(code, status string) {
		t.Helper()
		if err := c.Do(context.Background(), "POST", "/agent/requirements/"+code+"/status", map[string]any{"status": status, "session_key": key}, nil); err != nil {
			t.Fatal(err)
		}
	}
	plan := func(code string) store.Plan {
		var x store.Plan
		e.call("GET", "/api/plans/"+code, nil, &x)
		return x
	}

	// 你在网页上建计划：阶段 1 两条已有需求，阶段 2 新建一条
	var a, b store.Requirement
	e.call("POST", "/api/requirements", map[string]any{"title": "数据模型"}, &a)
	e.call("POST", "/api/requirements", map[string]any{"title": "接口"}, &b)
	var x store.Plan
	e.call("POST", "/api/plans", map[string]any{
		"title": "引入计划", "goal": "分阶段推进", "owner_session": key,
		"stages": []map[string]any{
			{"title": "后端", "requirements": []map[string]any{{"ref": a.Code}, {"ref": b.Code}}},
			{"title": "网页", "requirements": []map[string]any{{"title": "计划视图", "acceptance": "三端可用"}}},
		},
	}, &x)
	if x.Code != "P-1" || x.Status != store.PlanDraft || len(x.Stages) != 2 || len(x.Stages[0].Requirements) != 2 || x.Stages[1].Requirements[0].Title != "计划视图" {
		t.Fatalf("新建计划异常：%+v", x)
	}
	cCode := x.Stages[1].Requirements[0].Code
	var got store.Requirement
	e.call("GET", "/api/requirements/"+a.Code, nil, &got)
	if got.PlanCode != "P-1" || got.StageSeq != 1 || got.StageTitle != "后端" {
		t.Fatalf("需求应显示所属计划与阶段：%+v", got)
	}

	// 一条需求只能属于一个计划
	if code, _ := e.try("POST", "/api/plans", map[string]any{"title": "另一个", "stages": []map[string]any{{"title": "x", "requirements": []map[string]any{{"ref": a.Code}}}}}); code != 400 {
		t.Fatalf("同一需求放进第二个计划应被拒绝：%d", code)
	}

	// 开始：阶段 1 解锁，需求交给负责会话，挂起的会话被唤醒
	w := waitOnce(key)
	e.waitFor("登记挂起", func() bool { return e.session(key).Parked })
	e.call("POST", "/api/plans/P-1/start", map[string]any{}, &x)
	text := recv(t, w, "计划开始")
	if !strings.Contains(text, "计划进展") || !strings.Contains(text, "P-1「引入计划」计划开始") || !strings.Contains(text, "数据模型") || !strings.Contains(text, "接口") || strings.Contains(text, "计划视图") {
		t.Fatalf("开始后应送达计划进展与阶段 1 的需求：%s", text)
	}
	if x.Status != store.PlanActive || x.Current != 1 || x.Stages[0].Status != store.StageActive || x.Stages[1].Status != store.StageLocked {
		t.Fatalf("开始后阶段状态异常：%+v", x)
	}
	e.call("GET", "/api/requirements/"+a.Code, nil, &got)
	if got.Status != store.ReqReady || len(got.Sessions) != 1 {
		t.Fatalf("阶段 1 的需求应交给负责会话、待开始：%s %v", got.Status, got.Sessions)
	}

	// 需求没做完时不能交小结，也不能通过
	if err := c.Do(context.Background(), "POST", "/agent/plans/P-1/stages/1/submit", map[string]any{"summary": "做完了"}, nil); err == nil || !strings.Contains(err.Error(), a.Code) {
		t.Fatalf("还有需求没做完时交小结应被拒绝：%v", err)
	}
	if code, _ := e.try("POST", "/api/plans/P-1/stages/1/pass", map[string]any{}); code != 400 {
		t.Fatalf("还有需求没做完时通过应被拒绝：%d", code)
	}

	// 做完后交小结 → 待阶段验收
	setStatus(a.Code, store.ReqReview)
	setStatus(b.Code, store.ReqReview)
	e.call("POST", "/api/reviews", map[string]any{"items": []map[string]any{{"ref": b.Code, "verdict": "accept"}}}, nil)
	if err := c.Do(context.Background(), "POST", "/agent/plans/P-1/stages/1/submit", map[string]any{"summary": "两张表与接口，e2e 通过", "session_key": key}, &x); err != nil {
		t.Fatal(err)
	}
	if x.Stages[0].Status != store.StageReview || x.Stages[0].Summary != "两张表与接口，e2e 通过" {
		t.Fatalf("交小结后应待阶段验收：%+v", x.Stages[0])
	}

	// 单独退回阶段里的一条需求 → 阶段回到进行中（小结过时）
	e.call("POST", "/api/reviews", map[string]any{"items": []map[string]any{{"ref": a.Code, "verdict": "revise", "comment": "字段名改一下"}}}, nil)
	if st := plan("P-1").Stages[0].Status; st != store.StageActive {
		t.Fatalf("阶段里的需求被退回后阶段应回到进行中：%s", st)
	}
	recv(t, waitOnce(key), "需求修改意见") // 先收走这条修改意见

	// 退回整个阶段：必须附意见，意见送达
	if code, _ := e.try("POST", "/api/plans/P-1/stages/1/revise", map[string]any{}); code != 400 {
		t.Fatalf("退回阶段不写意见应被拒绝：%d", code)
	}
	w = waitOnce(key)
	e.call("POST", "/api/plans/P-1/stages/1/revise", map[string]any{"comment": "接口缺分页"}, &x)
	if text := recv(t, w, "退回阶段"); !strings.Contains(text, "用户退回了阶段 1「后端」：接口缺分页") {
		t.Fatalf("退回意见应送达：%s", text)
	}

	// 再做完 → 你通过整阶段：待验收的需求一并通过，阶段 2 解锁并交给会话
	setStatus(a.Code, store.ReqReview)
	c.Do(context.Background(), "POST", "/agent/plans/P-1/stages/1/submit", map[string]any{"summary": "已加分页"}, nil)
	w = waitOnce(key)
	e.call("POST", "/api/plans/P-1/stages/1/pass", map[string]any{"comment": "不错"}, &x)
	text = recv(t, w, "阶段通过")
	if !strings.Contains(text, "阶段 1「后端」已验收通过。现在进入阶段 2「网页」") || !strings.Contains(text, "附言：不错") || !strings.Contains(text, "计划视图") || !strings.Contains(text, "验收标准：三端可用") {
		t.Fatalf("通过后应送达下一阶段：%s", text)
	}
	if x.Current != 2 || x.Stages[0].Status != store.StagePassed || x.Stages[1].Status != store.StageActive || x.Stages[0].Requirements[0].Status != store.ReqDone {
		t.Fatalf("通过后状态异常：%+v", x)
	}

	// 已通过阶段里的需求被重开 → 计划暂停，不能往下
	e.call("POST", "/api/reviews", map[string]any{"items": []map[string]any{{"ref": a.Code, "verdict": "revise", "comment": "线上发现问题"}}}, nil)
	setStatus(cCode, store.ReqReview)
	if x = plan("P-1"); !x.Paused {
		t.Fatal("已通过阶段的需求被重开后计划应暂停")
	}
	if code, body := e.try("POST", "/api/plans/P-1/stages/2/pass", map[string]any{}); code != 400 || !strings.Contains(body, "重开") {
		t.Fatalf("暂停时通过应被拒绝：%d %s", code, body)
	}

	// 修好并验收后继续；最后一个阶段通过 → 计划完成
	setStatus(a.Code, store.ReqReview)
	e.call("POST", "/api/reviews", map[string]any{"items": []map[string]any{{"ref": a.Code, "verdict": "accept"}}}, nil)
	e.call("POST", "/api/plans/P-1/stages/2/pass", map[string]any{}, &x)
	if x.Status != store.PlanDone || x.Current != 0 || x.Paused {
		t.Fatalf("最后一阶段通过后计划应完成：%+v", x)
	}
}

func TestAgentPlanNeedsApproval(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	prompt := func(turn string) string {
		return hook(t, "claude", map[string]any{"session_id": "pa-1", "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "拆一下", "prompt_id": turn})
	}
	prompt("p0")
	c := client.New(10 * time.Second)

	// Agent 拆出计划：待你批准，新需求为草稿并关联它
	var x store.Plan
	if err := c.Do(context.Background(), "POST", "/agent/plans", map[string]any{
		"title": "支付回调重构", "session_key": "claude:pa-1",
		"stages": []map[string]any{{"title": "梳理", "requirements": []map[string]any{{"title": "列出回调路径"}}}, {"title": "改造", "requirements": []map[string]any{{"title": "幂等处理"}}}},
	}, &x); err != nil {
		t.Fatal(err)
	}
	if x.Status != store.PlanDraft || x.CreatedBy != "agent" || x.OwnerSession != "claude:pa-1" || x.Stages[0].Requirements[0].Status != store.ReqDraft {
		t.Fatalf("Agent 拆的计划应待批准：%+v", x)
	}

	// 不批准：搁置，附言（带图）送达
	img := e.upload(t, "mark.png", tinyPNG)
	e.call("POST", "/api/plans/"+x.Code+"/reject", map[string]any{"comment": "先别动幂等", "files": []string{img.ID}}, &x)
	if x.Status != store.PlanShelved {
		t.Fatalf("不批准后应搁置：%s", x.Status)
	}
	var out string
	e.waitFor("未批准附言送达", func() bool {
		out = prompt("p1")
		return strings.Contains(out, "用户没有批准这个计划") && strings.Contains(out, "附言：先别动幂等") && strings.Contains(out, "附图")
	})

	// 之后又批准：开始，阶段 1 交给它
	e.call("POST", "/api/plans/"+x.Code+"/start", map[string]any{"comment": "还是做吧"}, &x)
	e.waitFor("批准送达", func() bool {
		out = prompt("p2")
		return strings.Contains(out, "计划已获用户批准，开始") && strings.Contains(out, "列出回调路径")
	})
}

// D-60：后面阶段的需求在解锁前就被逐条验收完了，通过前一阶段时一并自动通过，计划不再卡住。
func TestPlanAutoPassFinishedStages(t *testing.T) {
	e := setup(t)
	var x store.Plan
	e.call("POST", "/api/plans", map[string]any{"title": "三段", "stages": []map[string]any{
		{"title": "一", "requirements": []map[string]any{{"title": "甲"}}},
		{"title": "二", "requirements": []map[string]any{{"title": "乙"}}},
		{"title": "三", "requirements": []map[string]any{{"title": "丙"}}},
	}}, &x)
	e.call("POST", "/api/plans/"+x.Code+"/start", map[string]any{}, &x)
	code := func(i int) string { return x.Stages[i].Requirements[0].Code }
	a, b, c := code(0), code(1), code(2)
	// 阶段 2 的需求在未解锁时被单独验收；阶段 3 还没做
	e.call("PUT", "/api/requirements/"+b, map[string]any{"title": "乙", "status": "done"}, nil)
	e.call("PUT", "/api/requirements/"+a, map[string]any{"title": "甲", "status": "review"}, nil)
	e.call("POST", "/api/plans/"+x.Code+"/stages/1/pass", map[string]any{}, &x)
	if x.Stages[1].Status != store.StagePassed || x.Stages[2].Status != store.StageActive || x.Current != 3 {
		t.Fatalf("阶段 2 应自动通过、阶段 3 解锁：%s %s current=%d", x.Stages[1].Status, x.Stages[2].Status, x.Current)
	}
	// 阶段 3 也验收完 → 通过阶段 3 后计划完成
	e.call("PUT", "/api/requirements/"+c, map[string]any{"title": "丙", "status": "review"}, nil)
	e.call("POST", "/api/plans/"+x.Code+"/stages/3/pass", map[string]any{}, &x)
	if x.Status != store.PlanDone {
		t.Fatalf("计划应完成：%s", x.Status)
	}
}
