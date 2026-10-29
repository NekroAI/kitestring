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

// 决策挂在需求的演进上（R-241）：
//   - decision_raise 没带需求、会话只关联一条没完成的需求时自动挂上；写 none、关联了多条、只剩已完成的都不挂；
//   - 按决策改需求：req_edit 带 from_decision（或 approval）声明依据，修改理由里明写编号的老数据也认；放行理由里写了编号的同样连上；
//     后果链里列出「因此改了…」，声明了别的决策时以声明为准，「接着」只列决策，不列修改记录。
func TestDecisionOnRequirement(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	c := client.New(10 * time.Second)
	ctx := context.Background()
	for _, sid := range []string{"dev", "pm"} {
		hook(t, "claude", map[string]any{"session_id": sid, "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "上线", "prompt_id": "p0"})
	}
	e.waitFor("会话入库", func() bool { return e.session("claude:pm").ProjectID != "" })
	if err := c.Do(ctx, "POST", "/agent/sessions/role", map[string]any{"session_key": "claude:pm", "role": "pm"}, nil); err != nil {
		t.Fatal(err)
	}
	raise := func(key string, body map[string]any) store.Decision {
		t.Helper()
		body["session_key"] = key
		var d store.Decision
		if err := c.Do(ctx, "POST", "/agent/decisions", body, &d); err != nil {
			t.Fatal(err)
		}
		return d
	}
	create := func(title, key string) store.Requirement {
		t.Helper()
		var r store.Requirement
		if err := c.Do(ctx, "POST", "/agent/requirements", map[string]any{"title": title, "acceptance": "1. 旧标准", "session_key": key}, &r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	chain := func(code string) store.DecisionChain {
		var ch store.DecisionChain
		e.call("GET", "/api/decisions/"+code+"/chain", nil, &ch)
		return ch
	}

	// 没关联需求：不挂
	if d := raise("claude:dev", map[string]any{"question": "先做哪个"}); d.RequirementID != "" {
		t.Fatalf("没关联需求时不该挂：%+v", d.RequirementCode)
	}
	r1 := create("导出 CSV", "claude:dev") // 建需求时自动关联
	d1 := raise("claude:dev", map[string]any{"question": "导出用逗号还是分号"})
	if d1.RequirementCode != r1.Code {
		t.Fatalf("只做一条需求时应自动挂到 %s：%q", r1.Code, d1.RequirementCode)
	}
	if d := raise("claude:dev", map[string]any{"question": "做完这些要不要上线", "requirement": "none"}); d.RequirementID != "" {
		t.Fatalf("写 none 不该挂：%s", d.RequirementCode)
	}
	r2 := create("导出 Excel", "claude:dev")
	if d := raise("claude:dev", map[string]any{"question": "表头要不要冻结"}); d.RequirementID != "" {
		t.Fatalf("关联了两条没完成的需求时不该自动挂：%s", d.RequirementCode)
	}
	// r1 搁置后只剩 r2 没完成：又只在做一条
	if err := c.Do(ctx, "POST", "/agent/requirements/"+r1.Code+"/status", map[string]any{"session_key": "claude:dev", "status": "shelved", "reason": "合并进 " + r2.Code}, nil); err != nil {
		t.Fatal(err)
	}
	if d := raise("claude:dev", map[string]any{"question": "Excel 要不要多个工作表"}); d.RequirementCode != r2.Code {
		t.Fatalf("已搁置的不算，应自动挂到 %s：%q", r2.Code, d.RequirementCode)
	}

	// 按决策改需求：声明 from_decision
	q := raise("claude:pm", map[string]any{"question": "导出的范围怎么定", "requirement": r2.Code, "options": []string{"只导当前页", "全部导出"}})
	e.call("POST", "/api/decisions/answers", map[string]any{"answers": []map[string]any{{"ref": q.Code, "answer": "全部导出"}}}, nil)
	edit := func(body map[string]any) error {
		body["session_key"] = "claude:pm"
		return c.Do(ctx, "POST", "/agent/requirements/"+r2.Code+"/edit", body, nil)
	}
	if err := edit(map[string]any{"acceptance": "1. 全部导出", "reason": "按用户的答复", "from_decision": q.Code}); err != nil {
		t.Fatal(err)
	}
	if err := edit(map[string]any{"detail": "x", "from_decision": "D-9999"}); err == nil || !strings.Contains(err.Error(), "from_decision") {
		t.Fatalf("依据的决策不存在应被拒：%v", err)
	}
	// 老写法：理由里明写编号；声明了别的决策时以声明为准（理由里提到 q 也不算 q 的）
	if err := edit(map[string]any{"title": "导出 Excel（全部）", "reason": "用户在 " + q.Code + " 选了全部导出"}); err != nil {
		t.Fatal(err)
	}
	other := raise("claude:pm", map[string]any{"question": "要不要加 PDF", "requirement": r2.Code})
	if err := edit(map[string]any{"priority": "P1", "reason": "参考 " + q.Code, "from_decision": other.Code}); err != nil {
		t.Fatal(err)
	}
	ch := chain(q.Code)
	if len(ch.Edits) != 2 || !ch.Edits[0].Declared || ch.Edits[1].Declared || ch.Edits[0].ReqCode != r2.Code {
		t.Fatalf("%s 应连着两次修改（第一次声明的，第二次理由里写的）：%+v", q.Code, ch.Edits)
	}
	if f := ch.Edits[0].Fields; len(f) != 1 || f[0] != "验收标准" {
		t.Fatalf("应写明改了验收标准：%v", f)
	}
	if len(ch.Followups) != 0 {
		t.Fatalf("修改记录不算「接着」的决策：%+v", ch.Followups)
	}
	if co := chain(other.Code); len(co.Edits) != 1 || co.Edits[0].Fields[0] != "优先级" {
		t.Fatalf("声明了 %s 的修改应连在它下面：%+v", other.Code, co.Edits)
	}
}
