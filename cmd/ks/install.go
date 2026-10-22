package main

import (
	"flag"
	"os"
	"path/filepath"

	"github.com/NekroAI/kitestring/internal/install"
)

func runInstall(args []string, uninstall bool) error {
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	dry := fs.Bool("dry-run", false, "只打印将要做的改动")
	claude := fs.Bool("claude", true, "接入 Claude Code")
	codex := fs.Bool("codex", true, "接入 Codex")
	daemonSvc := fs.Bool("daemon", true, "注册 daemon 常驻服务")
	hubSvc := fs.Bool("hub", false, "同时注册 hub 常驻服务（Hub 所在设备使用）")
	hubAddr := fs.String("hub-addr", "0.0.0.0:7310", "hub 服务监听地址")
	_ = fs.Parse(args)
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		exe = p
	}
	return install.Run(install.Options{KS: exe, Claude: *claude, Codex: *codex, Daemon: *daemonSvc, Hub: *hubSvc, HubAddr: *hubAddr, DryRun: *dry}, uninstall)
}
