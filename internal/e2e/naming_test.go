package e2e

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/client"
)

// R-416：会话自报名字和说明（任一身份接入时 --name / --note，之后 ks session name 改），和模型标题分开存；
// 不写时有默认名「目录名-工具」，同项目没结束的会话重名加 -2；团队消息的发送方写成「名字」（身份 · 说明），
// ks team sessions 按名字列出；轻接入会话能点对点发消息、回复，--to 认名字。
func TestSessionNames(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	c := client.New(30 * time.Second)
	ctx := context.Background()
	prompt := func(sid, id string) {
		hook(t, "claude", map[string]any{"session_id": sid, "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "干活", "prompt_id": id})
	}
	call := func(method, path string, body any, out any) error { return c.Do(ctx, method, path, body, out) }

	prompt("nm-pm", "p0")
	prompt("nm-a", "p0")
	prompt("nm-b", "p0")
	base := filepath.Base(cwd) + "-claude"
	e.waitFor("默认名", func() bool {
		return e.session("claude:nm-pm").Name != "" && e.session("claude:nm-a").Name != "" && e.session("claude:nm-b").Name != ""
	})
	// 1. 默认名：目录名-工具；同项目重名依次加 -2、-3，之后不再变
	names := map[string]bool{}
	for _, k := range []string{"claude:nm-pm", "claude:nm-a", "claude:nm-b"} {
		n := e.session(k).Name
		if !strings.HasPrefix(n, base) {
			t.Fatalf("默认名应是 %s 开头：%s", base, n)
		}
		names[n] = true
	}
	if !names[base] || !names[base+"-2"] || !names[base+"-3"] {
		t.Fatalf("重名应加后缀：%v", names)
	}
	before := e.session("claude:nm-a").Name
	prompt("nm-a", "p1")
	if got := e.session("claude:nm-a").Name; got != before {
		t.Fatalf("默认名不应随每轮变：%s → %s", before, got)
	}

	// 2. ks role 带 --name / --note：主调度起名
	var role struct {
		Prompt string `json:"prompt"`
	}
	if err := call("POST", "/agent/sessions/role", map[string]any{"session_key": "claude:nm-pm", "role": "pm", "name": "总调度台", "note": "排期和合并"}, &role); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(role.Prompt, "本会话名「总调度台」") {
		t.Fatalf("身份提示词应写明本会话名：%s", role.Prompt[:200])
	}
	// 轻接入起名，和别的会话重名时自动加后缀；名字和说明的校验
	if err := call("POST", "/agent/sessions/role", map[string]any{"session_key": "claude:nm-a", "role": "lite", "name": "美术助手", "note": "推 GLB 素材"}, &role); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []map[string]any{
		{"name": "pm"}, {"name": "a:b"}, {"name": strings.Repeat("长", 21)}, {"note": strings.Repeat("长", 41)},
	} {
		bad["session_key"] = "claude:nm-b"
		if err := call("POST", "/agent/sessions/name", bad, nil); err == nil {
			t.Fatalf("应拒绝 %v", bad)
		}
	}
	var named struct {
		Name     string `json:"name"`
		Suffixed bool   `json:"suffixed"`
	}
	if err := call("POST", "/agent/sessions/name", map[string]any{"session_key": "claude:nm-b", "name": "美术助手"}, &named); err != nil {
		t.Fatal(err)
	}
	if named.Name != "美术助手-2" || !named.Suffixed {
		t.Fatalf("重名应自动加 -2：%+v", named)
	}
	if err := call("POST", "/agent/sessions/role", map[string]any{"session_key": "claude:nm-b", "role": "lite"}, nil); err != nil {
		t.Fatal(err)
	}

	// 3. 模型标题不覆盖名字：标题是「此刻在做什么」
	if _, err := e.store.DB().Exec(`UPDATE sessions SET title = '整理贴图' WHERE key = 'claude:nm-a'`); err != nil {
		t.Fatal(err)
	}
	if x := e.session("claude:nm-a"); x.Name != "美术助手" || x.Note != "推 GLB 素材" || x.Title != "整理贴图" {
		t.Fatalf("名字、说明、标题应分开存：%+v", x)
	}

	// 4. ks team sessions：「名字」（身份 · 说明），此刻在做什么在后面
	var roster struct {
		Text string `json:"text"`
	}
	if err := call("GET", "/agent/team/sessions?session_key=claude:nm-pm", nil, &roster); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"「总调度台」（主调度 · 排期和合并）claude:nm-pm（你）", "「美术助手」（轻接入 · 推 GLB 素材）claude:nm-a", "此刻：整理贴图", "「美术助手-2」（轻接入）"} {
		if !strings.Contains(roster.Text, want) {
			t.Fatalf("会话列表缺 %q：\n%s", want, roster.Text)
		}
	}

	// 5. 轻接入按名字给主调度发消息：发送方写成「名字」（身份 · 说明）
	w := parkWait(t, e, c, "claude:nm-pm")
	var sent struct {
		Code string `json:"code"`
		To   string `json:"to"`
	}
	if err := call("POST", "/agent/team/tell", map[string]any{"session_key": "claude:nm-a", "to": "总调度台", "text": "GLB 已推，请看 A-3"}, &sent); err != nil {
		t.Fatal(err)
	}
	if sent.To != "claude:nm-pm" {
		t.Fatalf("--to 按名字应送到主调度：%+v", sent)
	}
	text := woke(t, w, "GLB 已推")
	if !strings.Contains(text, "「美术助手」（轻接入 · 推 GLB 素材）claude:nm-a 留言") {
		t.Fatalf("发送方应写成「名字」（身份 · 说明）：%s", text)
	}

	// 6. 主调度回复：送回轻接入；轻接入再按名字发给另一个轻接入（不分大小写的名字也认），并能回复
	var orig struct {
		Code string `json:"code"`
	}
	if err := call("POST", "/agent/team/tell", map[string]any{"session_key": "claude:nm-pm", "reply_to": sent.Code, "text": "收到"}, &orig); err != nil {
		t.Fatal(err)
	}
	if err := call("POST", "/agent/team/tell", map[string]any{"session_key": "claude:nm-a", "reply_to": orig.Code, "text": "好的"}, nil); err != nil {
		t.Fatalf("轻接入应能回复：%v", err)
	}
	var toB struct {
		To string `json:"to"`
	}
	if err := call("POST", "/agent/team/tell", map[string]any{"session_key": "claude:nm-a", "to": "美术助手-2", "text": "你做角色，我做场景"}, &toB); err != nil || toB.To != "claude:nm-b" {
		t.Fatalf("轻接入之间按名字发：%+v %v", toB, err)
	}
	// 名字找不到时报错，写明可填什么
	if err := call("POST", "/agent/team/tell", map[string]any{"session_key": "claude:nm-a", "to": "不存在的人", "text": "在吗"}, nil); err == nil || !strings.Contains(err.Error(), "会话名") {
		t.Fatalf("名字找不到应报错：%v", err)
	}
	// 自己的名字：不用发给自己
	if err := call("POST", "/agent/team/tell", map[string]any{"session_key": "claude:nm-a", "to": "美术助手", "text": "自言自语"}, nil); err == nil {
		t.Fatal("发给自己应报错")
	}

	// 7. ks session name 只改说明：名字不变
	if err := call("POST", "/agent/sessions/name", map[string]any{"session_key": "claude:nm-a", "note": "换成做 UI 图标"}, nil); err != nil {
		t.Fatal(err)
	}
	if x := e.session("claude:nm-a"); x.Name != "美术助手" || x.Note != "换成做 UI 图标" {
		t.Fatalf("只改说明：%+v", x)
	}
}
