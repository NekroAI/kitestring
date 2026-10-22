// ks 是 Kitestring 的单二进制入口，按子命令承担 hub / daemon / hook / mcp 等角色（见 docs/decisions.md D12）。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/NekroAI/kitestring/internal/client"
	"github.com/NekroAI/kitestring/internal/daemon"
	"github.com/NekroAI/kitestring/internal/hookcli"
	"github.com/NekroAI/kitestring/internal/hub"
	"github.com/NekroAI/kitestring/internal/mcpserver"
	"github.com/NekroAI/kitestring/internal/store"
	"github.com/NekroAI/kitestring/internal/version"
	"github.com/NekroAI/kitestring/web"
)

const usage = `纸鸢 Kitestring —— 多设备、多 agent 的个人驾驶舱

用法：ks <命令> [参数]

服务端：
  hub [serve]            启动 Hub（--addr 监听地址，默认 0.0.0.0:7310）
  hub passwd             设置 Web 登录密码
  hub device-add <名称>  新增设备并打印设备 token
  hub openapi            输出 OpenAPI 文档（前端生成类型用）

设备端：
  login --hub <地址> --token <token>   保存 Hub 地址与设备 token
  daemon                 启动设备常驻进程
  status                 查看本机 daemon 状态
  install / uninstall    写入或移除 Claude Code / Codex 的 hook 与 MCP 配置（--dry-run 预览）

agent 调用：
  hook <claude|codex> <event>   由 agent 的 hook 调用
  mcp                    stdio MCP server
  req list|get|create|status|evidence ...   需求操作（与 MCP 同能力）
  decision raise ...     提出决策

  version                打印版本
所有本机数据位于 ~/.kitestring（可用 KS_HOME 覆盖）。`

// 退出码约定：ks 永远不以 2 退出。Claude Code / Codex 把 hook 的退出码 2 视为“阻止”，
// hook 配置写错（如子命令不存在）时不能拦住用户的 agent。
func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(1)
	}
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "version", "--version", "-v":
		fmt.Printf("ks %s (%s)\n", version.Version, version.Commit)
	case "hub":
		err = runHub(args)
	case "login":
		err = runLogin(args)
	case "daemon":
		err = runDaemon()
	case "status":
		err = runStatus()
	case "install":
		err = runInstall(args, false)
	case "uninstall":
		err = runInstall(args, true)
	case "hook":
		os.Exit(hookcli.Run(args, os.Stdin, os.Stdout))
	case "mcp":
		err = mcpserver.Run(signalContext())
	case "req":
		err = runReq(args)
	case "decision":
		err = runDecision(args)
	case "help", "--help", "-h":
		fmt.Println(usage)
	default:
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(1)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "错误：", err)
		os.Exit(1)
	}
}

func signalContext() context.Context {
	ctx, _ := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	return ctx
}

func logger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
}

func openHubStore(data string) (*store.Store, error) {
	if data == "" {
		data = filepath.Join(daemon.Home(), "hub.db")
	}
	if err := os.MkdirAll(filepath.Dir(data), 0o700); err != nil {
		return nil, err
	}
	return store.Open(data)
}

func runHub(args []string) error {
	sub := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, args = args[0], args[1:]
	}
	fs := flag.NewFlagSet("hub", flag.ContinueOnError)
	addr := fs.String("addr", "0.0.0.0:7310", "监听地址")
	data := fs.String("data", "", "数据库路径（默认 ~/.kitestring/hub.db）")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if sub == "openapi" {
		b, err := json.MarshalIndent(hub.New(nil, logger()).OpenAPI(), "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(b))
		return nil
	}
	st, err := openHubStore(*data)
	if err != nil {
		return err
	}
	defer st.Close()
	h := hub.New(st, logger())
	ctx := signalContext()
	switch sub {
	case "serve":
		return h.Start(ctx, *addr, web.Handler())
	case "passwd":
		pw, err := readPassword()
		if err != nil {
			return err
		}
		if err := h.SetPassword(ctx, pw); err != nil {
			return err
		}
		fmt.Println("密码已更新，所有已登录的 Web 会话已注销。")
		return nil
	case "device-add":
		if fs.NArg() < 1 {
			return fmt.Errorf("用法：ks hub device-add <名称>")
		}
		tok := hub.NewDeviceToken()
		d, err := st.CreateDevice(ctx, fs.Arg(0), hub.HashToken(tok))
		if err != nil {
			return err
		}
		fmt.Printf("已新增设备 %s\n设备 token（只显示一次）：%s\n在该设备上执行：ks login --hub <Hub 地址> --token %s\n", d.Name, tok, tok)
		return nil
	default:
		return fmt.Errorf("未知的 hub 子命令：%s", sub)
	}
}

func readPassword() (string, error) {
	if pw := os.Getenv("KS_PASSWORD"); pw != "" {
		return pw, nil
	}
	fmt.Print("新密码（至少 8 位）：")
	var pw string
	if _, err := fmt.Scanln(&pw); err != nil {
		return "", err
	}
	if len(pw) < 8 {
		return "", fmt.Errorf("密码至少 8 位")
	}
	return pw, nil
}

func runLogin(args []string) error {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	hubURL := fs.String("hub", "", "Hub 地址，例：http://nexus-mini.local:7310")
	token := fs.String("token", "", "设备 token（kst_ 开头）")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *hubURL == "" || *token == "" {
		return fmt.Errorf("用法：ks login --hub <地址> --token <token>")
	}
	cfg, _ := daemon.LoadConfig()
	cfg.HubURL = strings.TrimSuffix(*hubURL, "/")
	cfg.Token = *token
	if err := daemon.SaveConfig(cfg); err != nil {
		return err
	}
	fmt.Println("已保存到", daemon.ConfigPath())
	return nil
}

func runDaemon() error {
	cfg, err := daemon.LoadConfig()
	if err != nil {
		return err
	}
	d, err := daemon.New(cfg, logger())
	if err != nil {
		return err
	}
	return d.Run(signalContext())
}

func runStatus() error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var st map[string]any
	if err := client.New(3*time.Second).Do(ctx, "GET", "/status", nil, &st); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(st, "", "  ")
	fmt.Println(string(b))
	return nil
}
