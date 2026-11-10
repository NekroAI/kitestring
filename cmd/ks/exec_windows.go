//go:build windows

package main

import (
	"errors"
	"os"
	"os/exec"
	"os/signal"
)

// execReplace：Windows 不能替换当前进程，改为在同一个终端里启动 bin、等它结束，并带回它的退出码。
// Ctrl+C 会同时送到新会话；ks 自己忽略它，等新会话处理。
func execReplace(bin string, argv []string) error {
	cmd := exec.Command(bin, argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	signal.Ignore(os.Interrupt)
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		os.Exit(ee.ExitCode())
	}
	if err != nil {
		return err
	}
	os.Exit(0)
	return nil
}
