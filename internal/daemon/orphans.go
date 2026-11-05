package daemon

import (
	"bufio"
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/NekroAI/kitestring/internal/protocol"
	"github.com/NekroAI/kitestring/internal/summary"
)

// 残留进程（R-209）：会话结束后，它开过的后台进程还在跑（典型是 pnpm dev 这类开发服务器）。
//
// 怎么认“会话开过的后台进程”，两路：
//  1. Claude Code 给每条命令的环境里都放了 CLAUDE_PID（Agent 进程号）和 CLAUDE_CODE_SESSION_ID：
//     daemon 读进程环境（macOS 用 ps -E，Linux 读 /proc/<pid>/environ），能认出所有由 Claude Code 开出来的进程，
//     包括 daemon 启动前、本功能上线前就开着的。
//  2. Codex 的命令环境里没有这类标记：扫后台任务（R-162）时，把会话下面的后台 shell 和它的子孙按 pid + 启动时间记进 daemon.db。
//
// 状态：开它的 Agent 进程已经不在（进程号不在，或进程号被复用、启动时间对不上）→ 残留进程；
// Agent 还在，但进程已经不在 Agent 的进程树下（shell 退出后被系统接管，父进程变成 1）→ 已脱离（会话还在，可能仍在用）；
// 其余（还挂在 Agent 下面的）是正常运行的，不列出、不给停。
//
// 停止只经 Hub 转来，daemon 当场重扫一遍：只停 daemon.db 里记过的、此刻判定为残留进程或已脱离的、pid 和启动时间都对得上的进程树；
// 其他一律拒绝，不按裸 pid 发信号。先 SIGTERM，几秒不退回报“仍在运行”，用户再确认才 SIGKILL。
//
// 覆盖不到的：Codex 在 app-server / 桌面 App 里跑（一个进程承载多个会话，R-162 本来就不认）；
// Codex 前台命令里用 & / nohup 甩出去、shell 当场就退出的进程（来不及在后台 shell 下面看到）；
// 同一进程 /clear 换了会话时旧会话开的进程（Agent 进程还在，算正常运行）。

const (
	orphanEvery  = 5           // 每 5 轮后台任务扫描（约 15 秒）扫一次
	orphanResend = time.Minute // 列表没变时，隔这么久重发一次（刷新 CPU / 内存）
	stopGrace    = 5 * time.Second
	killGrace    = 2 * time.Second
)

type orphanState struct {
	sig    string
	sentAt time.Time
	last   []protocol.OrphanProc
	// 测试里替换：读进程表、读进程环境、发信号、等待时长
	snap  func() ([]proc, error)
	envs  func([]proc) map[int]procEnv
	kill  func(pid int, sig syscall.Signal) error
	grace time.Duration
}

// bgShell 是扫后台任务时看到的一个会话后台 shell。
type bgShell struct {
	key, agent string
	pid        int
}

// procEnv 是从进程环境里读到的 Claude Code 标记。
type procEnv struct {
	claudePid int
	session   string
	pwd       string
}

// orphanItem 是一棵要列出的进程树。
type orphanItem struct {
	root    proc
	members []proc // 根进程和它此刻的全部子孙
	state   string
	key     string
	cwd     string
	agent   string
}

// recordShells 把会话后台 shell 及其子孙记进 daemon.db（Codex 只能靠这一路）。
func (d *Daemon) recordShells(now time.Time, ps []proc, shells []bgShell) {
	if len(shells) == 0 || d.q == nil {
		return
	}
	byPid, kids := procIndex(ps)
	known := d.q.procsLive()
	var add []procRec
	for _, sh := range shells {
		p, ok := byPid[sh.pid]
		if !ok {
			continue
		}
		d.mu.Lock()
		cwd := ""
		if info := d.sessions[sh.key]; info != nil {
			cwd = info.cwd
		}
		d.mu.Unlock()
		for _, m := range append([]proc{p}, descendants(p.pid, kids)...) {
			if _, ok := known[procID(m.pid, m.start)]; ok {
				continue
			}
			add = append(add, procRec{pid: m.pid, start: m.start, key: sh.key, agent: sh.agent, cwd: cwd, cmd: summary.Truncate(m.cmd, 500), source: "shell"})
		}
	}
	d.q.procsAdd(now, add)
}

func procIndex(ps []proc) (map[int]proc, map[int][]proc) {
	byPid := make(map[int]proc, len(ps))
	kids := map[int][]proc{}
	for _, p := range ps {
		byPid[p.pid] = p
		kids[p.ppid] = append(kids[p.ppid], p)
	}
	return byPid, kids
}

func descendants(pid int, kids map[int][]proc) []proc {
	var out []proc
	seen := map[int]bool{pid: true}
	queue := []int{pid}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, c := range kids[cur] {
			if !seen[c.pid] {
				seen[c.pid] = true
				out = append(out, c)
				queue = append(queue, c.pid)
			}
		}
	}
	return out
}

// lstartTime 解析 ps lstart（LC_ALL=C，如 "Sun Oct 18 01:27:31 2026"）。
func lstartTime(s string) time.Time {
	t, err := time.ParseInLocation("Mon Jan 2 15:04:05 2006", s, time.Local)
	if err != nil {
		return time.Time{}
	}
	return t
}

// agentAlive：开这个进程的 Agent 是否还在。agent 是“进程号@启动时间”时两者都要对上；
// 只有进程号时（环境变量里只有 CLAUDE_PID），要求那个进程是 Agent 本体且启动不晚于这个进程——进程号被复用的新进程一定启动得更晚。
func agentAlive(byPid map[int]proc, agent string, p proc) (int, bool) {
	pidStr, start, hasStart := strings.Cut(agent, "@")
	apid, err := strconv.Atoi(pidStr)
	if err != nil || apid <= 1 {
		return 0, false
	}
	a, ok := byPid[apid]
	if !ok {
		return apid, false
	}
	if hasStart {
		return apid, a.start == start
	}
	at, pt := lstartTime(a.start), lstartTime(p.start)
	return apid, !at.IsZero() && !pt.IsZero() && !at.After(pt) && isAgentCommand(strings.Fields(a.cmd))
}

// under：p 是否还在 Agent 的进程树下。
func under(byPid map[int]proc, p proc, agentPid int) bool {
	cur := p
	for i := 0; i < 64; i++ {
		if cur.ppid == agentPid {
			return true
		}
		next, ok := byPid[cur.ppid]
		if !ok || cur.ppid <= 1 {
			return false
		}
		cur = next
	}
	return false
}

// classifyOrphans 从一份进程表里认出残留进程与已脱离的进程树（纯函数，便于测试）。
func classifyOrphans(ps []proc, envs map[int]procEnv, recs map[string]procRec, self int) []orphanItem {
	byPid, kids := procIndex(ps)
	type flag struct{ state, key, cwd, agent string }
	flagged := map[int]flag{}
	for _, p := range ps {
		if p.pid == self || p.pid <= 1 || ignoreProcRe.MatchString(p.cmd) || isKsDaemon(p.cmd) {
			continue
		}
		var f flag
		if r, ok := recs[procID(p.pid, p.start)]; ok {
			f = flag{key: r.key, cwd: r.cwd, agent: r.agent}
		} else if e, ok := envs[p.pid]; ok && e.claudePid > 1 && e.claudePid != p.pid {
			f = flag{cwd: e.pwd, agent: strconv.Itoa(e.claudePid)}
			if e.session != "" {
				f.key = protocol.SessionKey(protocol.ToolClaude, e.session)
			}
			if a, ok := byPid[e.claudePid]; ok {
				if _, alive := agentAlive(byPid, f.agent, p); alive {
					f.agent += "@" + a.start // Agent 还在：连启动时间一起记，以后按实例核对
				}
			}
		} else {
			continue
		}
		apid, alive := agentAlive(byPid, f.agent, p)
		switch {
		case !alive:
			f.state = protocol.ProcOrphan
		case !under(byPid, p, apid):
			f.state = protocol.ProcDetached
		default:
			continue // 还在 Agent 下面：正常运行
		}
		flagged[p.pid] = f
	}
	var out []orphanItem
	for pid, f := range flagged {
		p := byPid[pid]
		if _, parentFlagged := flagged[p.ppid]; parentFlagged {
			continue // 只以树根为准
		}
		it := orphanItem{root: p, members: append([]proc{p}, descendants(p.pid, kids)...), state: f.state, key: f.key, cwd: f.cwd, agent: f.agent}
		out = append(out, it)
	}
	sort.Slice(out, func(i, j int) bool {
		ti, tj := lstartTime(out[i].root.start), lstartTime(out[j].root.start)
		if !ti.Equal(tj) {
			return ti.Before(tj)
		}
		return out[i].root.pid < out[j].root.pid
	})
	return out
}

var ksDaemonRe = regexp.MustCompile(`(^|/)ks\s+daemon\b`)

func isKsDaemon(cmd string) bool { return ksDaemonRe.MatchString(cmd) }

// readEnvs 读进程环境里的 Claude Code 标记（只认本用户的进程，读不到的跳过）。
func readEnvs(ps []proc) map[int]procEnv {
	out := map[int]procEnv{}
	parse := func(pid int, kv string) {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			return
		}
		e := out[pid]
		switch k {
		case "CLAUDE_PID":
			e.claudePid, _ = strconv.Atoi(v)
		case "CLAUDE_CODE_SESSION_ID":
			e.session = v
		case "PWD":
			e.pwd = v
		default:
			return
		}
		out[pid] = e
	}
	switch runtime.GOOS {
	case "linux":
		for _, p := range ps {
			b, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(p.pid), "environ"))
			if err != nil || !bytes.Contains(b, []byte("CLAUDE_PID=")) {
				continue
			}
			for _, kv := range bytes.Split(b, []byte{0}) {
				parse(p.pid, string(kv))
			}
		}
	default:
		// macOS：ps -E 把环境接在命令后面，用空格分开；只取认得的几个键
		cmd := exec.Command("ps", "-E", "-A", "-ww", "-o", "pid=,command=")
		cmd.Env = append(os.Environ(), "LC_ALL=C")
		b, err := cmd.Output()
		if err != nil {
			return out
		}
		sc := bufio.NewScanner(bytes.NewReader(b))
		sc.Buffer(make([]byte, 1<<20), 8<<20)
		for sc.Scan() {
			line := sc.Text()
			if !strings.Contains(line, " CLAUDE_PID=") {
				continue
			}
			f := strings.Fields(line)
			pid, err := strconv.Atoi(f[0])
			if err != nil {
				continue
			}
			for _, kv := range f[1:] {
				parse(pid, kv)
			}
		}
	}
	for pid, e := range out {
		if e.claudePid <= 1 {
			delete(out, pid)
		}
	}
	return out
}

var portArgRe = regexp.MustCompile(`(?:--port[= ]|-p\s+)(\d{2,5})\b`)

// listenPorts 查这些进程在监听的 TCP 端口（lsof；没有 lsof 或查不到时返回空）。
func listenPorts(pids []int) map[int][]int {
	out := map[int][]int{}
	if len(pids) == 0 {
		return out
	}
	list := make([]string, len(pids))
	for i, p := range pids {
		list[i] = strconv.Itoa(p)
	}
	b, _ := exec.Command("lsof", "-nP", "-a", "-iTCP", "-sTCP:LISTEN", "-p", strings.Join(list, ","), "-Fpn").Output()
	cur := 0
	for _, line := range strings.Split(string(b), "\n") {
		if line == "" {
			continue
		}
		switch line[0] {
		case 'p':
			cur, _ = strconv.Atoi(line[1:])
		case 'n':
			if i := strings.LastIndex(line, ":"); i >= 0 {
				if port, err := strconv.Atoi(line[i+1:]); err == nil && cur > 0 {
					out[cur] = append(out[cur], port)
				}
			}
		}
	}
	return out
}

// report 把进程树整理成上报给 Hub 的样子。
func orphanReport(items []orphanItem, ports map[int][]int) []protocol.OrphanProc {
	out := make([]protocol.OrphanProc, 0, len(items))
	for _, it := range items {
		o := protocol.OrphanProc{
			ID: procID(it.root.pid, it.root.start), Pid: it.root.pid, Start: it.root.start, State: it.state,
			Cwd: it.cwd, SessionKey: it.key, Procs: len(it.members),
		}
		if t := lstartTime(it.root.start); !t.IsZero() {
			o.StartedAt = t.UTC()
		}
		cmd := it.root.cmd
		if isShellProc(cmd) {
			cmd = procLabel(cmd)
		}
		o.Cmd = summary.Truncate(norm(cmd), 300)
		seen := map[int]bool{}
		if m := portArgRe.FindStringSubmatch(cmd); m != nil {
			if p, _ := strconv.Atoi(m[1]); p > 0 {
				o.Ports, seen[p] = append(o.Ports, p), true
			}
		}
		var listen []int
		for _, m := range it.members {
			o.CPU += m.cpu
			o.RSSKB += m.rss
			listen = append(listen, ports[m.pid]...)
		}
		sort.Ints(listen)
		for _, p := range listen {
			if !seen[p] && len(o.Ports) < 3 {
				o.Ports, seen[p] = append(o.Ports, p), true
			}
		}
		o.CPU = float64(int(o.CPU*10+0.5)) / 10
		out = append(out, o)
	}
	return out
}

func (d *Daemon) snapshot() ([]proc, map[int]procEnv, error) {
	snap, envs := d.orph.snap, d.orph.envs
	if snap == nil {
		snap = listProcs
	}
	if envs == nil {
		envs = readEnvs
	}
	ps, err := snap()
	if err != nil {
		return nil, nil, err
	}
	return ps, envs(ps), nil
}

// scanOrphans 认一遍残留进程 / 已脱离的进程，记进 daemon.db，有变化（或隔一分钟）时报给 Hub。
func (d *Daemon) scanOrphans(now time.Time, ps []proc) {
	if d.q == nil {
		return
	}
	envs := readEnvs
	if d.orph.envs != nil {
		envs = d.orph.envs
	}
	items, recs := d.classifyNow(now, ps, envs(ps))
	// 记过但已经不在的进程：标记结束（同一进程是后台命令时，R-208 的记录会补上准确的结束时间与退出码）
	live := map[string]bool{}
	for _, p := range ps {
		live[procID(p.pid, p.start)] = true
	}
	var gone []procRec
	for id, r := range recs {
		if !live[id] {
			gone = append(gone, r)
		}
	}
	if len(gone) > 0 {
		d.q.procsEnded(now, gone)
	}
	var pids []int
	for _, it := range items {
		for _, m := range it.members {
			pids = append(pids, m.pid)
		}
	}
	list := orphanReport(items, listenPorts(pids))
	var b strings.Builder
	for _, o := range list {
		b.WriteString(o.ID + "|" + o.State + "|" + o.SessionKey + "\n")
	}
	sig := b.String()
	d.mu.Lock()
	send := sig != d.orph.sig || now.Sub(d.orph.sentAt) >= orphanResend
	d.orph.last = list
	connected := d.connected
	if send && connected {
		d.orph.sig, d.orph.sentAt = sig, now
	}
	d.mu.Unlock()
	if send && connected {
		d.sendDirect(protocol.Envelope{Type: protocol.MsgOrphans, Orphans: list})
	}
}

// classifyNow 认一遍并把新认出的进程记进 daemon.db；返回进程树与（记录之前的）未结束记录。
func (d *Daemon) classifyNow(now time.Time, ps []proc, envs map[int]procEnv) ([]orphanItem, map[string]procRec) {
	recs := d.q.procsLive()
	items := classifyOrphans(ps, envs, recs, os.Getpid())
	var add []procRec
	for _, it := range items {
		for _, m := range it.members {
			if _, ok := recs[procID(m.pid, m.start)]; !ok {
				add = append(add, procRec{pid: m.pid, start: m.start, key: it.key, agent: it.agent, cwd: it.cwd, cmd: summary.Truncate(m.cmd, 500), source: "env"})
			}
		}
	}
	d.q.procsAdd(now, add)
	return items, recs
}

func (d *Daemon) sendDirect(e protocol.Envelope) {
	select {
	case d.direct <- e:
	default:
		d.log.Warn("发给 Hub 的消息排满了，丢弃", "type", e.Type)
	}
}

// resendOrphans：刚连上 Hub 时马上重扫重发（Hub 只在内存里记着各设备的列表）。
func (d *Daemon) resendOrphans() {
	d.mu.Lock()
	d.orph.sig = "\x00"
	d.orph.sentAt = time.Time{}
	d.mu.Unlock()
	select {
	case d.orphanKick <- struct{}{}:
	default:
	}
}

// handleStop 处理 Hub 转来的停止请求，把结果发回 Hub。
func (d *Daemon) handleStop(req protocol.StopProc) {
	res := d.stopProc(req)
	res.ReqID = req.ReqID
	d.log.Info("停止后台进程", "pid", req.Pid, "start", req.Start, "force", req.Force, "result", res.Result, "detail", res.Detail)
	d.sendDirect(protocol.Envelope{Type: protocol.MsgStopResult, StopResult: &res})
	d.resendOrphans()
}

var errNoSignal = errors.New("没有发信号")

// stopProc 核对后发信号：只停 daemon.db 记过的、此刻判定为残留进程或已脱离、pid 与启动时间都对得上的进程树。
func (d *Daemon) stopProc(req protocol.StopProc) protocol.StopResult {
	now := time.Now()
	ps, envs, err := d.snapshot()
	if err != nil {
		return protocol.StopResult{Result: protocol.StopFailed, Detail: "读不到进程表：" + err.Error()}
	}
	if req.Pid <= 1 || req.Start == "" {
		return protocol.StopResult{Result: protocol.StopRefused, Detail: "缺少进程号或启动时间"}
	}
	items, recs := d.classifyNow(now, ps, envs)
	var it *orphanItem
	for i := range items {
		if items[i].root.pid == req.Pid && items[i].root.start == req.Start {
			it = &items[i]
			break
		}
	}
	if it == nil {
		byPid, _ := procIndex(ps)
		p, ok := byPid[req.Pid]
		switch {
		case !ok:
			return protocol.StopResult{Result: protocol.StopGone, Detail: "进程已经不在了"}
		case p.start != req.Start:
			return protocol.StopResult{Result: protocol.StopRefused, Detail: "启动时间对不上：这个进程号已经是别的进程了，没有发信号"}
		}
		if _, known := recs[procID(p.pid, p.start)]; known {
			return protocol.StopResult{Result: protocol.StopRefused, Detail: "开它的会话还管着它（正常运行），不能停"}
		}
		if _, ok := envs[p.pid]; ok {
			return protocol.StopResult{Result: protocol.StopRefused, Detail: "开它的会话还管着它（正常运行），不能停"}
		}
		return protocol.StopResult{Result: protocol.StopRefused, Detail: "不是纸鸢记下的会话后台进程，不能停"}
	}
	if _, known := recs[procID(it.root.pid, it.root.start)]; !known {
		return protocol.StopResult{Result: protocol.StopRefused, Detail: "还没记进本机记录，稍后再试"}
	}
	sig, name, grace := syscall.SIGTERM, "SIGTERM", stopGrace
	if req.Force {
		sig, name, grace = syscall.SIGKILL, "SIGKILL", killGrace
	}
	if d.orph.grace > 0 {
		grace = d.orph.grace
	}
	kill := d.orph.kill
	if kill == nil {
		kill = syscall.Kill
	}
	sent := 0
	var firstErr error
	for _, m := range it.members { // 根进程在第一个
		if err := kill(m.pid, sig); err == nil {
			sent++
		} else if !errors.Is(err, syscall.ESRCH) && firstErr == nil {
			firstErr = err
		}
	}
	if sent == 0 {
		if firstErr == nil {
			firstErr = errNoSignal
		}
		res := protocol.StopResult{Result: protocol.StopFailed, Detail: "发信号失败：" + firstErr.Error()}
		d.q.procStopped(now, it.root.pid, it.root.start, name, res.Result)
		return res
	}
	start := time.Now()
	remain := len(it.members)
	for {
		remain = d.stillAlive(it.members)
		if remain == 0 || time.Since(start) >= grace {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	res := protocol.StopResult{Signal: name, Signals: sent, Remain: remain, Waited: int(time.Since(start) / time.Millisecond)}
	switch {
	case remain == 0 && req.Force:
		res.Result = protocol.StopKilled
	case remain == 0:
		res.Result = protocol.StopExited
	case req.Force:
		res.Result, res.Detail = protocol.StopFailed, "发了 SIGKILL 仍在运行"
	default:
		res.Result = protocol.StopRunning
	}
	d.q.procStopped(time.Now(), it.root.pid, it.root.start, name, res.Result)
	if remain == 0 {
		d.shells.markStopped(it.root.pid, it.root.start)
	}
	return res
}

// stillAlive：这些进程里还有几个在跑（pid 与启动时间都对得上才算）。
func (d *Daemon) stillAlive(members []proc) int {
	snap := d.orph.snap
	if snap == nil {
		snap = listProcs
	}
	ps, err := snap()
	if err != nil {
		return len(members)
	}
	byPid, _ := procIndex(ps)
	n := 0
	for _, m := range members {
		if p, ok := byPid[m.pid]; ok && p.start == m.start {
			n++
		}
	}
	return n
}
