package store

import (
	"context"
	"path/filepath"
	"testing"
)

// R-239：老数据补列时按原来的文字规则回填一次，界面照常显示——批准 / 拒绝、推荐选项、等待类型。
func TestExplicitBackfill(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "hub.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	db := s.DB()
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	mustExec(`INSERT INTO devices(id, name, token_hash, created_at) VALUES('dv', 'mac', 'h', '')`)
	sess := func(key, state, detail string) {
		mustExec(`INSERT INTO sessions(key, tool, session_id, device_id, state, state_detail, started_at, last_active_at) VALUES(?, 'claude', ?, 'dv', ?, ?, '', '')`, key, key, state, detail)
	}
	sess("claude:q", StateWaitReply, "AskUserQuestion · 用哪个方案？")
	sess("claude:plan", StateWaitReply, "ExitPlanMode")
	sess("claude:reply", StateWaitReply, "")
	sess("claude:perm", StateWaitApproval, "Bash rm -rf build")
	sess("claude:run", StateRunning, "")
	dec := func(seq int, approval bool, answer, options, rec string, timedOut bool) {
		mustExec(`INSERT INTO decisions(id, seq, question, context, options, recommendation, approval, status, answer, created_at, timed_out) VALUES(?, ?, 'q', '', ?, ?, ?, ?, ?, '', ?)`,
			"d"+string(rune('0'+seq)), seq, options, rec, approval, DecDelivered, answer, timedOut)
	}
	dec(1, true, "批准", `["批准","拒绝"]`, "", false)
	dec(2, true, "拒绝；范围太大", `["批准","拒绝"]`, "", false)
	dec(3, true, "超时未批，按规则视为拒绝这个请求", `["批准","拒绝"]`, "", true)
	dec(4, false, "批准 A", `["A","B"]`, "", false)
	dec(5, false, "方案 B", `["方案 A","方案 B"]`, "推荐「方案 B」，改动小", false)
	dec(6, false, "方案 A", `["方案 A","方案 B"]`, "方案 A", false)
	dec(7, false, "x", `["方案 A","方案 B"]`, "都行", false)
	// 模拟升级前的库：去掉新列，重开时补列并回填
	for _, c := range []string{"sessions.wait_kind", "decisions.verdict", "decisions.rec_option"} {
		tc := filepath.Ext(c)
		mustExec(`ALTER TABLE ` + c[:len(c)-len(tc)] + ` DROP COLUMN ` + tc[1:])
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	waits := map[string]string{"claude:q": WaitQuestion, "claude:plan": WaitQuestion, "claude:reply": "", "claude:perm": WaitApproval, "claude:run": ""}
	for k, want := range waits {
		x, err := s.GetSession(ctx, k)
		if err != nil {
			t.Fatal(err)
		}
		if x.WaitKind != want {
			t.Errorf("%s：wait_kind = %q，应为 %q", k, x.WaitKind, want)
		}
	}
	verdicts := map[string]string{"D-1": VerdictApproved, "D-2": VerdictRejected, "D-3": VerdictRejected, "D-4": ""}
	for code, want := range verdicts {
		d, err := s.GetDecision(ctx, code)
		if err != nil {
			t.Fatal(err)
		}
		if d.Verdict != want {
			t.Errorf("%s：verdict = %q，应为 %q", code, d.Verdict, want)
		}
	}
	recs := map[string]int{"D-5": 1, "D-6": 0, "D-7": -1, "D-1": -1}
	for code, want := range recs {
		d, err := s.GetDecision(ctx, code)
		if err != nil {
			t.Fatal(err)
		}
		if d.RecOption != want {
			t.Errorf("%s：rec_option = %d，应为 %d（老数据按原来的规则显示）", code, d.RecOption, want)
		}
	}

	// 回填只跑一次：之后新写入的数据不再从文字推
	if _, err := s.DB().Exec(`UPDATE decisions SET verdict = '' WHERE seq = 1`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if d, _ := s2.GetDecision(ctx, "D-1"); d.Verdict != "" {
		t.Errorf("回填只在补列时跑一次，重开不应再从文字推：%q", d.Verdict)
	}
}
