package hub

import (
	"testing"

	"github.com/NekroAI/kitestring/internal/store"
)

// R-416：默认名取工作目录名-工具（Windows 路径、太长、带冒号也稳定可用）；自报名字的校验。
func TestDefaultName(t *testing.T) {
	for _, c := range []struct {
		x    store.Session
		want string
	}{
		{store.Session{Cwd: "/Users/a/Projects/kitestring", Tool: "claude"}, "kitestring-claude"},
		{store.Session{Cwd: `D:\work\game\`, Tool: "codex"}, "game-codex"},
		{store.Session{Cwd: "", ProjectName: "kite", Tool: "dsh"}, "kite-dsh"},
		{store.Session{Cwd: "/x/a:b c", Tool: "claude"}, "a-b-c-claude"},
		{store.Session{Cwd: "/x/rollcall-squad", Tool: "claude"}, "rollcall-squad-claude"},
		{store.Session{Cwd: "/x/abcdefghijklmnopqrstuvwxyz", Tool: "claude"}, "abcdefghijklmnopqrstuvwx-claude"},
	} {
		if got := defaultName(c.x); got != c.want {
			t.Errorf("defaultName(%q) = %q，want %q", c.x.Cwd, got, c.want)
		}
	}
	for _, bad := range []string{"", "  ", "pm", "主助理", "参谋", "claude:x", "甲：乙", "一二三四五六七八九十一二三四五六七八九十一"} {
		if _, err := cleanName(bad); err == nil {
			t.Errorf("cleanName(%q) 应报错", bad)
		}
	}
	if n, err := cleanName("  美术  助手 "); err != nil || n != "美术 助手" {
		t.Errorf("名字应去掉多余空白：%q %v", n, err)
	}
	if _, err := cleanNote("一二三四五六七八九十一二三四五六七八九十一二三四五六七八九十一二三四五六七八九十一"); err == nil {
		t.Error("说明超过 40 字应报错")
	}
}
