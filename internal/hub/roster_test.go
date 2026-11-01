package hub

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/store"
)

// R-249：能否立刻叫醒只从记录推，按 harness 和状态分别写明原因。
func TestWakeAbility(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	later, earlier := now.Add(20*time.Minute), now.Add(-time.Minute)
	retired := now.Add(-time.Hour)
	cases := []struct {
		name string
		x    store.Session
		now  bool
		why  string
	}{
		{"Claude 后台 wait", store.Session{Tool: "claude", State: store.StateParked, Parked: true}, true, "后台 ks wait"},
		{"claude-x 后台 wait", store.Session{Tool: "claude", Launcher: "claude-x", State: store.StateParked, Parked: true}, true, "后台 ks wait"},
		{"Codex 前台 wait 未到期", store.Session{Tool: "codex", Client: "cli", State: store.StateParked, Parked: true, ParkedUntil: &later}, true, "前台 ks wait（到 "},
		{"Codex 前台 wait 已到期", store.Session{Tool: "codex", Client: "cli", State: store.StateParked, Parked: true, ParkedUntil: &earlier}, false, "已到期"},
		{"Codex 没挂 wait", store.Session{Tool: "codex", Client: "app-server", State: store.StateWaitReply}, false, "Codex 不能后台叫醒，等用户下次发消息"},
		{"Codex 在跑", store.Session{Tool: "codex", State: store.StateRunning}, false, "这一轮结束后收到"},
		{"DSH 插件待命", store.Session{Tool: "dsh", State: store.StateParked, Parked: true}, true, "DSH 插件待命"},
		{"DSH 没待命", store.Session{Tool: "dsh", State: store.StateWaitReply}, false, "DSH 插件没在待命"},
		{"Claude 在跑（挂着 wait 也先算忙）", store.Session{Tool: "claude", State: store.StateRunning, Parked: true}, false, "正在跑，这一轮结束后收到"},
		{"等授权", store.Session{Tool: "claude", State: store.StateWaitApproval}, false, "等用户授权"},
		{"提问工具在问", store.Session{Tool: "claude", State: store.StateWaitReply, WaitKind: store.WaitQuestion}, false, "提问工具"},
		{"Claude 没挂 wait", store.Session{Tool: "claude", State: store.StateWaitReply}, false, "没挂 ks wait"},
		{"已结束", store.Session{Tool: "claude", State: store.StateEnded}, false, "已结束"},
		{"失联", store.Session{Tool: "codex", State: store.StateLost, Parked: true}, false, "失联"},
		{"被接任卸任", store.Session{Tool: "claude", State: store.StateParked, Parked: true, RetiredAt: &retired}, false, "接任"},
	}
	for _, c := range cases {
		ok, why := wakeAbilityAt(c.x, now)
		if ok != c.now || !strings.Contains(why, c.why) {
			t.Errorf("%s：wake = %v「%s」，应为 %v 且含「%s」", c.name, ok, why, c.now, c.why)
		}
	}
}

// 在线会话列表：本项目没结束的会话，带身份（无身份写明）、工具、状态、手上的需求、能否叫醒；按身份过滤；跨项目；带已结束。
func TestRoster(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	h := New(st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx := context.Background()
	d, err := st.CreateDevice(ctx, "mac", "h1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`INSERT INTO projects(id, name, remote, created_at) VALUES('p1', 'kite', 'github.com/o/kite', ''), ('p2', 'other', 'github.com/o/other', '')`); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	until := now.Add(20 * time.Minute).Format(time.RFC3339Nano)
	add := func(key, tool, client, launcher, project, role, state string, parked bool, parkedUntil string, ago time.Duration) {
		t.Helper()
		at := now.Add(-ago).Format(time.RFC3339Nano)
		if _, err := st.DB().Exec(`INSERT INTO sessions(key, tool, session_id, device_id, project_id, client, launcher, role, title, state, parked, parked_until, started_at, last_active_at)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, key, tool, key, d.ID, project, client, launcher, role, "会话 "+key, state, parked, parkedUntil, at, at); err != nil {
			t.Fatal(err)
		}
	}
	add("claude:pm", "claude", "cli", "", "p1", "pm", store.StateParked, true, "", time.Minute)
	add("claude:dev", "claude", "cli", "claude-x", "p1", "dev", store.StateRunning, false, "", 2*time.Minute)
	add("codex:adv", "codex", "cli", "", "p1", "advisor", store.StateParked, true, until, 3*time.Minute)
	add("codex:app", "codex", "app-server", "", "p1", "", store.StateWaitReply, false, "", 4*time.Minute)
	add("dsh:x", "dsh", "web", "", "p1", "", store.StateParked, true, "", 5*time.Minute)
	add("claude:old", "claude", "cli", "", "p1", "", store.StateEnded, false, "", time.Hour)
	add("claude:far", "claude", "cli", "", "p2", "", store.StateWaitReply, false, "", time.Minute)
	r, err := st.CreateRequirement(ctx, store.Requirement{ProjectID: "p1", Title: "会话通信", Status: store.ReqInProgress, Priority: "P1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.LinkSession(ctx, r.ID, "claude:dev"); err != nil {
		t.Fatal(err)
	}

	out, err := h.roster(ctx, "claude:pm", "", false, false)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]RosterEntry{}
	for _, e := range out.Body.Sessions {
		got[e.Key] = e
	}
	if len(got) != 5 || out.Body.ProjectName != "kite" {
		t.Fatalf("本项目没结束的会话应有 5 个：%+v", out.Body.Sessions)
	}
	if first := out.Body.Sessions[0].Status; first != store.AvailStandby {
		t.Fatalf("待命的应排在前：%s", first)
	}
	want := map[string][4]string{ // 身份、工具、状态、能否叫醒
		"claude:pm":  {"主调度", "Claude Code", store.AvailStandby, "true"},
		"claude:dev": {"开发", "claude-x", store.AvailBusy, "false"},
		"codex:adv":  {"顾问", "Codex 终端", store.AvailStandby, "true"},
		"codex:app":  {"无身份", "Codex App", store.AvailOnline, "false"},
		"dsh:x":      {"无身份", "DSH", store.AvailStandby, "true"},
	}
	for k, w := range want {
		e := got[k]
		if e.RoleName != w[0] || e.Harness != w[1] || e.Status != w[2] || (e.WakeNow != (w[3] == "true")) {
			t.Errorf("%s：%+v，应为 %v", k, e, w)
		}
	}
	if !got["claude:pm"].Me || got["claude:dev"].Me {
		t.Error("只有调用者标「你」")
	}
	if o := got["claude:dev"].Open; len(o) != 1 || o[0].Code != r.Code {
		t.Errorf("开发手上应有 %s：%+v", r.Code, o)
	}
	for _, s := range []string{"项目「kite」：5 个没结束的会话（待命 3 · 忙 1 · 在线 1）", "claude:pm（你）「会话 claude:pm」 主调度 · Claude Code@mac · 待命 · 能立刻叫醒：挂着后台 ks wait",
		"codex:app「会话 codex:app」 无身份 · Codex App@mac · 在线 · 不能立刻叫醒：Codex 不能后台叫醒，等用户下次发消息", "手上：" + r.Code + "「会话通信」"} {
		if !strings.Contains(out.Body.Text, s) {
			t.Errorf("文字里应有「%s」：\n%s", s, out.Body.Text)
		}
	}

	// 按身份过滤：认中文名；none 只看无身份
	out, err = h.roster(ctx, "claude:pm", "顾问", false, false)
	if err != nil || len(out.Body.Sessions) != 1 || out.Body.Sessions[0].Key != "codex:adv" {
		t.Fatalf("按身份过滤：%+v %v", out, err)
	}
	out, err = h.roster(ctx, "claude:pm", "none", false, false)
	if err != nil || len(out.Body.Sessions) != 2 {
		t.Fatalf("只看无身份：%+v %v", out, err)
	}
	if _, err := h.roster(ctx, "claude:pm", "厨师", false, false); err == nil {
		t.Fatal("不认识的身份应报错")
	}
	// 带已结束
	out, err = h.roster(ctx, "claude:pm", "", false, true)
	if err != nil || len(out.Body.Sessions) != 6 || out.Body.Sessions[5].Key != "claude:old" || !strings.Contains(out.Body.Text, "会话已结束，离线") {
		t.Fatalf("带已结束：%+v %v", out, err)
	}
	// 跨项目：认不出会话也能看
	out, err = h.roster(ctx, "", "", true, false)
	if err != nil || len(out.Body.Sessions) != 6 || !strings.Contains(out.Body.Text, "项目 other") {
		t.Fatalf("跨项目：%+v %v", out, err)
	}
	// 不跨项目时必须认得出会话
	if _, err := h.roster(ctx, "", "", false, false); err == nil {
		t.Fatal("认不出会话、又不跨项目应报错")
	}
}
