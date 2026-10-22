package hub

import (
	"fmt"
	"strings"

	"github.com/NekroAI/kitestring/internal/protocol"
	"github.com/NekroAI/kitestring/internal/store"
)

// applyEvent 按 hook-matrix.md 第 5 节的规则，用一条事件推导会话的新状态。
// prev 为 nil 表示首次见到该会话。返回的会话不含关联字段。
func applyEvent(prev *store.Session, deviceID, projectID string, ev protocol.Event) store.Session {
	var x store.Session
	if prev != nil {
		x = *prev
	} else {
		x = store.Session{
			Key:       protocol.SessionKey(ev.Tool, ev.SessionID),
			Tool:      ev.Tool,
			SessionID: ev.SessionID,
			StartedAt: ev.Time,
			State:     store.StateIdle,
		}
	}
	// 乱序到达的旧事件只用于补充字段，不回退状态与活跃时间。
	stale := prev != nil && ev.Time.Before(prev.LastActiveAt)

	x.DeviceID = deviceID
	if projectID != "" {
		x.ProjectID = projectID
	}
	if ev.Cwd != "" {
		x.Cwd = ev.Cwd
	}
	if ev.Client != "" {
		x.Client = ev.Client
	}
	if ev.Model != "" {
		x.Model = ev.Model
	}
	if ev.Git != nil && ev.Git.Available {
		x.Branch = ev.Git.Branch
		x.Dirty = ev.Git.Dirty
		x.Worktree = ev.Git.Worktree
	}
	if ev.PermMode != "" {
		x.PermMode = ev.PermMode
	}
	if stale || !fromLatestTerminal(&x, ev) {
		return x
	}
	x.LastActiveAt = ev.Time
	prevState := x.State
	if prev == nil {
		prevState = ""
	}

	switch ev.Name {
	case "SessionStart":
		x.EndedAt = nil
		x.DoneAt = nil // 恢复了对话，不再算已完成
		x.State, x.StateDetail = store.StateIdle, ""
	case "UserPromptSubmit":
		x.EndedAt = nil
		x.State, x.StateDetail = store.StateRunning, ""
		if ev.Source == protocol.SourceAuto {
			break // 后台任务通知等自动消息：继续运行，但不算你的一轮，也不覆盖你最后说的话
		}
		x.Turns++
		x.TurnTools = 0
		x.Parked = false // 你直接在终端里发了消息
		x.DoneAt = nil // 你又给它发了消息，不再算已完成
		if ev.Prompt != "" {
			x.LastPrompt = ev.Prompt
		}
	case "PreToolUse", "PostToolUse", "SubagentStart", "SubagentStop", "PreCompact", "PostCompact":
		if ev.Name == "PreToolUse" && asksUser(ev.ToolName) {
			x.TurnTools++
			x.State, x.StateDetail = store.StateWaitReply, toolLabel(ev.ToolName, ev.ToolBrief)
			break
		}
		switch ev.Name {
		case "PreToolUse":
			x.TurnTools++
			x.LastTool = toolLabel(ev.ToolName, ev.ToolBrief)
			t := ev.Time
			x.LastToolAt = &t
		case "SubagentStart":
			x.Subagents++
		case "SubagentStop":
			x.Subagents = max(0, x.Subagents-1)
		case "PreCompact":
			x.Compactions++
		}
		// 只有工具调用才说明 agent 在干活。子 Agent 启停、上下文压缩可能发生在回合结束之后
		// （例如 Claude Code 结束后生成回顾摘要），只更新计数，不改状态。
		if ev.Name != "PreToolUse" && ev.Name != "PostToolUse" {
			break
		}
		// 等待授权时，只有同一回合继续推进的 PostToolUse 才解除。
		if x.State == store.StateWaitApproval && ev.Name != "PostToolUse" {
			break
		}
		if x.State != store.StateEnded {
			x.State, x.StateDetail = store.StateRunning, ""
		}
	case "PermissionRequest":
		if asksUser(ev.ToolName) {
			// 向用户提问 / 请用户确认计划也走授权通道，但对用户来说是“等你回答”。
			x.State, x.StateDetail = store.StateWaitReply, toolLabel(ev.ToolName, ev.ToolBrief)
			break
		}
		x.State = store.StateWaitApproval
		x.StateDetail = strings.TrimSpace(ev.ToolName + " " + ev.ToolBrief)
	case "Notification":
		switch ev.NotifType {
		case "permission_prompt":
			x.State = store.StateWaitApproval
		case "idle_prompt":
			if x.Parked {
				break // 待命时 Claude Code 照常发“空闲”提醒，不算等你回复
			}
			x.State, x.StateDetail = store.StateWaitReply, ""
		}
	case protocol.EventWait:
		// Agent 用 ks wait 挂在后台：待命，你在 Kitestring 上一回应它就继续。
		x.Parked = true
		if x.State != store.StateEnded && x.State != store.StateRunning {
			x.State, x.StateDetail = store.StateParked, ""
		}
	case protocol.EventWaitEnd:
		x.Parked = false
		if x.State == store.StateParked {
			if ev.Reason == "delivered" {
				x.State = store.StateRunning // 收到你的答复，马上继续
			} else {
				x.State = store.StateWaitReply
			}
		}
	case "Stop":
		if x.Parked {
			// 后台挂着的是 ks wait：不是“后台运行”也不是“等你回复”，而是待命。
			x.State, x.StateDetail = store.StateParked, ""
			break
		}
		if ev.BgTasks > 0 {
			// 还有后台任务在跑：结束后 Claude Code 会自动继续，并不是在等你。
			x.State, x.StateDetail = store.StateBackground, bgDetail(ev.BgTasks, ev.BgBrief)
			break
		}
		x.State, x.StateDetail = store.StateWaitReply, ""
	case "Interrupt":
		x.State, x.StateDetail = store.StateWaitReply, ""
	case "SessionEnd":
		t := ev.Time
		x.EndedAt = &t
		x.State, x.StateDetail = store.StateEnded, ""
		x.Subagents = 0
	}
	if x.State != prevState || x.StateSince.IsZero() {
		x.StateSince = ev.Time
	}
	return x
}

// fromLatestTerminal 处理同一对话在多个终端打开的情况：以你最近发消息（或最近启动 / 恢复）的终端为准。
// 较早终端的事件不改状态；它结束时只清除“另一终端也开着”的标记，不结束会话。
func fromLatestTerminal(x *store.Session, ev protocol.Event) bool {
	switch {
	case ev.Instance == "" || x.Instance == "" || ev.Instance == x.Instance:
		if ev.Instance != "" {
			x.Instance = ev.Instance
		}
		return true
	case ev.Name == "SessionStart" || (ev.Name == "UserPromptSubmit" && ev.Source != protocol.SourceAuto):
		x.PrevInstance, x.Instance = x.Instance, ev.Instance
		return true
	default:
		if ev.Name == "SessionEnd" && ev.Instance == x.PrevInstance {
			x.PrevInstance = ""
		}
		return false
	}
}

// bgDetail 生成后台任务说明，例如 “2 · 等待 CI 构建”。
func bgDetail(n int, brief string) string {
	if n <= 1 {
		return brief
	}
	return fmt.Sprintf("%d · %s", n, brief)
}

// asksUser 判断工具是否是在向用户提问或请求确认，而不是请求执行权限。
func asksUser(tool string) bool {
	return tool == "AskUserQuestion" || tool == "ExitPlanMode" || tool == "request_user_input"
}

// toolLabel 生成“工具 · 简述”形式的动作描述。
func toolLabel(tool, brief string) string {
	name := strings.TrimPrefix(tool, "mcp__")
	if brief == "" {
		return name
	}
	return name + " · " + brief
}
