package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/NekroAI/kitestring/internal/protocol"
	"github.com/NekroAI/kitestring/internal/summary"
	"github.com/NekroAI/kitestring/internal/version"
)

// 注入文本上限（字符）。Codex additionalContextLimit 缺省约 2,500 token，留足余量。
const maxInjectChars = 4000

// Daemon 是设备常驻进程。
type Daemon struct {
	cfg   Config
	log   *slog.Logger
	q     *queue
	git   *gitCache
	kick  chan struct{}
	proxy *httputil.ReverseProxy

	mu        sync.Mutex
	connected bool
	devCfg    protocol.DeviceConfig
	decisions []protocol.Decision
	sessions  map[string]*sessionInfo
	toolUses  map[string]toolUse // tool_use_id → 发起调用的会话（用于 MCP 调用识别会话）
	toolCond  *sync.Cond
}

type toolUse struct {
	session string
	at      time.Time
}

// sessionInfo 缓存每个会话不常变化的信息。
type sessionInfo struct {
	client string
	model  string
}

// New 创建 daemon。
func New(cfg Config, log *slog.Logger) (*Daemon, error) {
	if err := os.MkdirAll(Home(), 0o700); err != nil {
		return nil, err
	}
	q, err := openQueue(filepath.Join(Home(), "daemon.db"))
	if err != nil {
		return nil, err
	}
	hub, err := url.Parse(cfg.HubURL)
	if err != nil {
		return nil, fmt.Errorf("hub_url 无效：%w", err)
	}
	d := &Daemon{cfg: cfg, log: log, q: q, git: newGitCache(), kick: make(chan struct{}, 1), sessions: map[string]*sessionInfo{}, toolUses: map[string]toolUse{}}
	d.toolCond = sync.NewCond(&d.mu)
	d.proxy = &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(hub)
			r.Out.URL.Path = "/api" + r.In.URL.Path // /agent/... → /api/agent/...
			r.Out.Header.Set("Authorization", "Bearer "+cfg.Token)
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			http.Error(w, "无法连接 Kitestring Hub："+err.Error(), http.StatusBadGateway)
		},
	}
	return d, nil
}

// Run 启动 daemon，阻塞直到 ctx 结束。
func (d *Daemon) Run(ctx context.Context) error {
	defer d.q.close()
	sock := SocketPath()
	if c, err := net.DialTimeout("unix", sock, 300*time.Millisecond); err == nil {
		c.Close()
		return errors.New("已有 daemon 在运行：" + sock)
	}
	_ = os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return err
	}
	_ = os.Chmod(sock, 0o600)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /hook", d.handleHook)
	mux.HandleFunc("GET /status", d.handleStatus)
	mux.HandleFunc("GET /session", d.handleSessionLookup)
	mux.Handle("/agent/", d.proxy)
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()
	d.log.Info("daemon 已启动", "socket", sock, "hub", d.cfg.HubURL)

	backoff := time.Second
	for ctx.Err() == nil {
		start := time.Now()
		err := d.session(ctx)
		d.setConnected(false)
		if ctx.Err() != nil {
			break
		}
		if time.Since(start) > time.Minute {
			backoff = time.Second
		}
		d.log.Warn("与 Hub 的连接中断，稍后重连", "err", err, "retry_in", backoff)
		select {
		case <-ctx.Done():
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 30*time.Second)
	}
	return nil
}

func (d *Daemon) setConnected(v bool) {
	d.mu.Lock()
	d.connected = v
	d.mu.Unlock()
}

// session 维持一次 WebSocket 连接，直到出错。
func (d *Daemon) session(ctx context.Context) error {
	wsURL := strings.Replace(strings.TrimSuffix(d.cfg.HubURL, "/"), "http", "ws", 1) + "/api/device/ws"
	dctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	ws, resp, err := websocket.Dial(dctx, wsURL, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + d.cfg.Token}}})
	cancel()
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusUnauthorized {
			return errors.New("设备 token 无效或已吊销")
		}
		return err
	}
	ws.SetReadLimit(4 << 20)
	defer ws.CloseNow()
	cctx, ccancel := context.WithCancel(ctx)
	defer ccancel()

	host, _ := os.Hostname()
	if err := wsjson.Write(cctx, ws, protocol.Envelope{Type: protocol.MsgHello, Hello: &protocol.Hello{Version: version.Version, Hostname: host, Platform: runtime.GOOS + "/" + runtime.GOARCH}}); err != nil {
		return err
	}
	d.setConnected(true)
	d.log.Info("已连接 Hub")

	readErr := make(chan error, 1)
	acked := make(chan struct{}, 1)
	go func() {
		for {
			var e protocol.Envelope
			if err := wsjson.Read(cctx, ws, &e); err != nil {
				readErr <- err
				return
			}
			d.handleHubMsg(e)
			if e.Type == protocol.MsgAck {
				select {
				case acked <- struct{}{}:
				default:
				}
			}
		}
	}()

	inflight := false
	flush := time.NewTicker(5 * time.Second)
	defer flush.Stop()
	for {
		if !inflight {
			events, deliveries, err := d.q.pending(200)
			if err != nil {
				return err
			}
			if len(events) > 0 || len(deliveries) > 0 {
				env := protocol.Envelope{Type: protocol.MsgEvents, Events: events}
				if len(events) == 0 {
					env = protocol.Envelope{Type: protocol.MsgDelivered, Delivered: deliveries}
				} else if len(deliveries) > 0 {
					// 事件与回执分两条发送，回执紧随其后
					if err := wsjson.Write(cctx, ws, env); err != nil {
						return err
					}
					env = protocol.Envelope{Type: protocol.MsgDelivered, Delivered: deliveries}
				}
				if err := wsjson.Write(cctx, ws, env); err != nil {
					return err
				}
				inflight = true
			}
		}
		select {
		case err := <-readErr:
			return err
		case <-acked:
			inflight = false
		case <-d.kick:
		case <-flush.C:
			inflight = false // 超时未收到回执则重发（Hub 幂等）
		}
	}
}

func (d *Daemon) handleHubMsg(e protocol.Envelope) {
	switch e.Type {
	case protocol.MsgAck:
		if err := d.q.ack(e.Ack); err != nil {
			d.log.Warn("清理已确认消息失败", "err", err)
		}
	case protocol.MsgDecisions:
		for _, dec := range e.Decisions {
			for _, f := range dec.Files {
				go d.fetchAttachment(f)
			}
			if !dec.Delivered {
				// Hub 认为待接收：若本机已有注入记录但 Hub 未收到回执，保留记录；
				// 若 Hub 已收到过回执又重新置为待接收（改答 / 重发），则清除记录以便再次注入。
				if d.q.wasDelivered(dec.ID, dec.SessionKey) && !d.deliveryPending(dec.ID) {
					d.q.forgetDelivered(dec.ID)
				}
			}
		}
		d.mu.Lock()
		d.decisions = e.Decisions
		d.mu.Unlock()
	case protocol.MsgConfig:
		if e.Config != nil {
			d.mu.Lock()
			d.devCfg = *e.Config
			d.mu.Unlock()
		}
	}
}

func (d *Daemon) deliveryPending(decisionID string) bool {
	var n int
	_ = d.q.db.QueryRow(`SELECT COUNT(*) FROM outbox WHERE id = ?`, "delivered:"+decisionID).Scan(&n)
	return n > 0
}

func (d *Daemon) wake() {
	select {
	case d.kick <- struct{}{}:
	default:
	}
}

func (d *Daemon) handleStatus(w http.ResponseWriter, _ *http.Request) {
	d.mu.Lock()
	st := map[string]any{"connected": d.connected, "hub": d.cfg.HubURL, "queue": d.q.size(), "decisions": len(d.decisions), "version": version.Version}
	d.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(st)
}

// handleHook 处理一次 hook 调用：构造事件入队，并返回需要注入的上下文。必须快速返回。
func (d *Daemon) handleHook(w http.ResponseWriter, r *http.Request) {
	var req protocol.HookRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	ev, ok := d.buildEvent(req)
	if !ok {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err := d.q.pushEvent(ev); err != nil {
		d.log.Warn("事件入队失败", "err", err)
	}
	resp := protocol.HookResponse{Context: d.injection(ev)}
	d.wake()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func str(p map[string]any, k string) string {
	if v, ok := p[k].(string); ok {
		return v
	}
	return ""
}

// buildEvent 把 hook 原始输入裁剪为上报事件（D3：不含对话与代码正文）。
func (d *Daemon) buildEvent(req protocol.HookRequest) (protocol.Event, bool) {
	p := req.Payload
	sid := str(p, "session_id")
	if sid == "" || (req.Tool != protocol.ToolClaude && req.Tool != protocol.ToolCodex) {
		return protocol.Event{}, false
	}
	name := str(p, "hook_event_name")
	if name == "" {
		name = req.Event
	}
	key := protocol.SessionKey(req.Tool, sid)
	ev := protocol.Event{
		ID: protocol.NewID(), Time: time.Now().UTC(), Tool: req.Tool, Name: name, SessionID: sid,
		Cwd: str(p, "cwd"), Model: str(p, "model"), Source: str(p, "source"), Reason: str(p, "reason"),
		ToolName: str(p, "tool_name"), ToolUseID: str(p, "tool_use_id"), NotifType: str(p, "notification_type"),
		PermMode: str(p, "permission_mode"), AgentType: str(p, "agent_type"),
	}
	ev.TurnID = str(p, "turn_id")
	if ev.TurnID == "" {
		ev.TurnID = str(p, "prompt_id")
	}
	ev.Instance = d.agentInstance(req.PPID)
	if name == "UserPromptSubmit" {
		text, auto := protocol.CleanPrompt(str(p, "prompt"))
		ev.Prompt = summary.Truncate(text, 200)
		if auto {
			ev.Source = protocol.SourceAuto
		}
	}
	if name == "PreToolUse" && ev.ToolUseID != "" && strings.HasPrefix(ev.ToolName, "mcp__") {
		d.rememberToolUse(ev.ToolUseID, key)
	}
	if name == "PreToolUse" || name == "PermissionRequest" {
		if in, ok := p["tool_input"].(map[string]any); ok {
			ev.ToolBrief = toolBrief(ev.ToolName, in)
		}
	}
	ev.Git = d.git.get(ev.Cwd)

	d.mu.Lock()
	info := d.sessions[key]
	if info == nil {
		info = &sessionInfo{}
		d.sessions[key] = info
	}
	cfg := d.devCfg
	needClient := info.client == ""
	needModel := ev.Model == "" && req.Tool == protocol.ToolClaude && (info.model == "" || name == "Stop")
	d.mu.Unlock()

	// 以下两项需要读进程或文件，放在锁外。
	client, model := "", ""
	if needClient {
		client = detectClient(req)
	}
	if needModel {
		model = claudeModel(str(p, "transcript_path"))
	}

	d.mu.Lock()
	if client != "" {
		info.client = client
	}
	if model != "" {
		info.model = model
	}
	ev.Client = info.client
	if ev.Model == "" {
		ev.Model = info.model
	}
	d.mu.Unlock()
	if name == "Stop" {
		ev.BgTasks, ev.BgBrief = backgroundTasks(p)
	}
	if name == "Stop" && cfg.SendLastReply {
		ev.LastReply = summary.Truncate(str(p, "last_assistant_message"), 1500)
	}
	return ev, true
}

// agentInstance 沿父进程向上找到运行 hook 的 Claude Code / Codex 进程，返回“进程号@启动时间”，
// 用来区分同一对话在多个终端打开（例如公司 SSH 里的旧进程与在家恢复的新进程）。找不到时返回空串。
func (d *Daemon) agentInstance(ppid int) string {
	pid := ppid
	for i := 0; i < 5 && pid > 1; i++ {
		cmd := exec.Command("ps", "-o", "ppid=,lstart=,command=", "-p", strconv.Itoa(pid))
		cmd.Env = append(os.Environ(), "LC_ALL=C") // 固定 lstart 格式（中文环境下段数不同）
		out, err := cmd.Output()
		if err != nil {
			return ""
		}
		f := strings.Fields(string(out))
		if len(f) < 7 { // ppid + lstart（5 段）+ 命令
			return ""
		}
		if isAgentCommand(f[6:]) {
			return strconv.Itoa(pid) + "@" + strings.Join(f[1:6], " ")
		}
		pid, _ = strconv.Atoi(f[0])
	}
	return ""
}

// isAgentCommand 判断命令行是否是 Claude Code / Codex 本体（原生二进制或 node 启动的脚本）。
func isAgentCommand(args []string) bool {
	for i, a := range args {
		if i > 1 {
			break
		}
		switch filepath.Base(a) {
		case "claude", "codex":
			return true
		}
		if strings.Contains(a, "claude-code") || strings.Contains(a, "@openai/codex") {
			return true
		}
	}
	return false
}

// detectClient 推断客户端：Claude 看入口环境变量，Codex 看父进程是否为 app-server。
func detectClient(req protocol.HookRequest) string {
	if req.Tool == protocol.ToolClaude {
		c := req.Env["CLAUDE_CODE_ENTRYPOINT"]
		if t := req.Env["TERM_PROGRAM"]; t != "" && (c == "" || c == "cli") {
			if c == "" {
				c = "cli"
			}
			return c + "/" + t
		}
		return c
	}
	if req.PPID > 1 {
		out, err := exec.Command("ps", "-o", "command=", "-p", strconv.Itoa(req.PPID)).Output()
		if err == nil {
			cmd := string(out)
			switch {
			case strings.Contains(cmd, "app-server"):
				if strings.Contains(cmd, "ChatGPT.app") || strings.Contains(cmd, "CodexCLI.app") {
					return "desktop"
				}
				return "app-server"
			case strings.Contains(cmd, " exec"):
				return "exec"
			}
		}
	}
	return "cli"
}

var modelRe = regexp.MustCompile(`"model"\s*:\s*"([^"]+)"`)

// claudeModel 从 Claude transcript 尾部读取最近使用的模型名（hook 输入不含 model，见 hook-matrix.md）。
func claudeModel(path string) string {
	if path == "" {
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return ""
	}
	const tail = 256 << 10
	off := max(st.Size()-tail, 0)
	buf := make([]byte, st.Size()-off)
	if _, err := f.ReadAt(buf, off); err != nil && len(buf) == 0 {
		return ""
	}
	m := modelRe.FindAllSubmatch(buf, -1)
	for i := len(m) - 1; i >= 0; i-- {
		if s := string(m[i][1]); s != "" && s != "<synthetic>" {
			return s
		}
	}
	return ""
}

// injection 按 D15 计算本次 hook 需要注入的决策文本，并记录送达。
func (d *Daemon) injection(ev protocol.Event) string {
	redeliver := ev.Name == "SessionStart" && (ev.Source == "resume" || ev.Source == "compact")
	if ev.Name != "UserPromptSubmit" && !redeliver {
		return ""
	}
	key := protocol.SessionKey(ev.Tool, ev.SessionID)
	d.mu.Lock()
	all := append([]protocol.Decision(nil), d.decisions...)
	d.mu.Unlock()

	var fresh, again []protocol.Decision
	for _, dec := range all {
		if dec.SessionKey != key {
			continue
		}
		delivered := dec.Delivered || d.q.wasDelivered(dec.ID, key)
		switch {
		case !delivered:
			fresh = append(fresh, dec)
		case redeliver:
			again = append(again, dec)
		}
	}
	if len(fresh) == 0 && len(again) == 0 {
		return ""
	}
	var b strings.Builder
	var answers, feedback []protocol.Decision
	for _, dec := range fresh {
		if dec.Kind == "feedback" {
			feedback = append(feedback, dec)
		} else {
			answers = append(answers, dec)
		}
	}
	if len(answers) > 0 {
		b.WriteString("[Kitestring] 用户已对你之前提出的决策作出答复，请据此继续推进：\n")
		for _, dec := range answers {
			d.writeItem(&b, dec)
		}
	}
	if len(feedback) > 0 {
		b.WriteString("[Kitestring] 用户验收后退回了以下需求，请按修改意见继续处理，完成后重新提交验收：\n")
		for _, dec := range feedback {
			d.writeItem(&b, dec)
		}
	}
	if len(again) > 0 {
		b.WriteString("[Kitestring] 提醒：以下是此前已收到的用户答复与修改意见（会话恢复或上下文压缩后补充），继续遵循：\n")
		for _, dec := range again {
			d.writeItem(&b, dec)
		}
	}
	text := summary.Truncate(b.String(), maxInjectChars)
	now := time.Now().UTC()
	for _, dec := range fresh {
		if err := d.q.pushDelivery(protocol.Delivery{DecisionID: dec.ID, SessionKey: key, TurnID: ev.TurnID, Time: now}); err != nil {
			d.log.Warn("记录决策送达失败", "err", err)
		}
	}
	return text
}

// writeItem 写一条答复或修改意见；附件给出本地路径，agent 可直接读取。
func (d *Daemon) writeItem(b *strings.Builder, dec protocol.Decision) {
	if dec.Kind == "feedback" {
		fmt.Fprintf(b, "- %s「%s」修改意见：%s\n", dec.ReqCode, oneLine(dec.ReqTitle), dec.Answer)
	} else {
		fmt.Fprintf(b, "- %s 问题：%s\n  用户答复：%s\n", dec.Code, oneLine(dec.Question), dec.Answer)
	}
	for _, f := range dec.Files {
		label := "附图"
		if strings.HasPrefix(f.Mime, "video/") {
			label = "录屏"
		}
		p := d.attachmentPath(f)
		if _, err := os.Stat(p); err != nil && label == "附图" {
			d.fetchAttachment(f) // 送达前仍未下载完成：同步再试一次（录屏太大，不在 hook 里等）
		}
		if _, err := os.Stat(p); err == nil {
			fmt.Fprintf(b, "  %s：%s\n", label, p)
		} else {
			fmt.Fprintf(b, "  %s（未能下载，可在 Kitestring 网页查看）：%s\n", label, f.Name)
		}
	}
}

var mimeExt = map[string]string{"image/png": ".png", "image/jpeg": ".jpg", "image/gif": ".gif", "image/webp": ".webp", "video/mp4": ".mp4", "video/webm": ".webm"}

func (d *Daemon) attachmentPath(f protocol.File) string {
	return filepath.Join(Home(), "attachments", f.ID+mimeExt[f.Mime])
}

// fetchAttachment 把附件（图片或录屏）下载到本机（已存在则跳过）。
func (d *Daemon) fetchAttachment(f protocol.File) {
	p := d.attachmentPath(f)
	if _, err := os.Stat(p); err == nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(d.cfg.HubURL, "/")+"/api/agent/files/"+url.PathEscape(f.ID), nil)
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+d.cfg.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		d.log.Warn("下载附图失败", "file", f.ID, "err", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		d.log.Warn("下载附图失败", "file", f.ID, "status", resp.StatusCode)
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), "dl-*")
	if err != nil {
		return
	}
	_, err = io.Copy(tmp, io.LimitReader(resp.Body, 52<<20))
	tmp.Close()
	if err != nil {
		os.Remove(tmp.Name())
		return
	}
	_ = os.Rename(tmp.Name(), p)
}

func oneLine(s string) string { return summary.Truncate(strings.Join(strings.Fields(s), " "), 300) }

// backgroundTasks 统计 Stop 时仍在运行的后台任务（Claude Code 的 background_tasks），返回数量与简述。
func backgroundTasks(p map[string]any) (int, string) {
	list, _ := p["background_tasks"].([]any)
	n, brief := 0, ""
	for _, it := range list {
		m, _ := it.(map[string]any)
		if m == nil {
			continue
		}
		if st := str(m, "status"); st != "" && st != "running" && st != "pending" {
			continue
		}
		n++
		if brief == "" {
			brief = str(m, "description")
			if brief == "" {
				brief = str(m, "command")
			}
		}
	}
	return n, summary.Truncate(strings.Join(strings.Fields(brief), " "), 80)
}

// rememberToolUse 记录 MCP 工具调用来自哪个会话（PreToolUse 先于 MCP 请求到达）。
func (d *Daemon) rememberToolUse(id, session string) {
	d.mu.Lock()
	now := time.Now()
	for k, v := range d.toolUses {
		if now.Sub(v.at) > 10*time.Minute {
			delete(d.toolUses, k)
		}
	}
	d.toolUses[id] = toolUse{session: session, at: now}
	d.mu.Unlock()
	d.toolCond.Broadcast()
}

// lookupToolUse 等待至多 wait，返回 tool_use_id 对应的会话。
func (d *Daemon) lookupToolUse(id string, wait time.Duration) string {
	deadline := time.Now().Add(wait)
	stop := time.AfterFunc(wait, d.toolCond.Broadcast)
	defer stop.Stop()
	d.mu.Lock()
	defer d.mu.Unlock()
	for {
		if v, ok := d.toolUses[id]; ok {
			return v.session
		}
		if !time.Now().Before(deadline) {
			return ""
		}
		d.toolCond.Wait()
	}
}

// handleSessionLookup：ks mcp 用本次调用的 tool_use_id 查询会话（Claude Code 恢复会话后，MCP 进程环境里的会话 ID 会过期）。
func (d *Daemon) handleSessionLookup(w http.ResponseWriter, r *http.Request) {
	key := d.lookupToolUse(r.URL.Query().Get("tool_use_id"), time.Second)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"session_key": key})
}

var patchFileRe = regexp.MustCompile(`(?m)^\*\*\* (?:Update|Add|Delete) File: (.+)$`)

// toolBrief 从工具输入中提取一句动作简述：命令截断、文件名、搜索词等，不含文件内容（D3）。
func toolBrief(tool string, in map[string]any) string {
	clean := func(s string, n int) string { return summary.Truncate(strings.Join(strings.Fields(s), " "), n) }
	base := func(p string) string {
		if p == "" {
			return ""
		}
		return filepath.Base(p)
	}
	switch tool {
	case "Bash", "shell", "exec_command", "local_shell":
		if c := str(in, "command"); c != "" {
			return clean(c, 80)
		}
		if arr, ok := in["command"].([]any); ok {
			parts := make([]string, 0, len(arr))
			for _, a := range arr {
				if s, ok := a.(string); ok {
					parts = append(parts, s)
				}
			}
			return clean(strings.Join(parts, " "), 80)
		}
	case "Edit", "Write", "MultiEdit", "Read", "NotebookEdit":
		if f := str(in, "file_path"); f != "" {
			return base(f)
		}
		return base(str(in, "notebook_path"))
	case "apply_patch":
		patch := str(in, "input")
		if patch == "" {
			patch = str(in, "patch")
		}
		files := patchFileRe.FindAllStringSubmatch(patch, -1)
		switch len(files) {
		case 0:
		case 1:
			return base(strings.TrimSpace(files[0][1]))
		default:
			return fmt.Sprintf("%s 等 %d 个文件", base(strings.TrimSpace(files[0][1])), len(files))
		}
	case "Grep", "Glob":
		return clean(str(in, "pattern"), 60)
	case "WebFetch":
		if u, err := url.Parse(str(in, "url")); err == nil {
			return u.Host
		}
	case "WebSearch":
		return clean(str(in, "query"), 60)
	case "AskUserQuestion":
		if qs, ok := in["questions"].([]any); ok && len(qs) > 0 {
			if q, ok := qs[0].(map[string]any); ok {
				return clean(str(q, "question"), 80)
			}
		}
		return clean(str(in, "question"), 80)
	case "ExitPlanMode":
		return ""
	case "Task", "Agent":
		return clean(str(in, "description"), 60)
	}
	if d := str(in, "description"); d != "" {
		return clean(d, 60)
	}
	return ""
}
