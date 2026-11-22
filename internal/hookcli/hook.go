// Package hookcli 实现 ks hook <tool> <event>：Claude Code / Codex 调用的薄客户端。
// 只与本机 daemon 通信，带硬超时；任何失败都静默放行，绝不阻塞或打断 agent（D12）。
package hookcli

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"time"

	"github.com/NekroAI/kitestring/internal/client"
	"github.com/NekroAI/kitestring/internal/protocol"
)

// Timeout 是 hook 等待 daemon 的上限。
const Timeout = 1500 * time.Millisecond

// reportedEnv 是 hook 唯一会上报的环境变量白名单（D13）：CLAUDE_CONFIG_DIR 与 ANTIGRAVITY_APP_DATA_DIR 只是目录路径（R-240）。
// ANTHROPIC_API_KEY / ANTHROPIC_BASE_URL / ANTIGRAVITY_CSRF_TOKEN / ANTIGRAVITY_LS_ADDRESS 等凭证与网关地址不在其中，也不得加入。
var reportedEnv = []string{"CLAUDE_CODE_ENTRYPOINT", "TERM_PROGRAM", "CLAUDE_CONFIG_DIR", "ANTIGRAVITY_AGENT", "ANTIGRAVITY_APP_DATA_DIR"}

func collectEnv() map[string]string {
	env := map[string]string{}
	for _, k := range reportedEnv {
		if v := os.Getenv(k); v != "" {
			env[k] = v
		}
	}
	return env
}

// Run 执行 hook，始终返回 0。
func Run(args []string, stdin io.Reader, stdout io.Writer) int {
	if len(args) < 1 {
		return 0
	}
	tool, event := args[0], ""
	if len(args) > 1 {
		event = args[1]
	}
	raw, err := io.ReadAll(io.LimitReader(stdin, 4<<20))
	if err != nil {
		writeAgyFallback(stdout, tool, event)
		return 0
	}
	var payload map[string]any
	if json.Unmarshal(raw, &payload) != nil {
		writeAgyFallback(stdout, tool, event)
		return 0
	}
	if event == "" {
		event, _ = payload["hook_event_name"].(string)
		if event == "" {
			event, _ = payload["hookEventName"].(string)
		}
	}
	req := protocol.HookRequest{Tool: tool, Event: event, Payload: payload, PPID: os.Getppid(), Env: map[string]string{}}
	req.Env = collectEnv()
	ctx, cancel := context.WithTimeout(context.Background(), Timeout)
	defer cancel()
	var resp protocol.HookResponse
	if err := client.New(Timeout).Do(ctx, "POST", "/hook", req, &resp); err != nil {
		writeAgyFallback(stdout, tool, event)
		return 0
	}
	if tool == protocol.ToolAgy {
		writeAgyResponse(stdout, event, resp)
		return 0
	}
	if resp.Block != "" {
		// Stop hook 拦下结束：Claude Code 与 Codex 都认 decision=block，reason 交给 Agent 继续处理（R-83）
		_ = json.NewEncoder(stdout).Encode(map[string]any{"decision": "block", "reason": resp.Block})
		return 0
	}
	if resp.Context == "" {
		return 0
	}
	name, _ := payload["hook_event_name"].(string)
	if name == "" {
		name = event
	}
	_ = json.NewEncoder(stdout).Encode(map[string]any{
		"hookSpecificOutput": map[string]any{"hookEventName": name, "additionalContext": resp.Context},
	})
	return 0
}

func writeAgyFallback(stdout io.Writer, tool, event string) {
	if tool != protocol.ToolAgy {
		return
	}
	if event == "PreToolUse" || event == "Stop" {
		_ = json.NewEncoder(stdout).Encode(map[string]any{"decision": "allow"})
		return
	}
	_, _ = io.WriteString(stdout, "{}\n")
}

func writeAgyResponse(stdout io.Writer, event string, resp protocol.HookResponse) {
	if resp.Block != "" {
		// Antigravity 的 Stop hook 使用 decision=continue + reason 拦下结束并继续执行。
		_ = json.NewEncoder(stdout).Encode(map[string]any{
			"decision": "continue",
			"reason":   resp.Block,
		})
		return
	}
	if resp.Context != "" {
		out := map[string]any{
			"injectSteps": []map[string]any{
				{"systemMessage": map[string]any{"systemMessage": resp.Context}},
			},
		}
		if event == "PreToolUse" {
			out["decision"] = "allow"
		}
		_ = json.NewEncoder(stdout).Encode(out)
		return
	}
	writeAgyFallback(stdout, protocol.ToolAgy, event)
}
