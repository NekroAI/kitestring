package install

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NekroAI/kitestring/internal/profile"
)

// 临时 HOME + 假 claude 命令：只往 $CLAUDE_CONFIG_DIR/.claude.json（默认 $HOME/.claude.json）写注册，绝不碰真实配置。
func setup(t *testing.T) (home string) {
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	bin := t.TempDir()
	script := `#!/bin/sh
f="$HOME/.claude.json"
[ -n "$CLAUDE_CONFIG_DIR" ] && f="$CLAUDE_CONFIG_DIR/.claude.json"
mkdir -p "$(dirname "$f")"
if [ "$2" = "remove" ]; then rm -f "$f"; exit 0; fi
printf '{"mcpServers":{"kitestring":{"command":"%s"}}}' "$7" > "$f"
`
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	return home
}

func opts(dirs ...profile.Dir) (Options, *bytes.Buffer) {
	var out bytes.Buffer
	return Options{KS: "/opt/ks", Claude: true, ClaudeDirs: dirs, Out: &out}, &out
}

func hookCount(t *testing.T, path string) int {
	root, err := readJSON(path)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	hooks, _ := root["hooks"].(map[string]any)
	for _, g := range hooks {
		n += len(g.([]any))
	}
	return n
}

func TestInstallMultipleDirs(t *testing.T) {
	home := setup(t)
	x := filepath.Join(home, ".claude-x")
	o0, _ := opts()
	if err := Run(o0, false); err != nil {
		t.Fatal(err)
	}
	o, _ := opts(profile.Dir{Path: x, Launcher: "claude-x"})
	if err := Run(o, false); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{filepath.Join(home, ".claude"), x} {
		if hookCount(t, filepath.Join(d, "settings.json")) != len(ClaudeEvents) {
			t.Fatalf("%s hook 数量不对", d)
		}
		root, _ := readJSON(filepath.Join(d, "settings.json"))
		if b, _ := json.Marshal(root["permissions"]); !strings.Contains(string(b), "mcp__kitestring") {
			t.Fatalf("%s 缺权限白名单", d)
		}
		st := dirStatus("/opt/ks", home, d)
		if !st.MCP || st.MCPStale || st.Hooks != len(ClaudeEvents) || !st.Allow {
			t.Fatalf("%s 状态不对：%+v", d, st)
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".claude.json")); err != nil {
		t.Fatal("默认目录没注册 MCP")
	}
	// 不带参数重跑：记录里的目录一起更新，hook 不重复
	o, _ = opts()
	if err := Run(o, false); err != nil {
		t.Fatal(err)
	}
	if hookCount(t, filepath.Join(x, "settings.json")) != len(ClaudeEvents) || hookCount(t, filepath.Join(home, ".claude", "settings.json")) != len(ClaudeEvents) {
		t.Fatal("重跑后 hook 重复或丢失")
	}
	list, _ := profile.Load(home)
	if len(list) != 1 || list[0].Launcher != "claude-x" {
		t.Fatalf("记录不对：%+v", list)
	}
}

func TestInstallPicksUpShellEnvDir(t *testing.T) {
	home := setup(t)
	e := filepath.Join(home, ".claude-env")
	t.Setenv("CLAUDE_CONFIG_DIR", e)
	o, _ := opts()
	if err := Run(o, false); err != nil {
		t.Fatal(err)
	}
	if hookCount(t, filepath.Join(e, "settings.json")) != len(ClaudeEvents) {
		t.Fatal("当前 shell 的 CLAUDE_CONFIG_DIR 应一并接入")
	}
	if _, err := os.Stat(filepath.Join(home, ".claude.json")); err != nil {
		t.Fatal("默认目录的 MCP 应注册在 ~/.claude.json，不受 shell 变量影响")
	}
}

func TestUninstallOneThenAll(t *testing.T) {
	home := setup(t)
	x := filepath.Join(home, ".claude-x")
	o0, _ := opts()
	if err := Run(o0, false); err != nil {
		t.Fatal(err)
	}
	o, _ := opts(profile.Dir{Path: x})
	if err := Run(o, false); err != nil {
		t.Fatal(err)
	}
	def := filepath.Join(home, ".claude", "settings.json")
	// dry-run 不写文件，按目录列出
	o, out := opts(profile.Dir{Path: x})
	o.DryRun = true
	before, _ := os.ReadFile(filepath.Join(x, "settings.json"))
	if err := Run(o, true); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(x, "settings.json"))
	if !bytes.Equal(before, after) || !strings.Contains(out.String(), "claude-x") || !strings.Contains(out.String(), "将备份") {
		t.Fatalf("dry-run 不应写文件且应列出目录：%s", out.String())
	}
	// 只卸一个目录
	o, _ = opts(profile.Dir{Path: x})
	if err := Run(o, true); err != nil {
		t.Fatal(err)
	}
	if hookCount(t, filepath.Join(x, "settings.json")) != 0 {
		t.Fatal("指定目录应被清掉")
	}
	if hookCount(t, def) != len(ClaudeEvents) {
		t.Fatal("默认目录不应被动")
	}
	if list, _ := profile.Load(home); len(list) != 0 {
		t.Fatal("卸载后应从记录里去掉")
	}
	if m, _ := filepath.Glob(filepath.Join(x, "settings.json.ks-bak-*")); len(m) == 0 {
		t.Fatal("改动前应备份")
	}
	// 再装回 x，然后全卸
	o, _ = opts(profile.Dir{Path: x})
	_ = Run(o, false)
	o, _ = opts()
	if err := Run(o, true); err != nil {
		t.Fatal(err)
	}
	if hookCount(t, def) != 0 || hookCount(t, filepath.Join(x, "settings.json")) != 0 {
		t.Fatal("不带参数卸载应清掉全部")
	}
}

func TestStatusReportsMissing(t *testing.T) {
	home := setup(t)
	def := filepath.Join(home, ".claude")
	_ = os.MkdirAll(filepath.Join(def, "agents"), 0o755)
	_ = os.WriteFile(filepath.Join(def, "CLAUDE.md"), []byte("x"), 0o644)
	x := filepath.Join(home, ".claude-x")
	o, _ := opts(profile.Dir{Path: x})
	_ = Run(o, false)
	o, out := opts()
	o.Status = true
	if err := Run(o, false); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	if !strings.Contains(s, "claude-x") || !strings.Contains(s, "缺少 CLAUDE.md") || !strings.Contains(s, "缺少 agents") || strings.Contains(s, "缺少 skills") {
		t.Fatalf("状态输出不对：%s", s)
	}
	// ks 路径变了要提示
	o.KS = "/other/ks"
	out.Reset()
	_ = Run(o, false)
	if !strings.Contains(out.String(), "不一致") {
		t.Fatalf("应提示版本对不上：%s", out.String())
	}
}

func TestRelativeDirRejected(t *testing.T) {
	setup(t)
	o, _ := opts(profile.Dir{Path: "rel/dir"})
	if err := Run(o, false); err == nil {
		t.Fatal("相对路径应报错")
	}
}
