package daemon

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/NekroAI/kitestring/internal/protocol"
	"github.com/NekroAI/kitestring/internal/summary"
)

// 后台命令（R-208）：工作台的 shell 泳道。每条 run_in_background 的命令（Claude Code）、每个 Codex 会话下活过宽限期的后台 shell
// 记一条：类型、描述、命令、起止、结果、最后一行输出。只在本机处理，发给 Hub 的只有这些字段（最后一行输出截到 160 字），不传输出内容。
//
// 结束时间与退出码从哪来（hook 只告诉我们命令开始了）：
//  1. 进程：扫进程表时（每 3 秒）把命令对上它的 shell 进程，按“进程号 + 启动时间”盯住；进程不在了，结束时间就是这一刻（误差 3 秒内）。
//     daemon 不是这些进程的父进程，拿不到 wait status，所以进程这一路只给结束时间。
//  2. Claude Code 的结束通知：后台命令结束后，Claude Code 往对话里塞一条 <task-notification>（status = completed / failed / killed，
//     summary 里写着 exit code）。它既经 UserPromptSubmit hook 到达（实时），也写进对话记录（daemon 本来就在增量读，R-206）——
//     hook 漏了、daemon 重启期间结束的，从对话记录补上。退出码就取这里。
//  3. Claude Code 的输出文件（<临时目录>/claude-<uid>/<项目>/<会话>/tasks/<任务编号>.output）：命令结束后它在末尾写一行
//     “[exited with code N]” 或 “[killed]”。通知没来（会话已经退出、hook 漏了）也有，退出码多半从这里补齐。
//  4. Stop hook 的 background_tasks：每轮结束时列着后台任务的状态（completed / failed / killed），通知还没到时先用它定成败。
//
// 拿不到退出码的：Codex（没有结束通知和输出文件）、Claude Code 进程被强行结束、来不及写结束记录的。
// 这些照样有准确的结束时间，结果标“已结束 · 退出码未知”。只有本功能上线前开始、进程早已不在、对话里也没有结束通知的，
// 才连结束时间都不知道（lost）；有输出文件时取它最后修改的时间当结束时间。
//
// 跑飞（R-209 定下、挪到这里的判定）：在跑的命令，整棵进程树的 CPU 连续 5 分钟不低于一个核的 90%，
// 或常驻内存连续 10 分钟只涨不跌、涨到 1.5 倍以上且多了 200 MB 以上，就标黄；回落后取消。

const (
	shellMiss      = 3                  // 起来了、却连着这么多轮（约 9 秒）在进程表里对不上：太短（没赶上扫描）或命令行认不出，按输出文件定结束时间
	shellPendWait  = 30 * time.Minute   // 只有 PreToolUse、一直没起来（等授权被拒等）的，这么久后丢掉
	shellReadEvery = 15 * time.Second   // 在跑的命令多久读一次输出文件末尾
	shellMemKeep   = 2 * 24 * time.Hour // 结束这么久后不再留在内存里（迟到的通知还能对上）
	shellKeep      = 8 * 24 * time.Hour // daemon.db 里留多久（近 1 周档要计数）
	shellBatch     = 200
	shellAckWait   = 30 * time.Second
	shellLastMax   = 160
	shellCmdMax    = 1000

	hotCPU      = 90.0
	hotCPUFor   = 5 * time.Minute
	hotMemFor   = 10 * time.Minute
	hotMemGrow  = 1.5
	hotMemMinKB = 200 << 10
)

// shellRec 是一条后台命令：发给 Hub 的部分在 c 里，其余只在本机。
type shellRec struct {
	c        protocol.ShellCmd
	launched bool   // 确实起来了（PostToolUse、进程表里看到、对话记录里有结果）；没起来的不发
	taskID   string // Claude Code 的后台任务编号（输出文件 <编号>.output）
	outFile  string
	pid      int
	pstart   string
	ver      int64
	sent     int64
	pendAt   time.Time

	// 只在内存里
	miss     int
	readAt   time.Time
	outSize  int64
	cpuSince time.Time
	mem      []memSample // 每分钟一个（取这一分钟里的最大值）
}

type memSample struct {
	minute int64
	kb     int64
}

// shellSeen 是一个会话这一轮在进程表里看到的后台 shell。
type shellSeen struct {
	matched map[string]proc // 对上了后台命令的（ref → 进程）
	loose   []proc          // 没对上调用的（Codex 的后台命令）
}

type shellTracker struct {
	db  *sql.DB
	now func() time.Time
	tmp []string // Claude Code 放后台任务输出的目录（/tmp/claude-<uid>）

	mu      sync.Mutex
	recs    map[string]*shellRec // 会话键 + ref
	batches map[string][]shellSent
	seq     int64
	sentAt  time.Time
}

type shellSent struct {
	id  string
	ver int64
}

func shellID(key, ref string) string { return key + "\x00" + ref }

func openShells(db *sql.DB) (*shellTracker, error) {
	_, err := db.Exec(`
		-- 后台命令（R-208）：data 是发给 Hub 的那部分（protocol.ShellCmd 的 JSON）；ver / sent：有变化的版本号与 Hub 回执过的版本号
		CREATE TABLE IF NOT EXISTS bg_cmds (session_key TEXT NOT NULL, ref TEXT NOT NULL, data TEXT NOT NULL,
			launched INTEGER NOT NULL DEFAULT 0, task_id TEXT NOT NULL DEFAULT '', out_file TEXT NOT NULL DEFAULT '',
			pid INTEGER NOT NULL DEFAULT 0, pstart TEXT NOT NULL DEFAULT '', ver INTEGER NOT NULL DEFAULT 1, sent INTEGER NOT NULL DEFAULT 0,
			updated INTEGER NOT NULL DEFAULT 0, PRIMARY KEY (session_key, ref));`)
	if err != nil {
		return nil, err
	}
	s := &shellTracker{db: db, now: time.Now, recs: map[string]*shellRec{}, batches: map[string][]shellSent{}}
	uid := os.Getuid()
	s.tmp = []string{fmt.Sprintf("/tmp/claude-%d", uid)}
	if d := os.Getenv("CLAUDE_CODE_TMPDIR"); d != "" {
		s.tmp = append(s.tmp, filepath.Join(d, fmt.Sprintf("claude-%d", uid)))
	}
	now := s.now()
	_, _ = db.Exec(`DELETE FROM bg_cmds WHERE updated < ? AND sent >= ver`, now.Add(-shellKeep).Unix())
	rows, err := db.Query(`SELECT session_key, ref, data, launched, task_id, out_file, pid, pstart, ver, sent, updated FROM bg_cmds`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		r := &shellRec{}
		var key, ref, data string
		var launched int
		var updated int64
		if err := rows.Scan(&key, &ref, &data, &launched, &r.taskID, &r.outFile, &r.pid, &r.pstart, &r.ver, &r.sent, &updated); err != nil {
			return nil, err
		}
		if json.Unmarshal([]byte(data), &r.c) != nil {
			continue
		}
		r.launched = launched == 1
		r.pendAt = time.Unix(updated, 0)
		if r.c.Status != protocol.ShellRunning && r.sent >= r.ver && now.Sub(time.UnixMilli(r.c.End)) > shellMemKeep {
			continue // 早结束的只留在库里
		}
		s.recs[shellID(key, ref)] = r
	}
	return s, rows.Err()
}

// save 写进 daemon.db；bump 为真时版本号加一（要重新发给 Hub）。调用方持锁。
func (s *shellTracker) save(r *shellRec, bump bool) {
	if bump {
		r.ver++
	}
	b, _ := json.Marshal(r.c)
	_, _ = s.db.Exec(`INSERT INTO bg_cmds(session_key, ref, data, launched, task_id, out_file, pid, pstart, ver, sent, updated) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(session_key, ref) DO UPDATE SET data = excluded.data, launched = excluded.launched, task_id = excluded.task_id, out_file = excluded.out_file,
			pid = excluded.pid, pstart = excluded.pstart, ver = excluded.ver, updated = excluded.updated`,
		r.c.Key, r.c.Ref, string(b), btoi(r.launched), r.taskID, r.outFile, r.pid, r.pstart, r.ver, r.sent, s.now().Unix())
	if r.pid > 0 && r.c.End > 0 {
		// R-209 记下的同一个进程：补上准确的结束时间与退出码
		_, _ = s.db.Exec(`UPDATE bg_procs SET ended_at = ?, exit_code = COALESCE(?, exit_code) WHERE pid = ? AND start = ?`,
			time.UnixMilli(r.c.End).UTC().Format(time.RFC3339), r.c.Exit, r.pid, r.pstart)
	}
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ---------- 识别 ----------

var (
	shellWaitRe = regexp.MustCompile(`(^|[\s/'"&;|(])ks\s+wait\b`) // ks wait 是会话的待命状态，不单独成行
	cdPrefixRe  = regexp.MustCompile(`^(cd\s+("[^"]*"|'[^']*'|\S+)\s*(&&|;)\s*)+`)

	kindDeployRe  = regexp.MustCompile(`(?i)\bdeploy\b|deploy\.sh\b|\bkubectl\s+(apply|rollout)\b|\bhelm\s+(install|upgrade)\b|\bterraform\s+apply\b|\bvercel\b.*--prod`)
	kindPollRe    = regexp.MustCompile(`(?s)\b(until|while)\b.*\bdo\b.*\bsleep\b|\btail\s+-[a-zA-Z]*[fF]\b|\bgh\s+(run|pr\s+checks)\s+watch\b|\bgh\s+pr\s+checks\b.*--watch|\bwatch\s+-n\b`)
	kindBuildRe   = regexp.MustCompile(`\bgo\s+(test|build|vet|generate|run\s+\S*test)\b|\b(pnpm|npm|yarn|bun)\s+(run\s+)?(build|test|lint|typecheck|check|e2e|gen\S*)\b|\b(vitest|jest|pytest|tsc|eslint|playwright\s+test)\b|\bvite\s+build\b|\bcargo\s+(build|test|check|clippy|install)\b|\bmake\b|\bxcodebuild\b|\bswift\s+(build|test)\b|\bgradlew?\b|\bmvn\b|\bbuild[-_.]\S*\.(sh|ps1)\b|\bbuild\.(sh|ps1)\b`)
	kindServiceRe = regexp.MustCompile(`\b(pnpm|npm|yarn|bun)\s+(run\s+)?(dev|start|serve|preview)\b|\bvite\b|\bnext\s+(dev|start)\b|http\.server|\b(uvicorn|gunicorn|nodemon)\b|\bflask\s+run\b|\brails\s+s(erver)?\b|\bks\s+(hub|daemon)\b|--port\b|--addr\b|\bserve\b|\bdocker[\s-]compose\s+up\b|\bdocker\s+run\b`)
)

func isShellWait(cmd string) bool { return shellWaitRe.MatchString(cmd) }

// shellKind 按命令认类型：部署、轮询、构建 / 测试、服务，都不像的是其他。
func shellKind(cmd string) string {
	switch {
	case kindDeployRe.MatchString(cmd):
		return protocol.ShellDeploy
	case kindPollRe.MatchString(cmd):
		return protocol.ShellPoll
	case kindBuildRe.MatchString(cmd):
		return protocol.ShellBuild
	case kindServiceRe.MatchString(cmd):
		return protocol.ShellService
	}
	return protocol.ShellOther
}

// cmdSummary：没有描述时用的命令摘要——第一行，去掉开头的 cd 某目录 &&。
func cmdSummary(cmd string) string {
	first, _, _ := strings.Cut(strings.TrimSpace(cmd), "\n")
	first = norm(first)
	if s := strings.TrimSpace(cdPrefixRe.ReplaceAllString(first, "")); s != "" {
		first = s
	}
	return summary.Truncate(first, 80)
}

// shellResult 把结束通知的状态与退出码换成结果。被信号终止的（130 Ctrl-C、137 SIGKILL、143 SIGTERM、144 Claude Code 停掉）算被停止。
func shellResult(status string, exit *int) string {
	switch status {
	case "completed":
		if exit != nil && *exit != 0 {
			return protocol.ShellFailed
		}
		return protocol.ShellOK
	case "failed":
		if exit != nil {
			switch *exit {
			case 129, 130, 137, 143, 144:
				return protocol.ShellStopped
			}
		}
		return protocol.ShellFailed
	case "killed", "stopped":
		return protocol.ShellStopped
	}
	return ""
}

// ---------- 来自 hook / 对话记录 ----------

// begin 记下一条开始了的后台命令（PreToolUse，或对话记录里的 tool_use）。已记过的只补描述。
func (s *shellTracker) begin(key, agent, ref, cmd, desc string, at time.Time) {
	if s == nil || key == "" || ref == "" || strings.TrimSpace(cmd) == "" || isShellWait(cmd) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id := shellID(key, ref)
	if r := s.recs[id]; r != nil {
		if r.c.Label == "" || (desc != "" && r.c.Label != summary.Truncate(norm(desc), 80) && r.c.Label == cmdSummary(r.c.Cmd)) {
			r.c.Label = labelOf(cmd, desc)
			s.save(r, r.launched)
		}
		return
	}
	r := &shellRec{c: protocol.ShellCmd{Key: key, Agent: agent, Ref: ref, Kind: shellKind(cmd), Label: labelOf(cmd, desc), Cmd: summary.Truncate(strings.TrimSpace(cmd), shellCmdMax),
		Start: at.UnixMilli(), Status: protocol.ShellRunning}, pendAt: s.now()}
	s.recs[id] = r
	s.save(r, false)
}

func labelOf(cmd, desc string) string {
	if d := norm(desc); d != "" {
		return summary.Truncate(d, 80)
	}
	return cmdSummary(cmd)
}

// launch：命令确实起来了（PostToolUse 或对话记录里的结果），带着 Claude Code 的后台任务编号。
func (s *shellTracker) launch(key, ref, taskID string, at time.Time) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.recs[shellID(key, ref)]
	if r == nil {
		return
	}
	changed := !r.launched
	if !r.launched && r.pid == 0 && !at.IsZero() && r.c.Status == protocol.ShellRunning && at.UnixMilli()-r.c.Start > 5000 {
		r.c.Start = at.UnixMilli() // 等了授权才起来：从起来的时刻算
	}
	r.launched = true
	if taskID != "" && r.taskID != taskID {
		r.taskID, changed = taskID, true
	}
	if changed {
		s.save(r, true)
	}
}

// drop：PreToolUse 之后没起来（PostToolUseFailure，被拒绝等）。
func (s *shellTracker) drop(key, ref string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id := shellID(key, ref)
	if r := s.recs[id]; r != nil && !r.launched {
		delete(s.recs, id)
		_, _ = s.db.Exec(`DELETE FROM bg_cmds WHERE session_key = ? AND ref = ?`, key, ref)
	}
}

// notify：Claude Code 的结束通知（或 Stop 的 background_tasks）。exit 为空表示通知里没写退出码。
func (s *shellTracker) notify(key, ref, taskID, status string, exit *int, at time.Time, outFile string) {
	if s == nil {
		return
	}
	res := shellResult(status, exit)
	if res == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.recs[shellID(key, ref)]
	if r == nil && taskID != "" {
		for _, x := range s.recs {
			if x.c.Key == key && x.taskID == taskID {
				r = x
				break
			}
		}
	}
	if r == nil {
		return
	}
	changed := !r.launched
	r.launched = true
	if outFile != "" && r.outFile == "" {
		r.outFile = outFile
	}
	if taskID != "" && r.taskID == "" {
		r.taskID = taskID
	}
	if r.c.End == 0 {
		// 进程表里还盯着、还没发现它不在：通知来了就是结束了
		r.c.End = max(at.UnixMilli(), r.c.Start)
		changed = true
	}
	// 被纸鸢停掉的不改回失败；已经有退出码的结果，不被没写退出码的（Stop 的 background_tasks）改掉
	final := r.c.Status != protocol.ShellRunning && r.c.Status != protocol.ShellEnded && r.c.Status != protocol.ShellLost
	keep := (r.c.Status == protocol.ShellStopped && res == protocol.ShellFailed) || (final && exit == nil && r.c.Exit != nil)
	if !keep && r.c.Status != res {
		r.c.Status, changed = res, true
	}
	if !keep && exit != nil && (r.c.Exit == nil || *r.c.Exit != *exit) {
		v := *exit
		r.c.Exit, changed = &v, true
	}
	if r.c.Hot != "" {
		r.c.Hot, r.c.HotAt, r.c.RSSKB, changed = "", 0, 0, true
	}
	if s.readLast(r, true) {
		changed = true
	}
	if changed {
		s.save(r, true)
	}
}

var (
	notifRe    = regexp.MustCompile(`(?s)<task-notification>(.*?)</task-notification>`)
	notifTag   = func(tag string) *regexp.Regexp { return regexp.MustCompile(`<` + tag + `>([^<]*)</` + tag + `>`) }
	notifUse   = notifTag("tool-use-id")
	notifTask  = notifTag("task-id")
	notifOut   = notifTag("output-file")
	notifStat  = notifTag("status")
	notifSumm  = notifTag("summary")
	notifExit  = regexp.MustCompile(`exit code (-?\d+)`)
	taskIDText = regexp.MustCompile(`running in background with ID: ([A-Za-z0-9_-]+)`)
)

type notification struct {
	toolUse, task, out, status string
	exit                       *int
}

// parseNotifications 从一段消息里取出 Claude Code 的结束通知（一条消息里可能有好几条）。
func parseNotifications(text string) []notification {
	if !strings.Contains(text, "<task-notification>") {
		return nil
	}
	var out []notification
	for _, m := range notifRe.FindAllStringSubmatch(text, -1) {
		body := m[1]
		get := func(re *regexp.Regexp) string {
			if x := re.FindStringSubmatch(body); x != nil {
				return strings.TrimSpace(x[1])
			}
			return ""
		}
		n := notification{toolUse: get(notifUse), task: get(notifTask), out: get(notifOut), status: get(notifStat)}
		if n.toolUse == "" && n.task == "" {
			continue
		}
		if x := notifExit.FindStringSubmatch(get(notifSumm)); x != nil {
			if v, err := strconv.Atoi(x[1]); err == nil {
				n.exit = &v
			}
		}
		out = append(out, n)
	}
	return out
}

func (s *shellTracker) notifyText(key, text string, at time.Time) {
	for _, n := range parseNotifications(text) {
		s.notify(key, n.toolUse, n.task, n.status, n.exit, at, n.out)
	}
}

// stopTasks：Stop hook 的 background_tasks 里已经结束的后台 shell（通知还没到时先定成败）。
func (s *shellTracker) stopTasks(key string, p map[string]any, at time.Time) {
	list, _ := p["background_tasks"].([]any)
	for _, it := range list {
		m, _ := it.(map[string]any)
		if m == nil || (str(m, "type") != "" && str(m, "type") != "shell") {
			continue
		}
		if st := str(m, "status"); st == "completed" || st == "failed" || st == "killed" {
			var exit *int
			if st == "completed" {
				z := 0
				exit = &z
			}
			s.notify(key, "", str(m, "id"), st, exit, at, "")
		}
	}
}

// taskIDOf 取 PostToolUse 里的后台任务编号（tool_response.backgroundTaskId，或结果文字里的 ID）。
func taskIDOf(resp any) string {
	switch v := resp.(type) {
	case map[string]any:
		if id := str(v, "backgroundTaskId"); id != "" {
			return id
		}
		return taskIDOf(v["stdout"])
	case string:
		if m := taskIDText.FindStringSubmatch(v); m != nil {
			return m[1]
		}
	}
	return ""
}

// onHook：hook 事件里和后台命令有关的部分（调用方持 d.mu，这里只碰 shellTracker 自己的锁）。
func (s *shellTracker) onHook(key string, p map[string]any, ev *protocol.Event) {
	if s == nil {
		return
	}
	now := s.now()
	switch ev.Name {
	case "PreToolUse":
		in, _ := p["tool_input"].(map[string]any)
		if bg, _ := in["run_in_background"].(bool); bg && isShellTool(ev.ToolName) && ev.ToolUseID != "" {
			s.begin(key, ev.AgentID, ev.ToolUseID, shellCommand(in), str(in, "description"), now)
		}
	case "PostToolUse":
		if isShellTool(ev.ToolName) && ev.ToolUseID != "" {
			s.launch(key, ev.ToolUseID, taskIDOf(p["tool_response"]), now)
		}
	case "PostToolUseFailure":
		if ev.ToolUseID != "" {
			s.drop(key, ev.ToolUseID)
		}
	case "UserPromptSubmit":
		s.notifyText(key, str(p, "prompt"), now)
	case "Stop":
		s.stopTasks(key, p, now)
	}
}

// ---------- 来自对话记录（R-206 增量读的同一份） ----------

var (
	markBgCall  = []byte(`"run_in_background":true`)
	markBgID    = []byte(`"backgroundTaskId"`)
	markBgNotif = []byte(`task-notification`)
)

type shellLine struct {
	Timestamp time.Time `json:"timestamp"`
	Message   *struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
	ToolUseResult json.RawMessage `json:"toolUseResult"`
}

type contentBlock struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Text      string          `json:"text"`
	Content   json.RawMessage `json:"content"`
}

// transcriptLine 读 Claude Code 对话记录的一行：run_in_background 的调用、它的结果（后台任务编号）、结束通知。
func (s *shellTracker) transcriptLine(key, agent string, line []byte, cut time.Time) {
	if s == nil || key == "" {
		return
	}
	call, id, notif := bytes.Contains(line, markBgCall), bytes.Contains(line, markBgID), bytes.Contains(line, markBgNotif)
	if !call && !id && !notif {
		return
	}
	var l shellLine
	if json.Unmarshal(line, &l) != nil || l.Message == nil || l.Timestamp.Before(cut) {
		return
	}
	var blocks []contentBlock
	var text string
	if json.Unmarshal(l.Message.Content, &blocks) != nil {
		_ = json.Unmarshal(l.Message.Content, &text)
	}
	for _, b := range blocks {
		switch b.Type {
		case "tool_use":
			if !call || !isShellTool(b.Name) {
				continue
			}
			var in map[string]any
			if json.Unmarshal(b.Input, &in) != nil {
				continue
			}
			if bg, _ := in["run_in_background"].(bool); bg {
				s.begin(key, agent, b.ID, shellCommand(in), str(in, "description"), l.Timestamp)
			}
		case "tool_result":
			if id {
				var r any
				_ = json.Unmarshal(l.ToolUseResult, &r)
				s.launch(key, b.ToolUseID, taskIDOf(r), l.Timestamp)
			}
		case "text":
			text += b.Text
		}
	}
	if notif {
		s.notifyText(key, text, l.Timestamp)
	}
}

// ---------- 来自进程表 ----------

// calls：这个会话里起来了、还没在进程表里对上的后台命令，拿去和进程对（daemon 重启后、从对话记录补上的都靠它）。
func (s *shellTracker) calls(key string) map[string]*shellCall {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]*shellCall{}
	for _, r := range s.recs {
		if r.c.Key == key && r.c.Status == protocol.ShellRunning && r.pid == 0 && !strings.HasPrefix(r.c.Ref, "sh-") {
			out[r.c.Ref] = &shellCall{cmd: r.c.Cmd, bg: true, at: time.UnixMilli(r.c.Start)}
		}
	}
	return out
}

// tick 是每轮扫进程表之后的一步：对上进程、发现结束、采样跑飞、读最后一行输出。
func (s *shellTracker) tick(now time.Time, ps []proc, seen map[string]shellSeen) {
	if s == nil {
		return
	}
	byPid, kids := procIndex(ps)
	s.mu.Lock()
	defer s.mu.Unlock()
	unclaimed := map[string]bool{} // 这一轮有认不出的后台 shell 的会话：它们的命令先不按“对不上进程”结束
	// Codex（以及认不出调用的）后台 shell：按“sh-进程号@启动时间”各记一条
	for key, sn := range seen {
		for ref, p := range sn.matched {
			r := s.recs[shellID(key, ref)]
			if r == nil || r.pid != 0 || r.c.Status != protocol.ShellRunning {
				continue
			}
			r.pid, r.pstart, r.launched, r.miss = p.pid, p.start, true, 0
			r.c.Task = "sh-" + strconv.Itoa(p.pid)
			if t := lstartTime(p.start); !t.IsZero() && t.UnixMilli() < r.c.Start+5000 {
				r.c.Start = t.UnixMilli()
			}
			s.save(r, true)
		}
		if !strings.HasPrefix(key, protocol.ToolCodex+":") {
			// Claude Code 的后台命令都有 hook / 对话记录：命令行对不上的（被改写过），按启动时间就近认给还没对上的那条；
			// 认不出的 shell 不另起一条（照旧列在 R-162 的后台任务里）
			for _, p := range sn.loose {
				if r := s.nearest(key, p); r != nil {
					r.pid, r.pstart, r.launched, r.miss = p.pid, p.start, true, 0
					r.c.Task = "sh-" + strconv.Itoa(p.pid)
					s.save(r, true)
				} else {
					unclaimed[key] = true
				}
			}
			continue
		}
		for _, p := range sn.loose {
			ref := "sh-" + procID(p.pid, p.start)
			if s.recs[shellID(key, ref)] != nil {
				continue
			}
			cmd := procLabel(p.cmd)
			if isShellWait(cmd) {
				continue
			}
			start := now.Add(-p.age)
			if t := lstartTime(p.start); !t.IsZero() {
				start = t
			}
			r := &shellRec{c: protocol.ShellCmd{Key: key, Ref: ref, Task: "sh-" + strconv.Itoa(p.pid), Kind: shellKind(cmd), Label: cmdSummary(cmd), Cmd: summary.Truncate(cmd, shellCmdMax),
				Start: start.UnixMilli(), Status: protocol.ShellRunning}, launched: true, pid: p.pid, pstart: p.start, pendAt: now}
			s.recs[shellID(key, ref)] = r
			s.save(r, true)
		}
	}
	for id, r := range s.recs {
		if r.c.Status != protocol.ShellRunning {
			if r.sent >= r.ver && r.c.End > 0 && now.Sub(time.UnixMilli(r.c.End)) > shellMemKeep {
				delete(s.recs, id)
			}
			continue
		}
		if !r.launched {
			if now.Sub(r.pendAt) > shellPendWait {
				delete(s.recs, id)
				_, _ = s.db.Exec(`DELETE FROM bg_cmds WHERE session_key = ? AND ref = ?`, r.c.Key, r.c.Ref)
			}
			continue
		}
		if r.pid > 0 {
			if p, ok := byPid[r.pid]; ok && p.start == r.pstart {
				s.sample(r, now, append([]proc{p}, descendants(p.pid, kids)...))
				if s.readLast(r, false) {
					s.save(r, true)
				}
				continue
			}
			// 进程不在了：结束时间就是现在（误差一轮扫描）；成败等通知
			s.end(r, now.UnixMilli(), protocol.ShellEnded)
			continue
		}
		// 起来了却一直对不上进程：太短（没赶上扫描）或命令行认不出。有输出文件就拿它最后写入的时间当结束时间
		// 输出文件末尾有结束记录：结束了，退出码也有了
		if s.readLast(r, false) || r.c.Status != protocol.ShellRunning {
			s.save(r, true)
			if r.c.Status != protocol.ShellRunning {
				continue
			}
		}
		_, scanned := seen[r.c.Key]
		if scanned && unclaimed[r.c.Key] {
			continue
		}
		var mt time.Time
		if st, err := os.Stat(r.outFile); r.outFile != "" && err == nil {
			mt = st.ModTime()
		}
		if !scanned {
			// 开它的 Agent 这一轮没扫到（已经退出、或本机还不认得它）：输出文件半小时没动才算结束，没有输出文件的老命令不知道结束时间
			switch {
			case !mt.IsZero() && now.Sub(mt) > 30*time.Minute:
				s.end(r, max(r.c.Start, mt.UnixMilli()), protocol.ShellEnded)
			case mt.IsZero() && now.Sub(time.UnixMilli(r.c.Start)) > 30*time.Minute:
				s.end(r, 0, protocol.ShellLost)
			}
			continue
		}
		if r.miss++; r.miss < shellMiss {
			continue
		}
		if !mt.IsZero() {
			s.end(r, min(now.UnixMilli(), max(r.c.Start, mt.UnixMilli())), protocol.ShellEnded)
		} else if now.Sub(time.UnixMilli(r.c.Start)) < 2*time.Minute {
			s.end(r, now.UnixMilli(), protocol.ShellEnded)
		} else {
			s.end(r, 0, protocol.ShellLost) // 很早以前开始、进程早已不在（本功能上线前的）：不知道什么时候结束
		}
	}
	// 刚结束、还没有退出码的：Claude Code 稍后才在输出文件末尾写结束记录，结束后两分钟内接着看
	for _, r := range s.recs {
		if r.c.Status == protocol.ShellEnded && r.c.Exit == nil && r.outFile != "" && now.Sub(time.UnixMilli(r.c.End)) < 2*time.Minute {
			if s.readLast(r, false) {
				s.save(r, true)
			}
		}
	}
}

// nearest：同一会话里还没对上进程、启动时间离这个 shell 最近（15 秒内）的后台命令。调用方持锁。
func (s *shellTracker) nearest(key string, p proc) *shellRec {
	t := lstartTime(p.start)
	if t.IsZero() {
		return nil
	}
	var best *shellRec
	var gap int64 = 15000
	for _, r := range s.recs {
		if r.c.Key != key || r.pid != 0 || r.c.Status != protocol.ShellRunning || !r.launched {
			continue
		}
		d := t.UnixMilli() - r.c.Start
		if d < 0 {
			d = -d
		}
		if d <= gap {
			best, gap = r, d
		}
	}
	return best
}

func (s *shellTracker) end(r *shellRec, at int64, status string) {
	if at > 0 {
		r.c.End = max(at, r.c.Start)
	}
	r.c.Status = status
	r.c.Hot, r.c.HotAt, r.c.RSSKB = "", 0, 0
	r.cpuSince, r.mem = time.Time{}, nil
	s.readLast(r, true) // 输出文件末尾有结束记录时，这里补上退出码与结果
	s.save(r, true)
}

// sample 采一次整棵进程树的 CPU 与内存，判断跑飞。
func (s *shellTracker) sample(r *shellRec, now time.Time, members []proc) {
	var cpu float64
	var rss int64
	for _, m := range members {
		cpu += m.cpu
		rss += m.rss
	}
	if cpu >= hotCPU {
		if r.cpuSince.IsZero() {
			r.cpuSince = now
		}
	} else {
		r.cpuSince = time.Time{}
	}
	minute := now.Unix() / 60
	if n := len(r.mem); n > 0 && r.mem[n-1].minute == minute {
		r.mem[n-1].kb = max(r.mem[n-1].kb, rss)
	} else {
		r.mem = append(r.mem, memSample{minute, rss})
		if len(r.mem) > 30 {
			r.mem = r.mem[len(r.mem)-30:]
		}
	}
	hot, at := hotState(r.cpuSince, r.mem, now)
	switch {
	case hot != r.c.Hot || (hot != "" && at != r.c.HotAt):
		r.c.Hot, r.c.HotAt, r.c.RSSKB = hot, at, 0
		if hot != "" {
			r.c.RSSKB = rss
		}
		s.save(r, true)
	case hot != "" && (rss > r.c.RSSKB*11/10 || rss < r.c.RSSKB*9/10):
		r.c.RSSKB = rss // 内存变化超过一成再更新，免得每轮都发
		s.save(r, true)
	}
}

// hotState：CPU 连续 hotCPUFor 不低于 hotCPU；或内存连续 hotMemFor 每分钟都不比上一分钟少（允许 2% 抖动），
// 涨到 hotMemGrow 倍以上且多了 hotMemMinKB 以上。返回跑飞的种类与开始的时刻（Unix 毫秒）。
func hotState(cpuSince time.Time, mem []memSample, now time.Time) (string, int64) {
	if !cpuSince.IsZero() && now.Sub(cpuSince) >= hotCPUFor {
		return "cpu", cpuSince.UnixMilli()
	}
	n := len(mem)
	if n < 2 {
		return "", 0
	}
	// 从最新一分钟往前找连续不跌的一段（每分钟都要有样本）
	i := n - 1
	for i > 0 && mem[i-1].minute == mem[i].minute-1 && mem[i].kb*100 >= mem[i-1].kb*98 {
		i--
	}
	first, last := mem[i], mem[n-1]
	if time.Duration(last.minute-first.minute)*time.Minute >= hotMemFor && first.kb > 0 &&
		float64(last.kb) >= float64(first.kb)*hotMemGrow && last.kb-first.kb >= hotMemMinKB {
		return "mem", first.minute * 60000
	}
	return "", 0
}

// markStopped：纸鸢停掉了这个进程（R-209 的停止），记为被停止。
func (s *shellTracker) markStopped(pid int, start string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.recs {
		if r.pid == pid && r.pstart == start {
			if r.c.End == 0 {
				r.c.End = max(s.now().UnixMilli(), r.c.Start)
			}
			r.c.Status = protocol.ShellStopped
			r.c.Hot, r.c.HotAt, r.c.RSSKB = "", 0, 0
			s.save(r, true)
		}
	}
}

// ---------- 输出文件 ----------

// resolveOut 找 Claude Code 的输出文件：<临时目录>/claude-<uid>/<项目>/<会话>/tasks/<任务编号>.output。
func (s *shellTracker) resolveOut(r *shellRec) {
	if r.outFile != "" || r.taskID == "" {
		return
	}
	for _, dir := range s.tmp {
		if m, _ := filepath.Glob(filepath.Join(dir, "*", "*", "tasks", r.taskID+".output")); len(m) > 0 {
			r.outFile = m[0]
			return
		}
	}
}

// readLast 读输出文件末尾的最后一行（force：结束时一定读一次；平时 shellReadEvery 一次、大小变了才读）。变了返回真。调用方持锁。
func (s *shellTracker) readLast(r *shellRec, force bool) bool {
	s.resolveOut(r)
	if r.outFile == "" {
		return false
	}
	now := s.now()
	if !force && now.Sub(r.readAt) < shellReadEvery {
		return false
	}
	r.readAt = now
	st, err := os.Stat(r.outFile)
	if err != nil || (st.Size() == r.outSize && !force) {
		return false
	}
	r.outSize = st.Size()
	last, mark := tailLine(r.outFile, st.Size())
	changed := false
	if last != "" && last != r.c.Last {
		r.c.Last, changed = last, true
	}
	if mark.ok && r.c.Exit == nil && r.c.Status != protocol.ShellStopped {
		// 输出文件末尾的结束记录：退出码在这里（通知没来、Claude Code 已经退出时也有）
		if r.c.End == 0 {
			r.c.End = min(now.UnixMilli(), max(r.c.Start, st.ModTime().UnixMilli()))
		}
		if mark.killed {
			r.c.Status = protocol.ShellStopped
		} else {
			code := mark.code
			r.c.Exit = &code
			st := "completed"
			if code != 0 {
				st = "failed"
			}
			r.c.Status = shellResult(st, &code)
		}
		r.c.Hot, r.c.HotAt, r.c.RSSKB = "", 0, 0
		changed = true
	}
	return changed
}

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)|\x1b[@-Z\\-_]`)

var exitMarkRe = regexp.MustCompile(`^\[(exited with code (-?\d+)|killed)\]$`)

// outEnd 是 Claude Code 在输出文件末尾写的结束记录：“[exited with code N]” 或 “[killed]”。
type outEnd struct {
	ok     bool
	killed bool
	code   int
}

// tailLine 取文件最后一个非空行（去掉颜色控制符；进度条用 \r 覆盖的取最后一段），截到 shellLastMax 字。
// 末尾是 Claude Code 的结束记录时，另外返回它，最后一行取它前面那一行。
func tailLine(path string, size int64) (string, outEnd) {
	f, err := os.Open(path)
	if err != nil {
		return "", outEnd{}
	}
	defer f.Close()
	const n = 8 << 10
	off := max(0, size-n)
	buf := make([]byte, size-off)
	if _, err := f.ReadAt(buf, off); err != nil && err != io.EOF {
		return "", outEnd{}
	}
	var mark outEnd
	seen := false
	lines := strings.Split(string(buf), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if i == 0 && off > 0 {
			break // 第一段可能是半行
		}
		l := lines[i]
		if j := strings.LastIndex(strings.TrimRight(l, "\r"), "\r"); j >= 0 {
			l = l[j+1:]
		}
		l = norm(ansiRe.ReplaceAllString(l, ""))
		l = strings.Map(func(r rune) rune {
			if r < 0x20 || r == utf8.RuneError {
				return -1
			}
			return r
		}, l)
		if l == "" {
			continue
		}
		if !seen {
			seen = true
			if m := exitMarkRe.FindStringSubmatch(l); m != nil {
				mark.ok = true
				if m[1] == "killed" {
					mark.killed = true
				} else {
					mark.code, _ = strconv.Atoi(m[2])
				}
				continue
			}
		}
		return summary.Truncate(l, shellLastMax), mark
	}
	return "", mark
}

// ---------- 发给 Hub ----------

// pending 取一批有变化、还没发（或发出后又变了）的后台命令。上一批 shellAckWait 内没回执就先不取。
func (s *shellTracker) pending() (protocol.Envelope, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if len(s.batches) > 0 && now.Sub(s.sentAt) < shellAckWait {
		return protocol.Envelope{}, false
	}
	s.batches = map[string][]shellSent{}
	env := protocol.Envelope{Type: protocol.MsgShells}
	var sent []shellSent
	for id, r := range s.recs {
		if r.launched && r.sent < r.ver {
			env.Shells = append(env.Shells, r.c)
			sent = append(sent, shellSent{id, r.ver})
			if len(sent) >= shellBatch {
				break
			}
		}
	}
	if len(sent) == 0 {
		return env, false
	}
	s.seq++
	env.Batch = fmt.Sprintf("s%d-%d", now.UnixNano(), s.seq)
	s.batches[env.Batch] = sent
	s.sentAt = now
	return env, true
}

func (s *shellTracker) acked(ids []string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, b := range ids {
		for _, x := range s.batches[b] {
			if r := s.recs[x.id]; r != nil && r.sent < x.ver {
				r.sent = x.ver
				_, _ = s.db.Exec(`UPDATE bg_cmds SET sent = ? WHERE session_key = ? AND ref = ? AND sent < ?`, x.ver, r.c.Key, r.c.Ref, x.ver)
			}
		}
		delete(s.batches, b)
	}
}

func (s *shellTracker) reset() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.batches = map[string][]shellSent{}
	s.mu.Unlock()
}

func (d *Daemon) sendShells() {
	if d.shells == nil {
		return
	}
	d.mu.Lock()
	connected := d.connected
	d.mu.Unlock()
	if !connected {
		return
	}
	if env, ok := d.shells.pending(); ok {
		d.sendDirect(env)
	}
}
