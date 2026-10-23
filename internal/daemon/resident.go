package daemon

import (
	"time"

	"github.com/NekroAI/kitestring/internal/protocol"
	"github.com/NekroAI/kitestring/internal/roles"
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

func residentReason(tool, role string) string {
	how := "在后台运行 ks wait（Bash 工具设 run_in_background，工具超时设得比等待时间长）"
	if tool == protocol.ToolCodex {
		how = "运行 ks wait（前台运行，最多等 30 分钟）"
	}
	return "[Kitestring] 你是本项目的" + roles.Label(role) + "会话，需要常驻在线：先" + how + "挂起待命，再结束本轮。用户的需求、答复、留言会在你待命时送达。"
}

// resident：会话是否要求常驻在线。
func (d *Daemon) resident(key string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return roles.Resident(d.roles[key])
}
