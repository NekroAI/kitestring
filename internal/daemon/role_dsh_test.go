package daemon

import (
	"strings"
	"testing"

	"github.com/NekroAI/kitestring/internal/protocol"
	"github.com/NekroAI/kitestring/internal/roles"
)

// DSH 的压缩走 PostCompact（SessionStart 会把跑着的会话改成空闲）：
// 有身份时要补回身份提示词和 DSH 的待命说明，普通 Stop 不补。
func TestRolePromptForDSHPostCompact(t *testing.T) {
	d := &Daemon{roles: map[string]string{"dsh:s1": roles.PM}, customPrompts: map[string]string{}}
	p := d.rolePrompt(protocol.Event{Tool: protocol.ToolDSH, SessionID: "s1", Name: "PostCompact", Source: "compact"})
	if !strings.Contains(p, "DeepSeek Harness") || !strings.Contains(p, "不要运行 ks wait") {
		t.Fatalf("PostCompact 应补 DSH 身份提示词，得到 %q", p)
	}
	if got := d.rolePrompt(protocol.Event{Tool: protocol.ToolDSH, SessionID: "s1", Name: "Stop"}); got != "" {
		t.Fatalf("普通 Stop 不应补身份提示词，得到 %q", got)
	}
	resume := d.rolePrompt(protocol.Event{Tool: protocol.ToolDSH, SessionID: "s1", Name: "SessionStart", Source: "resume"})
	if !strings.Contains(resume, "不要运行 ks wait") {
		t.Fatalf("恢复会话仍应补身份提示词，得到 %q", resume)
	}
}
