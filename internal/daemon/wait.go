package daemon

import (
	"context"
	"encoding/json"
	"net/http"
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
)

// WaitResult 是 /wait 的返回：reason 为 delivered、timeout、replaced、canceled。
type WaitResult struct {
	Reason  string `json:"reason"`
	Text    string `json:"text,omitempty"`
	Session string `json:"session_key,omitempty"`
	Error   string `json:"error,omitempty"`
}

func (d *Daemon) handleWait(w http.ResponseWriter, r *http.Request) {
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
	tool, sid, ok := strings.Cut(key, ":")
	if !ok || sid == "" {
		_ = json.NewEncoder(w).Encode(WaitResult{Error: "认不出当前会话：请在 Claude Code / Codex 的对话里运行，或用 --session tool:id 指定"})
		return
	}
	timeout := DefaultWait
	if v, err := time.ParseDuration(q.Get("timeout")); err == nil && v > 0 {
		timeout = min(v, MaxWait)
	}

	d.mu.Lock()
	d.waitSeq++
	my := d.waitSeq
	d.waiting[key] = my
	d.toolCond.Broadcast() // 让同一会话较早的等待退出
	d.mu.Unlock()
	d.reportWait(tool, sid, protocol.EventWait, "")

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

	res := WaitResult{Reason: reason, Session: key}
	if reason == "delivered" {
		res.Text = d.injection(protocol.Event{Name: "UserPromptSubmit", Tool: tool, SessionID: sid, TurnID: "ks-wait"})
	}
	if reason != "replaced" {
		d.reportWait(tool, sid, protocol.EventWaitEnd, reason)
	}
	_ = json.NewEncoder(w).Encode(res)
}

// hasFreshLocked：该会话是否有还没送达的答复 / 修改意见 / 留言。调用方持有 d.mu。
func (d *Daemon) hasFreshLocked(key string) bool {
	for _, dec := range d.decisions {
		if dec.SessionKey == key && !dec.Delivered && !d.q.wasDelivered(dec.ID, key) {
			return true
		}
	}
	return false
}

// reportWait 把等待开始 / 结束作为合成事件上报，Hub 据此显示“挂起等你”。
func (d *Daemon) reportWait(tool, sid, name, reason string) {
	ev := protocol.Event{ID: protocol.NewID(), Time: time.Now().UTC(), Tool: tool, Name: name, SessionID: sid, Reason: reason}
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
		for inst, key := range insts {
			if instanceAlive(inst) {
				continue
			}
			d.mu.Lock()
			delete(d.instances, inst)
			d.mu.Unlock()
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
