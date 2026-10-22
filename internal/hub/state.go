package hub

import (
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
	if ev.Summary != "" {
		x.Summary = ev.Summary
	}
	if stale || ev.Name == protocol.EventSummary {
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
		x.State, x.StateDetail = store.StateIdle, ""
	case "UserPromptSubmit":
		x.EndedAt = nil
		x.State, x.StateDetail = store.StateRunning, ""
		x.Turns++
		x.TurnTools = 0
		if ev.Prompt != "" {
			x.LastPrompt = ev.Prompt
		}
	case "PreToolUse", "PostToolUse", "SubagentStart", "SubagentStop", "PreCompact", "PostCompact":
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
		// 等待授权时，只有同一回合继续推进的 PostToolUse 才解除；其余事件只刷新活跃时间。
		if x.State == store.StateWaitApproval && ev.Name != "PostToolUse" {
			break
		}
		if x.State != store.StateEnded {
			x.State, x.StateDetail = store.StateRunning, ""
		}
	case "PermissionRequest":
		x.State = store.StateWaitApproval
		x.StateDetail = strings.TrimSpace(ev.ToolName + " " + ev.ToolBrief)
	case "Notification":
		switch ev.NotifType {
		case "permission_prompt":
			x.State = store.StateWaitApproval
		case "idle_prompt":
			x.State, x.StateDetail = store.StateWaitReply, ""
		}
	case "Stop", "Interrupt":
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

// toolLabel 生成“工具 · 简述”形式的动作描述。
func toolLabel(tool, brief string) string {
	name := strings.TrimPrefix(tool, "mcp__")
	if brief == "" {
		return name
	}
	return name + " · " + brief
}
