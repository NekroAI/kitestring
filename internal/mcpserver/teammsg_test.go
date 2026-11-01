package mcpserver

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/NekroAI/kitestring/internal/client"
)

// R-249 P3：MCP 的 team_tell 与旧名 pm_notify 参数一致、发的请求一模一样；team_broadcast / team_sent 与 ks team broadcast / sent
// 走同一接口；发送方只认调用带的会话身份（meta），参数里没有能指定发送方的字段。
func TestTeamMessagingTools(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "ks-mcp-") // unix socket 路径要短
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	t.Setenv("KS_HOME", dir)
	ln, err := net.Listen("unix", filepath.Join(dir, "daemon.sock"))
	if err != nil {
		t.Fatal(err)
	}
	type call struct {
		Method, Path string
		Body         map[string]any
	}
	var mu sync.Mutex
	var calls []call
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		mu.Lock()
		calls = append(calls, call{r.Method, r.URL.RequestURI(), body})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(r.URL.Path, "/agent/team/sent") {
			_, _ = w.Write([]byte(`{"text":"D-9 点对点 · 要动手"}`))
			return
		}
		_, _ = w.Write([]byte(`{"message":"已送出"}`))
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { srv.Close() })

	s := mcp.NewServer(&mcp.Implementation{Name: "kitestring", Version: "test"}, nil)
	tl := &tools{c: client.New(5 * time.Second)}
	tl.register(s)
	tl.registerTeam(s)
	ct, st := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := s.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "test"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	// team_tell 与 pm_notify 的参数说明一致
	list, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	schemas := map[string]any{}
	for _, tl := range list.Tools {
		schemas[tl.Name] = tl.InputSchema
	}
	for _, n := range []string{"team_tell", "pm_notify", "team_broadcast", "team_sent"} {
		if schemas[n] == nil {
			t.Fatalf("缺少工具 %s", n)
		}
	}
	if !reflect.DeepEqual(schemas["team_tell"], schemas["pm_notify"]) {
		t.Fatal("pm_notify 应保留为旧名，参数与 team_tell 一致")
	}
	if b, _ := json.Marshal(schemas["team_broadcast"]); strings.Contains(string(b), "session") {
		t.Fatalf("广播参数里不应有能指定发送方的字段：%s", b)
	}

	meta := mcp.Meta{"kitestring/session": "dsh:me", "kitestring/agent": "sub-1"}
	run := func(name string, args map[string]any) string {
		t.Helper()
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Meta: meta, Name: name, Arguments: args})
		if err != nil || res.IsError {
			t.Fatalf("%s 失败：%v %+v", name, err, res)
		}
		return res.Content[0].(*mcp.TextContent).Text
	}
	tellArgs := map[string]any{"text": "R-1 看一下", "to": "codex:cx", "requirement": "R-1", "fyi": true}
	run("team_tell", tellArgs)
	run("pm_notify", tellArgs)
	run("team_broadcast", map[string]any{"text": "部署了", "roles": "dev,reviewer", "fyi": true})
	if got := run("team_sent", map[string]any{"ref": "D-9"}); got != "D-9 点对点 · 要动手" {
		t.Fatalf("team_sent 应原样给出 hub 写好的那段：%s", got)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 4 {
		t.Fatalf("应发 4 个请求：%+v", calls)
	}
	want := map[string]any{"session_key": "dsh:me", "agent_id": "sub-1", "text": "R-1 看一下", "to": "codex:cx", "requirement": "R-1", "fyi": true}
	if calls[0].Path != "/agent/team/tell" || !reflect.DeepEqual(calls[0].Body, want) || !reflect.DeepEqual(calls[0], calls[1]) {
		t.Fatalf("team_tell 与 pm_notify 应发同一个请求、发送方来自 meta：%+v", calls[:2])
	}
	if calls[2].Path != "/agent/team/broadcast" || !reflect.DeepEqual(calls[2].Body, map[string]any{"session_key": "dsh:me", "agent_id": "sub-1", "text": "部署了", "roles": "dev,reviewer", "fyi": true}) {
		t.Fatalf("team_broadcast 请求：%+v", calls[2])
	}
	if calls[3].Method != "GET" || calls[3].Path != "/agent/team/sent?ref=D-9&session_key=dsh%3Ame" {
		t.Fatalf("team_sent 请求：%+v", calls[3])
	}
}
