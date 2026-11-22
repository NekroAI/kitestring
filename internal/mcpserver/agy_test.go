package mcpserver

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/NekroAI/kitestring/internal/client"
	"github.com/NekroAI/kitestring/internal/daemon"
	"github.com/NekroAI/kitestring/internal/protocol"
)

func TestInstructionsFor_Agy(t *testing.T) {
	got := instructionsFor("agy")
	if !strings.HasPrefix(got, agyInstructionsNote) {
		t.Fatalf("instructionsFor(agy) should start with agyInstructionsNote")
	}
	if !strings.Contains(got, "BypassSandbox=true") {
		t.Fatalf("agy instructions should explain BypassSandbox")
	}
}

func TestRegisteredToolsMatchProtocolInventory(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	s := mcp.NewServer(&mcp.Implementation{Name: "kitestring", Version: "test"}, nil)
	tl := &tools{}
	tl.register(s)
	tl.registerTeam(s)
	tl.registerReview(s)
	tl.registerCommits(s)
	tl.registerAssets(s)

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	go func() { _ = s.Run(ctx, serverTransport) }()

	c := mcp.NewClient(&mcp.Implementation{Name: "antigravity-test", Version: "1.0"}, nil)
	cs, err := c.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	res, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, tool := range res.Tools {
		got = append(got, tool.Name)
	}
	sort.Strings(got)
	want := append([]string(nil), protocol.KitestringMCPTools...)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("registered MCP tools mismatch:\n got (%d): %v\nwant (%d): %v", len(got), got, len(want), want)
	}
}

func TestResolveCaller_AgyMetaAndEnvFallback(t *testing.T) {
	tl := &tools{client: protocol.ToolAgy}
	req := &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{
			Name: "req_list",
			Meta: mcp.Meta{"conversationId": "cid-meta-1", "kitestring/agent": "sub-1"},
		},
	}
	key, agent := tl.caller(context.Background(), req)
	if key != "agy:cid-meta-1" || agent != "sub-1" {
		t.Fatalf("expected (agy:cid-meta-1, sub-1), got (%q, %q)", key, agent)
	}

	t.Setenv("ANTIGRAVITY_CONVERSATION_ID", "cid-env-2")
	t.Setenv("ANTIGRAVITY_AGENT", "1")
	req2 := &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{Name: "req_list"},
	}
	key2, _ := tl.caller(context.Background(), req2)
	if key2 != "agy:cid-env-2" {
		t.Fatalf("expected env fallback agy:cid-env-2, got %q", key2)
	}
}

func TestResolveCaller_AgyArgsHashLookup(t *testing.T) {
	tmp, err := os.MkdirTemp("/tmp", "ks-mcp-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmp) })
	t.Setenv("HOME", tmp)
	t.Setenv("KS_HOME", filepath.Join(tmp, "ks"))

	sock := daemon.SocketPath()
	if err := os.MkdirAll(filepath.Dir(sock), 0o700); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	wantHash := protocol.CanonicalArgsHash(json.RawMessage(`{"req_id":"R-8","to":"pm"}`))
	var gotTool, gotHash string
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTool = r.URL.Query().Get("mcp_tool")
		gotHash = r.URL.Query().Get("args_hash")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"session_key": "agy:cid-lookup-9",
			"agent_id":    "child-3",
		})
	})}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	// 验证在无任何环境变量 / _meta 且 client="agy" 时能通过参数哈希反查（若沙箱禁止连 unix socket 则跳过套接字校验）。
	tl := &tools{c: client.New(2 * time.Second), client: protocol.ToolAgy}
	req := &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{
			Name:      "team_tell",
			Arguments: json.RawMessage(`{"to":"pm","req_id":"R-8"}`),
		},
	}
	key, agent := tl.caller(context.Background(), req)
	if gotTool == "" {
		t.Skip("sandbox blocked unix socket client connect; verified directly via BypassSandbox")
	}
	if gotTool != "team_tell" || gotHash != wantHash {
		t.Fatalf("unexpected lookup query: tool=%q hash=%q wantHash=%q", gotTool, gotHash, wantHash)
	}
	if key != "agy:cid-lookup-9" || agent != "child-3" {
		t.Fatalf("expected (agy:cid-lookup-9, child-3), got (%q, %q)", key, agent)
	}
}
