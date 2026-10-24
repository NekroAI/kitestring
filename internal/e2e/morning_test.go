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

type morningResp struct {
	Reports []struct {
		ProjectID string             `json:"project_id"`
		Since     time.Time          `json:"since"`
		Grant     *store.Grant       `json:"grant"`
		Note      *store.MorningNote `json:"note"`
		Done      []struct {
			RequirementCode string `json:"requirement_code"`
			Status          string `json:"status"`
			Deliveries      []struct {
				Kind, Text, Link string
			} `json:"deliveries"`
		} `json:"done"`
		Tells     []struct{ Text string } `json:"tells"`
		TellCount int                     `json:"tell_count"`
		Proxied   []store.Decision        `json:"proxied"`
		Releases  []struct {
			Code, Role, Reason, By string
			RequirementCode        string `json:"requirement_code"`
			Withdrawn              bool
		} `json:"releases"`
		Pending int      `json:"pending"`
		Blocked []string `json:"blocked"`
		Review  int      `json:"review"`
	} `json:"reports"`
	Advice []store.Advice `json:"advice"`
	Holds  []struct {
		DecisionCode string `json:"decision_code"`
		Kind         string `json:"kind"`
		Reason       string `json:"reason"`
	} `json:"holds"`
}

// 参谋小结（R-165）：没开窗口、没写总述时不出小结；只有 Adv 能写总述和验收建议；
// 开窗口后离开期间从窗口开启算起，汇总代答、放行、交了证据的需求、团队消息，③④ 只给计数；
// 待验收卡上挂参谋建议（重新交证据后旧建议作废），决策卡上挂没代答的原因（硬规则 / 超出授权）。
func TestMorningReport(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	c := client.New(10 * time.Second)
	ctx := context.Background()
	for _, sid := range []string{"pm", "adv", "dev"} {
		hook(t, "claude", map[string]any{"session_id": sid, "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "上线", "prompt_id": "p0"})
	}
	e.waitFor("会话入库", func() bool { return e.session("claude:dev").ProjectID != "" })
	for k, r := range map[string]string{"claude:pm": "pm", "claude:adv": "advisor", "claude:dev": "dev"} {
		if err := c.Do(ctx, "POST", "/agent/sessions/role", map[string]any{"session_key": k, "role": r}, nil); err != nil {
			t.Fatal(err)
		}
	}
	project := e.session("claude:dev").ProjectID
	get := func() morningResp {
		var out morningResp
		e.call("GET", "/api/morning?project_id="+project, nil, &out)
		return out
	}
	write := func(key string, body map[string]any) error {
		body["session_key"] = key
		return c.Do(ctx, "POST", "/agent/morning", body, nil)
	}
	mustFail := func(err error, want, what string) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s：应报「%s」，实际 %v", what, want, err)
		}
	}

	// 1. 没窗口、没总述：不出小结；只有 Adv 能写
	if m := get(); len(m.Reports) != 0 {
		t.Fatalf("没开窗口、没写总述时不应有小结：%+v", m.Reports)
	}
	mustFail(write("claude:dev", map[string]any{"text": "开发不该能写"}), "只有项目的 Adv", "Dev 写小结")
	mustFail(write("claude:pm", map[string]any{"text": "PM 不该能写"}), "只有项目的 Adv", "PM 写小结")
	mustFail(write("claude:adv", map[string]any{}), "什么都没写", "空写入")
	if err := write("claude:adv", map[string]any{"text": "  昨晚的总述  "}); err != nil {
		t.Fatal(err)
	}
	m := get()
	if len(m.Reports) != 1 || m.Reports[0].Note == nil || m.Reports[0].Note.Text != "昨晚的总述" || m.Reports[0].Grant != nil {
		t.Fatalf("只写了总述时小结应出现、正文去掉首尾空白：%+v", m.Reports)
	}
	if d := time.Since(m.Reports[0].Since); d < 11*time.Hour || d > 13*time.Hour {
		t.Fatalf("没开窗口时离开期间应从总述往前 12 小时算起：%v", d)
	}

	// 2. 开窗口：离开期间从窗口开启算起，之前写的总述不算
	time.Sleep(20 * time.Millisecond)
	var g store.Grant
	e.call("POST", "/api/grants", map[string]any{"project_id": project, "session_key": "claude:adv", "max_risk": "low", "expires_at": time.Now().Add(time.Hour).Format(time.RFC3339Nano)}, &g)
	m = get()
	if len(m.Reports) != 1 || m.Reports[0].Grant == nil || m.Reports[0].Grant.Code != g.Code || m.Reports[0].Note != nil || !m.Reports[0].Since.Equal(g.CreatedAt) {
		t.Fatalf("开窗口后离开期间应从窗口开启算起、旧总述不显示：%+v", m.Reports)
	}

	// 3. 离开期间：交验收（带提交）、放行草稿、代答、受阻、团队消息
	var r1 store.Requirement
	e.call("POST", "/api/requirements", map[string]any{"title": "导出 CSV", "project_id": project, "status": "in_progress"}, &r1)
	if err := c.Do(ctx, "POST", "/agent/requirements/"+r1.Code+"/evidence", map[string]any{"kind": "commit", "summary": "提交 abc1234：导出 CSV", "link": "abc1234", "session_key": "claude:dev"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.Do(ctx, "POST", "/agent/requirements/"+r1.Code+"/status", map[string]any{"status": "review", "session_key": "claude:dev"}, nil); err != nil {
		t.Fatal(err)
	}
	var r2, r3 store.Requirement
	if err := c.Do(ctx, "POST", "/agent/requirements", map[string]any{"title": "优惠券入口", "session_key": "claude:adv"}, &r2); err != nil {
		t.Fatal(err)
	}
	e.allowRelease("claude:adv")
	if err := c.Do(ctx, "POST", "/agent/requirements/"+r2.Code+"/status", map[string]any{"status": "ready", "session_key": "claude:adv", "reason": "做法明确"}, nil); err != nil {
		t.Fatal(err)
	}
	e.call("POST", "/api/requirements", map[string]any{"title": "积分规则", "project_id": project, "status": "in_progress"}, &r3)
	if err := c.Do(ctx, "POST", "/agent/requirements/"+r3.Code+"/status", map[string]any{"status": "blocked", "session_key": "claude:dev"}, nil); err != nil {
		t.Fatal(err)
	}
	raise := func(body map[string]any) store.Decision {
		t.Helper()
		body["session_key"] = "claude:dev"
		var d store.Decision
		if err := c.Do(ctx, "POST", "/agent/decisions", body, &d); err != nil {
			t.Fatal(err)
		}
		return d
	}
	d1 := raise(map[string]any{"question": "列表默认倒序？", "risk": "low", "options": []string{"倒序", "正序"}})
	d2 := raise(map[string]any{"question": "要不要改公开接口？", "risk": "high"})
	d3 := raise(map[string]any{"question": "批准这个方案？", "approval": true, "risk": "low"})
	d4 := raise(map[string]any{"question": "按钮文案用「导出」？", "risk": "low"})
	if err := c.Do(ctx, "POST", "/agent/decisions/"+d1.Code+"/answer", map[string]any{"session_key": "claude:adv", "answer": "倒序", "reason": "和看板一致", "risk": "low"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.Do(ctx, "POST", "/agent/team/tell", map[string]any{"session_key": "claude:dev", "text": "接口文档没写分页，先按 50 条做了"}, nil); err != nil {
		t.Fatal(err)
	}

	m = get()
	rp := m.Reports[0]
	if len(rp.Done) != 1 || rp.Done[0].RequirementCode != r1.Code || rp.Done[0].Status != store.ReqReview || len(rp.Done[0].Deliveries) != 1 || rp.Done[0].Deliveries[0].Link != "abc1234" {
		t.Fatalf("① 应列出交了证据的待验收需求和提交：%+v", rp.Done)
	}
	if len(rp.Proxied) != 1 || rp.Proxied[0].Code != d1.Code || rp.Proxied[0].ProxyReason != "和看板一致" {
		t.Fatalf("② 应列出代答：%+v", rp.Proxied)
	}
	if len(rp.Releases) != 1 || rp.Releases[0].RequirementCode != r2.Code || rp.Releases[0].Role != "advisor" || rp.Releases[0].Reason != "做法明确" || rp.Releases[0].Withdrawn {
		t.Fatalf("② 应列出放行：%+v", rp.Releases)
	}
	if rp.Pending != 3 || rp.Review != 1 || len(rp.Blocked) != 1 || rp.Blocked[0] != r3.Code {
		t.Fatalf("③④ 计数不对：pending %d review %d blocked %v", rp.Pending, rp.Review, rp.Blocked)
	}
	tellOK := false
	for _, x := range rp.Tells {
		tellOK = tellOK || strings.Contains(x.Text, "先按 50 条")
	}
	if !tellOK || rp.TellCount < 2 {
		t.Fatalf("应汇总团队消息（留言 + 交验收通知）：%d %+v", rp.TellCount, rp.Tells)
	}
	holds := map[string]string{}
	for _, h := range m.Holds {
		holds[h.DecisionCode] = h.Kind + "|" + h.Reason
	}
	if !strings.HasPrefix(holds[d2.Code], "scope|超出授权") || !strings.HasPrefix(holds[d3.Code], "hard|硬规则") {
		t.Fatalf("没代答的原因：%v", holds)
	}
	if _, ok := holds[d4.Code]; ok {
		t.Fatalf("范围内、窗口还开着的决策参谋还可以代答，不该标没代答：%v", holds)
	}

	// 4. 验收建议：只给待验收的需求、必须写理由、只有 Adv
	mustFail(write("claude:adv", map[string]any{"advice": []map[string]any{{"requirement": r2.Code, "verdict": "pass", "reason": "好"}}}), "不是待验收", "给非待验收需求写建议")
	mustFail(write("claude:adv", map[string]any{"advice": []map[string]any{{"requirement": r1.Code, "verdict": "pass"}}}), "要写理由", "不写理由")
	mustFail(write("claude:dev", map[string]any{"advice": []map[string]any{{"requirement": r1.Code, "verdict": "pass", "reason": "好"}}}), "只有项目的 Adv", "Dev 写建议")
	if err := write("claude:adv", map[string]any{"text": "R-x 交了验收，代答 1 条", "advice": []map[string]any{{"requirement": r1.Code, "verdict": "pass", "reason": "3/3 达标"}}}); err != nil {
		t.Fatal(err)
	}
	m = get()
	if len(m.Advice) != 1 || m.Advice[0].RequirementCode != r1.Code || m.Advice[0].Verdict != store.AdvicePass || m.Advice[0].Reason != "3/3 达标" || m.Advice[0].By != "claude:adv" {
		t.Fatalf("验收建议应能读到：%+v", m.Advice)
	}
	if m.Reports[0].Note == nil || m.Reports[0].Note.Text != "R-x 交了验收，代答 1 条" {
		t.Fatalf("离开期间写的总述应显示：%+v", m.Reports[0].Note)
	}
	// 重新交证据：旧建议作废
	time.Sleep(20 * time.Millisecond)
	if err := c.Do(ctx, "POST", "/agent/requirements/"+r1.Code+"/evidence", map[string]any{"kind": "test", "summary": "补了手机截图", "session_key": "claude:dev"}, nil); err != nil {
		t.Fatal(err)
	}
	if m = get(); len(m.Advice) != 0 {
		t.Fatalf("重新交证据后旧建议应作废：%+v", m.Advice)
	}
	if err := write("claude:adv", map[string]any{"advice": []map[string]any{{"requirement": r1.Code, "verdict": "return", "reason": "缺暗色截图"}}}); err != nil {
		t.Fatal(err)
	}
	if m = get(); len(m.Advice) != 1 || m.Advice[0].Verdict != store.AdviceReturn {
		t.Fatalf("新建议应覆盖：%+v", m.Advice)
	}
	if err := write("claude:adv", map[string]any{"advice": []map[string]any{{"requirement": r1.Code, "verdict": "clear"}}}); err != nil {
		t.Fatal(err)
	}
	if m = get(); len(m.Advice) != 0 {
		t.Fatalf("clear 应撤掉建议：%+v", m.Advice)
	}

	// 5. 推翻代答、撤回放行：小结里照样列着，并标出已推翻 / 已撤回
	e.call("POST", "/api/decisions/"+d1.Code+"/override", map[string]any{"answer": "正序"}, nil)
	e.call("POST", "/api/requirements/"+r2.Code+"/release/withdraw", map[string]any{}, nil)
	rp = get().Reports[0]
	if len(rp.Proxied) != 1 || rp.Proxied[0].OverriddenAt == nil || rp.Proxied[0].Answer != "正序" {
		t.Fatalf("推翻后代答仍列着并标出推翻：%+v", rp.Proxied)
	}
	if len(rp.Releases) != 1 || !rp.Releases[0].Withdrawn {
		t.Fatalf("撤回后放行应标已撤回：%+v", rp.Releases)
	}

	// 6. 撤销窗口：窗口结束不到 12 小时仍出小结；范围内没答的决策标「授权已结束」
	e.call("POST", "/api/grants/"+g.Code+"/revoke", map[string]any{}, nil)
	m = get()
	if len(m.Reports) != 1 {
		t.Fatal("窗口刚结束，小结应还在")
	}
	ended := false
	for _, h := range m.Holds {
		ended = ended || (h.DecisionCode == d4.Code && h.Kind == "ended")
	}
	if !ended {
		t.Fatalf("窗口结束后范围内没答的决策应标授权已结束：%+v", m.Holds)
	}
	// 清除总述：小结照样由窗口撑着
	if err := write("claude:adv", map[string]any{"text": ""}); err != nil {
		t.Fatal(err)
	}
	if m = get(); len(m.Reports) != 1 || m.Reports[0].Note != nil {
		t.Fatalf("清除总述后不应再有总述：%+v", m.Reports)
	}
}
