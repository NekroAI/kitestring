package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/NekroAI/kitestring/internal/install"
	"github.com/NekroAI/kitestring/internal/selfupdate"
	"github.com/NekroAI/kitestring/internal/version"
)

// runUpdate 实现 ks update（R-80）。
func runUpdate(args []string) error {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	check := fs.Bool("check", false, "只查有没有新版本，不安装")
	force := fs.Bool("force", false, "开发构建也换成最新发布版")
	if err := parseAnywhere(fs, args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("ks update 不接受多余的参数：%s", strings.Join(fs.Args(), " "))
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		exe = p
	}
	return selfupdate.Run(context.Background(), selfupdate.Options{
		Current: version.Version,
		Exe:     exe,
		GOOS:    runtime.GOOS,
		GOARCH:  runtime.GOARCH,
		Check:   *check,
		Force:   *force,
		Restart: func(exe string) error {
			n, err := install.RestartServices(exe, install.RestartOptions{})
			if err == nil && n == 0 {
				fmt.Println("本机没有注册用这个 ks 的常驻服务，无需重启；手动运行的 Hub 或后台服务请自行重启")
			}
			return err
		},
	})
}
