// Package install 负责把 ks 接入本机的 Claude Code 与 Codex（hook + MCP），并注册常驻服务。
// 所有改动前先备份；--dry-run 只打印将要做的事（D21）。
package install

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// ClaudeEvents 是接入 Claude Code 的 hook 事件（见 hook-matrix.md）。
var ClaudeEvents = []string{"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "PermissionRequest", "Notification", "Stop", "SubagentStart", "SubagentStop", "PreCompact", "SessionEnd"}

// CodexEvents 是接入 Codex 的 hook 事件。
var CodexEvents = []string{"SessionStart", "SessionEnd", "UserPromptSubmit", "PreToolUse", "PermissionRequest", "PostToolUse", "Stop", "Interrupt", "SubagentStart", "SubagentStop", "PreCompact", "PostCompact"}

// MCPTools 是 ks mcp 提供的工具，Codex 需要逐个配置免审批。
var MCPTools = []string{"req_list", "req_get", "req_create", "req_update_status", "evidence_attach", "decision_raise", "session_link_req"}

const (
	mcpName     = "kitestring"
	tomlBegin   = "# >>> kitestring (由 ks install 管理，请勿手改) >>>"
	tomlEnd     = "# <<< kitestring <<<"
	launchLabel = "ai.nekro.kitestring"
)

// Options 控制安装范围。
type Options struct {
	KS      string // ks 可执行文件绝对路径
	Claude  bool
	Codex   bool
	Daemon  bool // 注册 daemon 常驻服务
	Hub     bool // 注册 hub 常驻服务
	HubAddr string
	DryRun  bool
	Out     io.Writer
}

// Run 执行安装或卸载。
func Run(o Options, uninstall bool) error {
	if o.Out == nil {
		o.Out = os.Stdout
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	var errs []error
	step := func(name string, fn func() error) {
		if err := fn(); err != nil {
			errs = append(errs, fmt.Errorf("%s：%w", name, err))
			fmt.Fprintf(o.Out, "✗ %s：%v\n", name, err)
		}
	}
	if o.Claude {
		step("Claude hooks", func() error { return claudeHooks(o, home, uninstall) })
		step("Claude MCP", func() error { return claudeMCP(o, uninstall) })
	}
	if o.Codex {
		step("Codex hooks", func() error { return codexHooks(o, home, uninstall) })
		step("Codex MCP", func() error { return codexMCP(o, home, uninstall) })
	}
	if o.Daemon {
		step("daemon 服务", func() error { return service(o, home, "daemon", []string{o.KS, "daemon"}, uninstall) })
	}
	if o.Hub {
		step("hub 服务", func() error {
			return service(o, home, "hub", []string{o.KS, "hub", "serve", "--addr", o.HubAddr}, uninstall)
		})
	}
	if o.Codex && !uninstall && !o.DryRun {
		fmt.Fprintln(o.Out, "\n下一步（Codex 安全要求，需要你亲自操作）：打开 codex，输入 /hooks，把 ks hook codex ... 的各条 hook 设为信任。")
	}
	return errors.Join(errs...)
}

// backup 在修改前复制一份带时间戳的备份。
func backup(o Options, path string) error {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	dst := fmt.Sprintf("%s.ks-bak-%s", path, time.Now().Format("20060102-150405"))
	if o.DryRun {
		fmt.Fprintf(o.Out, "  将备份 %s → %s\n", path, dst)
		return nil
	}
	fmt.Fprintf(o.Out, "  已备份 %s → %s\n", path, dst)
	return os.WriteFile(dst, b, 0o600)
}

func writeFile(o Options, path string, data []byte, perm os.FileMode) error {
	if o.DryRun {
		fmt.Fprintf(o.Out, "  将写入 %s（%d 字节）\n", path, len(data))
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, perm)
}

func hookCommand(ks, tool, event string) string {
	return fmt.Sprintf("%s hook %s %s", shellQuote(ks), tool, event)
}

func shellQuote(s string) string {
	if regexp.MustCompile(`^[A-Za-z0-9_./-]+$`).MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// isOurs 判断一条 hook 命令是否由 ks 写入。
func isOurs(cmd, tool string) bool {
	return strings.Contains(cmd, " hook "+tool+" ") && strings.Contains(cmd, "ks")
}

// mergeHooks 在 Claude / Codex 共用的 hooks JSON 结构中替换 ks 的条目，保留用户的其他 hook。
func mergeHooks(root map[string]any, ks, tool string, events []string, remove bool) {
	hooks, _ := root["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	for ev, groups := range hooks {
		list, _ := groups.([]any)
		var kept []any
		for _, g := range list {
			gm, _ := g.(map[string]any)
			inner, _ := gm["hooks"].([]any)
			var keptInner []any
			for _, hk := range inner {
				hm, _ := hk.(map[string]any)
				if c, _ := hm["command"].(string); isOurs(c, tool) {
					continue
				}
				keptInner = append(keptInner, hk)
			}
			if len(keptInner) > 0 {
				gm["hooks"] = keptInner
				kept = append(kept, gm)
			}
		}
		if len(kept) > 0 {
			hooks[ev] = kept
		} else {
			delete(hooks, ev)
		}
	}
	if !remove {
		for _, ev := range events {
			list, _ := hooks[ev].([]any)
			entry := map[string]any{"hooks": []any{map[string]any{"type": "command", "command": hookCommand(ks, tool, ev), "timeout": 5}}}
			hooks[ev] = append(list, entry)
		}
	}
	if len(hooks) == 0 {
		delete(root, "hooks")
	} else {
		root["hooks"] = hooks
	}
}

func readJSON(path string) (map[string]any, error) {
	root := map[string]any{}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return root, nil
	}
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return root, nil
	}
	if err := json.Unmarshal(b, &root); err != nil {
		return nil, fmt.Errorf("解析 %s 失败：%w", path, err)
	}
	return root, nil
}

func marshal(v any) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
	return buf.Bytes()
}

func claudeHooks(o Options, home string, remove bool) error {
	path := filepath.Join(home, ".claude", "settings.json")
	root, err := readJSON(path)
	if err != nil {
		return err
	}
	mergeHooks(root, o.KS, "claude", ClaudeEvents, remove)
	// 允许 agent 调用 kitestring 的 MCP 工具而不逐次询问。
	perms, _ := root["permissions"].(map[string]any)
	if perms == nil {
		perms = map[string]any{}
	}
	allow, _ := perms["allow"].([]any)
	var kept []any
	for _, a := range allow {
		if a != "mcp__"+mcpName {
			kept = append(kept, a)
		}
	}
	if !remove {
		kept = append(kept, "mcp__"+mcpName)
	}
	if len(kept) > 0 {
		perms["allow"] = kept
	} else {
		delete(perms, "allow")
	}
	if len(perms) > 0 {
		root["permissions"] = perms
	} else {
		delete(root, "permissions")
	}
	if err := backup(o, path); err != nil {
		return err
	}
	fmt.Fprintf(o.Out, "✓ Claude hooks：%s\n", path)
	return writeFile(o, path, marshal(root), 0o644)
}

func claudeMCP(o Options, remove bool) error {
	bin, err := exec.LookPath("claude")
	if err != nil {
		return errors.New("未找到 claude 命令，跳过 MCP 注册")
	}
	args := []string{"mcp", "remove", "--scope", "user", mcpName}
	if o.DryRun {
		fmt.Fprintf(o.Out, "  将执行：claude %s\n", strings.Join(args, " "))
	} else {
		_ = exec.Command(bin, args...).Run() // 不存在时报错，忽略
	}
	if !remove {
		args = []string{"mcp", "add", "--scope", "user", mcpName, "--", o.KS, "mcp"}
		if o.DryRun {
			fmt.Fprintf(o.Out, "  将执行：claude %s\n", strings.Join(args, " "))
		} else if out, err := exec.Command(bin, args...).CombinedOutput(); err != nil {
			return fmt.Errorf("%v：%s", err, strings.TrimSpace(string(out)))
		}
	}
	fmt.Fprintln(o.Out, "✓ Claude MCP（user scope）")
	return nil
}

func codexHooks(o Options, home string, remove bool) error {
	path := filepath.Join(codexHome(home), "hooks.json")
	root, err := readJSON(path)
	if err != nil {
		return err
	}
	mergeHooks(root, o.KS, "codex", CodexEvents, remove)
	if err := backup(o, path); err != nil {
		return err
	}
	fmt.Fprintf(o.Out, "✓ Codex hooks：%s\n", path)
	return writeFile(o, path, marshal(root), 0o644)
}

func codexHome(home string) string {
	if h := os.Getenv("CODEX_HOME"); h != "" {
		return h
	}
	return filepath.Join(home, ".codex")
}

var tomlBlockRe = regexp.MustCompile(`(?s)\n?` + regexp.QuoteMeta(tomlBegin) + `.*?` + regexp.QuoteMeta(tomlEnd) + `\n?`)

// codexMCP 在 config.toml 末尾维护一个带标记的配置块：注册 MCP server，并为其工具配置免审批
// （审批策略为 never 时 Codex 会自动拒绝需要审批的 MCP 调用，见 hook-matrix.md）。
func codexMCP(o Options, home string, remove bool) error {
	path := filepath.Join(codexHome(home), "config.toml")
	b, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	content := tomlBlockRe.ReplaceAllString(string(b), "\n")
	if strings.Contains(content, "[mcp_servers."+mcpName+"]") {
		return fmt.Errorf("%s 中已有手写的 [mcp_servers.%s]，请先删除", path, mcpName)
	}
	if !remove {
		var blk strings.Builder
		fmt.Fprintf(&blk, "\n%s\n[mcp_servers.%s]\ncommand = %q\nargs = [\"mcp\"]\n", tomlBegin, mcpName, o.KS)
		for _, t := range MCPTools {
			fmt.Fprintf(&blk, "\n[mcp_servers.%s.tools.%s]\napproval_mode = \"approve\"\n", mcpName, t)
		}
		blk.WriteString(tomlEnd + "\n")
		content = strings.TrimRight(content, "\n") + "\n" + blk.String()
	}
	if err := backup(o, path); err != nil {
		return err
	}
	fmt.Fprintf(o.Out, "✓ Codex MCP：%s\n", path)
	return writeFile(o, path, []byte(content), 0o600)
}

// service 注册常驻服务：macOS 用 launchd，Linux 用 systemd --user。
func service(o Options, home, role string, argv []string, remove bool) error {
	switch runtime.GOOS {
	case "darwin":
		return launchd(o, home, role, argv, remove)
	case "linux":
		return systemd(o, home, role, argv, remove)
	default:
		return fmt.Errorf("暂不支持 %s 的常驻服务，请手动运行：%s", runtime.GOOS, strings.Join(argv, " "))
	}
}

func run(o Options, name string, args ...string) error {
	if o.DryRun {
		fmt.Fprintf(o.Out, "  将执行：%s %s\n", name, strings.Join(args, " "))
		return nil
	}
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s：%v %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func launchd(o Options, home, role string, argv []string, remove bool) error {
	label := launchLabel + "." + role
	path := filepath.Join(home, "Library", "LaunchAgents", label+".plist")
	domain := fmt.Sprintf("gui/%d", os.Getuid())
	_ = run(Options{DryRun: o.DryRun, Out: io.Discard}, "launchctl", "bootout", domain+"/"+label)
	if remove {
		if o.DryRun {
			fmt.Fprintf(o.Out, "  将删除 %s\n", path)
		} else {
			_ = os.Remove(path)
		}
		fmt.Fprintf(o.Out, "✓ 已移除 %s 服务\n", role)
		return nil
	}
	logDir := filepath.Join(home, ".kitestring", "logs")
	if !o.DryRun {
		_ = os.MkdirAll(logDir, 0o700)
	}
	var args strings.Builder
	for _, a := range argv {
		fmt.Fprintf(&args, "\t\t<string>%s</string>\n", xmlEscape(a))
	}
	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>%s</string>
	<key>ProgramArguments</key>
	<array>
%s	</array>
	<key>EnvironmentVariables</key>
	<dict>
		<key>PATH</key>
		<string>/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin</string>
	</dict>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>StandardOutPath</key>
	<string>%s</string>
	<key>StandardErrorPath</key>
	<string>%s</string>
</dict>
</plist>
`, label, args.String(), filepath.Join(logDir, role+".log"), filepath.Join(logDir, role+".log"))
	if err := writeFile(o, path, []byte(plist), 0o644); err != nil {
		return err
	}
	if err := run(o, "launchctl", "bootstrap", domain, path); err != nil {
		return err
	}
	fmt.Fprintf(o.Out, "✓ %s 服务（launchd：%s）\n", role, label)
	return nil
}

func xmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}

func systemd(o Options, home, role string, argv []string, remove bool) error {
	unit := "kitestring-" + role + ".service"
	path := filepath.Join(home, ".config", "systemd", "user", unit)
	if remove {
		_ = run(o, "systemctl", "--user", "disable", "--now", unit)
		if o.DryRun {
			fmt.Fprintf(o.Out, "  将删除 %s\n", path)
		} else {
			_ = os.Remove(path)
		}
		fmt.Fprintf(o.Out, "✓ 已移除 %s 服务\n", role)
		return nil
	}
	quoted := make([]string, len(argv))
	for i, a := range argv {
		quoted[i] = shellQuote(a)
	}
	content := fmt.Sprintf("[Unit]\nDescription=Kitestring %s\nAfter=network-online.target\n\n[Service]\nExecStart=%s\nRestart=always\nRestartSec=3\n\n[Install]\nWantedBy=default.target\n", role, strings.Join(quoted, " "))
	if err := writeFile(o, path, []byte(content), 0o644); err != nil {
		return err
	}
	if err := run(o, "systemctl", "--user", "daemon-reload"); err != nil {
		return err
	}
	if err := run(o, "systemctl", "--user", "enable", "--now", unit); err != nil {
		return err
	}
	fmt.Fprintf(o.Out, "✓ %s 服务（systemd --user：%s）\n", role, unit)
	return nil
}
