package e2e

import (
	"context"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/client"
	"github.com/NekroAI/kitestring/internal/daemon"
)

// R-59：会话里还跑着旧版 ks mcp（请求不带版本或版本不同）时，网页标“工具待更新”，Agent 收到一次提醒；换成新版后清除。
func TestStaleToolsFlagged(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	key := "claude:tl-1"
	prompt := func(id string) string {
		return hook(t, "claude", map[string]any{"session_id": "tl-1", "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "x", "prompt_id": id})
	}
	prompt("p0")
	e.waitFor("会话上报", func() bool { return e.session(key).Key != "" })

	// 旧版 MCP：直接走 unix socket，不带版本头
	raw := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", daemon.SocketPath())
	}}, Timeout: 5 * time.Second}
	req, _ := http.NewRequest("GET", "http://daemon/agent/requirements?session_key="+key, nil)
	resp, err := raw.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	out := prompt("p1")
	if !strings.Contains(out, "工具（MCP）还是升级前的版本") || !strings.Contains(out, "ks req") {
		t.Fatalf("应提醒 Agent 工具过期并给出替代方式：%s", out)
	}
	if again := prompt("p2"); strings.Contains(again, "升级前的版本") {
		t.Fatal("同一状态只提醒一次")
	}
	e.waitFor("网页标出工具待更新", func() bool { return e.session(key).ToolsStale != "" })

	// 换成新版（client 带版本头）后清除
	if err := client.New(5*time.Second).Do(context.Background(), "GET", "/agent/requirements?session_key="+key, nil, nil); err != nil {
		t.Fatal(err)
	}
	prompt("p3")
	e.waitFor("工具更新后清除标记", func() bool { return e.session(key).ToolsStale == "" })
}
