package e2e

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/client"
	"github.com/NekroAI/kitestring/internal/daemon"
	"github.com/NekroAI/kitestring/internal/roles"
	"github.com/NekroAI/kitestring/internal/store"
)

// 轻接入（R-359）：ks role lite 只拿几行短说明；每轮结束没挂 ks wait 时 Stop hook 拦一次，提示挂一轮；
// 这一轮等到超时就不再拦（不续挂）；等到答复的，处理完再结束时又挂一轮。只被发给它的事唤醒：
// 它提的决策被答复、点名发给它的团队消息；发给全体的广播、别的会话的事件不送。会话照常列在会话列表里，标「轻接入」。
// 同样一句话（ks role pm）切回完整身份，身份提示词里带回通用流程规矩。
func TestLiteSession(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	c := client.New(30 * time.Second)
	ctx := context.Background()
	prompt := func(sid, id string) string {
		return hook(t, "claude", map[string]any{"session_id": sid, "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "干活", "prompt_id": id})
	}
	stop := func(sid string) string {
		return hook(t, "claude", map[string]any{"session_id": sid, "cwd": cwd, "hook_event_name": "Stop"})
	}
	blocked := func(out string) bool { return strings.Contains(out, `"decision":"block"`) }
	setRole := func(key, role string) string {
		t.Helper()
		var out struct {
			Prompt string `json:"prompt"`
		}
		if err := c.Do(ctx, "POST", "/agent/sessions/role", map[string]any{"session_key": key, "role": role}, &out); err != nil {
			t.Fatal(err)
		}
		return out.Prompt
	}

	prompt("lite-1", "p0")
	prompt("pm-1", "p0")
	e.waitFor("会话入库", func() bool {
		return e.session("claude:lite-1").ProjectID != "" && e.session("claude:pm-1").ProjectID != ""
	})
	setRole("claude:pm-1", "pm")

	// 1. 一句话进入：别称也认；输出就是那几行短说明，不带流程规矩、接手概览
	p := setRole("claude:lite-1", "轻接入")
	if !strings.HasPrefix(p, roles.LitePrompt()) || strings.Contains(p, "evidence_attach") || strings.Contains(p, "req_create") {
		t.Fatalf("轻接入应只输出短说明：%s", p)
	}
	if e.session("claude:lite-1").Role != roles.Lite {
		t.Fatal("会话应记为轻接入")
	}
	// 主调度不会因为别的会话进入轻接入收到「接任」之类的团队动态
	asleep(t, parkWait(t, e, c, "claude:pm-1"), "主调度（轻接入不是团队身份）")

	// 5. 会话列表里照常列出，标「轻接入」
	var roster struct {
		Text string `json:"text"`
	}
	if err := c.Do(ctx, "GET", "/agent/team/sessions?session_key=claude:pm-1", nil, &roster); err != nil || !strings.Contains(roster.Text, "轻接入") {
		t.Fatalf("会话列表应列出轻接入会话：%s %v", roster.Text, err)
	}

	// 4. 结束一轮没挂 ks wait：拦一次提示挂一轮；同一轮不再拦
	var out string
	e.waitFor("轻接入会话被拦一次", func() bool { out = stop("lite-1"); return blocked(out) })
	if !strings.Contains(out, "轻接入") || !strings.Contains(out, "ks wait") || !strings.Contains(out, "不再续挂") {
		t.Fatalf("拦下理由应提示挂一轮 ks wait：%s", out)
	}
	if blocked(stop("lite-1")) {
		t.Fatal("同一轮只提醒一次")
	}

	// 挂一轮等到超时：标明轻接入，结束时不再拦
	prompt("lite-1", "p1")
	done := make(chan daemon.WaitResult, 1)
	go func() {
		var res daemon.WaitResult
		_ = c.Do(ctx, "GET", "/wait?session_key=claude:lite-1&timeout=1s", nil, &res)
		done <- res
	}()
	if res := <-done; res.Reason != "timeout" || !res.Lite || res.Resident {
		t.Fatalf("应超时、标明轻接入、不算常驻：%+v", res)
	}
	if out := stop("lite-1"); blocked(out) {
		t.Fatalf("等到超时后不再续挂：%s", out)
	}

	// 只被发给它的事唤醒：广播（要动手的）、别的会话的事件不送；点名的团队消息送到
	prompt("lite-1", "p2")
	w := parkWait(t, e, c, "claude:lite-1")
	var bc struct {
		Message string `json:"message"`
	}
	if err := c.Do(ctx, "POST", "/agent/team/broadcast", map[string]any{"session_key": "claude:pm-1", "text": "全体注意：R-1 改了接口"}, &bc); err != nil {
		t.Fatal(err)
	}
	if err := c.Do(ctx, "POST", "/agent/decisions", map[string]any{"question": "主调度问的事", "session_key": "claude:pm-1"}, nil); err != nil {
		t.Fatal(err)
	}
	asleep(t, w, "轻接入（广播、别的会话提的决策）")
	if err := c.Do(ctx, "POST", "/agent/team/tell", map[string]any{"session_key": "claude:pm-1", "to": "claude:lite-1", "text": "请看 R-1 的留言"}, nil); err != nil {
		t.Fatal(err)
	}
	text := woke(t, w, "请看 R-1 的留言")
	if strings.Contains(text, "全体注意") {
		t.Fatalf("广播不应送给轻接入：%s", text)
	}
	// 等到答复的：处理完再结束时又挂一轮
	e.waitFor("唤醒后结束又被拦", func() bool { return blocked(stop("lite-1")) })

	// 它自己提的决策被答复：唤醒
	prompt("lite-1", "p3")
	var dec struct {
		Code string `json:"code"`
	}
	if err := c.Do(ctx, "POST", "/agent/decisions", map[string]any{"question": "缓存放内存还是 Redis？", "session_key": "claude:lite-1"}, &dec); err != nil {
		t.Fatal(err)
	}
	w = parkWait(t, e, c, "claude:lite-1")
	e.call("POST", "/api/decisions/answers", map[string]any{"answers": []map[string]string{{"ref": dec.Code, "answer": "先放内存"}}}, nil)
	woke(t, w, "先放内存")

	// 1. 同样一句话切回完整身份：身份提示词带回通用流程规矩，Stop hook 按常驻拦
	p = setRole("claude:lite-1", "dev")
	for _, want := range []string{"开发（Dev）", "纸鸢的通用做法", "evidence_attach", "req_create"} {
		if !strings.Contains(p, want) {
			t.Fatalf("切回开发后提示词缺 %q", want)
		}
	}
	prompt("lite-1", "p4")
	e.waitFor("开发按常驻拦", func() bool { out = stop("lite-1"); return blocked(out) })
	if !strings.Contains(out, "开发（Dev）会话") {
		t.Fatalf("切回开发后应按常驻提示：%s", out)
	}
	if st := e.session("claude:lite-1"); st.Role != roles.Dev || st.State == store.StateEnded {
		t.Fatalf("切回开发后会话应记为开发：%+v", st.Role)
	}
}
