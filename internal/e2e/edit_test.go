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

// 改需求内容（R-134）：关联会话、项目 PM / Adv 能改标题 / 描述 / 验收标准 / 优先级 / 类型，每次修改都在时间线留下改前改后；
// 没关联的 Dev 被拒；已完成 / 已搁置的被拒；标题去掉编号前缀；进行中之后改验收标准要带用户已批准的批准类决策。
func TestEditRequirement(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	c := client.New(10 * time.Second)
	ctx := context.Background()
	for _, sid := range []string{"pm", "adv", "dev", "dev2"} {
		hook(t, "claude", map[string]any{"session_id": sid, "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "上线", "prompt_id": "p0"})
	}
	e.waitFor("会话入库", func() bool { return e.session("claude:dev2").ProjectID != "" })
	for k, r := range map[string]string{"claude:pm": "pm", "claude:adv": "advisor", "claude:dev": "dev", "claude:dev2": "dev"} {
		if err := c.Do(ctx, "POST", "/agent/sessions/role", map[string]any{"session_key": k, "role": r}, nil); err != nil {
			t.Fatal(err)
		}
	}
	create := func(title, key string) store.Requirement {
		var r store.Requirement
		if err := c.Do(ctx, "POST", "/agent/requirements", map[string]any{"title": title, "detail": "旧描述", "acceptance": "1. 旧标准", "session_key": key}, &r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	edit := func(ref, key string, body map[string]any) (store.Requirement, error) {
		var r store.Requirement
		body["session_key"] = key
		err := c.Do(ctx, "POST", "/agent/requirements/"+ref+"/edit", body, &r)
		return r, err
	}
	get := func(ref string) store.Requirement {
		var r store.Requirement
		e.call("GET", "/api/requirements/"+ref, nil, &r)
		return r
	}
	edits := func(r store.Requirement) []store.Decision {
		var out []store.Decision
		for _, f := range r.Feedback {
			if f.Kind == store.KindEdit {
				out = append(out, f)
			}
		}
		return out
	}
	mustFail := func(err error, want, what string) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s：应报「%s」，实际 %v", what, want, err)
		}
	}

	// 1. 创建者（Dev，建需求时自动关联）改自己的草稿：标题被清洗，时间线有改前改后
	r1 := create("导出 CSV", "claude:dev")
	got, err := edit(r1.Code, "claude:dev", map[string]any{"title": "R-12：导出 CSV 与 Excel", "detail": "新描述", "acceptance": "1. 新标准", "priority": "P1", "kind": "design", "reason": "方案定稿"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "导出 CSV 与 Excel" || got.Detail != "新描述" || got.Acceptance != "1. 新标准" || got.Priority != "P1" || got.Kind != "design" {
		t.Fatalf("修改没生效或标题没清洗：%+v", got)
	}
	if got.Status != store.ReqDraft {
		t.Fatalf("改内容不应改状态：%s", got.Status)
	}
	es := edits(get(r1.Code))
	if len(es) != 1 {
		t.Fatalf("应有 1 条修改记录：%d", len(es))
	}
	ed := es[0]
	if ed.AssignedBy != "claude:dev" || ed.Context != "dev" || ed.Status != store.DecClosed {
		t.Fatalf("修改记录应记下谁、什么身份、且不送达：%+v", ed)
	}
	if !strings.Contains(ed.Answer, "标题") || !strings.Contains(ed.Answer, "验收标准") || !strings.Contains(ed.Answer, "方案定稿") {
		t.Fatalf("修改说明应写改了哪些和理由：%s", ed.Answer)
	}
	if len(ed.Blocks) != 1 || len(ed.Blocks[0].Rows) != 5 {
		t.Fatalf("应有 5 个字段的改前改后：%+v", ed.Blocks)
	}
	row := ed.Blocks[0].Rows[0]
	if row[0] != "title" || row[2] != "导出 CSV" || row[3] != "导出 CSV 与 Excel" {
		t.Fatalf("标题改前改后不对：%v", row)
	}
	if row := ed.Blocks[0].Rows[4]; row[0] != "kind" || row[2] != "普通" || row[3] != "设计类" {
		t.Fatalf("类型改前改后不对：%v", row)
	}

	// 只给不变的值：没有要改的内容，不留痕
	_, err = edit(r1.Code, "claude:dev", map[string]any{"title": "导出 CSV 与 Excel"})
	mustFail(err, "没有要改的内容", "内容没变")
	// 去完前缀为空的标题保留原样（与新建一致）；空标题拒绝
	_, err = edit(r1.Code, "claude:dev", map[string]any{"title": "  "})
	mustFail(err, "标题不能为空", "空标题")

	// 2. 无权限：没关联的 Dev 被拒；同项目 PM / Adv 能改
	_, err = edit(r1.Code, "claude:dev2", map[string]any{"priority": "P0"})
	mustFail(err, "没有关联这条需求", "没关联的 Dev")
	if _, err := edit(r1.Code, "claude:pm", map[string]any{"priority": "P0", "reason": "用户催"}); err != nil {
		t.Fatalf("PM 应能改：%v", err)
	}
	if _, err := edit(r1.Code, "claude:adv", map[string]any{"detail": "Adv 补充"}); err != nil {
		t.Fatalf("Adv 应能改：%v", err)
	}
	es = edits(get(r1.Code))
	if len(es) != 3 || es[0].AssignedBy != "claude:adv" || es[0].Context != "advisor" || es[1].AssignedBy != "claude:pm" {
		t.Fatalf("PM / Adv 的修改应各留一条，最新在前：%+v", es)
	}
	if _, err := edit(r1.Code, "", map[string]any{"priority": "P3"}); err == nil {
		t.Fatal("不带会话应被拒")
	}

	// 3. 进行中之后改验收标准：要用户已批准的批准类决策；改其它字段照常
	r2 := create("积分规则", "claude:pm")
	e.call("PUT", "/api/requirements/"+r2.Code, map[string]any{"title": r2.Title, "detail": r2.Detail, "acceptance": r2.Acceptance, "status": "ready"}, nil)
	if err := c.Do(ctx, "POST", "/agent/sessions/claude:dev/link", map[string]any{"requirement": r2.Code}, nil); err != nil {
		t.Fatal(err)
	}
	if st := get(r2.Code).Status; st != store.ReqInProgress {
		t.Fatalf("应进行中：%s", st)
	}
	_, err = edit(r2.Code, "claude:dev", map[string]any{"acceptance": "1. 放宽的标准"})
	mustFail(err, "decision_raise", "进行中改验收标准没带批准（提示应给出 decision_raise 示例）")
	if _, err := edit(r2.Code, "claude:dev", map[string]any{"title": "积分规则 v2"}); err != nil {
		t.Fatalf("进行中改标题应直接可以：%v", err)
	}
	// PM 改了在做的需求：通知在做的 Dev
	if _, err := edit(r2.Code, "claude:pm", map[string]any{"detail": "PM 改的描述"}); err != nil {
		t.Fatal(err)
	}
	var decs struct {
		Decisions []store.Decision `json:"decisions"`
	}
	e.call("GET", "/api/decisions", nil, &decs)
	told := false
	for _, d := range decs.Decisions {
		if d.Kind == store.KindTell && d.SessionKey == "claude:dev" && d.RequirementID == r2.ID && strings.Contains(d.Answer, "被 PM") && strings.Contains(d.Answer, "按新的内容做") {
			told = true
		}
	}
	if !told {
		t.Fatal("PM 改了在做的需求应通知在做的 Dev")
	}
	raise := func(body map[string]any) store.Decision {
		var d store.Decision
		body["session_key"] = "claude:dev"
		if err := c.Do(ctx, "POST", "/agent/decisions", body, &d); err != nil {
			t.Fatal(err)
		}
		return d
	}
	answer := func(code, text string) {
		e.call("POST", "/api/decisions/answers", map[string]any{"answers": []map[string]any{{"ref": code, "answer": text}}}, nil)
	}
	dPending := raise(map[string]any{"question": "改验收标准？", "approval": true, "requirement": r2.Code})
	_, err = edit(r2.Code, "claude:dev", map[string]any{"acceptance": "1. 新", "approval": dPending.Code})
	mustFail(err, "还没答复", "批准未答")
	answer(dPending.Code, "拒绝：不能放宽")
	_, err = edit(r2.Code, "claude:dev", map[string]any{"acceptance": "1. 新", "approval": dPending.Code})
	mustFail(err, "没有批准", "被拒绝的批准")
	dPlain := raise(map[string]any{"question": "用哪个？", "options": []string{"A", "B"}, "requirement": r2.Code})
	answer(dPlain.Code, "批准 A")
	_, err = edit(r2.Code, "claude:dev", map[string]any{"acceptance": "1. 新", "approval": dPlain.Code})
	mustFail(err, "不是批准类", "普通决策")
	dOther := raise(map[string]any{"question": "改验收标准？", "approval": true, "requirement": r1.Code})
	answer(dOther.Code, "批准")
	_, err = edit(r2.Code, "claude:dev", map[string]any{"acceptance": "1. 新", "approval": dOther.Code})
	mustFail(err, "不是 "+r2.Code+" 上的决策", "别的需求上的批准")
	dOK := raise(map[string]any{"question": "改验收标准？", "approval": true, "requirement": r2.Code})
	answer(dOK.Code, "批准")
	got, err = edit(r2.Code, "claude:dev", map[string]any{"acceptance": "1. 新", "approval": dOK.Code, "reason": "范围合并"})
	if err != nil {
		t.Fatalf("带用户批准应能改验收标准：%v", err)
	}
	if got.Acceptance != "1. 新" {
		t.Fatalf("验收标准没改：%q", got.Acceptance)
	}
	if es := edits(get(r2.Code)); !strings.Contains(es[0].Answer, dOK.Code) {
		t.Fatalf("修改记录应写凭哪条批准：%s", es[0].Answer)
	}

	// 4. 已完成 / 已搁置的被拒（PM 也不行）
	e.call("PUT", "/api/requirements/"+r2.Code, map[string]any{"title": got.Title, "detail": got.Detail, "acceptance": got.Acceptance, "status": "done"}, nil)
	_, err = edit(r2.Code, "claude:pm", map[string]any{"title": "完成后再改"})
	mustFail(err, "已完成", "已完成的需求")
	if err := c.Do(ctx, "POST", "/agent/requirements/"+r1.Code+"/status", map[string]any{"status": "shelved", "session_key": "claude:dev", "reason": "不做了"}, nil); err != nil {
		t.Fatal(err)
	}
	_, err = edit(r1.Code, "claude:dev", map[string]any{"title": "搁置后再改"})
	mustFail(err, "已搁置", "已搁置的需求")
	if n := len(edits(get(r1.Code))); n != 3 {
		t.Fatalf("被拒的修改不应留痕：%d", n)
	}
}
