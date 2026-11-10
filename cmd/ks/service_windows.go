//go:build windows

package main

import (
	"context"
	"os"

	"golang.org/x/sys/windows"
)

func redirectStderr(f *os.File) error {
	os.Stdout, os.Stderr = f, f
	// 运行时的崩溃信息直接写标准句柄：也换成日志文件
	if err := windows.SetStdHandle(windows.STD_ERROR_HANDLE, windows.Handle(f.Fd())); err != nil {
		return err
	}
	return windows.SetStdHandle(windows.STD_OUTPUT_HANDLE, windows.Handle(f.Fd()))
}

// exitWithParent：Windows 不会因为父进程退出而结束子进程，schtasks /end 只结束 conhost。
// 等父进程的句柄变为有信号（已退出），就结束 ctx，让 ks 正常收尾退出。
func exitWithParent(ctx context.Context) context.Context {
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(os.Getppid()))
	if err != nil {
		return ctx
	}
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		defer windows.CloseHandle(h)
		_, _ = windows.WaitForSingleObject(h, windows.INFINITE)
		cancel()
	}()
	return ctx
}
