package hub

import (
	"strings"
	"testing"
	"time"
)

// R-320：修改授权申请的改动说明与命令增删。
func TestAmendChanges(t *testing.T) {
	at := time.Date(2026, 11, 9, 12, 0, 0, 0, time.Local)
	before := amendScope{Projects: []string{"a"}, Names: []string{"甲"}, Days: secretKeepExpiry, Expires: &at, Allowed: []string{"sh -c ./x --prod", "w"}}
	if got := amendChanges(before, before); len(got) != 0 {
		t.Fatalf("没改不应有说明：%v", got)
	}
	after := before
	after.Projects, after.Names = []string{"a", "b"}, []string{"甲", "乙"}
	after.Days, after.Allowed, after.Disk = 30, []string{"w", "sh -c *"}, true
	got := strings.Join(amendChanges(before, after), "；")
	for _, w := range []string{"项目：甲 → 甲、乙", "有效期：至 11-09 12:00 → 30 天", "允许的命令：加上「sh -c *」，去掉「sh -c ./x --prod」", "写入磁盘：不允许 → 允许"} {
		if !strings.Contains(got, w) {
			t.Fatalf("改动说明应含 %q：%s", w, got)
		}
	}
	after = before
	after.Allowed = []string{}
	if got := amendChanges(before, after); len(got) != 1 || got[0] != "允许的命令：改为不限命令" {
		t.Fatalf("改成不限命令：%v", got)
	}
	if got := amendChanges(amendScope{Days: secretKeepExpiry}, amendScope{Days: secretKeepExpiry, Allowed: []string{"w"}}); got[0] != "允许的命令：不限命令 → 只允许「w」" {
		t.Fatalf("从不限命令收窄：%v", got)
	}
}

// 说明里教会话的写法：一个参数里含空格时用 * 代替空格（sh -c 的整段脚本）。
func TestAllowedScriptArg(t *testing.T) {
	argv := []string{"/bin/sh", "-c", `printf %s "$GITHUB_TOKEN" | npx wrangler secret put GITHUB_TOKEN --name nekro-ai-portal`}
	prog := program{path: "/bin/sh", viaPath: true}
	if !commandAllowed([]string{"sh -c printf*wrangler*secret*put*"}, argv, prog) {
		t.Fatal("参数内的 * 应匹配含空格的整段脚本")
	}
	if commandAllowed([]string{`sh -c printf %s "$GITHUB_TOKEN" | npx wrangler secret put GITHUB_TOKEN --name nekro-ai-portal`}, argv, prog) {
		t.Fatal("拆成多段的写法匹配不上（R-320 反馈里的情形）")
	}
}

func TestAmendAllowed(t *testing.T) {
	cur := []string{"sh -c ./x --prod", "w"}
	got, err := amendAllowed(cur, secretAmendIn{RemoveAllowed: []string{"sh  -c ./x --prod"}, AddAllowed: []string{"sh -c *", "w"}})
	if err != nil || strings.Join(got, "|") != "w|sh -c *" {
		t.Fatalf("增删：%v %v", got, err)
	}
	if strings.Join(cur, "|") != "sh -c ./x --prod|w" {
		t.Fatal("不应改动原来的列表")
	}
	if got, err := amendAllowed(cur, secretAmendIn{Allowed: []string{"a", "a "}}); err != nil || strings.Join(got, "|") != "a" {
		t.Fatalf("整份替换：%v %v", got, err)
	}
	if got, err := amendAllowed(cur, secretAmendIn{AnyCommand: true}); err != nil || len(got) != 0 {
		t.Fatalf("不限命令：%v %v", got, err)
	}
	if _, err := amendAllowed(cur, secretAmendIn{RemoveAllowed: []string{"nope"}}); err == nil {
		t.Fatal("去掉不存在的命令应报错")
	}
	if _, err := amendAllowed(cur, secretAmendIn{RemoveAllowed: cur}); err == nil {
		t.Fatal("全去掉 = 不限命令，要明确写")
	}
}
