package e2e

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/client"
	"github.com/NekroAI/kitestring/internal/daemon"
	"github.com/NekroAI/kitestring/internal/store"
)

type waitCheckOut struct {
	Retired  bool   `json:"retired"`
	Offline  bool   `json:"offline"`
	Hint     string `json:"hint"`
	MaxWait  string `json:"max_wait"`
	IdleLeft string `json:"idle_left"`
}

// R-272：ks wait 单次等待不超过 55 分钟（项目设置可调）；常驻会话连续空等累计超过项目设置的时长后，
// 超时醒来时收到写交接下线的指示、记为下线（Stop 不再拦、ks wait 直接返回）；有要动手的事或用户发消息时空等重新计时；
// 下线后点名给它的消息照常入队，新会话接任时拿到交接、接手概览和积压的消息。
func TestIdleOffline(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	c := client.New(30 * time.Second)
	ctx := context.Background()
	ev := func(sid, name string, extra map[string]any) string {
		p := map[string]any{"session_id": sid, "cwd": cwd, "hook_event_name": name}
		for k, v := range extra {
			p[k] = v
		}
		return hook(t, "claude", p)
	}
	for _, sid := range []string{"io-pm", "io-aide", "io-new"} {
		ev(sid, "UserPromptSubmit", map[string]any{"prompt": "上线", "prompt_id": "p0"})
	}
	e.waitFor("会话入库", func() bool { return e.session("claude:io-new").ProjectID != "" })
	for _, kr := range [][2]string{{"claude:io-pm", "pm"}, {"claude:io-aide", "aide"}} {
		if err := c.Do(ctx, "POST", "/agent/sessions/role", map[string]any{"session_key": kr[0], "role": kr[1]}, nil); err != nil {
			t.Fatal(err)
		}
	}
	pid := e.session("claude:io-pm").ProjectID
	check := func(after bool) waitCheckOut {
		t.Helper()
		q := "/wait/check?session_key=claude:io-pm"
		if after {
			q += "&after=timeout"
		}
		var out waitCheckOut
		if err := c.Do(ctx, "GET", q, nil, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	// 1. 单次等待上限：默认 55 分钟；--timeout 3h 在 daemon 里也截到 55 分钟；项目设置可调得更短
	if out := check(false); out.MaxWait != "55m0s" || out.IdleLeft != "4h0m0s" {
		t.Fatalf("默认单次等待应为 55 分钟、离下线 4 小时：%+v", out)
	}
	long := make(chan daemon.WaitResult, 1)
	go func() {
		var res daemon.WaitResult
		_ = c.Do(ctx, "GET", "/wait?session_key=claude:io-pm&timeout=3h", nil, &res)
		long <- res
	}()
	e.waitFor("挂起等待", func() bool { return e.session("claude:io-pm").Parked })
	if u := e.session("claude:io-pm").ParkedUntil; u == nil || time.Until(*u) > 56*time.Minute || time.Until(*u) < 50*time.Minute {
		t.Fatalf("--timeout 3h 应被截到 55 分钟：%v", u)
	}
	x := e.session("claude:io-pm")
	if x.IdleWaitSince == nil {
		t.Fatal("挂上 ks wait 应记下空等起点")
	}
	p, err := e.store.GetProject(ctx, pid)
	if err != nil {
		t.Fatal(err)
	}
	e.call("PUT", "/api/projects/"+pid, map[string]any{"name": p.Name, "doc_root": p.DocRoot, "wait_max_minutes": 30, "idle_offline_minutes": 120}, nil)
	if out := check(false); out.MaxWait != "30m0s" {
		t.Fatalf("项目设成 30 分钟后单次等待应为 30 分钟：%+v", out)
	}

	// 2. 有要动手的事（主助理发来团队消息）：等待被叫醒，空等起点清除
	if err := c.Do(ctx, "POST", "/agent/team/tell", map[string]any{"session_key": "claude:io-aide", "to": "pm", "text": "R-1 先做哪个？"}, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case res := <-long:
		if res.Reason != "delivered" {
			t.Fatalf("应被团队消息叫醒：%+v", res)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("团队消息没有叫醒主调度")
	}
	e.waitFor("叫醒后空等重新计时", func() bool { return e.session("claude:io-pm").IdleWaitSince == nil })

	// 3. 用户在终端发消息也重新计时
	short := func() {
		t.Helper()
		var res daemon.WaitResult
		if err := c.Do(ctx, "GET", "/wait?session_key=claude:io-pm&timeout=300ms", nil, &res); err != nil || res.Reason != "timeout" {
			t.Fatalf("短等待应超时：%+v %v", res, err)
		}
		flushed()
	}
	short()
	e.waitFor("超时续等时仍在累计", func() bool { return e.session("claude:io-pm").IdleWaitSince != nil })
	ev("io-pm", "UserPromptSubmit", map[string]any{"prompt": "看看进度", "prompt_id": "p1"})
	e.waitFor("用户发消息后空等重新计时", func() bool { return e.session("claude:io-pm").IdleWaitSince == nil })
	// 后台任务通知（自动消息，例如 ks wait 自己超时）不算
	short()
	ev("io-pm", "UserPromptSubmit", map[string]any{"prompt": "<task-notification>ks wait 结束</task-notification>", "prompt_id": "p2"})
	if e.session("claude:io-pm").IdleWaitSince == nil {
		t.Fatal("自动消息不应打断空等")
	}

	// 4. 没到点：超时复查不下线，照常续等
	if out := check(true); out.Offline {
		t.Fatalf("空等没到点不应下线：%+v", out)
	}
	if err := e.store.ResetIdleWait(ctx, "claude:io-pm", time.Now().Add(-30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if out := check(false); !strings.HasPrefix(out.IdleLeft, "1h29m") && !strings.HasPrefix(out.IdleLeft, "1h30m") {
		t.Fatalf("应按累计空等算出离下线还剩多久：%+v", out)
	}
	// 下线前 Stop 照常拦（常驻会话没挂 ks wait）
	if !strings.Contains(ev("io-pm", "Stop", nil), "block") {
		t.Fatal("下线前没挂 ks wait 结束应被拦")
	}

	// 5. 空等累计到 2 小时（项目设置）：超时复查记为下线，给出写交接下线的指示
	if err := e.store.ResetIdleWait(ctx, "claude:io-pm", time.Now().Add(-2*time.Hour-time.Minute)); err != nil {
		t.Fatal(err)
	}
	out := check(true)
	if !out.Offline || !out.Retired || !strings.Contains(out.Hint, "handoff_write") || !strings.Contains(out.Hint, "不要再运行 ks wait") || !strings.Contains(out.Hint, "ks takeover pm") {
		t.Fatalf("空等到点应下线并给出写交接的指示：%+v", out)
	}
	if x := e.session("claude:io-pm"); x.OfflineAt == nil || x.Role != "pm" {
		t.Fatalf("应记为下线、身份保留：%+v", x)
	}
	// 下线后：ks wait 直接返回；Stop 不再拦，结束这一轮记为空闲而不是等你回复
	if out := check(false); !out.Retired || !strings.Contains(out.Hint, "还没写交接") {
		t.Fatalf("下线后 ks wait 应直接返回并提醒写交接：%+v", out)
	}
	e.waitFor("Stop 不再拦", func() bool { return !strings.Contains(ev("io-pm", "Stop", nil), "block") })
	if st := e.session("claude:io-pm").State; st != store.StateIdle {
		t.Fatalf("下线后结束本轮应记为空闲：%s", st)
	}
	// 主助理收到一条只告知
	e.waitFor("主助理收到下线通知", func() bool {
		for _, d := range tellsTo(e, "claude:io-aide") {
			if d.FYI && strings.Contains(d.Answer, "交接下线") {
				return true
			}
		}
		return false
	})
	// 写交接
	if err := c.Do(ctx, "POST", "/agent/handoff", map[string]any{"session_key": "claude:io-pm", "doing": "R-9 做到联调，分支 io-line", "caveats": "别停 5199"}, nil); err != nil {
		t.Fatal(err)
	}
	if out := check(false); !out.Retired || !strings.Contains(out.Hint, "你已交接下线") {
		t.Fatalf("写完交接后 ks wait 应直接返回：%+v", out)
	}

	// 6. 下线后点名给主调度的消息照常入队（送达状态写明已下线、接任时转给新会话）；终端关掉后仍然入队
	ev("io-pm", "SessionEnd", nil)
	e.waitFor("旧会话结束", func() bool { return e.session("claude:io-pm").State == store.StateEnded })
	var told struct {
		Delivered  bool   `json:"delivered"`
		To         string `json:"to"`
		Message    string `json:"message"`
		Recipients []struct {
			Status string `json:"status"`
			Reason string `json:"reason"`
		} `json:"recipients"`
	}
	if err := c.Do(ctx, "POST", "/agent/team/tell", map[string]any{"session_key": "claude:io-aide", "to": "pm", "text": "R-9 用户说先别合"}, &told); err != nil {
		t.Fatal(err)
	}
	if !told.Delivered || told.To != "claude:io-pm" || len(told.Recipients) != 1 || !strings.Contains(told.Recipients[0].Reason, "已下线") {
		t.Fatalf("下线后点名消息应照常入队：%+v", told)
	}
	// 会话面板 / 工作台：会话带 offline_at
	var detail struct {
		Session  store.Session   `json:"session"`
		Handoffs []store.Handoff `json:"handoffs"`
	}
	e.call("GET", "/api/sessions/claude:io-pm", nil, &detail)
	if detail.Session.OfflineAt == nil || len(detail.Handoffs) != 1 || detail.Handoffs[0].Status != store.HandoffWritten {
		t.Fatalf("会话详情应带下线时间和写好的交接：%+v %+v", detail.Session.OfflineAt, detail.Handoffs)
	}

	// 7. 新会话接任：交接、接手概览、积压消息都交给它；旧会话卸任、下线标记清除
	var role struct {
		Prompt  string   `json:"prompt"`
		Demoted []string `json:"demoted"`
		Handoff bool     `json:"handoff"`
	}
	if err := c.Do(ctx, "POST", "/agent/sessions/role", map[string]any{"session_key": "claude:io-new", "role": "pm"}, &role); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"## 交接", "R-9 做到联调", "别停 5199", "接手概览", "积压的 1 条"} {
		if !strings.Contains(role.Prompt, want) {
			t.Fatalf("接任提示词应含 %q：%s", want, role.Prompt)
		}
	}
	if !role.Handoff || len(role.Demoted) != 1 || role.Demoted[0] != "claude:io-pm" {
		t.Fatalf("应接任下线的主调度：%+v", role)
	}
	if old := e.session("claude:io-pm"); old.Role != "" || old.RetiredAt == nil || old.OfflineAt != nil {
		t.Fatalf("旧会话应卸任、下线标记清除：role=%q retired=%v offline=%v", old.Role, old.RetiredAt, old.OfflineAt)
	}
	got := false
	for _, d := range tellsTo(e, "claude:io-new") {
		got = got || strings.Contains(d.Answer, "用户说先别合")
	}
	if !got {
		t.Fatal("积压的消息应转给新会话")
	}
	// 新会话第一次挂 ks wait 就收到积压的消息
	var res daemon.WaitResult
	if err := c.Do(ctx, "GET", "/wait?session_key=claude:io-new&timeout=5s", nil, &res); err != nil || res.Reason != "delivered" || !strings.Contains(res.Text, "用户说先别合") {
		t.Fatalf("新会话应收到积压的消息：%+v %v", res, err)
	}
}

// R-272：主助理接手概览列出别的会话提的、还没答的决策（主助理订阅 decision 是为了查冲突，上一任下线期间新提的会漏查）。
func TestAideOverviewOtherDecisions(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	c := client.New(30 * time.Second)
	ctx := context.Background()
	for _, sid := range []string{"ov-dev", "ov-aide"} {
		hook(t, "claude", map[string]any{"session_id": sid, "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "上线", "prompt_id": "p0"})
	}
	e.waitFor("会话入库", func() bool { return e.session("claude:ov-aide").ProjectID != "" })
	if err := c.Do(ctx, "POST", "/agent/sessions/role", map[string]any{"session_key": "claude:ov-dev", "role": "dev"}, nil); err != nil {
		t.Fatal(err)
	}
	var d store.Decision
	if err := c.Do(ctx, "POST", "/agent/decisions", map[string]any{"session_key": "claude:ov-dev", "question": "导出用逗号还是分号", "requirement": "none"}, &d); err != nil {
		t.Fatal(err)
	}
	var role struct {
		Prompt string `json:"prompt"`
	}
	if err := c.Do(ctx, "POST", "/agent/sessions/role", map[string]any{"session_key": "claude:ov-aide", "role": "aide"}, &role); err != nil {
		t.Fatal(err)
	}
	i := strings.Index(role.Prompt, "别的会话提的、还没答的决策")
	if i < 0 || !strings.Contains(role.Prompt[i:], d.Code) || !strings.Contains(role.Prompt[i:], "Dev") {
		t.Fatalf("主助理接手概览应列出别的会话提的决策 %s：%s", d.Code, role.Prompt)
	}
}
