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

// 放行（R-166）：项目开关默认关闭；打开后 PM / Adv 能把草稿改为待开始，Dev 不能；必须写理由；
// 时间线记下放行人与理由；你可以撤回（有会话在做时通知它先停下）；PM 派草稿也算放行、同样要理由；done 仍只由你设置。
func TestRelease(t *testing.T) {
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
	create := func(title string) store.Requirement {
		var r store.Requirement
		if err := c.Do(ctx, "POST", "/agent/requirements", map[string]any{"title": title, "session_key": "claude:adv"}, &r); err != nil {
			t.Fatal(err)
		}
		if r.Status != store.ReqDraft {
			t.Fatalf("Agent 记下的需求应是草稿：%s", r.Status)
		}
		return r
	}
	status := func(ref, key, st, reason string) (store.Requirement, error) {
		var r store.Requirement
		err := c.Do(ctx, "POST", "/agent/requirements/"+ref+"/status", map[string]any{"status": st, "session_key": key, "reason": reason}, &r)
		return r, err
	}
	get := func(ref string) store.Requirement {
		var r store.Requirement
		e.call("GET", "/api/requirements/"+ref, nil, &r)
		return r
	}
	releases := func(r store.Requirement) []store.Decision {
		var out []store.Decision
		for _, f := range r.Feedback {
			if f.Kind == store.KindRelease {
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

	r1, r2, r3, r4 := create("导出 CSV"), create("优惠券入口"), create("积分规则"), create("通知中心")

	// 1. 新项目默认关闭：PM 放行、PM 派草稿都被拒绝
	var projects struct {
		Projects []store.Project `json:"projects"`
	}
	e.call("GET", "/api/projects", nil, &projects)
	for _, p := range projects.Projects {
		if p.ID == r1.ProjectID && p.AgentRelease {
			t.Fatal("放行开关默认应关闭")
		}
	}
	_, err := status(r1.Code, "claude:pm", "ready", "做法明确")
	mustFail(err, "先请用户放行", "开关关着时 PM 放行")
	err = c.Do(ctx, "POST", "/agent/requirements/"+r4.Code+"/assign", map[string]any{"session_key": "claude:pm", "to": "claude:dev", "reason": "做法明确"}, nil)
	mustFail(err, "先请用户放行", "开关关着时 PM 派草稿")
	if get(r4.Code).Status != store.ReqDraft {
		t.Fatal("被拒绝的派活不应改状态")
	}

	e.allowRelease("claude:pm")

	// 2. Dev 不能放行；不写理由被拒绝
	_, err = status(r1.Code, "claude:dev", "ready", "我觉得可以")
	mustFail(err, "只有 PM 或 Adv", "Dev 放行")
	_, err = status(r1.Code, "claude:pm", "ready", "  ")
	mustFail(err, "必须写一句理由", "不写理由")
	if get(r1.Code).Status != store.ReqDraft {
		t.Fatal("被拒绝的放行不应改状态")
	}

	// 3. PM 放行：待开始，看板上能读到放行人与理由，时间线有放行记录；放行的会话不被关联
	got, err := status(r1.Code, "claude:pm", "ready", "做法明确，照 R-1 的导入做")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.ReqReady || got.Release == nil || got.Release.By != "claude:pm" || got.Release.Role != "pm" || got.Release.Reason != "做法明确，照 R-1 的导入做" {
		t.Fatalf("PM 放行后应是待开始并带放行信息：%s %+v", got.Status, got.Release)
	}
	for _, k := range got.Sessions {
		if k == "claude:pm" {
			t.Fatal("放行不应把 PM 关联到需求上")
		}
	}
	if rs := releases(get(r1.Code)); len(rs) != 1 || rs[0].AssignedBy != "claude:pm" || rs[0].Answer != "做法明确，照 R-1 的导入做" || rs[0].Status != store.DecClosed {
		t.Fatalf("时间线应有一条放行记录：%+v", rs)
	}
	var list struct {
		Requirements []store.Requirement `json:"requirements"`
	}
	e.call("GET", "/api/requirements", nil, &list)
	for _, r := range list.Requirements {
		if r.Code == r1.Code && (r.Release == nil || r.Release.By != "claude:pm") {
			t.Fatalf("列表里也应带放行人：%+v", r.Release)
		}
	}

	// 4. Adv 能放行；只能从草稿放行，已搁置的不行
	if got, err = status(r2.Code, "claude:adv", "ready", "讨论过，范围清楚"); err != nil || got.Release == nil || got.Release.Role != "advisor" {
		t.Fatalf("Adv 应能放行：%v %+v", err, got.Release)
	}
	if _, err = status(r3.Code, "claude:pm", "shelved", "不做了"); err != nil {
		t.Fatal(err)
	}
	_, err = status(r3.Code, "claude:pm", "ready", "重新打开")
	mustFail(err, "只能放行草稿", "放行已搁置的")
	_, err = status(r1.Code, "claude:pm", "ready", "再放一次")
	mustFail(err, "只能放行草稿", "放行已经待开始的")

	// 5. 你在网页上撤回：改回草稿，时间线标为已撤回
	got = store.Requirement{}
	e.call("POST", "/api/requirements/"+r1.Code+"/release/withdraw", map[string]any{"reason": "再想想"}, &got)
	if got.Status != store.ReqDraft || got.Release != nil {
		t.Fatalf("撤回后应回到草稿、不再显示放行人：%s %+v", got.Status, got.Release)
	}
	if rs := releases(got); len(rs) != 1 || rs[0].CloseReason != "用户撤回：再想想" {
		t.Fatalf("放行记录应标为已撤回：%+v", rs)
	}

	// 6. 有会话在做时撤回：通知它先停下
	if err := c.Do(ctx, "POST", "/agent/sessions/claude:dev/link", map[string]any{"requirement": r2.Code}, nil); err != nil {
		t.Fatal(err)
	}
	if st := get(r2.Code).Status; st != store.ReqInProgress {
		t.Fatalf("开发开工后应是进行中：%s", st)
	}
	got = store.Requirement{}
	e.call("POST", "/api/requirements/"+r2.Code+"/release/withdraw", map[string]any{}, &got)
	if got.Status != store.ReqDraft || releases(got)[0].CloseReason != "用户撤回" {
		t.Fatalf("进行中也能撤回：%s %+v", got.Status, releases(got))
	}
	var decs struct {
		Decisions []store.Decision `json:"decisions"`
	}
	e.call("GET", "/api/decisions", nil, &decs)
	told := false
	for _, d := range decs.Decisions {
		if d.Kind == store.KindTell && d.SessionKey == "claude:dev" && d.RequirementID == r2.ID && strings.Contains(d.Answer, "已被撤回为草稿") && strings.Contains(d.Answer, "先停下") {
			told = true
		}
	}
	if !told {
		t.Fatal("撤回进行中的需求应通知在做的开发先停下")
	}
	// 没有放行记录的不能撤回
	resp, _ := e.web.Post(e.srv.URL+"/api/requirements/"+r2.Code+"/release/withdraw", "application/json", strings.NewReader("{}"))
	if resp.StatusCode != 400 {
		t.Fatalf("没有可撤回的放行应返回 400：%d", resp.StatusCode)
	}
	resp.Body.Close()

	// 7. PM 派草稿 = 顺带放行：必须写理由，留痕；PM 自己改回草稿也算撤回
	err = c.Do(ctx, "POST", "/agent/requirements/"+r4.Code+"/assign", map[string]any{"session_key": "claude:pm", "to": "claude:dev"}, nil)
	mustFail(err, "必须写一句理由", "派草稿不写理由")
	if err := c.Do(ctx, "POST", "/agent/requirements/"+r4.Code+"/assign", map[string]any{"session_key": "claude:pm", "to": "claude:dev", "reason": "小改动，直接做"}, nil); err != nil {
		t.Fatal(err)
	}
	got = get(r4.Code)
	if got.Status != store.ReqReady || got.Release == nil || got.Release.By != "claude:pm" || got.Release.Reason != "小改动，直接做" {
		t.Fatalf("派草稿应记一次放行：%s %+v", got.Status, got.Release)
	}
	hasAssign := false
	for _, f := range got.Feedback {
		hasAssign = hasAssign || f.Kind == store.KindAssign && f.AssignedBy == "claude:pm"
	}
	if !hasAssign || len(releases(got)) != 1 {
		t.Fatalf("时间线应同时有派活与放行：%+v", got.Feedback)
	}
	if got, err = status(r4.Code, "claude:pm", "draft", "先等设计"); err != nil {
		t.Fatal(err)
	}
	if got.Release != nil || !strings.Contains(releases(got)[0].CloseReason, "改回草稿：先等设计") {
		t.Fatalf("PM 改回草稿应算撤回：%+v", releases(got))
	}

	// 8. 验收通过仍只由你：Agent 设 done 被拒，你在网页上验收照常
	if _, err = status(r2.Code, "claude:adv", "ready", "设计定了"); err != nil {
		t.Fatal(err)
	}
	if _, err = status(r2.Code, "claude:dev", "review", ""); err != nil {
		t.Fatal(err)
	}
	_, err = status(r2.Code, "claude:pm", "done", "")
	mustFail(err, "只能由用户", "PM 设为已完成")
	_, err = status(r2.Code, "claude:adv", "done", "")
	mustFail(err, "只能由用户", "Adv 设为已完成")
	e.call("POST", "/api/reviews", map[string]any{"items": []map[string]any{{"ref": r2.Code, "verdict": "accept"}}}, nil)
	if st := get(r2.Code).Status; st != store.ReqDone {
		t.Fatalf("你验收后应是已完成：%s", st)
	}
}

// 关联草稿也是放行路径（R-166 补充）：开关关着时只关联、仍是草稿并提示；开着时记一次放行（理由「开始做 R-x」）再进入进行中；
// 待开始的照旧进入进行中、不另记放行。
func TestLinkDraftRelease(t *testing.T) {
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
	create := func(title string) store.Requirement {
		var r store.Requirement
		if err := c.Do(ctx, "POST", "/agent/requirements", map[string]any{"title": title, "session_key": "claude:pm"}, &r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	type linked struct {
		store.Requirement
		Notices []string `json:"notices"`
	}
	linkBy := func(key, ref string) linked {
		var out linked
		if err := c.Do(ctx, "POST", "/agent/sessions/"+key+"/link", map[string]any{"requirement": ref}, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	link := func(ref string) linked { return linkBy("claude:pm", ref) }
	count := func(r store.Requirement) (n int) {
		for _, f := range r.Feedback {
			if f.Kind == store.KindRelease {
				n++
			}
		}
		return
	}
	r1, r2, r3 := create("导出 CSV"), create("优惠券入口"), create("积分规则")

	// 开关关着：只关联，不改状态，提示先请用户放行
	got := link(r1.Code)
	if got.Status != store.ReqDraft || got.Release != nil || count(got.Requirement) != 0 {
		t.Fatalf("开关关着时关联草稿不应改状态：%s %+v", got.Status, got.Release)
	}
	if len(got.Notices) == 0 || !strings.Contains(got.Notices[0], "先请用户放行") {
		t.Fatalf("应提示先请用户放行：%v", got.Notices)
	}
	linkedPM := false
	for _, k := range got.Sessions {
		linkedPM = linkedPM || k == "claude:pm"
	}
	if !linkedPM {
		t.Fatal("开关关着时仍应关联会话")
	}

	e.allowRelease("claude:pm")

	// 已是待开始的：照旧进入进行中，不另记放行
	if err := c.Do(ctx, "POST", "/agent/requirements/"+r2.Code+"/status", map[string]any{"status": "ready", "session_key": "claude:pm", "reason": "做法明确"}, nil); err != nil {
		t.Fatal(err)
	}
	if got = link(r2.Code); got.Status != store.ReqInProgress || count(got.Requirement) != 1 || len(got.Notices) != 0 {
		t.Fatalf("待开始的关联后应进入进行中且只有原来那条放行：%s %d %v", got.Status, count(got.Requirement), got.Notices)
	}

	// 开关开着，Dev 关联草稿：只关联、不放行，状态不变
	r4 := create("会员等级")
	if got = linkBy("claude:dev", r4.Code); got.Status != store.ReqDraft || got.Release != nil || count(got.Requirement) != 0 {
		t.Fatalf("Dev 关联草稿不应放行：%s %+v", got.Status, got.Release)
	}
	if len(got.Notices) == 0 || !strings.Contains(got.Notices[0], "这条还是草稿，请 PM 放行") {
		t.Fatalf("Dev 关联草稿应提示请 PM 放行：%v", got.Notices)
	}

	// 开关开着，PM 关联草稿：记一次放行（理由「开始做 R-x」），进入进行中；可撤回
	got = link(r3.Code)
	if got.Status != store.ReqInProgress || got.Release == nil || got.Release.By != "claude:pm" || got.Release.Reason != "开始做 "+r3.Code {
		t.Fatalf("开关开着时关联草稿应记放行并进入进行中：%s %+v", got.Status, got.Release)
	}
	var back store.Requirement
	e.call("POST", "/api/requirements/"+r3.Code+"/release/withdraw", map[string]any{}, &back)
	if back.Status != store.ReqDraft || back.Release != nil {
		t.Fatalf("应能撤回关联带出的放行：%s %+v", back.Status, back.Release)
	}
}
