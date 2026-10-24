package daemon

import (
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/protocol"
)

const (
	t1 = "Sat Oct 17 10:00:00 2026"
	t2 = "Sun Oct 18 01:27:31 2026"
	t3 = "Sat Oct 24 09:00:00 2026"
)

// R-209：从进程表认孤儿 / 已脱离；还挂在 Agent 下面的不列；Agent 进程号被复用按已退出处理。
func TestClassifyOrphans(t *testing.T) {
	ps := []proc{
		{pid: 100, ppid: 1, start: t1, cmd: "claude --resume"},                         // 还在的 Agent
		{pid: 200, ppid: 100, start: t2, cmd: "/bin/zsh -c eval 'pnpm dev'"},           // 挂在 Agent 下面：正常运行
		{pid: 201, ppid: 200, start: t2, cmd: "node vite"},                             // 同上
		{pid: 300, ppid: 1, start: t2, cmd: "node server.js --port 4000", cpu: 1.5},    // Agent 还在，但已脱离
		{pid: 400, ppid: 1, start: t2, cmd: "node pnpm dev --port 5199", rss: 1000},    // Agent 999 不在了：孤儿
		{pid: 401, ppid: 400, start: t2, cmd: "node react-router dev", rss: 2000},      // 孤儿的子进程，归到同一棵树
		{pid: 500, ppid: 1, start: t3, cmd: "node unrelated"},                          // 进程号 500 被复用（启动更晚、也不是 Agent）
		{pid: 501, ppid: 1, start: t2, cmd: "python -m http.server"},                   // CLAUDE_PID=500：Agent 已经不在
		{pid: 600, ppid: 1, start: t2, cmd: "/bin/zsh -lc pnpm dev"},                   // Codex 的后台 shell（daemon.db 记过）
		{pid: 700, ppid: 1, start: t3, cmd: "codex"},                                   // 进程号 700 已经是另一个 codex（启动时间不同）
		{pid: 800, ppid: 1, start: t2, cmd: "/bin/zsh -c eval 'ks wait --timeout 2h'"}, // ks wait 不算
		{pid: 900, ppid: 1, start: t2, cmd: "sleep 100"},                               // 没有标记、没有记录：不认识，不列
	}
	envs := map[int]procEnv{
		200: {claudePid: 100, session: "s-live"}, 201: {claudePid: 100, session: "s-live"},
		300: {claudePid: 100, session: "s-live", pwd: "/p/live"},
		400: {claudePid: 999, session: "s-dead", pwd: "/p/qantrip"}, 401: {claudePid: 999, session: "s-dead"},
		501: {claudePid: 500, session: "s-reused"},
		800: {claudePid: 999},
	}
	recs := map[string]procRec{procID(600, t2): {pid: 600, start: t2, key: "codex:c1", agent: "700@" + t2, source: "shell"}}
	items := classifyOrphans(ps, envs, recs, 0)
	got := map[int]orphanItem{}
	for _, it := range items {
		got[it.root.pid] = it
	}
	if len(got) != 4 {
		t.Fatalf("应认出 4 棵树（300 已脱离、400 / 501 / 600 孤儿），得到 %v", items)
	}
	if got[300].state != protocol.ProcDetached || got[300].agent != "100@"+t1 {
		t.Fatalf("300 应是已脱离、并记下 Agent 实例：%+v", got[300])
	}
	if it := got[400]; it.state != protocol.ProcOrphan || len(it.members) != 2 || it.key != "claude:s-dead" || it.cwd != "/p/qantrip" {
		t.Fatalf("400 应是带一个子进程的孤儿：%+v", it)
	}
	if got[501].state != protocol.ProcOrphan {
		t.Fatal("CLAUDE_PID 的进程号被复用（启动更晚、不是 Agent）应算孤儿")
	}
	if got[600].state != protocol.ProcOrphan || got[600].key != "codex:c1" {
		t.Fatalf("Codex 记录的 Agent 启动时间对不上应算孤儿：%+v", got[600])
	}
	for _, pid := range []int{200, 201, 401, 800, 900} {
		if _, ok := got[pid]; ok {
			t.Fatalf("%d 不该单独列出", pid)
		}
	}
	rep := orphanReport([]orphanItem{got[400]}, map[int][]int{401: {9229, 5199}})
	if r := rep[0]; r.ID != "400@"+t2 || r.RSSKB != 3000 || r.Procs != 2 || len(r.Ports) != 2 || r.Ports[0] != 5199 || r.Ports[1] != 9229 {
		t.Fatalf("上报内容不对：%+v", r)
	}
}

// spawn 起一个测试用进程并负责收尸，返回它的“启动时间”。
func spawn(t *testing.T, args ...string) (*exec.Cmd, string) {
	t.Helper()
	cmd := exec.Command(args[0], args[1:]...)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-done
	})
	time.Sleep(150 * time.Millisecond) // 让 sh 把 trap 设好
	ps, err := listProcs()
	if err != nil {
		t.Skip("没有 ps")
	}
	for _, p := range ps {
		if p.pid == cmd.Process.Pid {
			return cmd, p.start
		}
	}
	t.Fatal("进程表里找不到刚起的进程")
	return nil, ""
}

func alive(cmd *exec.Cmd) bool {
	return cmd.ProcessState == nil && cmd.Process.Signal(syscall.Signal(0)) == nil
}

func stopDaemon(t *testing.T) *Daemon {
	d := newQDaemon(t, t.TempDir())
	t.Cleanup(func() { d.q.close() })
	d.orph.envs = func([]proc) map[int]procEnv { return nil } // 不读本测试进程继承来的 Claude Code 环境
	d.orph.grace = 1500 * time.Millisecond
	return d
}

const goneAgent = "99999@Mon Jan 1 00:00:00 2001"

// 孤儿：先 SIGTERM，退出后写进记录。
func TestStopOrphan(t *testing.T) {
	d := stopDaemon(t)
	cmd, start := spawn(t, "sleep", "300")
	pid := cmd.Process.Pid
	d.q.procsAdd(time.Now(), []procRec{{pid: pid, start: start, key: "claude:dead", agent: goneAgent, source: "env"}})
	res := d.stopProc(protocol.StopProc{Pid: pid, Start: start})
	if res.Result != protocol.StopExited || res.Signal != "SIGTERM" {
		t.Fatalf("应 SIGTERM 后退出：%+v", res)
	}
	var sig, result string
	_ = d.q.db.QueryRow(`SELECT stop_signal, stop_result FROM bg_procs WHERE pid = ?`, pid).Scan(&sig, &result)
	if sig != "SIGTERM" || result != protocol.StopExited {
		t.Fatalf("停止结果没写进记录：%q %q", sig, result)
	}
}

// 不理 SIGTERM 的：回报仍在运行，确认后 SIGKILL。
func TestStopForce(t *testing.T) {
	d := stopDaemon(t)
	cmd, start := spawn(t, "sh", "-c", `trap "" TERM; sleep 300`)
	pid := cmd.Process.Pid
	d.q.procsAdd(time.Now(), []procRec{{pid: pid, start: start, agent: goneAgent, source: "env"}})
	res := d.stopProc(protocol.StopProc{Pid: pid, Start: start})
	if res.Result != protocol.StopRunning || !alive(cmd) {
		t.Fatalf("不理 SIGTERM 时应回报仍在运行：%+v", res)
	}
	res = d.stopProc(protocol.StopProc{Pid: pid, Start: start, Force: true})
	if res.Result != protocol.StopKilled || res.Signal != "SIGKILL" {
		t.Fatalf("强制停止应 SIGKILL 后退出：%+v", res)
	}
}

// pid 复用 / 没记过 / Hub 乱发的 pid：一律拒绝，不发信号。
func TestStopRefusesUnverified(t *testing.T) {
	d := stopDaemon(t)
	var signalled []int
	d.orph.kill = func(pid int, sig syscall.Signal) error { signalled = append(signalled, pid); return nil }
	cmd, start := spawn(t, "sleep", "300")
	pid := cmd.Process.Pid
	// 没记过的进程（不是会话开出来的）
	if res := d.stopProc(protocol.StopProc{Pid: pid, Start: start}); res.Result != protocol.StopRefused {
		t.Fatalf("没记过的进程应拒绝：%+v", res)
	}
	d.q.procsAdd(time.Now(), []procRec{{pid: pid, start: start, agent: goneAgent, source: "env"}})
	// 进程号对得上、启动时间不对（pid 已被复用）
	if res := d.stopProc(protocol.StopProc{Pid: pid, Start: "Mon Jan 1 00:00:00 2001"}); res.Result != protocol.StopRefused {
		t.Fatalf("启动时间对不上应拒绝：%+v", res)
	}
	// 只给进程号
	if res := d.stopProc(protocol.StopProc{Pid: pid}); res.Result != protocol.StopRefused {
		t.Fatalf("只给进程号应拒绝：%+v", res)
	}
	// 系统进程
	if res := d.stopProc(protocol.StopProc{Pid: 1, Start: start}); res.Result != protocol.StopRefused {
		t.Fatalf("pid 1 应拒绝：%+v", res)
	}
	if len(signalled) != 0 || !alive(cmd) {
		t.Fatalf("核对不通过时不该发信号：%v", signalled)
	}
}

// 会话还在（进程还挂在 Agent 下面）：不给停。
func TestStopRefusesLiveSession(t *testing.T) {
	d := stopDaemon(t)
	var signalled []int
	d.orph.kill = func(pid int, sig syscall.Signal) error { signalled = append(signalled, pid); return nil }
	ps, err := listProcs()
	if err != nil {
		t.Skip("没有 ps")
	}
	self := ""
	for _, p := range ps {
		if p.pid == os.Getpid() {
			self = strconv.Itoa(p.pid) + "@" + p.start
		}
	}
	cmd, start := spawn(t, "sleep", "300")
	pid := cmd.Process.Pid
	// 把本测试进程当作开它的 Agent：Agent 还在、sleep 还挂在它下面
	d.q.procsAdd(time.Now(), []procRec{{pid: pid, start: start, key: "claude:live", agent: self, source: "shell"}})
	items, _ := d.classifyNow(time.Now(), mustProcs(t), nil)
	for _, it := range items {
		if it.root.pid == pid {
			t.Fatal("会话还在的进程不该列为孤儿")
		}
	}
	if res := d.stopProc(protocol.StopProc{Pid: pid, Start: start}); res.Result != protocol.StopRefused {
		t.Fatalf("会话还在应拒绝：%+v", res)
	}
	if len(signalled) != 0 || !alive(cmd) {
		t.Fatal("会话还在时不该发信号")
	}
}

func mustProcs(t *testing.T) []proc {
	ps, err := listProcs()
	if err != nil {
		t.Skip("没有 ps")
	}
	return ps
}
