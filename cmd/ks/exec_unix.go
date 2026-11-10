//go:build !windows

package main

import (
	"os"
	"syscall"
)

// execReplace 用 bin 替换当前进程（终端直接交给新会话）。
func execReplace(bin string, argv []string) error {
	return syscall.Exec(bin, argv, os.Environ())
}
