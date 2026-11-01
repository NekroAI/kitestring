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

// R-249 P4：MCP 的 req_watch / req_watching 与 ks req watch / unwatch / watching 走同一接口、同一组参数（off、mode），
// 订阅者只认调用带的会话身份；req_watching 原样给出 hub 写好的那段（含「因关联而订阅」）。
func TestWatchTools(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "ks-mcp-")
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
		if strings.HasPrefix(r.URL.Path, "/agent/watching") {
			_, _ = w.Write([]byte(`{"text":"- R-1 · 因关联而订阅"}`))
			return
		}
		_, _ = w.Write([]byte(`{"message":"已订阅 R-1"}`))
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
	list, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]string{}
	for _, x := range list.Tools {
		b, _ := json.Marshal(x.InputSchema)
		found[x.Name] = string(b)
	}
	if found["req_watch"] == "" || found["req_watching"] == "" {
		t.Fatal("缺少 req_watch / req_watching")
	}
	if strings.Contains(found["req_watch"], "session") {
		t.Fatalf("req_watch 参数里不应有能指定订阅者的字段：%s", found["req_watch"])
	}
	meta := mcp.Meta{"kitestring/session": "codex:me"}
	run := func(name string, args map[string]any) string {
		t.Helper()
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Meta: meta, Name: name, Arguments: args})
		if err != nil || res.IsError {
			t.Fatalf("%s 失败：%v %+v", name, err, res)
		}
		return res.Content[0].(*mcp.TextContent).Text
	}
	run("req_watch", map[string]any{"ref": "R-1"})
	run("req_watch", map[string]any{"ref": "R-1", "mode": "action"})
	run("req_watch", map[string]any{"ref": "R-1", "off": true})
	if got := run("req_watching", map[string]any{}); got != "- R-1 · 因关联而订阅" {
		t.Fatalf("req_watching 应原样给出 hub 那段：%s", got)
	}
	mu.Lock()
	defer mu.Unlock()
	// 与 ks req watch / watch --action-only / unwatch 的请求体一致（见 cmd/ks 的 TestWatchBody）
	want := []map[string]any{
		{"session_key": "codex:me", "mode": "all"},
		{"session_key": "codex:me", "mode": "action"},
		{"session_key": "codex:me", "off": true},
	}
	for i, w := range want {
		if calls[i].Method != "POST" || calls[i].Path != "/agent/requirements/R-1/watch" || !reflect.DeepEqual(calls[i].Body, w) {
			t.Fatalf("第 %d 个请求：%+v", i, calls[i])
		}
	}
	if calls[3].Method != "GET" || calls[3].Path != "/agent/watching?session_key=codex%3Ame" {
		t.Fatalf("req_watching 请求：%+v", calls[3])
	}
}
