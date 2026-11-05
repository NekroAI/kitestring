package daemon

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/NekroAI/kitestring/internal/protocol"
	"github.com/NekroAI/kitestring/internal/summary"
)

// 后台任务（R-162）：hook 不会告诉我们后台 shell 什么时候结束，daemon 每隔几秒扫一次进程表，
// 找 agent 进程（Claude Code / Codex）下面的 shell 子进程：正在执行的前台工具调用（命令对得上）不算，
// 活过宽限期的其余 shell 就是后台任务；进程没了即视为结束。变化时上报 KsTasks（当前全集）。
// 只上报标签（run_in_background 的描述或截断的命令）与开始时间，不读命令输出。
// 子代理不靠进程表：Hub 按 SubagentStart / SubagentStop 的 agent_id 记，这里只把父会话 Agent 调用的描述捎给 SubagentStart。

const (
	taskScanEvery = 3 * time.Second
	taskGrace     = 2 * time.Second // 刚起的 shell 可能是前台调用还没登记、hook 或状态栏命令，活过宽限期才算
)

// shellCall 是一次 shell 工具调用（PreToolUse 记下，PostToolUse 时前台的删掉，后台的留作标签线索）。
type shellCall struct {
	cmd  string
	desc string
	bg   bool
	at   time.Time
}

// agentCall 是父会话发起、还没对上 SubagentStart 的子代理调用。
type agentCall struct {
	seq  int64 // daemon.db 里的行号（0 表示只在内存）
	typ  string
	desc string
	at   time.Time
}

type taskState struct {
	calls   map[string]map[string]*shellCall // 会话键 → tool_use_id → 调用
	agents  map[string][]agentCall           // 会话键 → 待对上的子代理调用（先进先出）
	sent    map[string]string                // 会话键 → 上次上报的全集签名
	lastEvt map[string]time.Time             // 会话键 → 最近一次 hook 事件（同一进程 /clear 换了会话时认最新的）
}

func newTaskState() taskState {
	return taskState{calls: map[string]map[string]*shellCall{}, agents: map[string][]agentCall{}, sent: map[string]string{}, lastEvt: map[string]time.Time{}}
}

func isShellTool(name string) bool {
	switch name {
	case "Bash", "shell", "exec_command", "local_shell", "bash", "pwsh":
		return true
	}
	return false
}

func isAgentTool(name string) bool { return name == "Agent" || name == "Task" }

// shellCommand 取 shell 工具调用的命令原文（只在本机用来对进程，不上报）。
func shellCommand(in map[string]any) string {
	if c := str(in, "command"); c != "" {
		return c
	}
	if arr, ok := in["command"].([]any); ok {
		parts := make([]string, 0, len(arr))
		for _, a := range arr {
			if s, ok := a.(string); ok {
				parts = append(parts, s)
			}
		}
		// Codex 的数组形式常是 ["bash", "-lc", "命令"]：取最后一段
		if len(parts) >= 3 && (parts[len(parts)-2] == "-lc" || parts[len(parts)-2] == "-c") {
			return parts[len(parts)-1]
		}
		return strings.Join(parts, " ")
	}
	return ""
}

// noteTaskEvent 用 hook 事件更新后台任务线索；SubagentStart 时把对上的描述写进 ev.ToolBrief。
func (d *Daemon) noteTaskEvent(key string, p map[string]any, ev *protocol.Event) {
	now := time.Now()
	d.mu.Lock()
	defer d.mu.Unlock()
	t := &d.tasks
	t.lastEvt[key] = now
	switch ev.Name {
	case "PreToolUse":
		in, _ := p["tool_input"].(map[string]any)
		switch {
		case isShellTool(ev.ToolName) && ev.ToolUseID != "":
			cmd := shellCommand(in)
			if cmd == "" {
				return
			}
			bg, _ := in["run_in_background"].(bool)
			if t.calls[key] == nil {
				t.calls[key] = map[string]*shellCall{}
			}
			t.calls[key][ev.ToolUseID] = &shellCall{cmd: cmd, desc: str(in, "description"), bg: bg, at: now}
		case isAgentTool(ev.ToolName):
			c := agentCall{typ: str(in, "subagent_type"), desc: str(in, "description"), at: now}
			if d.q != nil {
				d.q.agentCallsAdd(key, c)
			} else {
				t.agents[key] = append(d.agentQueue(key, now), c)
			}
		}
	case "PostToolUse", "PostToolUseFailure":
		if c := t.calls[key][ev.ToolUseID]; c != nil && !c.bg {
			delete(t.calls[key], ev.ToolUseID)
		}
	case "SubagentStart":
		q := d.agentQueue(key, now)
		for i, a := range q {
			if a.typ == "" || ev.AgentType == "" || a.typ == ev.AgentType {
				ev.ToolBrief = summary.Truncate(strings.Join(strings.Fields(a.desc), " "), 80)
				if d.q != nil {
					d.q.agentCallDel(a.seq)
				} else {
					t.agents[key] = append(q[:i:i], q[i+1:]...)
				}
				break
			}
		}
		if ev.ToolBrief == "" {
			// 配不上（重启前派出的、或 hook 漏了）：从子代理自己的记录里取第一句提示词当名字
			ev.ToolBrief = subagentPromptName(str(p, "transcript_path"), ev.AgentID)
		}
	case "SessionEnd":
		delete(t.calls, key)
		delete(t.agents, key)
		if d.q != nil {
			d.q.agentCallsClear(key)
		}
	}
}

// agentQueue 返回会话待对上的子代理调用（先进先出，丢掉 10 分钟前的；有 daemon.db 时以它为准，重启不丢，调用方持锁）。
func (d *Daemon) agentQueue(key string, now time.Time) []agentCall {
	if d.q == nil {
		q := d.tasks.agents[key]
		for len(q) > 0 && now.Sub(q[0].at) > 10*time.Minute {
			q = q[1:]
		}
		d.tasks.agents[key] = q
		return q
	}
	var out []agentCall
	for _, r := range d.q.agentCallsLoad(key) {
		if now.Sub(r.at) > 10*time.Minute {
			d.q.agentCallDel(r.seq)
			continue
		}
		c := r.agentCall
		c.seq = r.seq
		out = append(out, c)
	}
	return out
}

// subagentPromptName 读子代理记录（<会话>/subagents/agent-<id>.jsonl）里第一条提示词，取第一句当名字；读不到返回空。
func subagentPromptName(transcript, agentID string) string {
	if transcript == "" || agentID == "" {
		return ""
	}
	path := filepath.Join(strings.TrimSuffix(transcript, ".jsonl"), "subagents", "agent-"+agentID+".jsonl")
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	var buf [64 << 10]byte
	n, _ := f.Read(buf[:])
	line, _, _ := strings.Cut(string(buf[:n]), "\n")
	var m struct {
		Message struct {
			Content any `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal([]byte(line), &m) != nil {
		return ""
	}
	text := ""
	switch c := m.Message.Content.(type) {
	case string:
		text = c
	case []any:
		for _, b := range c {
			if bm, ok := b.(map[string]any); ok {
				if s, _ := bm["text"].(string); s != "" {
					text = s
					break
				}
			}
		}
	}
	text = strings.Join(strings.Fields(text), " ")
	if i := strings.IndexAny(text, "。！？"); i > 8 {
		text = text[:i]
	}
	return summary.Truncate(text, 60)
}

// proc 是进程表里的一行。
type proc struct {
	pid, ppid int
	age       time.Duration
	cmd       string
	start     string  // 启动时间（ps lstart，LC_ALL=C，空白规整为单个空格）：和 pid 一起认进程，防 pid 复用（R-209）
	cpu       float64 // %CPU
	rss       int64   // 常驻内存（KB）
}

// listProcs 读一次本机进程表。
func listProcs() ([]proc, error) {
	cmd := exec.Command("ps", "-A", "-ww", "-o", "pid=,ppid=,%cpu=,rss=,stat=,lstart=,etime=,command=")
	cmd.Env = psEnv()
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var ps []proc
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 12 { // pid ppid %cpu rss stat lstart（5 段）etime 命令
			continue
		}
		if strings.HasPrefix(f[4], "Z") {
			continue // 僵尸进程已经退出，只是父进程还没收尸
		}
		pid, err1 := strconv.Atoi(f[0])
		ppid, err2 := strconv.Atoi(f[1])
		if err1 != nil || err2 != nil {
			continue
		}
		cpu, _ := strconv.ParseFloat(f[2], 64)
		rss, _ := strconv.ParseInt(f[3], 10, 64)
		ps = append(ps, proc{pid: pid, ppid: ppid, cpu: cpu, rss: rss, start: strings.Join(f[5:10], " "), age: parseEtime(f[10]), cmd: strings.Join(f[11:], " ")})
	}
	return ps, nil
}

// psEnv：启动时间按 C 的格式写（lstart 的段数固定），命令行按 UTF-8 原样输出（C 下中文会被转义成 M-x，对不上调用，R-208）。
func psEnv() []string {
	env := make([]string, 0, len(os.Environ())+3)
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "LC_ALL=") && !strings.HasPrefix(kv, "LC_TIME=") && !strings.HasPrefix(kv, "LC_CTYPE=") {
			env = append(env, kv)
		}
	}
	return append(env, "LC_TIME=C", "LC_CTYPE=en_US.UTF-8")
}

// parseEtime 解析 ps 的 etime（[[dd-]hh:]mm:ss）。
func parseEtime(s string) time.Duration {
	days := 0
	if a, b, ok := strings.Cut(s, "-"); ok {
		days, _ = strconv.Atoi(a)
		s = b
	}
	parts := strings.Split(s, ":")
	secs := 0
	for _, p := range parts {
		n, _ := strconv.Atoi(p)
		secs = secs*60 + n
	}
	return time.Duration(days*86400+secs) * time.Second
}

var (
	// 常驻或已知的子进程不算后台任务：ks hook / ks wait（待命）/ ks mcp 与其他 MCP 服务
	ignoreProcRe = regexp.MustCompile(`(^|[\s/'"])ks\s+(hook|wait|mcp)\b|mcp[-_ ]?server|server[-_]mcp|\bmcp\b`)
	claudeEvalRe = regexp.MustCompile(`eval '((?:[^']|'\\'')*)'`)
)

// isShellProc：命令是 sh / bash / zsh / fish / dash 带 -c（Claude Code 与 Codex 都这样跑命令）。
func isShellProc(cmd string) bool {
	f := strings.Fields(cmd)
	if len(f) < 2 {
		return false
	}
	switch strings.TrimPrefix(filepath.Base(f[0]), "-") {
	case "sh", "bash", "zsh", "fish", "dash":
	default:
		return false
	}
	for _, a := range f[1:min(3, len(f))] {
		if strings.HasPrefix(a, "-") && strings.Contains(a, "c") {
			return true
		}
	}
	return false
}

// procLabel 从 shell 进程的命令行里取出真正执行的命令。
func procLabel(cmd string) string {
	if m := claudeEvalRe.FindStringSubmatch(cmd); m != nil {
		return strings.ReplaceAll(m[1], `'\''`, `'`)
	}
	f := strings.Fields(cmd)
	for i, a := range f {
		if i > 0 && strings.HasPrefix(a, "-") && strings.Contains(a, "c") {
			return strings.Join(f[i+1:], " ")
		}
	}
	return cmd
}

func norm(s string) string { return strings.Join(strings.Fields(s), " ") }

// matchCall：进程命令行里是否就是这条调用的命令（原样，或 Claude Code 转义单引号后的样子）。
func matchCall(procCmd string, c *shellCall) bool {
	pc, cc := norm(procCmd), norm(c.cmd)
	if cc == "" {
		return false
	}
	if strings.Contains(pc, cc) || strings.Contains(pc, norm(strings.ReplaceAll(c.cmd, `'`, `'\''`))) {
		return true
	}
	// 多行命令在进程表里可能被改写，退而比较第一行的前 40 个字
	first, _, _ := strings.Cut(strings.TrimSpace(c.cmd), "\n")
	if r := []rune(norm(first)); len(r) >= 12 {
		return strings.Contains(norm(procLabel(procCmd)), string(r[:min(40, len(r))]))
	}
	return false
}

// sessionTasks 按进程表算出一个会话当前在跑的后台 shell（调用方持锁）。
// matched 是对上了后台调用的 shell（tool_use_id → 进程），不论活了多久（R-208 据此按进程号 + 启动时间盯住它）；
// loose 是没对上任何调用、活过宽限期的后台 shell（Codex 的后台命令只能这样认）。
func sessionTasks(now time.Time, children []proc, calls map[string]*shellCall) (out []protocol.Task, matched map[string]proc, loose []proc) {
	used := map[string]bool{}
	matched = map[string]proc{}
	var unmatched []proc
	for _, p := range children {
		if !isShellProc(p.cmd) || ignoreProcRe.MatchString(p.cmd) {
			continue
		}
		var hit *shellCall
		for id, c := range calls {
			if !used[id] && matchCall(p.cmd, c) {
				hit, used[id] = c, true
				if c.bg {
					matched[id] = p
				}
				break
			}
		}
		if hit != nil && !hit.bg {
			continue // 前台调用还在跑
		}
		if p.age < taskGrace {
			continue
		}
		label := ""
		if hit != nil {
			label = hit.desc
		}
		if label == "" {
			label = procLabel(p.cmd)
		}
		if hit == nil {
			unmatched = append(unmatched, p)
		}
		out = append(out, protocol.Task{
			ID:        "sh-" + strconv.Itoa(p.pid),
			Label:     summary.Truncate(norm(label), 80),
			StartedAt: now.Add(-p.age).Truncate(time.Second).UTC(),
		})
	}
	// 命令没对上的前台调用（命令行被改写过）：把同样多个最新的未对上 shell 当作它们，不算后台
	fg := 0
	for id, c := range calls {
		if !c.bg && !used[id] {
			fg++
		}
	}
	if fg > 0 && len(unmatched) > 0 {
		sort.Slice(unmatched, func(i, j int) bool { return unmatched[i].age < unmatched[j].age })
		drop := map[string]bool{}
		for _, p := range unmatched[:min(fg, len(unmatched))] {
			drop["sh-"+strconv.Itoa(p.pid)] = true
		}
		kept := out[:0]
		for _, t := range out {
			if !drop[t.ID] {
				kept = append(kept, t)
			}
		}
		out = kept
		keptLoose := unmatched[:0]
		for _, p := range unmatched {
			if !drop["sh-"+strconv.Itoa(p.pid)] {
				keptLoose = append(keptLoose, p)
			}
		}
		unmatched = keptLoose
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.Before(out[j].StartedAt) })
	return out, matched, unmatched
}

func taskSig(ts []protocol.Task) string {
	var b strings.Builder
	for _, t := range ts {
		b.WriteString(t.ID + "\x00" + t.Label + "\x01")
	}
	return b.String()
}

// watchTasks 定期扫描进程表，后台 shell 有变化时上报（R-162）。
func (d *Daemon) watchTasks(ctx context.Context) {
	t := time.NewTicker(taskScanEvery)
	defer t.Stop()
	n := orphanEvery - 1 // 启动后第一轮就扫残留进程
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-d.orphanKick:
			n = orphanEvery // 停止进程后、重连 Hub 后马上重扫一次残留进程
		}
		now := time.Now()
		ps, err := listProcs()
		if err != nil {
			continue
		}
		d.scanTasks(now, ps)
		if n++; n >= orphanEvery {
			n = 0
			d.scanOrphans(now, ps)
		}
	}
}

func (d *Daemon) scanTasks(now time.Time, ps []proc) {
	// 每个 agent 进程归到一个会话：同一进程 /clear 后换了会话取最近有事件的；
	// Codex 经 app-server / 桌面 App 跑时一个进程承载多个会话，分不清归属，不认
	byPid := map[int]string{}
	agentInst := map[int]string{} // agent 进程号 → “进程号@启动时间”
	shared := map[int]bool{}
	d.mu.Lock()
	for inst, key := range d.instances {
		pidStr, _, _ := strings.Cut(inst, "@")
		pid, err := strconv.Atoi(pidStr)
		if err != nil {
			continue
		}
		// 一个 DSH Host 同时开着多个会话时同样分不清 shell 归谁（R-251）
		if info := d.sessions[key]; info != nil && (info.client == "app-server" || info.client == "desktop") || len(d.dshInst[inst]) > 1 {
			shared[pid] = true
			continue
		}
		if cur, ok := byPid[pid]; !ok || d.tasks.lastEvt[key].After(d.tasks.lastEvt[cur]) {
			byPid[pid] = key
			agentInst[pid] = inst
		}
	}
	d.mu.Unlock()
	if len(byPid) == 0 {
		return
	}
	children := map[int][]proc{}
	for _, p := range ps {
		if _, ok := byPid[p.ppid]; ok {
			children[p.ppid] = append(children[p.ppid], p)
		}
	}
	type report struct {
		key   string
		tasks []protocol.Task
	}
	var reports []report
	var shells []bgShell
	seen := map[string]shellSeen{}
	pend := map[string]map[string]*shellCall{}
	if d.shells != nil {
		for _, key := range byPid {
			pend[key] = d.shells.calls(key)
		}
	}
	d.mu.Lock()
	for pid, key := range byPid {
		if shared[pid] {
			continue
		}
		calls := d.tasks.calls[key]
		// 前台调用丢了 PostToolUse（被中断）时进程也没了，过久的线索清掉
		for id, c := range calls {
			if now.Sub(c.at) > 24*time.Hour {
				delete(calls, id)
			}
		}
		// 记着的后台命令（R-208，daemon 重启后、从对话记录补上的）也拿来对进程
		if len(pend[key]) > 0 {
			all := make(map[string]*shellCall, len(calls)+len(pend[key]))
			for id, c := range pend[key] {
				all[id] = c
			}
			for id, c := range calls {
				all[id] = c
			}
			calls = all
		}
		tasks, matched, loose := sessionTasks(now, children[pid], calls)
		seen[key] = shellSeen{matched: matched, loose: loose}
		for _, tk := range tasks {
			if sp, err := strconv.Atoi(strings.TrimPrefix(tk.ID, "sh-")); err == nil {
				shells = append(shells, bgShell{key: key, agent: agentInst[pid], pid: sp})
			}
		}
		// 后台调用的线索在进程结束后就没用了
		for id, c := range calls {
			if c.bg && now.Sub(c.at) > time.Minute && !anyMatch(children[pid], c) {
				delete(calls, id)
			}
		}
		sig := taskSig(tasks)
		if prev, ok := d.tasks.sent[key]; ok && prev == sig {
			continue
		}
		d.tasks.sent[key] = sig
		reports = append(reports, report{key, tasks})
	}
	d.mu.Unlock()
	d.recordShells(now, ps, shells)
	if d.shells != nil {
		d.shells.tick(now, ps, seen)
		d.sendShells()
	}
	for _, r := range reports {
		tool, sid, ok := strings.Cut(r.key, ":")
		if !ok {
			continue
		}
		ev := protocol.Event{ID: eventID(), Time: now.UTC(), Tool: tool, Name: protocol.EventTasks, SessionID: sid, Tasks: r.tasks}
		if err := d.q.pushEvent(ev); err != nil {
			d.log.Warn("上报后台任务失败", "err", err)
			continue
		}
		d.wake()
	}
}

func anyMatch(ps []proc, c *shellCall) bool {
	for _, p := range ps {
		if matchCall(p.cmd, c) {
			return true
		}
	}
	return false
}
