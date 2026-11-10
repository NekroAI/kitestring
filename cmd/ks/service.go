package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/NekroAI/kitestring/internal/daemon"
	"github.com/NekroAI/kitestring/internal/selfupdate"
)

// 常驻服务模式（--service，Windows 的任务计划程序这样启动 ks daemon / ks hub serve）：
// 日志写进 ~/.kitestring/logs/<role>.log（超过 10 MB 时旧的改名为 .1），
// Windows 上承载它的 conhost 退出（schtasks /end）时一同退出，并清掉 ks update 留下的旧 ks.exe。

const serviceLogMax = 10 << 20

// startService 把标准错误接到日志文件，返回随父进程退出而结束的 ctx。
func startService(ctx context.Context, role string) (context.Context, error) {
	dir := filepath.Join(daemon.Home(), "logs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return ctx, err
	}
	path := filepath.Join(dir, role+".log")
	if st, err := os.Stat(path); err == nil && st.Size() > serviceLogMax {
		_ = os.Rename(path, path+".1")
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return ctx, fmt.Errorf("打开日志 %s：%w", path, err)
	}
	if err := redirectStderr(f); err != nil {
		return ctx, err
	}
	if exe, err := os.Executable(); err == nil {
		selfupdate.RemoveStale(exe) // ks update 换下来的旧文件：旧服务进程已退出，这时删得掉
	}
	return exitWithParent(ctx), nil
}
