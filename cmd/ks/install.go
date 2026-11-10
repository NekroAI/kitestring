package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/NekroAI/kitestring/internal/install"
	"github.com/NekroAI/kitestring/internal/profile"
)

// claudeDirFlag 收集可重复的 --claude-dir；紧跟其后的 --launcher 归属最近一个目录。
type claudeDirFlag struct{ dirs []profile.Dir }

func (f *claudeDirFlag) String() string { return "" }
func (f *claudeDirFlag) Set(v string) error {
	f.dirs = append(f.dirs, profile.Dir{Path: v})
	return nil
}

type launcherFlag struct{ f *claudeDirFlag }

func (l launcherFlag) String() string { return "" }
func (l launcherFlag) Set(v string) error {
	if len(l.f.dirs) == 0 {
		return errors.New("--launcher 要写在对应的 --claude-dir 之后")
	}
	l.f.dirs[len(l.f.dirs)-1].Launcher = v
	return nil
}

func runInstall(args []string, uninstall bool) error {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	dry := fs.Bool("dry-run", false, "只打印将要做的改动")
	claude := fs.Bool("claude", true, "接入 Claude Code")
	codex := fs.Bool("codex", true, "接入 Codex")
	daemonSvc := fs.Bool("daemon", true, "注册 daemon 常驻服务")
	hubSvc := fs.Bool("hub", false, "同时注册 hub 常驻服务（Hub 所在设备使用）")
	hubAddr := fs.String("hub-addr", "0.0.0.0:7310", "hub 服务监听地址")
	autostart := fs.Bool("autostart", true, "Windows：常驻服务随登录自动启动；--autostart=false 只注册、不随登录启动")
	dirs := &claudeDirFlag{}
	fs.Var(dirs, "claude-dir", "接入这个 Claude 配置目录（CLAUDE_CONFIG_DIR），可重复；卸载时只移除指定的目录")
	fs.Var(launcherFlag{dirs}, "launcher", "紧跟在 --claude-dir 之后：开这个配置的启动命令（如 claude-x），用于「复制恢复命令」")
	status := fs.Bool("status", false, "只列出各 Claude 配置目录的接入状态，不改任何文件")
	if err := parseAnywhere(fs, args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("ks install / uninstall 不接受多余的参数：%s", strings.Join(fs.Args(), " "))
	}
	// 卸载时指定了 --claude-dir 就只处理这些 Claude 目录：Codex 和 daemon 除非显式写了对应开关，否则不碰
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	if uninstall && len(dirs.dirs) > 0 {
		if !set["codex"] {
			*codex = false
		}
		if !set["daemon"] {
			*daemonSvc = false
		}
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		exe = p
	}
	return install.Run(install.Options{KS: exe, Claude: *claude, Codex: *codex, Daemon: *daemonSvc, Hub: *hubSvc, HubAddr: *hubAddr, NoAutostart: !*autostart, DryRun: *dry, ClaudeDirs: dirs.dirs, Status: *status}, uninstall)
}
