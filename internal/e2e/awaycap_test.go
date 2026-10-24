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

// 离开期间的代答上限（R-210 评审 P1）：代答上限 = 离开时选的范围与旧窗口中较严的一个；
// 离开结束后旧窗口按原到期时间恢复。decision_answer（MCP）与 ks decision answer 都是调同一个 /agent/decisions/{ref}/answer，
// 所以这里走这个接口就覆盖了两个入口；限时请求超时只会拒绝、不写 proxy_by（见最后一段）。
func TestAwayScopeCapsOldGrant(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	c := client.New(10 * time.Second)
	ctx := context.Background()
	for _, sid := range []string{"adv", "dev"} {
		hook(t, "claude", map[string]any{"session_id": sid, "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "上线", "prompt_id": "p0"})
	}
	e.waitFor("会话入库", func() bool { return e.session("claude:dev").ProjectID != "" })
	for k, r := range map[string]string{"claude:adv": "aide", "claude:dev": "dev"} {
		if err := c.Do(ctx, "POST", "/agent/sessions/role", map[string]any{"session_key": k, "role": r}, nil); err != nil {
			t.Fatal(err)
		}
	}
	project := e.session("claude:dev").ProjectID
	raise := func(q, risk string) store.Decision {
		t.Helper()
		var d store.Decision
		if err := c.Do(ctx, "POST", "/agent/decisions", map[string]any{"session_key": "claude:dev", "question": q, "risk": risk}, &d); err != nil {
			t.Fatal(err)
		}
		return d
	}
	answer := func(d store.Decision) error {
		return c.Do(ctx, "POST", "/agent/decisions/"+d.Code+"/answer", map[string]any{"session_key": "claude:adv", "answer": "好", "reason": "理由", "risk": "low", "hard_check": "none"}, nil)
	}
	proxyBy := func(d store.Decision) string {
		t.Helper()
		var got store.Decision
		if err := c.Do(ctx, "GET", "/agent/decisions/"+d.Code, nil, &got); err != nil {
			t.Fatal(err)
		}
		return got.ProxyBy
	}
	mustOK := func(d store.Decision, what string) {
		t.Helper()
		if err := answer(d); err != nil {
			t.Fatalf("%s：应能代答：%v", what, err)
		}
		if proxyBy(d) != "claude:adv" {
			t.Fatalf("%s：proxy_by 应写入", what)
		}
	}
	mustDeny := func(d store.Decision, want, what string) {
		t.Helper()
		if err := answer(d); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s：应被拒（%s），实际 %v", what, want, err)
		}
		if proxyBy(d) != "" {
			t.Fatalf("%s：proxy_by 不应写入", what)
		}
	}

	// 一个 24 小时的旧 medium 窗口，下面几段都靠它
	var old store.Grant
	exp := time.Now().Add(20 * time.Hour)
	e.call("POST", "/api/grants", map[string]any{"project_id": project, "session_key": "claude:adv", "max_risk": "medium", "expires_at": exp.Format(time.RFC3339Nano)}, &old)

	// 没有离开：旧窗口行为不变（低、中可代答，高不行）
	mustOK(raise("按钮文案用「保存」？", "low"), "没离开 · 低风险")
	mustOK(raise("列表每页显示 20 条？", "medium"), "没离开 · 中风险")
	mustDeny(raise("换一种存储格式？", "high"), "超出授权范围", "没离开 · 高风险")

	// 离开 none + 旧 medium：低风险也不代答，proxy_by 不写
	e.call("POST", "/api/away", map[string]any{"expect_minutes": 30, "scope": "none"}, nil)
	low := raise("空状态写「暂无数据」？", "low")
	mustDeny(low, "不代答", "离开 none · 低风险")
	mustDeny(raise("每页显示 30 条？", "medium"), "不代答", "离开 none · 中风险")
	var rep awayReportResp
	e.call("POST", "/api/away/end", nil, &rep)

	// 离开结束：旧窗口按原到期时间恢复（之前被拒的低风险现在可答）
	var gs struct {
		Grants []store.Grant `json:"grants"`
	}
	e.call("GET", "/api/grants", nil, &gs)
	if len(gs.Grants) != 1 || gs.Grants[0].Status != store.GrantActive || gs.Grants[0].MaxRisk != "medium" || !gs.Grants[0].ExpiresAt.Equal(old.ExpiresAt) {
		t.Fatalf("离开结束后旧窗口应原样恢复：%+v", gs.Grants)
	}
	mustOK(low, "离开结束 · 旧窗口恢复（低风险）")
	mustOK(raise("每页显示 40 条？", "medium"), "离开结束 · 旧窗口恢复（中风险）")

	// 离开 low + 旧 medium：低风险可代答，中风险被拒
	e.call("POST", "/api/away", map[string]any{"expect_minutes": 30, "scope": "low"}, nil)
	mustOK(raise("提示语用「已保存」？", "low"), "离开 low · 低风险")
	mid := raise("每页显示 50 条？", "medium")
	mustDeny(mid, "超出授权范围", "离开 low · 中风险")
	e.call("POST", "/api/away/end", nil, &rep)
	mustOK(mid, "离开 low 结束后 · 中风险恢复")

	// 离开 medium + 旧 medium：不变
	e.call("POST", "/api/away", map[string]any{"expect_minutes": 30, "scope": "medium"}, nil)
	mustOK(raise("每页显示 60 条？", "medium"), "离开 medium · 中风险")
	e.call("POST", "/api/away/end", nil, &rep)

	// 限时请求超时：自动处理只拒绝，不写 proxy_by（离开 none 期间也一样）
	e.call("POST", "/api/away", map[string]any{"expect_minutes": 30, "scope": "none"}, nil)
	exp2 := time.Now().Add(300 * time.Millisecond)
	req, err := e.store.CreateDecision(ctx, store.Decision{ProjectID: project, SessionKey: "claude:dev", Kind: store.KindDecision, Question: "批准开 1 个 Dev？", Approval: true, RequestKind: store.RequestDevs, RequestCount: 1, ExpiresAt: &exp2})
	if err != nil {
		t.Fatal(err)
	}
	e.hub.ResumeRequestExpiry(ctx)
	e.waitFor("限时请求超时自动处理", func() bool {
		d, err := e.store.GetDecision(ctx, req.ID)
		return err == nil && d.TimedOut
	})
	if d, _ := e.store.GetDecision(ctx, req.ID); d.ProxyBy != "" || d.Verdict != store.VerdictRejected {
		t.Fatalf("超时只能按规则拒绝、不能写 proxy_by：%+v", d)
	}
}
