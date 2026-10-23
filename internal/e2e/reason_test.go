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

// 可选的一句话理由（R-157）：
//   - 决策作答时可附理由（可不填）：随答复注入 Agent、记在决策上、进后果链；不附理由时注入与以前一样
//   - 搁置需求时可附原因（类别 + 一句话，都可不填），进状态历史；Agent 搁置必须写原因
//   - 旧数据（没有理由的决策、没有原因的搁置）照常显示
func TestOptionalReasons(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	prompt := func(turn string) string {
		return hook(t, "claude", map[string]any{"session_id": "rs-1", "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "继续", "prompt_id": turn})
	}
	prompt("p0")
	e.waitFor("会话入库", func() bool { return e.session("claude:rs-1").ProjectID != "" })
	c := client.New(5 * time.Second)
	ctx := context.Background()
	key := "claude:rs-1"

	// 决策：不附理由 = 旧行为；附理由 = 注入里多一行、决策上记下、后果链带出
	var d1, d2 store.Decision
	for i, d := range []*store.Decision{&d1, &d2} {
		if err := c.Do(ctx, "POST", "/agent/decisions", map[string]any{"question": "用哪种方案？", "options": []string{"甲", "乙"}, "recommendation": "甲", "session_key": key}, d); err != nil {
			t.Fatal(i, err)
		}
	}
	e.call("POST", "/api/decisions/answers", map[string]any{"answers": []map[string]any{{"ref": d1.Code, "answer": "甲"}}}, nil)
	var out string
	e.waitFor("无理由答复送达", func() bool { out = prompt("p1"); return strings.Contains(out, d1.Code) })
	if strings.Contains(out, "理由") {
		t.Fatalf("没写理由时注入不应出现理由：\n%s", out)
	}
	var got store.Decision
	if err := c.Do(ctx, "GET", "/agent/decisions/"+d1.Code, nil, &got); err != nil || got.AnswerReason != "" {
		t.Fatalf("没写理由应为空：%v %q", err, got.AnswerReason)
	}

	e.call("POST", "/api/decisions/answers", map[string]any{"answers": []map[string]any{{"ref": d2.Code, "answer": "乙", "reason": "  甲的迁移成本太高\n  现在做不了  "}}}, nil)
	e.waitFor("带理由答复送达", func() bool { out = prompt("p2"); return strings.Contains(out, d2.Code) })
	if !strings.Contains(out, "用户答复：乙") || !strings.Contains(out, "用户的理由：甲的迁移成本太高 现在做不了") {
		t.Fatalf("注入里应有答复和理由：\n%s", out)
	}
	if err := c.Do(ctx, "GET", "/agent/decisions/"+d2.Code, nil, &got); err != nil || got.AnswerReason != "甲的迁移成本太高 现在做不了" {
		t.Fatalf("理由应记在决策上：%v %q", err, got.AnswerReason)
	}
	var ch store.DecisionChain
	d3 := store.Decision{}
	if err := c.Do(ctx, "POST", "/agent/decisions", map[string]any{"question": "接着问", "from_decision": d2.Code, "session_key": key}, &d3); err != nil {
		t.Fatal(err)
	}
	e.call("GET", "/api/decisions/"+d2.Code+"/chain", nil, &ch)
	e.call("GET", "/api/decisions/"+d3.Code+"/chain", nil, &ch)
	if ch.From == nil || ch.From.Reason != "甲的迁移成本太高 现在做不了" {
		t.Fatalf("后果链里的来源决策应带理由：%+v", ch.From)
	}

	// 搁置：用户不写原因照常；写了类别 + 一句话进状态历史；只选类别也行
	var r1, r2, r3 store.Requirement
	e.call("POST", "/api/requirements", map[string]any{"title": "搁置一", "status": "ready"}, &r1)
	e.call("POST", "/api/requirements", map[string]any{"title": "搁置二", "status": "ready"}, &r2)
	e.call("POST", "/api/requirements", map[string]any{"title": "搁置三", "status": "ready"}, &r3)
	e.call("PUT", "/api/requirements/"+r1.Code, map[string]any{"title": "搁置一", "status": "shelved"}, nil)
	e.call("PUT", "/api/requirements/"+r2.Code, map[string]any{"title": "搁置二", "status": "shelved", "shelve_kind": "duplicate", "shelve_reason": "和 R-1 重复"}, nil)
	e.call("PUT", "/api/requirements/"+r3.Code, map[string]any{"title": "搁置三", "status": "shelved", "shelve_kind": "dropped"}, nil)
	last := func(code string) store.StatusChange {
		var r store.Requirement
		e.call("GET", "/api/requirements/"+code, nil, &r)
		return r.StatusChanges[len(r.StatusChanges)-1]
	}
	if s := last(r1.Code); s.To != "shelved" || s.Reason != "" || s.ReasonKind != "" || s.Actor != "user" {
		t.Fatalf("用户不写原因搁置应照常、无原因：%+v", s)
	}
	if s := last(r2.Code); s.ReasonKind != "duplicate" || s.Reason != "和 R-1 重复" {
		t.Fatalf("搁置原因应进状态历史：%+v", s)
	}
	if s := last(r3.Code); s.ReasonKind != "dropped" || s.Reason != "" {
		t.Fatalf("只选类别不写一句话也行：%+v", s)
	}
	// 只有搁置带原因：别的状态变化即使带了 shelve_reason 也不记
	var r4 store.Requirement
	e.call("POST", "/api/requirements", map[string]any{"title": "不搁置", "status": "ready"}, &r4)
	e.call("PUT", "/api/requirements/"+r4.Code, map[string]any{"title": "不搁置", "status": "in_progress", "shelve_reason": "不该记"}, nil)
	if s := last(r4.Code); s.Reason != "" {
		t.Fatalf("非搁置不记原因：%+v", s)
	}

	// Agent 搁置必须写原因
	var r5 store.Requirement
	if err := c.Do(ctx, "POST", "/agent/requirements", map[string]any{"title": "Agent 搁置", "session_key": key}, &r5); err != nil {
		t.Fatal(err)
	}
	if err := c.Do(ctx, "POST", "/agent/requirements/"+r5.Code+"/status", map[string]any{"status": "shelved", "session_key": key}, nil); err == nil || !strings.Contains(err.Error(), "reason") {
		t.Fatalf("Agent 搁置不写原因应被拒：%v", err)
	}
	if err := c.Do(ctx, "POST", "/agent/requirements/"+r5.Code+"/status", map[string]any{"status": "shelved", "session_key": key, "reason": "做重了，改在别的需求上", "reason_kind": "duplicate"}, nil); err != nil {
		t.Fatal(err)
	}
	if s := last(r5.Code); s.Actor != "agent" || s.ReasonKind != "duplicate" || s.Reason != "做重了，改在别的需求上" {
		t.Fatalf("Agent 搁置原因应进状态历史：%+v", s)
	}
	// 其他状态不要求原因
	var r6 store.Requirement
	if err := c.Do(ctx, "POST", "/agent/requirements", map[string]any{"title": "Agent 改状态", "session_key": key}, &r6); err != nil {
		t.Fatal(err)
	}
	if err := c.Do(ctx, "POST", "/agent/requirements/"+r6.Code+"/status", map[string]any{"status": "blocked", "session_key": key}, nil); err != nil {
		t.Fatalf("非搁置不要求原因：%v", err)
	}
	// 按需求查状态历史带出原因（参谋复盘用）
	var sc struct {
		Changes []store.StatusChange `json:"changes"`
	}
	e.call("GET", "/api/status-changes?requirement="+r2.Code, nil, &sc)
	if n := len(sc.Changes); n == 0 || sc.Changes[n-1].Reason != "和 R-1 重复" {
		t.Fatalf("状态历史接口应带原因：%+v", sc.Changes)
	}
}
