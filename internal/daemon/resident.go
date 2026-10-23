package daemon

import (
	"strings"
	"time"

	"github.com/NekroAI/kitestring/internal/protocol"
	"github.com/NekroAI/kitestring/internal/roles"
	"github.com/NekroAI/kitestring/internal/summary"
)

// 常驻在线（R-83）：PM 与开发会话要一直挂着 ks wait，需求、答复才送得到。
// 它们结束一轮时如果没挂着 ks wait，Stop hook 拦下结束，提示 Agent 先挂上再结束；
// ks wait 超时后 Agent 被唤醒、再结束时同样会被拦下，于是自动续等。

const (
	// maxStopBlocks：连续拦下这么多次 Agent 仍不挂 ks wait，就放行，避免空转耗额度。
	maxStopBlocks = 2
	// waitGrace：Claude Code 刚在后台启动 ks wait 就结束本轮时，等待登记可能比 Stop 晚到一点。
	waitGrace = time.Second
)

// stopGuard 返回拦下结束的理由；不需要拦时返回空串。对每个 hook 事件调用。
func (d *Daemon) stopGuard(ev protocol.Event) string {
	key := protocol.SessionKey(ev.Tool, ev.SessionID)
	if ev.Name == "UserPromptSubmit" {
		d.mu.Lock()
		delete(d.stopBlocks, key)
		d.mu.Unlock()
		return ""
	}
	if ev.Name != "Stop" {
		return ""
	}
	d.mu.Lock()
	role := d.roles[key]
	d.mu.Unlock()
	if !roles.Resident(role) {
		return ""
	}
	for deadline := time.Now().Add(waitGrace); ; {
		d.mu.Lock()
		_, parked := d.waiting[key]
		n := d.stopBlocks[key]
		if parked {
			d.mu.Unlock()
			return ""
		}
		if time.Now().After(deadline) {
			if n >= maxStopBlocks {
				delete(d.stopBlocks, key)
				d.mu.Unlock()
				d.log.Info("常驻会话多次未挂 ks wait，放行结束", "session", key)
				return ""
			}
			d.stopBlocks[key] = n + 1
			d.mu.Unlock()
			return residentReason(ev.Tool, role)
		}
		d.mu.Unlock()
		time.Sleep(100 * time.Millisecond)
	}
}

// holdReply 留下被拦下的那次结束时的回复（R-109）。
func (d *Daemon) holdReply(ev protocol.Event) {
	if ev.Reply == "" {
		return
	}
	key := protocol.SessionKey(ev.Tool, ev.SessionID)
	d.mu.Lock()
	defer d.mu.Unlock()
	if prev := d.heldReply[key]; prev != "" {
		ev.Reply = prev + "\n\n" + ev.Reply
	}
	d.heldReply[key] = summary.Truncate(ev.Reply, protocol.ReplyMax)
}

// mergeHeldReply 在这一轮真正结束时把留下的回复并到前面；你发新消息时丢弃（那一轮已经过去了）。
func (d *Daemon) mergeHeldReply(ev *protocol.Event) {
	if ev.Name != "Stop" && ev.Name != "UserPromptSubmit" {
		return
	}
	key := protocol.SessionKey(ev.Tool, ev.SessionID)
	d.mu.Lock()
	held := d.heldReply[key]
	delete(d.heldReply, key)
	d.mu.Unlock()
	if held == "" || ev.Name != "Stop" {
		return
	}
	ev.Reply = summary.Truncate(strings.TrimSpace(held+"\n\n"+ev.Reply), protocol.ReplyMax)
}

func residentReason(tool, role string) string {
	how := "在后台运行 ks wait（Bash 工具设 run_in_background，工具超时设得比等待时间长）"
	if tool == protocol.ToolCodex {
		how = "运行 ks wait（前台运行，最多等 30 分钟）"
	}
	return "[Kitestring] 你是本项目的 " + roles.Label(role) + " 会话，需要常驻在线：先" + how + "挂起待命，再结束本轮。用户的需求、答复、留言会在你待命时送达。"
}

// resident：会话是否要求常驻在线。
func (d *Daemon) resident(key string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return roles.Resident(d.roles[key])
}

// 同一对话在多个终端同时运行（R-70）：以你最近发消息（或最近启动 / 恢复）的终端为准，
// 只有它领活（需求、答复、留言的送达，ks wait）；较早的终端被提醒一次，之后不再收到送达。

const staleText = "[Kitestring] 这个对话已经在另一个终端里继续了，本终端不再接收需求、答复和留言。请不要在这里继续改代码或提交，告诉用户关掉本终端，或在这里重新发一条消息把对话接回来。"

// noteInstance 登记事件所属终端，返回它是否是较早的终端（不领活）。
func (d *Daemon) noteInstance(key string, ev protocol.Event) bool {
	if ev.Instance == "" {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	cur := d.latest[key]
	if cur == "" || cur == ev.Instance || ev.Name == "SessionStart" || (ev.Name == "UserPromptSubmit" && ev.Source != protocol.SourceAuto) {
		d.latest[key] = ev.Instance
		delete(d.staleWarn, ev.Instance)
		return false
	}
	return true
}

// isStale：该终端是否已被同一对话的更新终端取代。
func (d *Daemon) isStale(key, inst string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	cur := d.latest[key]
	return cur != "" && cur != inst
}

// staleNote：较早终端第一次触发 hook 时的提醒；之后返回空串。
func (d *Daemon) staleNote(inst string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.staleWarn[inst] {
		return ""
	}
	d.staleWarn[inst] = true
	return staleText + "\n"
}
