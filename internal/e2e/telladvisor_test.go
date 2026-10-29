package e2e

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/client"
)

// R-248：团队消息能发给顾问（认中文名和别称）；多个顾问时默认最近活跃的、不含自己，可用 to_session 点名；
// 没有在线顾问时报错退回给发送方。
func TestTellToAdvisor(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	c := client.New(30 * time.Second)
	ctx := context.Background()
	for _, sid := range []string{"pm", "aide", "adv1", "adv2"} {
		hook(t, "claude", map[string]any{"session_id": sid, "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "上线", "prompt_id": "p0"})
		e.waitFor("会话入库", func() bool { return e.session("claude:"+sid).Key != "" })
	}
	setRole := func(key, role string) {
		t.Helper()
		if err := c.Do(ctx, "POST", "/agent/sessions/role", map[string]any{"session_key": key, "role": role}, nil); err != nil {
			t.Fatal(err)
		}
	}
	setRole("claude:pm", "pm")
	setRole("claude:aide", "aide")
	tell := func(body map[string]any) (tellRes, error) {
		var out tellRes
		err := c.Do(ctx, "POST", "/agent/team/tell", body, &out)
		return out, err
	}

	// 没有顾问在线：报错退回，写明原因
	if _, err := tell(map[string]any{"session_key": "claude:aide", "text": "交接 R-247", "to": "advisor"}); err == nil || !strings.Contains(err.Error(), "没有在线的顾问") {
		t.Fatalf("没有顾问时应报清楚的错：%v", err)
	}

	setRole("claude:adv1", "advisor")
	out, err := tell(map[string]any{"session_key": "claude:aide", "text": "交接 R-247", "to": "顾问"})
	if err != nil || !out.Delivered || out.To != "claude:adv1" || out.Role != "advisor" {
		t.Fatalf("应送给顾问：%+v %v", out, err)
	}

	// 两个顾问：默认最近活跃的（adv2 刚有动静）；点名可指定 adv1
	setRole("claude:adv2", "adviser")
	hook(t, "claude", map[string]any{"session_id": "adv2", "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "再来", "prompt_id": "p1"})
	out, err = tell(map[string]any{"session_key": "claude:aide", "text": "看下方案", "to": "Adv"})
	if err != nil || out.To != "claude:adv2" {
		t.Fatalf("多个顾问应发给最近活跃的：%+v %v", out, err)
	}
	out, err = tell(map[string]any{"session_key": "claude:aide", "text": "点名", "to": "advisor", "to_session": "claude:adv1"})
	if err != nil || out.To != "claude:adv1" {
		t.Fatalf("to_session 应点名：%+v %v", out, err)
	}
	// 顾问之间互发：不会选回自己
	out, err = tell(map[string]any{"session_key": "claude:adv2", "text": "你怎么看", "to": "advisor"})
	if err != nil || out.To != "claude:adv1" {
		t.Fatalf("顾问发给顾问不应选中自己：%+v %v", out, err)
	}
	// to_session 身份对不上 / 不是本项目有身份的会话：拒绝
	if _, err := tell(map[string]any{"session_key": "claude:aide", "text": "x", "to": "reviewer", "to_session": "claude:adv1"}); err == nil || !strings.Contains(err.Error(), "身份是顾问") {
		t.Fatalf("身份对不上应拒绝：%v", err)
	}
	if _, err := tell(map[string]any{"session_key": "claude:aide", "text": "x", "to_session": "claude:nobody"}); err == nil {
		t.Fatal("不存在的会话应拒绝")
	}
}

// R-248：自动识别不到当前会话时，CLI 按 --session 自报发送方（session_claimed）：hub 要求它是没结束、属于项目的会话，并在返回里注明；
// 已结束的、不存在的会话被拒。
func TestTellClaimedSession(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	c := client.New(30 * time.Second)
	ctx := context.Background()
	for _, sid := range []string{"pm", "cx", "gone"} {
		hook(t, "claude", map[string]any{"session_id": sid, "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "上线", "prompt_id": "p0"})
		e.waitFor("会话入库", func() bool { return e.session("claude:"+sid).Key != "" })
	}
	if err := c.Do(ctx, "POST", "/agent/sessions/role", map[string]any{"session_key": "claude:pm", "role": "pm"}, nil); err != nil {
		t.Fatal(err)
	}
	hook(t, "claude", map[string]any{"session_id": "gone", "cwd": cwd, "hook_event_name": "SessionEnd"})
	e.waitFor("会话结束", func() bool { return e.session("claude:gone").State == "ended" })

	tell := func(key string) (tellRes, error) {
		var out tellRes
		err := c.Do(ctx, "POST", "/agent/team/tell", map[string]any{"session_key": key, "text": "进展", "session_claimed": true}, &out)
		return out, err
	}
	out, err := tell("claude:cx")
	if err != nil || !out.Delivered || !strings.Contains(out.Message, "按 --session claude:cx 认定发送方") {
		t.Fatalf("识别不到时合法 --session 应可发并注明：%+v %v", out, err)
	}
	if _, err := tell("claude:gone"); err == nil || !strings.Contains(err.Error(), "没结束") {
		t.Fatalf("已结束的会话应被拒：%v", err)
	}
	if _, err := tell("claude:nobody"); err == nil {
		t.Fatalf("不存在的会话应被拒")
	}
}
