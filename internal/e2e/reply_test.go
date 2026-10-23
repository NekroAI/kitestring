package e2e

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/NekroAI/kitestring/internal/client"
	"github.com/NekroAI/kitestring/internal/protocol"
	"github.com/NekroAI/kitestring/internal/store"
)

// R-109：一轮结束时的最后回复（Stop 的 last_assistant_message）同步到 Hub，会话详情 API 能读到；
// Claude Code 与 Codex 都覆盖；超长时截到 2 万字；回复不写进事件记录；没带回复的 Stop 不清掉上一轮的。
func TestLastReplySynced(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	type detail struct {
		Session   store.Session    `json:"session"`
		Events    []protocol.Event `json:"events"`
		LastReply *store.LastReply `json:"last_reply"`
	}
	get := func(key string) detail {
		var d detail
		e.call("GET", "/api/sessions/"+key, nil, &d)
		return d
	}

	reply := "## 清场报告\n\n- R-1 已验收\n- R-2 **待你确认**\n\n```go\nfmt.Println(1)\n```"
	for _, tool := range []string{"claude", "codex"} {
		sid := tool + "-r1"
		key := tool + ":" + sid
		hook(t, tool, map[string]any{"session_id": sid, "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "清场"})
		e.waitFor("会话入库", func() bool { return e.session(key).Key != "" })
		if d := get(key); d.LastReply != nil {
			t.Fatalf("%s：还没结束过一轮，不应有最后回复：%+v", tool, d.LastReply)
		}
		hook(t, tool, map[string]any{"session_id": sid, "cwd": cwd, "hook_event_name": "Stop", "last_assistant_message": reply})
		e.waitFor(tool+" 最后回复入库", func() bool {
			d := get(key)
			return d.LastReply != nil && d.LastReply.Text == reply && !d.LastReply.At.IsZero()
		})
		for _, ev := range get(key).Events {
			if ev.Reply != "" || strings.Contains(ev.LastReply, "清场报告") {
				t.Fatalf("%s：回复不应写进事件记录：%+v", tool, ev)
			}
		}
	}

	// 下一轮：没带回复的 Stop 不清掉上一轮的；超长回复截到 2 万字
	key := "claude:claude-r1"
	hook(t, "claude", map[string]any{"session_id": "claude-r1", "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "再来"})
	hook(t, "claude", map[string]any{"session_id": "claude-r1", "cwd": cwd, "hook_event_name": "Stop"})
	e.waitFor("等待回复", func() bool { return e.session(key).State == store.StateWaitReply })
	if d := get(key); d.LastReply == nil || d.LastReply.Text != reply {
		t.Fatalf("空回复不应覆盖上一轮：%+v", d.LastReply)
	}
	hook(t, "claude", map[string]any{"session_id": "claude-r1", "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "写长的"})
	hook(t, "claude", map[string]any{"session_id": "claude-r1", "cwd": cwd, "hook_event_name": "Stop", "last_assistant_message": strings.Repeat("长", 30000)})
	e.waitFor("长回复截断入库", func() bool {
		d := get(key)
		return d.LastReply != nil && strings.HasPrefix(d.LastReply.Text, "长长") && utf8.RuneCountInString(d.LastReply.Text) <= protocol.ReplyMax+1
	})

	// 常驻 PM：这一轮的报告写完就结束，被拦下（没挂 ks wait）；挂上后再结束——两段回复都要看得到
	hook(t, "claude", map[string]any{"session_id": "pm-r", "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "清场"})
	e.waitFor("PM 入库", func() bool { return e.session("claude:pm-r").Key != "" })
	c := client.New(30 * time.Second)
	if err := c.Do(context.Background(), "POST", "/agent/sessions/role", map[string]any{"session_key": "claude:pm-r", "role": "pm"}, nil); err != nil {
		t.Fatal(err)
	}
	e.waitFor("身份入库", func() bool { return e.session("claude:pm-r").Role == "pm" })
	if out := hook(t, "claude", map[string]any{"session_id": "pm-r", "cwd": cwd, "hook_event_name": "Stop", "last_assistant_message": "## 清场报告\n全部完成"}); !strings.Contains(out, `"decision":"block"`) {
		t.Fatalf("没挂 ks wait 应被拦：%s", out)
	}
	go func() { _ = c.Do(context.Background(), "GET", "/wait?session_key=claude:pm-r&timeout=3s", nil, nil) }()
	e.waitFor("挂起登记", func() bool { return e.session("claude:pm-r").Parked })
	hook(t, "claude", map[string]any{"session_id": "pm-r", "cwd": cwd, "hook_event_name": "Stop", "last_assistant_message": "已挂上 ks wait 待命。"})
	e.waitFor("被拦下的报告并入最后回复", func() bool {
		d := get("claude:pm-r")
		return d.LastReply != nil && d.LastReply.Text == "## 清场报告\n全部完成\n\n已挂上 ks wait 待命。"
	})
}
