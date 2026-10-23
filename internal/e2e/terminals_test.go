package e2e

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/client"
	"github.com/NekroAI/kitestring/internal/daemon"
	"github.com/NekroAI/kitestring/internal/protocol"
)

// R-70：同一对话在两个终端同时运行时，会话带「另一个终端也开着」标记；
// 送达（ks wait）只给最新终端，较早终端的 ks wait 立刻被告知不再领活，hook 上只提醒一次；
// 最新终端退出后，较早终端重新领活。
func TestTwoTerminalsOnlyLatestGetsDelivery(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	c := client.New(30 * time.Second)
	ctx := context.Background()
	oldAgent := fakeAgent(t, "claude", "sleep 120; echo r70")
	newAgent := fakeAgent(t, "claude", "sleep 120; echo r70")
	oldPid, newPid := oldAgent.Process.Pid, newAgent.Process.Pid
	key := "claude:dup-1"
	send := func(ppid int, name string, extra map[string]any) protocol.HookResponse {
		t.Helper()
		p := map[string]any{"session_id": "dup-1", "cwd": cwd, "hook_event_name": name}
		for k, v := range extra {
			p[k] = v
		}
		var resp protocol.HookResponse
		if err := c.Do(ctx, "POST", "/hook", protocol.HookRequest{Tool: "claude", Event: name, Payload: p, PPID: ppid}, &resp); err != nil {
			t.Fatal(err)
		}
		return resp
	}

	send(oldPid, "UserPromptSubmit", map[string]any{"prompt": "旧终端开工"})
	e.waitFor("会话入库", func() bool { return e.session(key).Key != "" })
	if e.session(key).OtherTerminal {
		t.Fatal("只有一个终端时不应提示")
	}
	send(newPid, "UserPromptSubmit", map[string]any{"prompt": "新终端接着干"})
	e.waitFor("另一个终端提示", func() bool { return e.session(key).OtherTerminal })

	// 较早终端的 hook：不领活，第一次带提醒，之后不再重复
	r1 := send(oldPid, "PreToolUse", map[string]any{"tool_name": "Bash", "tool_use_id": "tu-1", "tool_input": map[string]any{"command": "ls"}})
	if !strings.Contains(r1.Context, "另一个终端") {
		t.Fatalf("较早终端应被提醒：%q", r1.Context)
	}
	if r2 := send(oldPid, "PreToolUse", map[string]any{"tool_name": "Bash", "tool_use_id": "tu-2", "tool_input": map[string]any{"command": "ls"}}); r2.Context != "" {
		t.Fatalf("提醒只给一次：%q", r2.Context)
	}

	// 网页留言：较早终端的 ks wait 立刻被拒，最新终端的 ks wait 收到
	e.call("POST", "/api/sessions/"+key+"/reply", map[string]any{"comment": "只发给最新终端"}, nil)
	var stale daemon.WaitResult
	if err := c.Do(ctx, "GET", "/wait?ppid="+itoa(oldPid)+"&timeout=5s", nil, &stale); err != nil {
		t.Fatal(err)
	}
	if stale.Reason != "stale" || strings.Contains(stale.Text, "只发给最新终端") {
		t.Fatalf("较早终端的 ks wait 应被拒且不带留言：%+v", stale)
	}
	var fresh daemon.WaitResult
	if err := c.Do(ctx, "GET", "/wait?ppid="+itoa(newPid)+"&timeout=20s", nil, &fresh); err != nil {
		t.Fatal(err)
	}
	if fresh.Reason != "delivered" || !strings.Contains(fresh.Text, "只发给最新终端") {
		t.Fatalf("最新终端应收到留言：%+v", fresh)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }
