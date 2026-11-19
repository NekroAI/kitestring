package install

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// 固定启动器（R-354）：macOS 上 hub 服务经 $KS_HOME/bin/ks-launcher 运行，
// ProgramArguments 是 <ks-launcher> <ks> hub serve …。
//
// ks 是临时签名的，macOS 的隐私授权（外置硬盘、完全磁盘访问）按文件哈希认，每次部署都会失效；
// 授权给一个从不更新的启动器后，挂在它下面的新版 ks 照样能访问。所以启动器只在不存在时安装，
// ks install 和部署都绝不替换它（内容不同也不替换）；卸载服务也不删它，删了要重新授权。
//
// 只有 hub 服务经启动器：只有 Hub 读写附件目录（可能在外置硬盘上）；后台服务不碰附件，
// 保持原样，少一层进程、出问题时影响面也小。

// LauncherName 是启动器的文件名。
const LauncherName = "ks-launcher"

// LauncherPath 是启动器的安装位置：$KS_HOME/bin/ks-launcher（默认 ~/.kitestring/bin/ks-launcher），与 deploy.sh 一致。
func LauncherPath(home string) string {
	ksHome := os.Getenv("KS_HOME")
	if ksHome == "" {
		ksHome = filepath.Join(home, ".kitestring")
	}
	return filepath.Join(ksHome, "bin", LauncherName)
}

// ensureLauncher 返回 hub 服务要用的启动器路径；缺失时从 ks 旁边的 ks-launcher 装一份。
// 已存在就原样使用、不比较内容、不替换。用不了（不可执行、找不到来源）时返回空：hub 服务这次不经启动器运行，并说明原因。
func ensureLauncher(o Options, home string) string {
	dst := LauncherPath(home)
	st, err := os.Stat(dst)
	switch {
	case err == nil && st.Mode().IsRegular() && st.Mode().Perm()&0o111 != 0:
		fmt.Fprintf(o.Out, "  hub 服务经启动器运行：%s（不会被替换，macOS 的访问授权记在它上面）\n", dst)
		return dst
	case err == nil:
		fmt.Fprintf(o.Out, "  ! 启动器 %s 不是可执行文件，hub 服务这次不经启动器运行。用 chmod 755 修好权限后重跑 ks install --hub；不要换成别的文件，换了要在系统设置里重新允许\n", dst)
		return ""
	case !errors.Is(err, os.ErrNotExist):
		fmt.Fprintf(o.Out, "  ! 读不到启动器 %s（%v），hub 服务这次不经启动器运行\n", dst, err)
		return ""
	}
	src := filepath.Join(filepath.Dir(o.KS), LauncherName)
	if st, err := os.Stat(src); err != nil || !st.Mode().IsRegular() {
		fmt.Fprintf(o.Out, "  ! 没有找到启动器（%s 不存在），hub 服务这次不经启动器运行：附件目录在外置硬盘上时，每次更新 ks 后都要重新授权。"+
			"在源码目录运行 make build，把 bin/%s 复制到 %s 后重跑 ks install --hub（用 scripts/deploy.sh 部署时会自动装好）\n", src, LauncherName, dst)
		return ""
	}
	if o.DryRun {
		fmt.Fprintf(o.Out, "  将安装启动器 %s → %s\n", src, dst)
		return dst
	}
	if err := copyExecutable(src, dst); err != nil {
		fmt.Fprintf(o.Out, "  ! 安装启动器 %s 失败（%v），hub 服务这次不经启动器运行\n", dst, err)
		return ""
	}
	fmt.Fprintf(o.Out, "  ✓ 已安装启动器 %s（来自 %s）。附件目录在外置硬盘上时，第一次访问会弹出 macOS 的询问，允许即可；"+
		"也可以先在「系统设置 → 隐私与安全性 → 完全磁盘访问权限」里加入它。以后部署不会替换它，授权一直有效\n", dst, src)
	return dst
}

// copyExecutable 复制 src 到 dst（先写临时文件再改名，权限 0755）；dst 已存在时不覆盖。
func copyExecutable(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dst), "."+LauncherName+"-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o755); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// Link 在 dst 已存在时失败：两个 ks install 同时跑也不会覆盖先装好的那份
	if err := os.Link(tmp.Name(), dst); err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil
		}
		return err
	}
	return nil
}

// launchctl 执行一条 launchctl 命令，测试替换。
var launchctl = func(args ...string) ([]byte, error) {
	return exec.Command("launchctl", args...).CombinedOutput()
}

// launchdPause 是等待 launchd 的间隔，测试替换。
var launchdPause = time.Sleep

const bootstrapAttempts = 5

// bootstrap 载入服务。bootout 是异步的，旧实例还没卸完时 bootstrap 会报 5（Input/output error）：
// 先等旧实例卸载（最多约 5 秒），仍报 5 或 37（操作进行中）时隔 1 秒重试，最多 5 次（R-367）。
func bootstrap(o Options, domain, label, path string) error {
	args := []string{"bootstrap", domain, path}
	if o.DryRun {
		fmt.Fprintf(o.Out, "  将执行：launchctl %s\n", strings.Join(args, " "))
		return nil
	}
	for i := 0; i < 10; i++ {
		if _, err := launchctl("print", domain+"/"+label); err != nil {
			break // 已卸载
		}
		launchdPause(500 * time.Millisecond)
	}
	var out []byte
	var err error
	for n := 1; n <= bootstrapAttempts; n++ {
		if out, err = launchctl(args...); err == nil {
			return nil
		}
		var ec interface{ ExitCode() int }
		if !errors.As(err, &ec) || (ec.ExitCode() != 5 && ec.ExitCode() != 37) {
			break
		}
		if n < bootstrapAttempts {
			launchdPause(time.Second)
		}
	}
	return fmt.Errorf("launchctl %s：%v %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
}
