// ks-launcher 是 macOS 上 Hub 常驻服务的固定启动器（R-354）。launchd 运行
//
//	~/.kitestring/bin/ks-launcher /path/to/ks hub serve …
//
// 它把后面的命令作为子进程启动，转发 SIGTERM / SIGINT / SIGHUP，等子进程退出后返回同样的退出码
// （被信号杀掉时返回 128 + 信号值）。除此之外什么都不做。
//
// 为什么要它：ks 是临时签名的，macOS 的隐私授权（访问外置硬盘、完全磁盘访问）按文件哈希认，
// 每次部署换了 ks，授权就失效，launchd 起的 Hub 访问外置硬盘时会卡住等系统弹窗。
// 授权记在启动器上、挂在它下面的进程沿用它的授权；启动器本身从不更新，授权一次以后部署都不用重授。
//
// 所以这个程序要保持稳定：装好以后部署绝不替换它（内容不同也不替换，只提示），
// 删掉或替换后要在「系统设置 → 隐私与安全性」重新允许。改这个文件不会影响已经装好的启动器。
//
// 用 Go 写、不用 C：构建只要 Go（构建 ks 本来就要），不需要 Xcode 命令行工具，发布包也能交叉编译；
// 只依赖标准库，CGO_ENABLED=0。
package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "用法：ks-launcher <程序绝对路径> [参数…]")
		os.Exit(2)
	}
	// 先接住信号再启动子进程：启动瞬间收到的停止信号也能转给子进程
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	p, err := os.StartProcess(os.Args[1], os.Args[1:], &os.ProcAttr{
		Env:   os.Environ(),
		Files: []*os.File{os.Stdin, os.Stdout, os.Stderr},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "ks-launcher：无法启动 %s：%v\n", os.Args[1], err)
		os.Exit(127)
	}
	go func() {
		for s := range sigs {
			_ = p.Signal(s)
		}
	}()
	st, err := p.Wait()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ks-launcher：等待 %s 退出失败：%v\n", os.Args[1], err)
		os.Exit(1)
	}
	os.Exit(exitCode(st))
}

// exitCode 把子进程的结束状态换成启动器的退出码：正常退出原样返回，被信号杀掉返回 128 + 信号值。
func exitCode(st *os.ProcessState) int {
	if ws, ok := st.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return st.ExitCode()
}
