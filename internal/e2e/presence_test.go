package e2e

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/client"
	"github.com/NekroAI/kitestring/internal/store"
)

// presenceTells：发给 key 的身份变动通知（R-319）。
func presenceTells(e *env, key, kind string) []store.Decision {
	var got []store.Decision
	for _, d := range tellsTo(e, key) {
		if strings.HasPrefix(d.Answer, "身份变动（presence · "+kind+"）：") {
			got = append(got, d)
		}
	}
	return got
}

// presenceHas：key 收到过含 want 的身份变动通知，且是只告知。
func presenceHas(e *env, key, kind, want string) bool {
	for _, d := range presenceTells(e, key, kind) {
		if d.FYI && strings.Contains(d.Answer, want) {
			return true
		}
	}
	return false
}

// R-319：有身份的会话接任、不再担任身份、下线时，本项目其他在线会话收到只告知（不唤醒）；无身份会话的上下线不发；
// 接任时的接手概览列出当前在线的会话和身份；转送积压的「某身份下线」消息时，那个身份已被接任就注明「已由 X 接任」。
func TestPresenceNotice(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	c := client.New(30 * time.Second)
	ctx := context.Background()
	ev := func(sid, name string, extra map[string]any) string {
		p := map[string]any{"session_id": sid, "cwd": cwd, "hook_event_name": name}
		for k, v := range extra {
			p[k] = v
		}
		return hook(t, "claude", p)
	}
	for _, sid := range []string{"pr-pm", "pr-aide", "pr-plain", "pr-pm2", "pr-aide2", "pr-x"} {
		ev(sid, "UserPromptSubmit", map[string]any{"prompt": "上线", "prompt_id": "p0"})
	}
	e.waitFor("会话入库", func() bool { return e.session("claude:pr-x").ProjectID != "" })
	type roleOut struct {
		Prompt string `json:"prompt"`
	}
	setRole := func(key, role string) string {
		t.Helper()
		var out roleOut
		if err := c.Do(ctx, "POST", "/agent/sessions/role", map[string]any{"session_key": key, "role": role}, &out); err != nil {
			t.Fatal(err)
		}
		return out.Prompt
	}
	// pr-pm2 / pr-aide2 / pr-x 先结束本轮再说（接任前它们是无身份会话；pr-x 后面用来测无身份会话结束不通知）
	setRole("claude:pr-pm", "pm")

	// 1. 主助理接任：在线的主调度、无身份会话都收到只告知的「上线」，主调度挂着的 ks wait 不被唤醒
	pmW := parkWait(t, e, c, "claude:pr-pm")
	for i := 0; i < 5; i++ { // 先收掉已有的消息
		select {
		case <-pmW:
			pmW = parkWait(t, e, c, "claude:pr-pm")
			continue
		case <-time.After(300 * time.Millisecond):
		}
		break
	}
	prompt := setRole("claude:pr-aide", "aide")
	asleep(t, pmW, "主调度（主助理上线是只告知）")
	for _, k := range []string{"claude:pr-pm", "claude:pr-plain"} {
		if !presenceHas(e, k, "上线", "接任主助理，已上线") {
			t.Fatalf("%s 应收到主助理上线的只告知：%+v", k, presenceTells(e, k, "上线"))
		}
	}
	if presenceHas(e, "claude:pr-aide", "上线", "（claude:pr-aide） 接任") {
		t.Fatal("接任的会话自己不应收到自己的上线通知")
	}
	// 接手概览：当前在线一栏列出主调度和无身份会话
	i := strings.Index(prompt, "### 当前在线")
	if i < 0 {
		t.Fatalf("接手概览应有「当前在线」一栏：%s", prompt)
	}
	online := prompt[i:]
	if j := strings.Index(online[4:], "### "); j >= 0 {
		online = online[:j+4]
	}
	for _, want := range []string{"主调度 claude:pr-pm", "无身份 claude:pr-plain", "主助理 claude:pr-aide（你）"} {
		if !strings.Contains(online, want) {
			t.Fatalf("当前在线应含 %q：%s", want, online)
		}
	}
	// 主调度的 ks wait 下一轮开始时带上这条只告知
	drainQuiet(t, pmW, "主调度", "pr-pm", "接任主助理")

	// 2. 无身份会话上下线不发：pr-x 结束
	before := len(presenceTells(e, "claude:pr-pm", "下线")) + len(presenceTells(e, "claude:pr-aide", "下线"))
	ev("pr-x", "SessionEnd", nil)
	e.waitFor("pr-x 结束", func() bool { return e.session("claude:pr-x").State == store.StateEnded })
	if n := len(presenceTells(e, "claude:pr-pm", "下线")) + len(presenceTells(e, "claude:pr-aide", "下线")); n != before {
		t.Fatalf("无身份会话结束不应通知：%d → %d", before, n)
	}

	// 3. 复现 11-08：主助理先停下下线，主调度再停下（停下通知排队给已下线的主助理）；
	//    新主调度接任 → 在线会话收到上线；新主助理接任 → 积压的「主调度停下」转过来，注明已由新主调度接任
	stopOffline := func(sid string) {
		t.Helper()
		e.call("POST", "/api/sessions/claude:"+sid+"/stop", nil, nil)
		e.waitFor(sid+" 停下后下线", func() bool { // 第一次 Stop 收到停下提醒，再结束本轮记为已下线
			ev(sid, "Stop", nil)
			return e.session("claude:"+sid).OfflineAt != nil
		})
	}
	stopOffline("pr-aide")
	stopOffline("pr-pm")
	var stopNote store.Decision
	for _, d := range tellsTo(e, "claude:pr-aide") {
		if strings.Contains(d.Answer, "停下，写完交接后下线") {
			stopNote = d
		}
	}
	if stopNote.AboutKey != "claude:pr-pm" || stopNote.AboutRole != "pm" {
		t.Fatalf("主调度停下的通知应排队给主助理，并记下说的是哪个身份：%+v", stopNote)
	}
	if !presenceHas(e, "claude:pr-plain", "下线", "主助理「") || !presenceHas(e, "claude:pr-plain", "下线", "（claude:pr-pm） 已停下并下线") {
		t.Fatalf("无身份在线会话应收到主调度、主助理停下后下线的只告知：%+v", presenceTells(e, "claude:pr-plain", "下线"))
	}
	pmPrompt := setRole("claude:pr-pm2", "pm")
	if !presenceHas(e, "claude:pr-plain", "上线", "（claude:pr-pm2） 接任主调度，已上线。原主调度「") {
		t.Fatalf("新主调度接任应通知在线会话，并写明原主调度已交接：%+v", presenceTells(e, "claude:pr-plain", "上线"))
	}
	if !strings.Contains(pmPrompt, "（没有在线的主助理）") {
		t.Fatalf("新主调度的当前在线应注明没有在线的主助理：%s", pmPrompt)
	}
	aidePrompt := setRole("claude:pr-aide2", "aide")
	if !strings.Contains(aidePrompt, "主调度 claude:pr-pm2") || strings.Contains(aidePrompt, "主调度 claude:pr-pm「") {
		t.Fatalf("新主助理的当前在线应列出新主调度、不列已交接的旧主调度：%s", aidePrompt)
	}
	// 新主助理下一轮开始时收到积压的停下通知，末尾注明主调度已由新主调度接任
	out := ev("pr-aide2", "UserPromptSubmit", map[string]any{"prompt": "继续", "prompt_id": "p1"})
	if !strings.Contains(out, "停下，写完交接后下线") || !strings.Contains(out, "主调度已由") || !strings.Contains(out, "（claude:pr-pm2） 接任") {
		t.Fatalf("积压的主调度停下通知应注明已由新主调度接任：%s", out)
	}
	// 新主助理接任前作为无身份在线会话已收到过「旧主调度上线」，上一任积压的同一条广播不再重复送
	if n := strings.Count(out, "（claude:pr-pm） 接任主调度"); n != 1 {
		t.Fatalf("同一条广播只应送达一次，实际 %d 次：%s", n, out)
	}

	// 4. 不再担任身份（ks role none）与会话结束：在线会话收到「下线」
	setRole("claude:pr-plain", "reviewer")
	setRole("claude:pr-plain", "none")
	if !presenceHas(e, "claude:pr-pm2", "下线", "（claude:pr-plain） 不再担任评审，现为无身份会话") {
		t.Fatalf("不再担任身份应通知在线会话：%+v", presenceTells(e, "claude:pr-pm2", "下线"))
	}
	ev("pr-pm2", "SessionEnd", nil)
	e.waitFor("新主调度结束", func() bool { return e.session("claude:pr-pm2").State == store.StateEnded })
	if !presenceHas(e, "claude:pr-aide2", "下线", "（claude:pr-pm2） 的会话已结束") {
		t.Fatalf("主调度会话结束应通知在线会话：%+v", presenceTells(e, "claude:pr-aide2", "下线"))
	}
}
