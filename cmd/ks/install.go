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
	claude := fs.Bool("claude", true, "接入 Claude Code（指定目标时仅处理所选目标）")
	codex := fs.Bool("codex", true, "接入 Codex（指定目标时仅处理所选目标）")
	agy := fs.Bool("agy", true, "接入 Google Antigravity / agy（指定目标时仅处理所选目标）")
	daemonSvc := fs.Bool("daemon", true, "注册 daemon 常驻服务（指定目标时仅处理所选目标）")
	hubSvc := fs.Bool("hub", false, "注册 hub 常驻服务（Hub 所在设备使用）")
	hubAddr := fs.String("hub-addr", "0.0.0.0:7310", "hub 服务监听地址")
	hubFiles := fs.String("hub-files-dir", "", "hub 服务的附件目录（写进服务的 --files-dir，例如外置硬盘上的目录，须已存在）；不写时沿用已安装 hub 服务里配置的目录（没配置过则用数据库旁的 files）；写 default 改回默认目录")
	autostart := fs.Bool("autostart", true, "Windows：常驻服务随登录自动启动；--autostart=false 只注册、不随登录启动")
	dirs := &claudeDirFlag{}
	fs.Var(dirs, "claude-dir", "接入这个 Claude 配置目录（CLAUDE_CONFIG_DIR），可重复；卸载时只移除指定的目录")
	fs.Var(launcherFlag{dirs}, "launcher", "紧跟在 --claude-dir 之后：开这个配置的启动命令（如 claude-x），用于「复制恢复命令」")
	status := fs.Bool("status", false, "只列出各配置目录与插件的接入状态，不改任何文件")
	if err := parseAnywhere(fs, args); err != nil {
		return err
	}
	posTargets := map[string]bool{}
	for _, arg := range fs.Args() {
		switch strings.ToLower(strings.TrimSpace(arg)) {
		case "claude":
			posTargets["claude"] = true
		case "codex":
			posTargets["codex"] = true
		case "agy", "antigravity":
			posTargets["agy"] = true
		case "daemon":
			posTargets["daemon"] = true
		case "hub":
			posTargets["hub"] = true
		default:
			return fmt.Errorf("不支持的接入目标：%s（可选：claude、codex、agy、daemon、hub）", arg)
		}
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })

	// 约定：直接 ks install / uninstall 处理全部设备端目标（Claude / Codex / Antigravity / daemon）；
	// 只要显式指定了目标名（如 ks install agy）或显式开启了某个目标开关（如 ks install --agy / --hub），
	// 就只处理被选中的目标，不动其他组件。
	explicitSelect := len(posTargets) > 0 ||
		(set["claude"] && *claude) ||
		(set["codex"] && *codex) ||
		(set["agy"] && *agy) ||
		(set["daemon"] && *daemonSvc) ||
		(set["hub"] && *hubSvc)

	var runClaude, runCodex, runAgy, runDaemon, runHub bool
	if explicitSelect {
		pick := func(name string, flagVal bool) bool {
			if set[name] {
				return flagVal
			}
			return posTargets[name]
		}
		runClaude = pick("claude", *claude)
		runCodex = pick("codex", *codex)
		runAgy = pick("agy", *agy)
		runDaemon = pick("daemon", *daemonSvc)
		runHub = pick("hub", *hubSvc)
	} else {
		runClaude = *claude
		runCodex = *codex
		runAgy = *agy
		runDaemon = *daemonSvc
		runHub = *hubSvc
		// 卸载时指定了 --claude-dir 就只处理这些 Claude 目录：其他组件除非显式开启，否则不碰
		if uninstall && len(dirs.dirs) > 0 {
			if !set["codex"] {
				runCodex = false
			}
			if !set["agy"] {
				runAgy = false
			}
			if !set["daemon"] {
				runDaemon = false
			}
		}
	}

	// R-354：不写沿用已安装服务的附件目录；改回默认要显式写 default
	filesDefault := strings.TrimSpace(*hubFiles) == "default"
	if set["hub-files-dir"] && strings.TrimSpace(*hubFiles) == "" {
		return errors.New("--hub-files-dir 不能为空：改回默认附件目录请写 --hub-files-dir default，沿用已安装服务的目录就不写这个参数")
	}
	filesDir := ""
	if !filesDefault {
		var err error
		if filesDir, err = hubFilesDir(*hubFiles, ""); err != nil {
			return err
		}
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		exe = p
	}
	return install.Run(install.Options{
		KS:                 exe,
		Claude:             runClaude,
		Codex:              runCodex,
		Agy:                runAgy,
		Daemon:             runDaemon,
		Hub:                runHub,
		HubAddr:            *hubAddr,
		HubFilesDir:        filesDir,
		HubFilesDirDefault: filesDefault,
		NoAutostart:        !*autostart,
		DryRun:             *dry,
		ClaudeDirs:         dirs.dirs,
		Status:             *status,
	}, uninstall)
}
