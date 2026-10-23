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
  hub device-revoke <名称>  吊销设备（token 立即失效）
  hub device-rm <名称>   删除已吊销的设备（保留历史，名称可重新使用）
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
  req assign <R-x> --to <会话|auto>   PM 把需求交给开发
  req review <R-x> <意见> [--image]   Rev 交审查意见（不改需求状态）
  team                   工人池：本项目的 PM 与 Dev 会话（在线状态、上下文、手上的需求）
  team need <数量> --reason ...   PM 提醒用户开 Dev 会话
  team tell <文字> [--req R-x]   给本项目的 PM 留言（只送给 PM，不进用户收件箱；交验收时 PM 会自动收到通知）
  brief <简报>           PM 写给用户的简报，显示在工作台顶部
  plan create|get|submit ...   计划：拆阶段（等用户批准）、查看进度、交阶段小结（只写跨需求的效果和遗留问题）
  decision raise ...     提出决策（一次只问一件事）
  decision withdraw <D-x> [--reason 原因] [--replaced-by D-y]   撤回自己提的、用户还没答复的决策
  写作规范：证据第一句是结论（40 字以内），再逐条写验收判定；证据超 600 字、决策说明超 400 字时照常保存并提示压缩
  表达块：req evidence / decision raise / plan submit 可加 --blocks 块.json（清单、对比、截图、流程、指标、改动范围、选项、表格），纸鸢统一渲染
  role <pm|dev|advisor|reviewer|none>   声明本会话的身份（PM / Dev / Adv / Rev），打印身份提示词
  wait [--timeout 1h]    挂起等待用户在 Kitestring 上的答复 / 验收 / 留言，收到后输出并退出（Claude Code 后台运行，默认 1h；Codex 前台运行，最多 30m）

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
	case "plan":
		err = runPlan(args)
	case "decision":
		err = runDecision(args)
	case "wait":
		err = runWait(args)
	case "role":
		err = runRole(args)
	case "brief":
		err = runBrief(args)
	case "team":
		err = runTeam(args)
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
	// 参数可写在位置参数之后（如 device-add <名称> --data x）：否则会静默落到默认数据库
	if err := parseAnywhere(fs, args); err != nil {
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
	dbPath := *data
	if dbPath == "" {
		dbPath = filepath.Join(daemon.Home(), "hub.db")
	}
	h.SetFilesDir(filepath.Join(filepath.Dir(dbPath), "files"))
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
	case "device-revoke", "device-rm":
		if fs.NArg() < 1 {
			return fmt.Errorf("用法：ks hub %s <名称>", sub)
		}
		devs, err := st.ListDevices(ctx)
		if err != nil {
			return err
		}
		for _, d := range devs {
			if d.Name != fs.Arg(0) {
				continue
			}
			if sub == "device-revoke" {
				if err := st.RevokeDevice(ctx, d.ID); err != nil {
					return err
				}
				fmt.Printf("已吊销设备 %s（正在运行的 Hub 需重启或在网页上吊销后才断开其连接）\n", d.Name)
				return nil
			}
			if err := st.DeleteDevice(ctx, d.ID); err != nil {
				return err
			}
			fmt.Printf("已删除设备 %s，名称可重新使用（历史会话保留）\n", d.Name)
			return nil
		}
		return fmt.Errorf("没有名为 %q 的设备", fs.Arg(0))
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
	hubURL := fs.String("hub", "", "Hub 地址，例：http://hub.local:7310")
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
