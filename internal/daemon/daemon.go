package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
}

// sessionInfo 缓存每个会话不常变化的信息。
type sessionInfo struct {
	client     string
	model      string
	lastPrompt string
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
	d := &Daemon{cfg: cfg, log: log, q: q, git: newGitCache(), kick: make(chan struct{}, 1), sessions: map[string]*sessionInfo{}}
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
	if ev.Name == "Stop" {
		d.maybeSummarize(ev, req.Payload)
	}
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
	}
	ev.TurnID = str(p, "turn_id")
	if ev.TurnID == "" {
		ev.TurnID = str(p, "prompt_id")
	}
	if name == "UserPromptSubmit" {
		ev.Prompt = summary.Truncate(strings.TrimSpace(str(p, "prompt")), 200)
	}
	if name == "PermissionRequest" {
		if in, ok := p["tool_input"].(map[string]any); ok {
			brief := str(in, "description")
			if brief == "" {
				brief = str(in, "command")
			}
			ev.ToolBrief = summary.Truncate(strings.ReplaceAll(brief, "\n", " "), 80)
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
	if ev.Prompt != "" {
		info.lastPrompt = ev.Prompt
	}
	d.mu.Unlock()
	if name == "Stop" && cfg.SendLastReply {
		ev.LastReply = summary.Truncate(str(p, "last_assistant_message"), 1500)
	}
	return ev, true
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
	if len(fresh) > 0 {
		b.WriteString("[Kitestring] 用户已对你之前提出的决策作出答复，请据此继续推进：\n")
		for _, dec := range fresh {
			fmt.Fprintf(&b, "- %s 问题：%s\n  用户答复：%s\n", dec.Code, oneLine(dec.Question), dec.Answer)
		}
	}
	if len(again) > 0 {
		b.WriteString("[Kitestring] 提醒：以下是此前已收到的用户决策（会话恢复或上下文压缩后补充），继续遵循：\n")
		for _, dec := range again {
			fmt.Fprintf(&b, "- %s 问题：%s\n  用户答复：%s\n", dec.Code, oneLine(dec.Question), dec.Answer)
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

func oneLine(s string) string { return summary.Truncate(strings.Join(strings.Fields(s), " "), 300) }

// maybeSummarize 在 Hub 指定设备侧摘要时，用本机配置的模型生成摘要并补发合成事件。
func (d *Daemon) maybeSummarize(ev protocol.Event, p map[string]any) {
	d.mu.Lock()
	onDevice := d.devCfg.SummaryOnDevice
	prompt := ""
	if info := d.sessions[protocol.SessionKey(ev.Tool, ev.SessionID)]; info != nil {
		prompt = info.lastPrompt
	}
	d.mu.Unlock()
	if !onDevice || !d.cfg.Summary.Enabled() {
		return
	}
	reply := summary.Truncate(str(p, "last_assistant_message"), 1500)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
		defer cancel()
		text, err := summary.Generate(ctx, d.cfg.Summary, prompt, reply)
		if err != nil {
			d.log.Warn("设备侧摘要失败", "err", err)
			return
		}
		sev := protocol.Event{ID: protocol.NewID(), Time: time.Now().UTC(), Tool: ev.Tool, Name: protocol.EventSummary, SessionID: ev.SessionID, Summary: text}
		if err := d.q.pushEvent(sev); err == nil {
			d.wake()
		}
	}()
}
