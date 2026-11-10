package install

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/NekroAI/kitestring/internal/profile"
)

// status 逐个 Claude 配置目录列出 hook、MCP、权限是否到位，版本是否对得上，
// 再提示这个目录相对 ~/.claude 缺了哪些个人配置（只提示，不自动建软链接）。
func status(o Options, home string) error {
	saved, err := profile.Load(home)
	if err != nil {
		return err
	}
	dirs := []string{profile.DefaultDir(home)}
	for _, d := range saved {
		dirs = append(dirs, d.Path)
	}
	if e := os.Getenv("CLAUDE_CONFIG_DIR"); e != "" {
		if p, err := profile.Expand(home, e); err == nil && !profile.IsDefault(home, p) {
			dirs = append(dirs, p)
		}
	}
	seen := map[string]bool{}
	for _, d := range dirs {
		if seen[d] {
			continue
		}
		seen[d] = true
		st := dirStatus(o.KS, home, d)
		fmt.Fprintf(o.Out, "\n[Claude 配置 %s]\n", configLabel(home, d))
		mark := func(ok bool) string {
			if ok {
				return "✓"
			}
			return "✗"
		}
		fmt.Fprintf(o.Out, "  %s hook：%d/%d 个事件", mark(st.Hooks == len(ClaudeEvents)), st.Hooks, len(ClaudeEvents))
		if st.Hooks > 0 && st.HooksStale > 0 {
			fmt.Fprintf(o.Out, "（其中 %d 个指向的 ks 路径与当前不一致，重跑 ks install 更新）", st.HooksStale)
		}
		fmt.Fprintln(o.Out)
		fmt.Fprintf(o.Out, "  %s 权限：permissions.allow 含 mcp__%s\n", mark(st.Allow), mcpName)
		fmt.Fprintf(o.Out, "  %s MCP：%s", mark(st.MCP), profile.ConfigFile(home, d))
		if st.MCP && st.MCPStale {
			fmt.Fprint(o.Out, "（注册的 ks 路径与当前不一致，重跑 ks install 更新）")
		}
		fmt.Fprintln(o.Out)
		if !profile.IsDefault(home, d) {
			missing := missingFromDefault(home, d)
			if len(missing) == 0 {
				fmt.Fprintln(o.Out, "  相对 ~/.claude 没有缺少用户级 CLAUDE.md / agents / skills")
			}
			for _, m := range missing {
				fmt.Fprintf(o.Out, "  ! 缺少 %s（~/.claude 里有；想共用可自行%s）\n", m, linkHint(profile.DefaultDir(home), d, m))
			}
		}
	}
	return nil
}

// DirStatus 是一个配置目录的接入状态。
type DirStatus struct {
	Hooks      int  // 到位的 hook 事件数
	HooksStale int  // 其中 ks 路径与当前不一致的数
	Allow      bool // permissions.allow 含 mcp__kitestring
	MCP        bool // .claude.json 注册了 kitestring
	MCPStale   bool // 注册的命令与当前 ks 不一致
}

func dirStatus(ks, home, dir string) DirStatus {
	var st DirStatus
	root, _ := readJSON(filepath.Join(dir, "settings.json"))
	hooks, _ := root["hooks"].(map[string]any)
	for _, ev := range ClaudeEvents {
		list, _ := hooks[ev].([]any)
		found, fresh := false, false
		for _, g := range list {
			gm, _ := g.(map[string]any)
			inner, _ := gm["hooks"].([]any)
			for _, hk := range inner {
				hm, _ := hk.(map[string]any)
				if c := hookLine(hm); isOurs(c, "claude") {
					found = true
					if c == hookLine(hookEntry(ks, "claude", ev)) {
						fresh = true
					}
				}
			}
		}
		if found {
			st.Hooks++
			if !fresh {
				st.HooksStale++
			}
		}
	}
	perms, _ := root["permissions"].(map[string]any)
	allow, _ := perms["allow"].([]any)
	for _, a := range allow {
		if a == "mcp__"+mcpName {
			st.Allow = true
		}
	}
	cfg, _ := readJSON(profile.ConfigFile(home, dir))
	servers, _ := cfg["mcpServers"].(map[string]any)
	if sv, ok := servers[mcpName].(map[string]any); ok {
		st.MCP = true
		st.MCPStale = sv["command"] != ks
	}
	return st
}

// linkHint 是把 ~/.claude 里的 name 链接到 dir 的命令。Windows 上目录用 mklink /J（不需要管理员），
// 文件的符号链接要开发者模式或管理员。
func linkHint(def, dir, name string) string {
	if runtime.GOOS == "windows" {
		if name == "CLAUDE.md" {
			return fmt.Sprintf("建链接（需开发者模式或管理员）：cmd /c mklink %s %s", winQuote(filepath.Join(dir, name)), winQuote(filepath.Join(def, name)))
		}
		return fmt.Sprintf("建链接：cmd /c mklink /J %s %s", winQuote(filepath.Join(dir, name)), winQuote(filepath.Join(def, name)))
	}
	return fmt.Sprintf("软链接：ln -s ~/.claude/%s %s", name, filepath.Join(dir, name))
}

// missingFromDefault 列出 ~/.claude 里有、这个目录里没有的用户级 CLAUDE.md / agents / skills。
func missingFromDefault(home, dir string) []string {
	var out []string
	for _, n := range []string{"CLAUDE.md", "agents", "skills"} {
		if _, err := os.Stat(filepath.Join(profile.DefaultDir(home), n)); err != nil {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, n)); err != nil {
			out = append(out, n)
		}
	}
	return out
}
