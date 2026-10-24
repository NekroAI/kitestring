package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/protocol"
)

func newTestShells(t *testing.T) (*shellTracker, *queue) {
	t.Helper()
	q, err := openQueue(filepath.Join(t.TempDir(), "daemon.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { q.close() })
	s, err := openShells(q.db)
	if err != nil {
		t.Fatal(err)
	}
	s.tmp = []string{t.TempDir()}
	return s, q
}

func TestShellKind(t *testing.T) {
	cases := map[string]string{
		"cd /p && go test ./...":                          protocol.ShellBuild,
		"pnpm build && pnpm test":                         protocol.ShellBuild,
		"cd web && pnpm exec vitest run":                  protocol.ShellBuild,
		"scripts/deploy.sh --dry-run":                     protocol.ShellDeploy,
		"make deploy":                                     protocol.ShellDeploy,
		"pnpm dev --port 5199":                            protocol.ShellService,
		"KS_HOME=/tmp/k ks hub --addr 127.0.0.1:7408":     protocol.ShellService,
		"python3 -m http.server 8000":                     protocol.ShellService,
		"until grep -q done /tmp/x.log; do sleep 2; done": protocol.ShellPoll,
		"gh run watch 397139 --exit-status --interval 60": protocol.ShellPoll,
		"tail -f /tmp/daemon.log":                         protocol.ShellPoll,
		"sleep 45; echo done":                             protocol.ShellOther,
		"ffmpeg -i in.mov out.mp4":                        protocol.ShellOther,
	}
	for cmd, want := range cases {
		if got := shellKind(cmd); got != want {
			t.Errorf("%q：得到 %s，期望 %s", cmd, got, want)
		}
	}
	if cmdSummary("cd /Users/x/p && cd web && pnpm build\nsecond line") != "pnpm build" {
		t.Fatalf("命令摘要应去掉 cd 前缀、只取第一行：%q", cmdSummary("cd /Users/x/p && cd web && pnpm build\nsecond line"))
	}
	if !isShellWait("cd /p && ks wait --timeout 2h") || !isShellWait("/Users/x/.local/bin/ks wait") || isShellWait("go test ./internal/mcpserver") {
		t.Fatal("ks wait 的识别不对")
	}
}

// 样本取自 Claude Code 2.1.285 写进对话记录的真实结束通知。
const notifSample = "<task-notification>\n<task-id>bzkxpjpjm</task-id>\n<tool-use-id>toolu_01W</tool-use-id>\n<output-file>/private/tmp/claude-501/-p/s/tasks/bzkxpjpjm.output</output-file>\n<status>failed</status>\n<summary>Background command \"Run tests\" failed with exit code 1</summary>\n</task-notification>\n" +
	"<task-notification>\n<task-id>b2</task-id>\n<tool-use-id>toolu_02</tool-use-id>\n<status>killed</status>\n<summary>Background command \"x\" was stopped after reaching its background time limit</summary>\n</task-notification>"

func TestParseNotifications(t *testing.T) {
	ns := parseNotifications(notifSample)
	if len(ns) != 2 {
		t.Fatalf("应取出两条：%+v", ns)
	}
	if n := ns[0]; n.toolUse != "toolu_01W" || n.task != "bzkxpjpjm" || n.status != "failed" || n.exit == nil || *n.exit != 1 || !strings.HasSuffix(n.out, ".output") {
		t.Fatalf("第一条：%+v", n)
	}
	if ns[1].exit != nil || shellResult(ns[1].status, nil) != protocol.ShellStopped {
		t.Fatal("到了时限被停掉的应算被停止")
	}
	one, zero, sig := 1, 0, 144
	if shellResult("completed", &zero) != protocol.ShellOK || shellResult("failed", &one) != protocol.ShellFailed || shellResult("failed", &sig) != protocol.ShellStopped {
		t.Fatal("结果换算不对")
	}
}

func TestTailLine(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.output")
	body := strings.Repeat("noise line\n", 2000) + "\x1b[32mok\x1b[0m  github.com/x/y  0.5s\n" + "progress 10%\rprogress 100%\n\n"
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, m := tailLine(p, int64(len(body))); got != "progress 100%" || m.ok {
		t.Fatalf("应取最后一个非空行、进度条取最后一段：%q", got)
	}
	long := strings.Repeat("长", 300) + "\n"
	_ = os.WriteFile(p, []byte(long), 0o600)
	if got, _ := tailLine(p, int64(len(long))); len([]rune(got)) != shellLastMax+1 {
		t.Fatalf("应截到 %d 字：%d", shellLastMax, len([]rune(got)))
	}
}

// Claude Code 在输出文件末尾写的结束记录（样本取自 2.1.285 的真实输出文件）
func TestTailLineExitMark(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.output")
	for body, want := range map[string]outEnd{
		"done-test\n\n[exited with code 0]\n": {ok: true},
		"FAIL\n[exited with code 144]\n":      {ok: true, code: 144},
		"serving\n[killed]\n":                 {ok: true, killed: true},
	} {
		_ = os.WriteFile(p, []byte(body), 0o600)
		last, m := tailLine(p, int64(len(body)))
		if m != want || last == "" || strings.HasPrefix(last, "[") {
			t.Fatalf("%q：%q %+v", body, last, m)
		}
	}
}

func TestHotState(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	if h, _ := hotState(now.Add(-6*time.Minute), nil, now); h != "cpu" {
		t.Fatal("CPU 占满 6 分钟应算跑飞")
	}
	if h, _ := hotState(now.Add(-3*time.Minute), nil, now); h != "" {
		t.Fatal("3 分钟还不算")
	}
	m := now.Unix() / 60
	var rising []memSample
	for i := 0; i <= 11; i++ {
		rising = append(rising, memSample{m - 11 + int64(i), int64(100<<10) + int64(i)*(30<<10)})
	}
	if h, at := hotState(time.Time{}, rising, now); h != "mem" || at != (m-11)*60000 {
		t.Fatalf("内存 11 分钟一直涨、涨了 330 MB：应算跑飞（%s %d）", h, at)
	}
	dip := append([]memSample{}, rising...)
	dip[6].kb = dip[5].kb / 2
	if h, _ := hotState(time.Time{}, dip, now); h != "" {
		t.Fatal("中间跌过一半的不算持续上涨")
	}
	flat := []memSample{}
	for i := 0; i <= 11; i++ {
		flat = append(flat, memSample{m - 11 + int64(i), 500 << 10})
	}
	if h, _ := hotState(time.Time{}, flat, now); h != "" {
		t.Fatal("不涨的不算")
	}
}

const shellStart = "Sat Oct 24 09:00:00 2026"

// 完整流程：PreToolUse → PostToolUse（任务编号）→ 进程表对上 → 进程不在了（结束时间）→ 结束通知（退出码）。
func TestShellTrackerLifecycle(t *testing.T) {
	s, _ := newTestShells(t)
	now := time.Date(2026, 10, 24, 9, 0, 0, 0, time.Local)
	s.now = func() time.Time { return now }
	key := "claude:s1"
	ev := &protocol.Event{Name: "PreToolUse", ToolName: "Bash", ToolUseID: "toolu_1"}
	s.onHook(key, map[string]any{"tool_input": map[string]any{"command": "cd /p && go test ./...", "description": "Run go tests", "run_in_background": true}}, ev)
	// ks wait 不记
	s.onHook(key, map[string]any{"tool_input": map[string]any{"command": "ks wait --timeout 2h", "run_in_background": true}}, &protocol.Event{Name: "PreToolUse", ToolName: "Bash", ToolUseID: "toolu_w"})
	// 前台命令不记
	s.onHook(key, map[string]any{"tool_input": map[string]any{"command": "go build ./..."}}, &protocol.Event{Name: "PreToolUse", ToolName: "Bash", ToolUseID: "toolu_f"})
	if _, ok := s.pending(); ok {
		t.Fatal("还没起来的不发")
	}
	s.onHook(key, map[string]any{"tool_response": map[string]any{"stdout": "", "backgroundTaskId": "bt1"}}, &protocol.Event{Name: "PostToolUse", ToolName: "Bash", ToolUseID: "toolu_1"})
	if len(s.recs) != 1 {
		t.Fatalf("只应记一条：%d", len(s.recs))
	}
	if c := s.calls(key); c["toolu_1"] == nil || !c["toolu_1"].bg {
		t.Fatal("没对上进程的后台命令要拿去对进程")
	}
	// 输出文件
	dir := filepath.Join(s.tmp[0], "-p", "s1", "tasks")
	_ = os.MkdirAll(dir, 0o700)
	_ = os.WriteFile(filepath.Join(dir, "bt1.output"), []byte("=== RUN TestX\n"), 0o600)
	shell := proc{pid: 4242, ppid: 100, start: shellStart, cmd: "/bin/zsh -c eval 'cd /p && go test ./...'", cpu: 120, rss: 50 << 10}
	child := proc{pid: 4243, ppid: 4242, start: shellStart, cmd: "go test", cpu: 0, rss: 10 << 10}
	now = now.Add(3 * time.Second)
	s.tick(now, []proc{shell, child}, map[string]shellSeen{key: {matched: map[string]proc{"toolu_1": shell}}})
	r := s.recs[shellID(key, "toolu_1")]
	if r.pid != 4242 || r.c.Task != "sh-4242" || r.c.Status != protocol.ShellRunning || r.c.Last != "=== RUN TestX" {
		t.Fatalf("应对上进程并读到最后一行：%+v", r.c)
	}
	if r.c.Kind != protocol.ShellBuild || r.c.Label != "Run go tests" {
		t.Fatalf("类型与描述：%+v", r.c)
	}
	env, ok := s.pending()
	if !ok || len(env.Shells) != 1 || env.Shells[0].Ref != "toolu_1" || env.Type != protocol.MsgShells {
		t.Fatalf("应发一条：%+v", env)
	}
	s.acked([]string{env.Batch})
	if _, ok := s.pending(); ok {
		t.Fatal("回执过、没变化的不再发")
	}
	// CPU 一直占满：5 分钟后标跑飞
	for i := 0; i < 110; i++ {
		now = now.Add(3 * time.Second)
		s.tick(now, []proc{shell, child}, map[string]shellSeen{key: {}})
	}
	if r.c.Hot != "cpu" || r.c.RSSKB != 60<<10 {
		t.Fatalf("CPU 占满 5 分钟以上应标跑飞：%+v", r.c)
	}
	// 进程不在了：结束时间是这一轮，退出码还不知道
	now = now.Add(3 * time.Second)
	_ = os.WriteFile(filepath.Join(dir, "bt1.output"), []byte("=== RUN TestX\nFAIL\tgithub.com/x\t0.3s\n"), 0o600)
	s.tick(now, nil, map[string]shellSeen{})
	if r.c.Status != protocol.ShellEnded || r.c.End != now.UnixMilli() || r.c.Exit != nil || r.c.Hot != "" || r.c.Last != "FAIL github.com/x 0.3s" {
		t.Fatalf("进程不在了应记结束时间、读最后一行：%+v", r.c)
	}
	// 通知稍后到（会话还在跑一轮）：补上退出码，结束时间不变
	end := r.c.End
	s.onHook(key, map[string]any{"prompt": strings.ReplaceAll(strings.ReplaceAll(notifSample, "toolu_01W", "toolu_1"), "bzkxpjpjm", "bt1")}, &protocol.Event{Name: "UserPromptSubmit"})
	if r.c.Status != protocol.ShellFailed || r.c.Exit == nil || *r.c.Exit != 1 || r.c.End != end {
		t.Fatalf("通知应补上退出码：%+v", r.c)
	}
	// Stop 的 background_tasks（没有退出码）不改掉通知给的结果
	s.stopTasks(key, map[string]any{"background_tasks": []any{map[string]any{"id": "bt1", "type": "shell", "status": "failed"}}}, now)
	if *r.c.Exit != 1 {
		t.Fatal("没写退出码的结果不应盖掉通知")
	}
	// 重启后从 daemon.db 读回来
	s2, err := openShells(s.db)
	if err != nil {
		t.Fatal(err)
	}
	if x := s2.recs[shellID(key, "toolu_1")]; x == nil || x.c.Status != protocol.ShellFailed || x.pid != 4242 || x.taskID != "bt1" {
		t.Fatalf("重启后应读回：%+v", x)
	}
}

// 太短、没赶上扫描的命令：用输出文件最后写入的时间当结束时间；对话记录里补上的老命令进程早已不在、也没有输出文件：结束时间未知。
func TestShellTrackerMissed(t *testing.T) {
	s, _ := newTestShells(t)
	now := time.Date(2026, 10, 24, 9, 0, 0, 0, time.Local)
	s.now = func() time.Time { return now }
	key := "claude:s1"
	dir := filepath.Join(s.tmp[0], "-p", "s1", "tasks")
	_ = os.MkdirAll(dir, 0o700)
	out := filepath.Join(dir, "bq.output")
	_ = os.WriteFile(out, []byte("built\n"), 0o600)
	mt := now.Add(time.Second)
	_ = os.Chtimes(out, mt, mt)
	s.begin(key, "", "toolu_q", "pnpm build", "", now)
	s.launch(key, "toolu_q", "bq", now)
	// 对话记录里的老命令（三天前，进程早已不在）
	old := `{"type":"assistant","timestamp":"2026-10-21T09:00:00Z","message":{"content":[{"type":"tool_use","id":"toolu_old","name":"Bash","input":{"command":"sleep 999","run_in_background":true}}]}}`
	res := `{"type":"user","timestamp":"2026-10-21T09:00:01Z","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_old","content":"Command running in background with ID: bold."}]},"toolUseResult":{"stdout":"","backgroundTaskId":"bold"}}`
	s.transcriptLine(key, "", []byte(old), now.Add(-shellKeep))
	s.transcriptLine(key, "", []byte(res), now.Add(-shellKeep))
	// Agent 不在这一轮里（本机还不认得它）：输出文件末尾有结束记录的照样结束、拿到退出码
	s.begin("claude:s2", "", "toolu_m", "pnpm build", "", now)
	s.launch("claude:s2", "toolu_m", "bm", now)
	_ = os.WriteFile(filepath.Join(dir, "bm.output"), []byte("built\n[exited with code 2]\n"), 0o600)
	for i := 0; i < shellMiss; i++ {
		now = now.Add(3 * time.Second)
		s.tick(now, nil, map[string]shellSeen{key: {}})
	}
	if m := s.recs[shellID("claude:s2", "toolu_m")]; m.c.Status != protocol.ShellFailed || *m.c.Exit != 2 || m.c.Last != "built" {
		t.Fatalf("结束记录里的退出码：%+v", m.c)
	}
	q := s.recs[shellID(key, "toolu_q")]
	if q.c.Status != protocol.ShellEnded || q.c.End != mt.UnixMilli() || q.c.Last != "built" {
		t.Fatalf("短命令应按输出文件定结束时间：%+v", q.c)
	}
	o := s.recs[shellID(key, "toolu_old")]
	if o == nil || o.c.Status != protocol.ShellLost || o.c.End != 0 || o.taskID != "bold" {
		t.Fatalf("老命令什么都不知道：%+v", o)
	}
	// 对话记录里后面有它的结束通知：补上结果，结束时间取通知的时刻
	n := `{"type":"user","timestamp":"2026-10-21T10:00:00Z","message":{"content":"<task-notification>\n<task-id>bold</task-id>\n<tool-use-id>toolu_old</tool-use-id>\n<status>completed</status>\n<summary>Background command \"sleep 999\" completed (exit code 0)</summary>\n</task-notification>"}}`
	s.transcriptLine(key, "", []byte(n), now.Add(-shellKeep))
	if o.c.Status != protocol.ShellOK || o.c.End == 0 || *o.c.Exit != 0 {
		t.Fatalf("通知应补上结果：%+v", o.c)
	}
}

// Codex 的后台 shell 没有 hook：按进程号 + 启动时间各记一条，进程不在了就结束（退出码未知）。
func TestShellTrackerCodex(t *testing.T) {
	s, _ := newTestShells(t)
	now := time.Date(2026, 10, 24, 9, 0, 0, 0, time.Local)
	s.now = func() time.Time { return now }
	p := proc{pid: 77, ppid: 70, start: shellStart, cmd: "/bin/zsh -lc pnpm dev --port 5199", age: time.Minute}
	w := proc{pid: 78, ppid: 70, start: shellStart, cmd: "/bin/zsh -lc ks wait", age: time.Minute}
	s.tick(now, []proc{p, w}, map[string]shellSeen{"codex:c1": {loose: []proc{p, w}}, "claude:x": {loose: []proc{p}}})
	if len(s.recs) != 1 {
		t.Fatalf("只记 Codex 的那一条（ks wait 不算，Claude Code 的不另起）：%d", len(s.recs))
	}
	r := s.recs[shellID("codex:c1", "sh-"+procID(77, shellStart))]
	if r == nil || r.c.Kind != protocol.ShellService || r.c.Label != "pnpm dev --port 5199" || r.c.Task != "sh-77" {
		t.Fatalf("Codex 的后台 shell：%+v", r)
	}
	now = now.Add(3 * time.Second)
	s.tick(now, nil, map[string]shellSeen{})
	if r.c.Status != protocol.ShellEnded || r.c.End != now.UnixMilli() {
		t.Fatalf("进程不在了应结束：%+v", r.c)
	}
	// 纸鸢停掉的（R-209）记为被停止
	s.markStopped(77, shellStart)
	if r.c.Status != protocol.ShellStopped {
		t.Fatal("停掉的应记为被停止")
	}
}

// 命令行对不上（被改写过）的：按启动时间就近认给还没对上进程的那条，不按“对不上进程”提前结束。
func TestShellTrackerNearest(t *testing.T) {
	s, _ := newTestShells(t)
	now := time.Date(2026, 10, 24, 9, 0, 1, 0, time.Local)
	s.now = func() time.Time { return now }
	key := "claude:s1"
	s.begin(key, "", "toolu_n", "sleep 40 && echo 完成", "", now)
	s.launch(key, "toolu_n", "", now)
	p := proc{pid: 55, ppid: 1, start: shellStart, cmd: "/bin/zsh -c eval 'rewritten'", age: 5 * time.Second}
	for i := 0; i < shellMiss+1; i++ {
		now = now.Add(3 * time.Second)
		s.tick(now, []proc{p}, map[string]shellSeen{key: {loose: []proc{p}}})
	}
	r := s.recs[shellID(key, "toolu_n")]
	if r.pid != 55 || r.c.Status != protocol.ShellRunning {
		t.Fatalf("应按启动时间认上：%+v pid=%d", r.c, r.pid)
	}
}
