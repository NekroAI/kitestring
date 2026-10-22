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
		return 0
	}
	var payload map[string]any
	if json.Unmarshal(raw, &payload) != nil {
		return 0
	}
	if event == "" {
		event, _ = payload["hook_event_name"].(string)
	}
	req := protocol.HookRequest{Tool: tool, Event: event, Payload: payload, PPID: os.Getppid(), Env: map[string]string{}}
	for _, k := range []string{"CLAUDE_CODE_ENTRYPOINT", "TERM_PROGRAM"} {
		if v := os.Getenv(k); v != "" {
			req.Env[k] = v
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), Timeout)
	defer cancel()
	var resp protocol.HookResponse
	if err := client.New(Timeout).Do(ctx, "POST", "/hook", req, &resp); err != nil || resp.Context == "" {
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
