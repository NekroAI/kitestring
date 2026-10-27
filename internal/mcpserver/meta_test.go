package mcpserver

import (
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// DSH 的 Host 插件直接按调用带会话身份；这条路径要压过 Claude / Codex 的推断。
func TestMetaSessionKey(t *testing.T) {
	req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Meta: mcp.Meta{
		"kitestring/session": "dsh:session-1",
		"kitestring/agent":   "sub-2",
	}}}
	if got := metaSessionKey(req); got != "dsh:session-1" {
		t.Fatalf("metaSessionKey = %q", got)
	}
	if got := metaAgentID(req); got != "sub-2" {
		t.Fatalf("metaAgentID = %q", got)
	}
	if got := sessionKeyFromMeta(req); got != "dsh:session-1" {
		t.Fatalf("sessionKeyFromMeta = %q", got)
	}
}

func TestMetaSessionKeyEmpty(t *testing.T) {
	req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{}}
	if got := metaSessionKey(req); got != "" {
		t.Fatalf("空 meta 应返回空，得到 %q", got)
	}
}

// DSH 的说明要先盖掉“挂 ks wait”，再进通用说明。
func TestInstructionsForDSH(t *testing.T) {
	plain := instructionsFor("")
	if strings.Contains(plain, "不要运行 ks wait") {
		t.Fatal("普通客户端的说明不应出现 DSH 专用段落")
	}
	dsh := instructionsFor("dsh")
	if !strings.HasPrefix(dsh, dshInstructionsNote) {
		t.Fatal("DSH 说明应以 dshInstructionsNote 开头")
	}
	if !strings.Contains(dsh, "不要运行 ks wait") {
		t.Fatal("DSH 说明缺少不要运行 ks wait 的提示")
	}
}
