package e2e

import (
	"context"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/client"
	"github.com/NekroAI/kitestring/internal/store"
)

// 状态变更历史（R-153）：每次换状态都记一行（从、到、时间、操作者、经由）；放行 / 退回修改与对应记录互相引用；
// 需求详情带历史，API 可按需求 / 时间段查；没有记录的旧需求照常显示。
func TestStatusHistory(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	c := client.New(10 * time.Second)
	ctx := context.Background()
	for _, sid := range []string{"pm", "dev"} {
		hook(t, "claude", map[string]any{"session_id": sid, "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "上线", "prompt_id": "p0"})
	}
	e.waitFor("会话入库", func() bool { return e.session("claude:dev").ProjectID != "" })
	for k, r := range map[string]string{"claude:pm": "pm", "claude:dev": "dev"} {
		if err := c.Do(ctx, "POST", "/agent/sessions/role", map[string]any{"session_key": k, "role": r}, nil); err != nil {
			t.Fatal(err)
		}
	}
	e.allowRelease("claude:pm")
	get := func(ref string) store.Requirement {
		var r store.Requirement
		e.call("GET", "/api/requirements/"+ref, nil, &r)
		return r
	}
	status := func(ref, key, st, reason string) {
		t.Helper()
		if err := c.Do(ctx, "POST", "/agent/requirements/"+ref+"/status", map[string]any{"status": st, "session_key": key, "reason": reason}, nil); err != nil {
			t.Fatal(err)
		}
	}

	// 旧需求：上线前就有、没有任何记录（模拟：建好后清空历史表），详情照常读、历史为空
	var old store.Requirement
	e.call("POST", "/api/requirements", map[string]any{"title": "旧需求", "status": "in_progress"}, &old)
	if _, err := e.store.DB().Exec(`DELETE FROM status_changes WHERE requirement_id = ?`, old.ID); err != nil {
		t.Fatal(err)
	}
	if g := get(old.Code); g.Status != store.ReqInProgress || len(g.StatusChanges) != 0 {
		t.Fatalf("旧需求应照常读出、没有历史：%s %d", g.Status, len(g.StatusChanges))
	}

	start := time.Now().Add(-time.Second)
	// Dev 记下草稿 → PM 放行 → Dev 关联开始做 → 交验收 → 你退回 → 交验收 → 你验收通过
	var r store.Requirement
	if err := c.Do(ctx, "POST", "/agent/requirements", map[string]any{"title": "导出 CSV", "session_key": "claude:dev"}, &r); err != nil {
		t.Fatal(err)
	}
	status(r.Code, "claude:pm", "ready", "做法明确")
	if err := c.Do(ctx, "POST", "/agent/sessions/claude:dev/link", map[string]any{"requirement": r.Code}, nil); err != nil {
		t.Fatal(err)
	}
	status(r.Code, "claude:dev", "review", "")
	e.call("POST", "/api/reviews", map[string]any{"items": []map[string]any{{"ref": r.Code, "verdict": "revise", "comment": "文件名带上日期"}}}, nil)
	status(r.Code, "claude:dev", "review", "")
	e.call("POST", "/api/reviews", map[string]any{"items": []map[string]any{{"ref": r.Code, "verdict": "accept"}}}, nil)

	g := get(r.Code)
	type want struct{ from, to, actor, session, role, via string }
	wants := []want{
		{"", "draft", "agent", "claude:dev", "dev", "create"},
		{"draft", "ready", "agent", "claude:pm", "pm", "release"},
		{"ready", "in_progress", "agent", "claude:dev", "dev", "link"},
		{"in_progress", "review", "agent", "claude:dev", "dev", "agent"},
		{"review", "in_progress", "user", "", "", "revise"},
		{"in_progress", "review", "agent", "claude:dev", "dev", "agent"},
		{"review", "done", "user", "", "", "accept"},
	}
	if len(g.StatusChanges) != len(wants) {
		t.Fatalf("应有 %d 次状态变化，实际 %d：%+v", len(wants), len(g.StatusChanges), g.StatusChanges)
	}
	for i, w := range wants {
		x := g.StatusChanges[i]
		if x.From != w.from || x.To != w.to || x.Actor != w.actor || x.Session != w.session || x.Role != w.role || x.Via != w.via || x.At.IsZero() {
			t.Fatalf("第 %d 次状态变化不对：want %+v got %+v", i+1, w, x)
		}
	}
	// 放行、退回修改与对应记录互相引用
	var rel, fb string
	for _, f := range g.Feedback {
		switch f.Kind {
		case store.KindRelease:
			rel = f.Code
		case store.KindFeedback:
			fb = f.Code
		}
	}
	if rel == "" || g.StatusChanges[1].Ref != rel {
		t.Fatalf("放行那次状态变化应引用放行记录 %s：%q", rel, g.StatusChanges[1].Ref)
	}
	if fb == "" || g.StatusChanges[4].Ref != fb {
		t.Fatalf("退回那次状态变化应引用修改意见 %s：%q", fb, g.StatusChanges[4].Ref)
	}

	// 你在网页上改状态：操作者是你、经由网页；改回草稿撤回放行时引用那条放行
	var r2 store.Requirement
	if err := c.Do(ctx, "POST", "/agent/requirements", map[string]any{"title": "通知中心", "session_key": "claude:dev"}, &r2); err != nil {
		t.Fatal(err)
	}
	status(r2.Code, "claude:pm", "ready", "做法明确")
	rel2 := get(r2.Code).Release.Code
	e.call("POST", "/api/requirements/"+r2.Code+"/release/withdraw", map[string]any{"reason": "先不做"}, nil)
	e.call("PUT", "/api/requirements/"+r2.Code, map[string]any{"title": "通知中心", "status": "shelved"}, nil)
	h := get(r2.Code).StatusChanges
	if n := len(h); n != 4 || h[2].Via != "withdraw" || h[2].Ref != rel2 || h[2].Actor != "user" || h[3].Via != "web" || h[3].Actor != "user" || h[3].To != "shelved" {
		t.Fatalf("撤回放行与网页改状态记录不对：%+v", h)
	}

	// 按需求、按时间段查
	var out struct {
		Changes []store.StatusChange `json:"changes"`
	}
	e.call("GET", "/api/status-changes?requirement="+r.Code, nil, &out)
	if len(out.Changes) != len(wants) || out.Changes[0].RequirementCode != r.Code {
		t.Fatalf("按需求查应得 %d 条：%d", len(wants), len(out.Changes))
	}
	e.call("GET", "/api/status-changes?since="+url.QueryEscape(start.Format(time.RFC3339)), nil, &out)
	if len(out.Changes) != len(wants)+4 {
		t.Fatalf("按时间段查应只含窗口内的 %d 条（不含旧需求）：%d", len(wants)+4, len(out.Changes))
	}
	e.call("GET", "/api/status-changes?until="+url.QueryEscape(start.Add(-time.Hour).Format(time.RFC3339)), nil, &out)
	if len(out.Changes) != 0 {
		t.Fatalf("窗口之前不应有记录：%d", len(out.Changes))
	}
	if code := e.callStatus("GET", "/api/status-changes?since=昨天", nil); code != 400 {
		t.Fatalf("时间写错应 400：%d", code)
	}
	// Agent 也能查（参谋复盘用）
	var aout struct {
		Changes []store.StatusChange `json:"changes"`
	}
	if err := c.Do(ctx, "GET", "/agent/status-changes?requirement="+r2.Code, nil, &aout); err != nil || len(aout.Changes) != 4 {
		t.Fatalf("Agent 按需求查应得 4 条：%v %d", err, len(aout.Changes))
	}
}
