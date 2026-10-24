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

type aideRoleOut struct {
	Role    string   `json:"role"`
	Label   string   `json:"label"`
	Name    string   `json:"name"`
	Prompt  string   `json:"prompt"`
	Demoted []string `json:"demoted"`
}

// 主助理（R-190）：ks role aide 声明，每项目唯一，接任时旧的卸任并收到通知；中文名、别称（参谋、接管 PM、当顾问……）都能识别；
// 旧标识 pm / dev / advisor / reviewer 照旧可用；主助理默认订阅 decision / review、常驻；
// 代答、放行、写小结 / 验收建议只给主助理，顾问调用被拒；没有主助理时代答、顾问放行不可用；
// 主助理卸任，绑在它身上的授权窗口失效。
func TestAideRole(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	c := client.New(30 * time.Second)
	ctx := context.Background()
	for _, sid := range []string{"a1", "a2", "adv", "pm", "dev", "x"} {
		hook(t, "claude", map[string]any{"session_id": sid, "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "上线", "prompt_id": "p0"})
	}
	e.waitFor("会话入库", func() bool { return e.session("claude:x").ProjectID != "" })
	setRole := func(key, role string) (aideRoleOut, error) {
		var out aideRoleOut
		err := c.Do(ctx, "POST", "/agent/sessions/role", map[string]any{"session_key": key, "role": role}, &out)
		return out, err
	}
	mustRole := func(key, role string) aideRoleOut {
		t.Helper()
		out, err := setRole(key, role)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	mustFail := func(err error, want, what string) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s：应报「%s」，实际 %v", what, want, err)
		}
	}

	// 1. 别称识别 + 旧标识兼容：说法都映射到标识，网页上看到的是标识
	for say, want := range map[string]string{
		"参谋": "aide", "做主助理": "aide", "Aide": "aide", "当 助理": "aide",
		"接管 PM": "pm", "主调度": "pm", "项目经理": "pm",
		"当顾问": "advisor", "Adv": "advisor",
		"评审": "reviewer", "审查员": "reviewer", "做开发": "dev", "程序员": "dev",
		"pm": "pm", "dev": "dev", "advisor": "advisor", "reviewer": "reviewer",
	} {
		out := mustRole("claude:x", say)
		if out.Role != want || e.session("claude:x").Role != want {
			t.Fatalf("「%s」应识别为 %s，得到 %+v", say, want, out)
		}
	}
	if _, err := setRole("claude:x", "厨师"); err == nil {
		t.Fatal("认不出的身份应报错")
	}
	mustRole("claude:x", "none")

	// 2. 声明主助理：提示词写明职责与别称，显示名 Aide / 主助理
	out := mustRole("claude:a1", "aide")
	if out.Label != "Aide" || out.Name != "主助理" || len(out.Demoted) != 0 {
		t.Fatalf("主助理身份信息不对：%+v", out)
	}
	for _, want := range []string{"你的会话身份：主助理（Aide）", "参谋", "away_set", "decision_answer", "主调度（PM）", "子任务"} {
		if !strings.Contains(out.Prompt, want) {
			t.Fatalf("主助理提示词缺少 %q：%s", want, out.Prompt)
		}
	}
	if p := e.session("claude:a1").Presence(); p == "" {
		t.Fatal("主助理是常驻身份，应有在线状态")
	}

	// 3. 默认订阅：开发提决策 → 主助理被唤醒；顾问（不常驻、不订阅）不被唤醒
	mustRole("claude:dev", "dev")
	mustRole("claude:adv", "advisor")
	aW, advW := parkWait(t, e, c, "claude:a1"), parkWait(t, e, c, "claude:adv")
	var d store.Decision
	if err := c.Do(ctx, "POST", "/agent/decisions", map[string]any{"session_key": "claude:dev", "question": "列表按时间倒序？", "risk": "low", "options": []string{"倒序", "正序"}}, &d); err != nil {
		t.Fatal(err)
	}
	woke(t, aW, "订阅的事件", "decision", d.Code)
	asleep(t, advW, "顾问（不订阅事件）")
	if tellCount(e, "claude:adv") != 0 {
		t.Fatal("项目有主助理时，顾问不应收到身份调整提示")
	}

	// 4. 顾问调用主助理的职责被拒：代答、放行、写小结 / 验收建议
	e.allowRelease("claude:pm")
	var r store.Requirement
	if err := c.Do(ctx, "POST", "/agent/requirements", map[string]any{"title": "导出 CSV", "session_key": "claude:adv"}, &r); err != nil {
		t.Fatal(err)
	}
	err := c.Do(ctx, "POST", "/agent/requirements/"+r.Code+"/status", map[string]any{"status": "ready", "session_key": "claude:adv", "reason": "范围清楚"}, nil)
	mustFail(err, "放行草稿是主助理的职责", "顾问放行")
	err = c.Do(ctx, "POST", "/agent/morning", map[string]any{"session_key": "claude:adv", "text": "顾问写小结"}, nil)
	mustFail(err, "是主助理的职责", "顾问写小结")
	if code, body := e.webTry("POST", "/api/grants", map[string]any{"project_id": r.ProjectID, "session_key": "claude:adv", "max_risk": "low", "expires_at": time.Now().Add(time.Hour)}); code != 400 || !strings.Contains(body, "主助理") {
		t.Fatalf("不能授权顾问：%d %s", code, body)
	}
	var g store.Grant
	e.call("POST", "/api/grants", map[string]any{"project_id": r.ProjectID, "session_key": "claude:a1", "max_risk": "low", "expires_at": time.Now().Add(time.Hour).Format(time.RFC3339Nano)}, &g)
	err = c.Do(ctx, "POST", "/agent/decisions/"+d.Code+"/answer", map[string]any{"session_key": "claude:adv", "answer": "倒序", "reason": "和看板一致", "risk": "low"}, nil)
	mustFail(err, "代答决策是主助理的职责", "顾问代答")
	// 主助理能放行、能写小结
	if err := c.Do(ctx, "POST", "/agent/requirements/"+r.Code+"/status", map[string]any{"status": "ready", "session_key": "claude:a1", "reason": "范围清楚"}, nil); err != nil {
		t.Fatalf("主助理应能放行：%v", err)
	}
	if err := c.Do(ctx, "POST", "/agent/morning", map[string]any{"session_key": "claude:a1", "text": "导出 CSV 已放行"}, nil); err != nil {
		t.Fatalf("主助理应能写小结：%v", err)
	}

	// 5. 唯一性：a2 接任主助理 → a1 卸任、被唤醒收到通知；a1 名下的授权窗口失效
	drainQuiet(t, parkWait(t, e, c, "claude:a1"), "主助理（授权窗口变化只告知）", "a1", "授权窗口") // 先收掉放行开关、开窗口的消息（只告知，不叫醒，R-227）
	aW = parkWait(t, e, c, "claude:a1")
	out = mustRole("claude:a2", "参谋")
	if out.Role != "aide" || len(out.Demoted) != 1 || out.Demoted[0] != "claude:a1" {
		t.Fatalf("接任主助理应降级 a1：%+v", out)
	}
	woke(t, aW, "主助理（Aide）身份已由", "接任")
	if a, b := e.session("claude:a1").Role, e.session("claude:a2").Role; a != "" || b != "aide" {
		t.Fatalf("身份应为 旧=空 新=aide，得到 %q %q", a, b)
	}
	var gs struct {
		Grants []store.Grant `json:"grants"`
	}
	e.call("GET", "/api/grants?project_id="+r.ProjectID, nil, &gs)
	if len(gs.Grants) == 0 || gs.Grants[0].Code != g.Code || gs.Grants[0].Status != store.GrantRevoked {
		t.Fatalf("主助理卸任后它名下的窗口应失效：%+v", gs.Grants)
	}
	err = c.Do(ctx, "POST", "/agent/decisions/"+d.Code+"/answer", map[string]any{"session_key": "claude:a2", "answer": "倒序", "reason": "和看板一致", "risk": "low"}, nil)
	mustFail(err, "没有开授权窗口", "新主助理没开窗口时代答")

	// 6. 没有主助理：代答不可用（只有主助理能代答），顾问照样不能放行，事情留给用户
	mustRole("claude:a2", "none")
	err = c.Do(ctx, "POST", "/agent/decisions/"+d.Code+"/answer", map[string]any{"session_key": "claude:adv", "answer": "倒序", "reason": "和看板一致", "risk": "low"}, nil)
	mustFail(err, "主助理的职责", "没有主助理时顾问代答")
	var r2 store.Requirement
	if err := c.Do(ctx, "POST", "/agent/requirements", map[string]any{"title": "积分规则", "session_key": "claude:adv"}, &r2); err != nil {
		t.Fatal(err)
	}
	err = c.Do(ctx, "POST", "/agent/requirements/"+r2.Code+"/status", map[string]any{"status": "ready", "session_key": "claude:adv", "reason": "范围清楚"}, nil)
	mustFail(err, "放行草稿是主助理的职责", "没有主助理时顾问放行")
	var got store.Decision
	if err := c.Do(ctx, "GET", "/agent/decisions/"+d.Code, nil, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status != store.DecPending {
		t.Fatalf("没人代答，决策应仍等用户：%s", got.Status)
	}
}

// 过渡迁移（R-190）：项目还没有主助理时，常驻（挂 ks wait）的顾问收到一次身份调整提示，再挂 ks wait 不重复；有主助理时不发。
func TestAideHintForAdvisor(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	c := client.New(30 * time.Second)
	ctx := context.Background()
	for _, sid := range []string{"adv", "adv2", "aide"} {
		hook(t, "claude", map[string]any{"session_id": sid, "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "上线", "prompt_id": "p0"})
	}
	e.waitFor("会话入库", func() bool { return e.session("claude:aide").ProjectID != "" })
	for _, k := range []string{"claude:adv", "claude:adv2"} {
		if err := c.Do(ctx, "POST", "/agent/sessions/role", map[string]any{"session_key": k, "role": "advisor"}, nil); err != nil {
			t.Fatal(err)
		}
	}
	woke(t, parkWait(t, e, c, "claude:adv"), "代答、放行、验收建议、离开期间小结已移到主助理", "ks role aide")
	if n := tellCount(e, "claude:adv"); n != 1 {
		t.Fatalf("应只有一条提示：%d", n)
	}
	asleep(t, parkWait(t, e, c, "claude:adv"), "顾问再挂 ks wait（已提示过）")
	if n := tellCount(e, "claude:adv"); n != 1 {
		t.Fatalf("同一会话只提示一次：%d", n)
	}
	// 有了主助理：另一个顾问常驻也不再提示
	if err := c.Do(ctx, "POST", "/agent/sessions/role", map[string]any{"session_key": "claude:aide", "role": "aide"}, nil); err != nil {
		t.Fatal(err)
	}
	asleep(t, parkWait(t, e, c, "claude:adv2"), "有主助理时的顾问")
	if n := tellCount(e, "claude:adv2"); n != 0 {
		t.Fatalf("有主助理时不应提示顾问：%d", n)
	}
}

// 部署后的存量迁移（R-190）：Hub 启动时，绑在顾问会话上的生效授权窗口失效；项目没有主助理时，持有窗口的顾问收到一次提示，重启不重复。
func TestAideStartupMigration(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	c := client.New(30 * time.Second)
	ctx := context.Background()
	hook(t, "claude", map[string]any{"session_id": "adv", "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "上线", "prompt_id": "p0"})
	e.waitFor("会话入库", func() bool { return e.session("claude:adv").ProjectID != "" })
	if err := c.Do(ctx, "POST", "/agent/sessions/role", map[string]any{"session_key": "claude:adv", "role": "advisor"}, nil); err != nil {
		t.Fatal(err)
	}
	project := e.session("claude:adv").ProjectID
	g, err := e.store.CreateGrant(ctx, project, "claude:adv", "low", time.Now().Add(time.Hour)) // 上线前开给顾问的窗口
	if err != nil {
		t.Fatal(err)
	}
	e.hub.MigrateAide(ctx)
	e.hub.MigrateAide(ctx) // 再启动一次不重复提示
	if cur, _ := e.store.GetGrant(ctx, g.ID); cur.Status != store.GrantRevoked {
		t.Fatalf("顾问名下的窗口应失效：%s", cur.Status)
	}
	text := woke(t, parkWait(t, e, c, "claude:adv"), "已移到主助理", "ks role aide", "已失效")
	if strings.Count(text, "已移到主助理；本项目还没有主助理") != 1 {
		t.Fatalf("提示只应一次：%s", text)
	}
}
