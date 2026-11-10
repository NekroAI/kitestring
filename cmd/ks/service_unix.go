//go:build !windows

package main

import (
	"context"
	"os"
	"syscall"
)

func redirectStderr(f *os.File) error {
	os.Stdout, os.Stderr = f, f
	return syscall.Dup2(int(f.Fd()), 2)
}

// exitWithParent：launchd / systemd 自己管进程，不用跟着父进程退出。
func exitWithParent(ctx context.Context) context.Context { return ctx }
