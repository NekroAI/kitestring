package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// R-249：在线会话列表的状态与工具名只从记录推；Presence 不受影响。
func TestAvailability(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	later, earlier := now.Add(10*time.Minute), now.Add(-time.Minute)
	cases := []struct {
		name string
		x    Session
		want string
	}{
		{"已结束", Session{Tool: "claude", State: StateEnded}, AvailOffline},
		{"失联", Session{Tool: "codex", State: StateLost, Parked: true}, AvailOffline},
		{"Claude 在跑", Session{Tool: "claude", State: StateRunning}, AvailBusy},
		{"Claude 挂着后台 wait 但你插话在跑", Session{Tool: "claude", State: StateRunning, Parked: true}, AvailBusy},
		{"后台任务在跑", Session{Tool: "claude", State: StateBackground}, AvailBusy},
		{"等授权", Session{Tool: "claude", State: StateWaitApproval, Parked: true}, AvailBusy},
		{"提问工具在问", Session{Tool: "claude", State: StateWaitReply, WaitKind: WaitQuestion}, AvailBusy},
		{"Claude 后台 wait 待命", Session{Tool: "claude", State: StateParked, Parked: true, ParkedUntil: &later}, AvailStandby},
		{"claude-x 后台 wait 待命", Session{Tool: "claude", Launcher: "claude-x", State: StateParked, Parked: true}, AvailStandby},
		{"Claude 后台 wait 记录过了到期时间仍算待命", Session{Tool: "claude", State: StateParked, Parked: true, ParkedUntil: &earlier}, AvailStandby},
		{"Codex 前台 wait 未到期", Session{Tool: "codex", State: StateParked, Parked: true, ParkedUntil: &later}, AvailStandby},
		{"Codex 前台 wait 已到期", Session{Tool: "codex", State: StateParked, Parked: true, ParkedUntil: &earlier}, AvailOnline},
		{"Codex 一轮结束没挂 wait", Session{Tool: "codex", State: StateWaitReply}, AvailOnline},
		{"DSH 插件待命", Session{Tool: "dsh", State: StateParked, Parked: true}, AvailStandby},
		{"DSH 在跑", Session{Tool: "dsh", State: StateRunning}, AvailBusy},
		{"Claude 等你回复", Session{Tool: "claude", State: StateWaitReply}, AvailOnline},
		{"刚开始", Session{Tool: "claude", State: StateIdle}, AvailOnline},
	}
	for _, c := range cases {
		if got := c.x.AvailabilityAt(now); got != c.want {
			t.Errorf("%s：Availability = %s，应为 %s", c.name, got, c.want)
		}
	}
	// 无身份会话 Presence 照旧为空，不因新函数改变
	if p := (Session{Tool: "claude", State: StateParked, Parked: true}).Presence(); p != "" {
		t.Errorf("无身份会话 Presence 应为空：%q", p)
	}
}

func TestHarness(t *testing.T) {
	cases := map[string]Session{
		"Claude Code": {Tool: "claude", Client: "cli"},
		"claude-x":    {Tool: "claude", Client: "cli", Launcher: "claude-x"},
		"Codex 终端":    {Tool: "codex", Client: "cli"},
		"Codex App":   {Tool: "codex", Client: "app-server"},
		"Codex exec":  {Tool: "codex", Client: "exec"},
		"Codex":       {Tool: "codex"},
		"DSH":         {Tool: "dsh", Client: "web"},
	}
	for want, x := range cases {
		if got := x.Harness(); got != want {
			t.Errorf("%+v：Harness = %q，应为 %q", x, got, want)
		}
	}
	if got := (Session{Tool: "codex", Client: "desktop"}).Harness(); got != "Codex App" {
		t.Errorf("Codex 桌面版应为 Codex App：%q", got)
	}
}

// SessionRoster：按项目过滤、默认不含已结束（失联照列）、不含后台任务、带关联需求；跨项目、带已结束、上限与总数。
func TestSessionRoster(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	db := s.DB()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO devices(id, name, token_hash, created_at) VALUES('dv', 'mac', 'h', '')`)
	exec(`INSERT INTO projects(id, name, remote, created_at) VALUES('p1', 'kite', 'github.com/o/kite', ''), ('p2', 'other', 'github.com/o/other', '')`)
	add := func(key, project, state string, bg bool, active string) {
		exec(`INSERT INTO sessions(key, tool, session_id, device_id, project_id, state, background, started_at, last_active_at) VALUES(?, 'claude', ?, 'dv', ?, ?, ?, '', ?)`, key, key, project, state, bg, active)
	}
	add("claude:a", "p1", StateParked, false, "2026-11-01T10:00:00Z")
	add("claude:b", "p1", StateRunning, false, "2026-11-01T11:00:00Z")
	add("claude:lost", "p1", StateLost, false, "2026-11-01T09:00:00Z")
	add("claude:end", "p1", StateEnded, false, "2026-11-01T08:00:00Z")
	add("codex:bg", "p1", StateIdle, true, "2026-11-01T12:00:00Z")
	add("claude:o", "p2", StateIdle, false, "2026-11-01T07:00:00Z")
	r, err := s.CreateRequirement(ctx, Requirement{ProjectID: "p1", Title: "会话通信", Status: ReqInProgress, Priority: "P1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.LinkSession(ctx, r.ID, "claude:a"); err != nil {
		t.Fatal(err)
	}
	keys := func(l []Session) []string {
		out := []string{}
		for _, x := range l {
			out = append(out, x.Key)
		}
		return out
	}
	l, total, err := s.SessionRoster(ctx, "p1", false, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := keys(l); total != 3 || len(got) != 3 || got[0] != "claude:b" || got[1] != "claude:a" || got[2] != "claude:lost" {
		t.Fatalf("本项目没结束的会话：%v（共 %d）", got, total)
	}
	if len(l[1].Requirements) != 1 || l[1].Requirements[0].Code != r.Code {
		t.Fatalf("应带关联需求：%+v", l[1].Requirements)
	}
	l, total, err = s.SessionRoster(ctx, "p1", true, 2)
	if err != nil {
		t.Fatal(err)
	}
	if total != 4 || len(l) != 2 {
		t.Fatalf("带已结束、上限 2：%v（共 %d）", keys(l), total)
	}
	l, total, err = s.SessionRoster(ctx, "", false, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 4 || keys(l)[3] != "claude:o" {
		t.Fatalf("跨项目：%v（共 %d）", keys(l), total)
	}
}
