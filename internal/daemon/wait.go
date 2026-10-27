package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/NekroAI/kitestring/internal/protocol"
)

// 挂起等待（D-10）：Agent 做完一段后在后台运行 ks wait。Hub 推来针对该会话的新答复、修改意见或留言时，
// 这里返回与 hook 注入相同的文字并结束，Agent 随即被唤醒继续。只因你的人工输入唤醒，不做定时续跑；
// 超过最长等待时间安静结束。同一会话只保留最新的一个等待。

const (
	DefaultWait = time.Hour
	MaxWait     = 12 * time.Hour
	// CodexWait：Codex 在前台运行 ks wait（它没有后台任务），整段等待都占着这一轮；
	// 默认、也最多等 30 分钟，超时后收尾结束本轮（R-74）。
	CodexWait = 30 * time.Minute
)

// dshWaitText：DSH 的待命由 Host 插件负责，模型自己调 ks wait 时用它说明情况并立即返回。
const dshWaitText = "[Kitestring] 这个 DSH 会话由纸鸢插件自动待命：结束这一轮即可，插件会在空闲时接住后续消息，不需要运行 ks wait。"

// WaitResult 是 /wait 的返回：reason 为 delivered、timeout、replaced、canceled。
type WaitResult struct {
	Reason  string `json:"reason"`
	Text    string `json:"text,omitempty"`
	Session string `json:"session_key,omitempty"`
	Timeout string `json:"timeout,omitempty"` // 实际用的最长等待时间
	Resident bool  `json:"resident,omitempty"` // 会话是 PM / 开发，要求常驻在线：超时后接着等（R-83）
	Error   string `json:"error,omitempty"`
}

func (d *Daemon) handleWait(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	w.Header().Set("Content-Type", "application/json")
	key := q.Get("session_key")
	inst := ""
	if key == "" {
		ppid, _ := strconv.Atoi(q.Get("ppid"))
		if inst = d.agentInstance(ppid); inst != "" {
			d.mu.Lock()
			key = d.instances[inst]
			d.mu.Unlock()
		}
	}
	if key != "" && inst != "" && d.isStale(key, inst) {
		// 同一对话在更新的终端里继续了：这个较早终端不再领活（R-70）
		_ = json.NewEncoder(w).Encode(WaitResult{Reason: "stale", Session: key, Text: staleText})
		return
	}
	tool, sid, ok := strings.Cut(key, ":")
	if !ok || sid == "" {
		_ = json.NewEncoder(w).Encode(WaitResult{Error: "认不出当前会话：请在 Claude Code / Codex / DSH 的对话里运行，或用 --session tool:id 指定"})
		return
	}
	// DSH 由 Host 插件用 via=plugin 走同一个 /wait 接口待命；模型自己跑 ks wait 时直接返回，
	// 避免插件已经待命的情况下再多挂一层前台等待（那种等待会占着回合不放）。
	if tool == protocol.ToolDSH && q.Get("via") != "plugin" {
		_ = json.NewEncoder(w).Encode(WaitResult{Reason: "disabled", Session: key, Text: dshWaitText})
		return
	}
	timeout, limit := DefaultWait, MaxWait
	if tool == protocol.ToolCodex {
		timeout, limit = CodexWait, CodexWait
	}
	if v, err := time.ParseDuration(q.Get("timeout")); err == nil && v > 0 {
		timeout = min(v, limit)
	}
	until := time.Now().Add(timeout).UTC()

	d.mu.Lock()
	d.waitSeq++
	my := d.waitSeq
	d.waiting[key] = my
	delete(d.stopBlocks, key) // 已按要求挂上，不再算连续拦下
	d.toolCond.Broadcast() // 让同一会话较早的等待退出
	d.mu.Unlock()
	d.reportWait(tool, sid, protocol.EventWait, "", &until, waitOn(q.Get("on")))

	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	go func() {
		<-ctx.Done()
		d.mu.Lock()
		d.toolCond.Broadcast()
		d.mu.Unlock()
	}()

	reason := ""
	d.mu.Lock()
	for reason == "" {
		switch {
		case d.waiting[key] != my:
			reason = "replaced"
		case d.hasFreshLocked(key):
			reason = "delivered"
		case r.Context().Err() != nil:
			reason = "canceled"
		case ctx.Err() != nil:
			reason = "timeout"
		default:
			d.toolCond.Wait()
		}
	}
	if d.waiting[key] == my {
		delete(d.waiting, key)
	}
	d.mu.Unlock()

	res := WaitResult{Reason: reason, Session: key, Timeout: timeout.String(), Resident: d.resident(key)}
	if reason == "delivered" {
		res.Text = d.injection(protocol.Event{Name: "UserPromptSubmit", Tool: tool, SessionID: sid, TurnID: "ks-wait"})
	}
	if reason != "replaced" {
		d.reportWait(tool, sid, protocol.EventWaitEnd, reason, nil, "")
	}
	_ = json.NewEncoder(w).Encode(res)
}

// waitOn 规整 /wait 的 on 参数；空或不合法时返回空（沿用上次的订阅）。
func waitOn(v string) string {
	if v == "" {
		return ""
	}
	out, err := protocol.ParseWaitOn(v)
	if err != nil {
		return ""
	}
	return out
}

// hasFreshLocked：该会话是否有还没送达、会唤醒 ks wait 的答复 / 修改意见 / 留言。静默消息（Quiet，R-198）不算，
// 它留到下一轮或别的消息唤醒时一并注入。调用方持有 d.mu。
func (d *Daemon) hasFreshLocked(key string) bool {
	for _, dec := range d.decisions {
		if dec.SessionKey == key && !dec.Delivered && !dec.Quiet && !d.q.wasDelivered(dec.ID, key) {
			return true
		}
	}
	return false
}

// reportWait 把等待开始 / 结束作为合成事件上报，Hub 据此显示“挂起等你”。
// waitOn 是 ks wait 开始时随事件上报的订阅（R-164）；不认识的值当作没给（沿用上次）。
func (d *Daemon) reportWait(tool, sid, name, reason string, until *time.Time, waitOn string) {
	ev := protocol.Event{ID: protocol.NewID(), Time: time.Now().UTC(), Tool: tool, Name: name, SessionID: sid, Reason: reason, Until: until, WaitOn: waitOn}
	if err := d.q.pushEvent(ev); err != nil {
		d.log.Warn("上报挂起等待失败", "err", err)
	}
	d.wake()
}

// watchInstances 每分钟检查一次本机见过的 agent 进程是否还在（进程号 + 启动时间都对得上）。
// 终端被直接关掉时 Claude Code / Codex 来不及发 SessionEnd，会话会一直显示运行中；
// 这里替它补发一条 SessionEnd，Hub 按“最新终端”规则决定是否结束会话。
func (d *Daemon) watchInstances(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		d.mu.Lock()
		insts := make(map[string]string, len(d.instances))
		for k, v := range d.instances {
			insts[k] = v
		}
		d.mu.Unlock()
		d.checkInstances(instanceAlive)
	}
}

// seedInstances 记下 Hub 下发的会话终端实例（R-204）：daemon 重启后内存里没有它们，进程在重启前后退出就永远没人补发结束。
func (d *Daemon) seedInstances(list []protocol.SessionInstance) {
	d.mu.Lock()
	defer d.mu.Unlock()
	add := func(inst, key string) {
		if inst == "" {
			return
		}
		for _, k := range d.seeded[inst] {
			if k == key {
				return
			}
		}
		d.seeded[inst] = append(d.seeded[inst], key)
	}
	for _, x := range list {
		add(x.Instance, x.Key)
		add(x.PrevInstance, x.Key)
	}
}

// checkInstances 核对本机见过的（含 Hub 下发的）终端实例，进程不在了就补发 SessionEnd。
func (d *Daemon) checkInstances(alive func(string) bool) {
	d.mu.Lock()
	keys := map[string]map[string]bool{}
	for inst, key := range d.instances {
		keys[inst] = map[string]bool{key: true}
	}
	for inst, ks := range d.seeded {
		if keys[inst] == nil {
			keys[inst] = map[string]bool{}
		}
		for _, k := range ks {
			keys[inst][k] = true
		}
	}
	d.mu.Unlock()
	for inst, set := range keys {
		if alive(inst) {
			continue
		}
		d.mu.Lock()
		delete(d.instances, inst)
		delete(d.seeded, inst)
		for key := range set {
			if d.latest[key] == inst {
				delete(d.latest, key) // 最新终端没了，较早的那个重新领活
			}
		}
		delete(d.staleWarn, inst)
		d.mu.Unlock()
		for key := range set {
			tool, sid, ok := strings.Cut(key, ":")
			if !ok {
				continue
			}
			ev := protocol.Event{ID: protocol.NewID(), Time: time.Now().UTC(), Tool: tool, Name: "SessionEnd", SessionID: sid, Reason: "process_gone", Instance: inst}
			if err := d.q.pushEvent(ev); err != nil {
				d.log.Warn("补发会话结束失败", "err", err)
				continue
			}
			d.log.Info("agent 进程已退出，补发会话结束", "session", key)
			d.wake()
		}
	}
}

// instanceAlive：“进程号@启动时间”对应的进程是否仍在运行。
func instanceAlive(inst string) bool {
	pid, start, ok := strings.Cut(inst, "@")
	if !ok {
		return true
	}
	cmd := exec.Command("ps", "-o", "lstart=", "-p", pid)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.Output()
	if err != nil {
		return false // ps 找不到该进程时以非零退出
	}
	return strings.Join(strings.Fields(string(out)), " ") == start
}

// handleWaitCheck：ks wait 开始前的自查（R-117）。从对话记录里读最新一段回复，连同会话键交给 Hub，
// Hub 返回名下待处理事项与要输出的提示；Hub 连不上时返回空（不影响等待）。
func (d *Daemon) handleWaitCheck(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	w.Header().Set("Content-Type", "application/json")
	key := q.Get("session_key")
	if key == "" {
		ppid, _ := strconv.Atoi(q.Get("ppid"))
		if inst := d.agentInstance(ppid); inst != "" {
			d.mu.Lock()
			key = d.instances[inst]
			d.mu.Unlock()
		}
	}
	if key == "" {
		_, _ = w.Write([]byte("{}"))
		return
	}
	d.mu.Lock()
	tp := ""
	if info := d.sessions[key]; info != nil {
		tp = info.transcript
	}
	d.mu.Unlock()
	body, _ := json.Marshal(map[string]string{"text": latestReply(tp)})
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(d.cfg.HubURL, "/")+"/api/agent/sessions/"+url.PathEscape(key)+"/wait-check", bytes.NewReader(body))
	if err != nil {
		_, _ = w.Write([]byte("{}"))
		return
	}
	req.Header.Set("Authorization", "Bearer "+d.cfg.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		_, _ = w.Write([]byte("{}"))
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = w.Write([]byte("{}"))
		return
	}
	_, _ = io.Copy(w, io.LimitReader(resp.Body, 1<<20))
}

// latestReply 从对话记录尾部读出 Agent 最新的一段文字回复（Claude Code 与 Codex 的格式都认）；读不到时返回空。
// Claude Code 在后台运行 ks wait 时这一轮还没结束，最新的回复只在对话记录里，Hub 存的还是上一轮的。
func latestReply(path string) string {
	buf := readTail(path, 512<<10)
	if len(buf) == 0 {
		return ""
	}
	lines := bytes.Split(buf, []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		line := bytes.TrimSpace(lines[i])
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var x struct {
			Type    string `json:"type"`
			Message struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"message"`
			Payload struct {
				Type    string          `json:"type"`
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"payload"`
		}
		if json.Unmarshal(line, &x) != nil {
			continue
		}
		var content json.RawMessage
		switch {
		case x.Type == "assistant" && x.Message.Role == "assistant": // Claude Code
			content = x.Message.Content
		case x.Type == "response_item" && x.Payload.Type == "message" && x.Payload.Role == "assistant": // Codex
			content = x.Payload.Content
		default:
			continue
		}
		var parts []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(content, &parts) != nil {
			continue
		}
		var texts []string
		for _, p := range parts {
			if (p.Type == "text" || p.Type == "output_text") && strings.TrimSpace(p.Text) != "" {
				texts = append(texts, strings.TrimSpace(p.Text))
			}
		}
		if len(texts) > 0 {
			return strings.Join(texts, "\n\n")
		}
	}
	return ""
}
