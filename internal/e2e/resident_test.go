package e2e

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/client"
	"github.com/NekroAI/kitestring/internal/daemon"
	"github.com/NekroAI/kitestring/internal/roles"
	"github.com/NekroAI/kitestring/internal/store"
)

// 常驻在线（R-83）：PM / 开发结束一轮时没挂 ks wait，Stop hook 拦下；连续拦 2 次仍不挂就放行；
// 挂着 ks wait 时不拦；没有身份的会话不受影响；拦下的那次不上报 Stop。
func TestResidentStopGuard(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	c := client.New(30 * time.Second)
	ctx := context.Background()
	prompt := func(sid, id string) {
		hook(t, "claude", map[string]any{"session_id": sid, "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "干活", "prompt_id": id})
	}
	stop := func(sid string) string {
		return hook(t, "claude", map[string]any{"session_id": sid, "cwd": cwd, "hook_event_name": "Stop"})
	}
	blocked := func(out string) bool { return strings.Contains(out, `"decision":"block"`) }

	prompt("dev-1", "p0")
	prompt("plain-1", "p0")
	e.waitFor("会话入库", func() bool { return e.session("claude:dev-1").Key != "" && e.session("claude:plain-1").Key != "" })

	// 没有身份：照常结束
	if out := stop("plain-1"); blocked(out) {
		t.Fatalf("无身份会话不应被拦：%s", out)
	}
	e.waitFor("普通会话等你回复", func() bool { return e.session("claude:plain-1").State == store.StateWaitReply })

	if err := c.Do(ctx, "POST", "/agent/sessions/role", map[string]any{"session_key": "claude:dev-1", "role": "dev"}, nil); err != nil {
		t.Fatal(err)
	}
	// 等 daemon 收到身份：第一次被拦即说明已生效
	var out string
	e.waitFor("开发会话被拦", func() bool { out = stop("dev-1"); return blocked(out) })
	if !strings.Contains(out, "ks wait") || !strings.Contains(out, "Dev 会话") {
		t.Fatalf("拦下理由应提示挂 ks wait：%s", out)
	}
	if !blocked(stop("dev-1")) {
		t.Fatal("第 2 次仍应拦下")
	}
	if blocked(stop("dev-1")) {
		t.Fatal("连续拦 2 次后应放行")
	}
	e.waitFor("放行后上报结束", func() bool { return e.session("claude:dev-1").State == store.StateWaitReply })

	// 新的一轮：挂着 ks wait 时结束不拦，显示待命
	prompt("dev-1", "p1")
	done := make(chan daemon.WaitResult, 1)
	go func() {
		var res daemon.WaitResult
		_ = c.Do(ctx, "GET", "/wait?session_key=claude:dev-1&timeout=2s", nil, &res)
		done <- res
	}()
	e.waitFor("挂起", func() bool { return e.session("claude:dev-1").Parked })
	if out := stop("dev-1"); blocked(out) {
		t.Fatalf("挂着 ks wait 时不应拦：%s", out)
	}
	e.waitFor("待命", func() bool { return e.session("claude:dev-1").State == store.StateParked })

	// 超时：提示接着等；网页显示运行中（不算等你回复）；再结束时又被拦，于是自动续等
	res := <-done
	if res.Reason != "timeout" || !res.Resident {
		t.Fatalf("应超时且标明常驻：%+v", res)
	}
	e.waitFor("超时后显示运行中", func() bool { return e.session("claude:dev-1").State == store.StateRunning })
	if !blocked(stop("dev-1")) {
		t.Fatal("超时后结束应再被拦下")
	}
	if st := e.session("claude:dev-1").State; st != store.StateRunning {
		t.Fatalf("被拦下的结束不应上报，状态应仍为运行中，得到 %s", st)
	}
}

// 顾问两种模式（R-123）：Adv 默认不常驻，没挂 ks wait 结束也不被 Stop hook 拦；
// 常驻与否只由提示词约定，提示词写明两种模式与切换口令。
func TestAdvisorNotResident(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	c := client.New(30 * time.Second)
	ctx := context.Background()
	hook(t, "claude", map[string]any{"session_id": "adv-1", "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "聊聊", "prompt_id": "p0"})
	e.waitFor("会话入库", func() bool { return e.session("claude:adv-1").Key != "" })
	if err := c.Do(ctx, "POST", "/agent/sessions/role", map[string]any{"session_key": "claude:adv-1", "role": "advisor"}, nil); err != nil {
		t.Fatal(err)
	}
	e.waitFor("身份生效", func() bool { return e.session("claude:adv-1").Role == "advisor" })
	for i := 0; i < 3; i++ {
		out := hook(t, "claude", map[string]any{"session_id": "adv-1", "cwd": cwd, "hook_event_name": "Stop"})
		if strings.Contains(out, `"decision":"block"`) {
			t.Fatalf("Adv 默认不常驻，不应被拦：%s", out)
		}
	}
	e.waitFor("正常结束", func() bool { return e.session("claude:adv-1").State == store.StateWaitReply })
	p := roles.Prompt(roles.Advisor)
	for _, want := range []string{"默认", "常驻", "不用常驻"} {
		if !strings.Contains(p, want) {
			t.Fatalf("顾问提示词缺少 %q", want)
		}
	}
	if roles.Resident(roles.Advisor) {
		t.Fatal("Adv 不应是常驻身份")
	}
}
