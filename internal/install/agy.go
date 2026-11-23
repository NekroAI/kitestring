package install

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/NekroAI/kitestring/internal/protocol"
	"github.com/NekroAI/kitestring/internal/version"
)

const agyPluginName = "kitestring"

var (
	// agyFlatEvents 在 Antigravity hooks.json 里使用扁平 handler 数组。
	agyFlatEvents = []string{"SessionStart", "PreInvocation", "Stop"}
	// agyMatchedEvents 在 Antigravity hooks.json 里使用 matcher 分组。
	agyMatchedEvents = []string{"PreToolUse", "PostToolUse"}
	agySemverRe      = regexp.MustCompile(`^\d+\.\d+\.\d+([.-][0-9A-Za-z.-]+)?$`)
)

func agyPluginDir(home string) string {
	return filepath.Join(home, ".gemini", "config", "plugins", agyPluginName)
}

func agyCLIDir(home string) string {
	return filepath.Join(home, ".gemini", "antigravity-cli")
}

func agySettingsPath(home string) string {
	return filepath.Join(agyCLIDir(home), "settings.json")
}

func agyDetected(home string) bool {
	for _, dir := range []string{agyCLIDir(home), filepath.Join(home, ".gemini", "antigravity")} {
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			return true
		}
	}
	return false
}

func agyInstalled(home string) bool {
	if _, err := os.Stat(filepath.Join(agyPluginDir(home), "plugin.json")); err == nil {
		return true
	}
	return false
}

func agyPluginVersion() string {
	v := strings.TrimPrefix(strings.TrimSpace(version.Version), "v")
	if agySemverRe.MatchString(v) {
		return v
	}
	return "0.1.0"
}

func agyCommandLine(ks, event string) string {
	if hookGOOS == "windows" {
		return fmt.Sprintf("%s hook agy %s", winQuote(ks), event)
	}
	return hookCommand(ks, protocol.ToolAgy, event)
}

func agyHooksPayload(ks string) map[string]any {
	pluginHooks := map[string]any{}
	for _, ev := range agyFlatEvents {
		pluginHooks[ev] = []any{
			map[string]any{
				"type":    "command",
				"command": agyCommandLine(ks, ev),
				"timeout": 5,
			},
		}
	}
	for _, ev := range agyMatchedEvents {
		pluginHooks[ev] = []any{
			map[string]any{
				"matcher": "*",
				"hooks": []any{
					map[string]any{
						"type":    "command",
						"command": agyCommandLine(ks, ev),
						"timeout": 5,
					},
				},
			},
		}
	}
	return map[string]any{
		agyPluginName: pluginHooks,
	}
}

func agyPluginManifest() map[string]any {
	return map[string]any{
		"name":        agyPluginName,
		"version":     agyPluginVersion(),
		"description": "Kitestring (纸鸢) multi-agent cockpit integration for Google Antigravity",
		"logo":        "assets/logo.svg",
	}
}

const agyPluginLogoSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="#D97757" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M12 2L2 9l10 13L22 9 12 2z"/><path d="M12 2v20"/><path d="M2 9h20"/></svg>` + "\n"

func agyMCPConfig(ks string) map[string]any {
	return map[string]any{
		"mcpServers": map[string]any{
			agyPluginName: map[string]any{
				"command": ks,
				"args":    []string{"mcp", "--client", protocol.ToolAgy},
			},
		},
	}
}

const agyPluginRulesMD = `# Kitestring（纸鸢）Antigravity 协同规则

- 通过 Kitestring MCP 工具（kitestring/*）与本机团队协作；MCP 工具运行在宿主机，可直接调用。
- 在终端运行 ks role / ks wait / ks secret run / ks team 等需要连接本机 daemon 的 CLI 命令时，若启用了终端沙箱，请附带 BypassSandbox=true。
- 担任角色后结束本轮前若需要挂起待命，请使用 run_command（WaitMsBeforeAsync=500, BypassSandbox=true）在后台运行 ks wait，然后结束本轮；唤醒时系统会自动继续。
`

const agyMCPServerID = agyPluginName + "_" + agyPluginName

func agyRequiredAllowEntries() []string {
	entries := make([]string, 0, 1+len(protocol.KitestringMCPTools))
	entries = append(entries, "command(ks)")
	for _, tool := range protocol.KitestringMCPTools {
		entries = append(entries, "mcp("+agyMCPServerID+"/"+tool+")")
	}
	return entries
}

func agyInstall(o Options, home string, remove bool) error {
	pDir := agyPluginDir(home)
	settingsPath := agySettingsPath(home)
	if remove {
		if o.DryRun {
			if agyInstalled(home) {
				fmt.Fprintf(o.Out, "  将移除目录 %s\n", pDir)
			}
		} else if err := os.RemoveAll(pDir); err != nil {
			return err
		}
		if err := agyUpdateSettings(o, settingsPath, true); err != nil {
			return err
		}
		if !o.DryRun {
			fmt.Fprintf(o.Out, "✓ 已移除 Antigravity 插件（%s）\n", pDir)
		}
		return nil
	}

	files := []struct {
		rel  string
		data []byte
		perm os.FileMode
	}{
		{"plugin.json", marshal(agyPluginManifest()), 0o644},
		{"hooks.json", marshal(agyHooksPayload(o.KS)), 0o644},
		{"mcp_config.json", marshal(agyMCPConfig(o.KS)), 0o644},
		{filepath.Join("rules", "AGENTS.md"), []byte(agyPluginRulesMD), 0o644},
		{filepath.Join("assets", "logo.svg"), []byte(agyPluginLogoSVG), 0o644},
	}
	for _, f := range files {
		if err := writeFile(o, filepath.Join(pDir, f.rel), f.data, f.perm); err != nil {
			return err
		}
	}
	if err := agyUpdateSettings(o, settingsPath, false); err != nil {
		return err
	}
	if !o.DryRun {
		fmt.Fprintf(o.Out, "✓ 已写入 Antigravity 插件（%s）与权限配置（%s）\n", pDir, settingsPath)
	}
	return nil
}

func agyUpdateSettings(o Options, settingsPath string, remove bool) error {
	if remove {
		if _, err := os.Stat(settingsPath); os.IsNotExist(err) {
			return nil
		}
	}
	root, err := readJSON(settingsPath)
	if err != nil {
		return err
	}
	perms, _ := root["permissions"].(map[string]any)
	if perms == nil {
		if remove {
			return nil
		}
		perms = map[string]any{}
	}
	rawAllow, _ := perms["allow"].([]any)
	kept := make([]any, 0, len(rawAllow))
	seen := map[string]bool{}
	for _, item := range rawAllow {
		s, ok := item.(string)
		if !ok {
			kept = append(kept, item)
			continue
		}
		if s == "command(ks)" || strings.HasPrefix(s, "mcp("+agyMCPServerID+"/") {
			if remove {
				continue
			}
		}
		// 迁移/清理旧版未带插件命名空间的 mcp(kitestring/*) 条目
		if strings.HasPrefix(s, "mcp(kitestring/") {
			continue
		}
		if !seen[s] {
			seen[s] = true
			kept = append(kept, s)
		}
	}
	if !remove {
		for _, want := range agyRequiredAllowEntries() {
			if !seen[want] {
				seen[want] = true
				kept = append(kept, want)
			}
		}
	}
	if len(kept) == 0 {
		delete(perms, "allow")
	} else {
		perms["allow"] = kept
	}
	if len(perms) == 0 {
		delete(root, "permissions")
	} else {
		root["permissions"] = perms
	}
	if err := backup(o, settingsPath); err != nil {
		return err
	}
	return writeFile(o, settingsPath, marshal(root), 0o600)
}

type agyDirStatus struct {
	Plugin bool
	Hooks  int
	MCP    bool
	Rules  bool
	Allow  int
	Stale  bool
}

func agyStatus(ks, home string) agyDirStatus {
	var st agyDirStatus
	pDir := agyPluginDir(home)
	if _, err := os.Stat(filepath.Join(pDir, "plugin.json")); err == nil {
		st.Plugin = true
	}
	if _, err := os.Stat(filepath.Join(pDir, "rules", "AGENTS.md")); err == nil {
		st.Rules = true
	}
	if hooksRoot, err := readJSON(filepath.Join(pDir, "hooks.json")); err == nil {
		if ksHooks, ok := hooksRoot[agyPluginName].(map[string]any); ok {
			for _, ev := range agyFlatEvents {
				if list, ok := ksHooks[ev].([]any); ok && len(list) > 0 {
					st.Hooks++
					if hm, ok := list[0].(map[string]any); ok {
						if cmd, _ := hm["command"].(string); cmd != agyCommandLine(ks, ev) {
							st.Stale = true
						}
					}
				}
			}
			for _, ev := range agyMatchedEvents {
				if groups, ok := ksHooks[ev].([]any); ok && len(groups) > 0 {
					st.Hooks++
					if gm, ok := groups[0].(map[string]any); ok {
						if inner, ok := gm["hooks"].([]any); ok && len(inner) > 0 {
							if hm, ok := inner[0].(map[string]any); ok {
								if cmd, _ := hm["command"].(string); cmd != agyCommandLine(ks, ev) {
									st.Stale = true
								}
							}
						}
					}
				}
			}
		}
	}
	if mcpRoot, err := readJSON(filepath.Join(pDir, "mcp_config.json")); err == nil {
		if servers, ok := mcpRoot["mcpServers"].(map[string]any); ok {
			if cfg, ok := servers[agyPluginName].(map[string]any); ok {
				st.MCP = true
				if cmd, _ := cfg["command"].(string); cmd != ks {
					st.Stale = true
				}
			}
		}
	}
	if settingsRoot, err := readJSON(agySettingsPath(home)); err == nil {
		if perms, ok := settingsRoot["permissions"].(map[string]any); ok {
			if allow, ok := perms["allow"].([]any); ok {
				required := agyRequiredAllowEntries()
				for _, item := range allow {
					if s, ok := item.(string); ok && slices.Contains(required, s) {
						st.Allow++
					}
				}
			}
		}
	}
	return st
}
