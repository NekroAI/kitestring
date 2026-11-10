package install

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// RestartOptions 控制 RestartServices；空值按本机默认（与 scripts/deploy.sh 用同一组环境变量）。
type RestartOptions struct {
	Home      string // 默认 os.UserHomeDir()
	GOOS      string // 默认 runtime.GOOS
	Launchctl string // 默认 $KS_LAUNCHCTL，再默认 launchctl
	Domain    string // launchd 域，默认 $LAUNCHD_PREFIX，再默认 gui/<uid>
	Systemctl string // 默认 $KS_SYSTEMCTL，再默认 systemctl
	Schtasks  string // 默认 $KS_SCHTASKS，再默认 schtasks
	Out       io.Writer
	// Exec 执行一条命令，测试注入；默认 exec.Command(...).CombinedOutput()
	Exec func(name string, args ...string) ([]byte, error)
	// Pause 等旧进程退出（Windows 先停后启），测试注入；默认 time.Sleep
	Pause func(time.Duration)
}

// RestartServices 重启 ks install 注册过、且用的正是 exe 这个文件的常驻服务（先 Hub 后后台服务），
// 让它们换上刚替换的新版本。没注册的跳过；注册的是别处的 ks 时只提示、不重启。
// 返回重启了几个服务。
func RestartServices(exe string, o RestartOptions) (int, error) {
	if o.Out == nil {
		o.Out = os.Stdout
	}
	if o.Home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return 0, err
		}
		o.Home = h
	}
	if o.GOOS == "" {
		o.GOOS = runtime.GOOS
	}
	if o.Exec == nil {
		o.Exec = func(name string, args ...string) ([]byte, error) {
			return exec.Command(name, args...).CombinedOutput()
		}
	}
	if o.Pause == nil {
		o.Pause = time.Sleep
	}
	restarted := 0
	for _, role := range []string{"hub", "daemon"} {
		name := serviceName(role)
		var path, cmd string
		var args []string
		switch o.GOOS {
		case "darwin":
			label := launchLabel + "." + role
			path = filepath.Join(o.Home, "Library", "LaunchAgents", label+".plist")
			cmd = firstNonEmpty(o.Launchctl, os.Getenv("KS_LAUNCHCTL"), "launchctl")
			domain := firstNonEmpty(o.Domain, os.Getenv("LAUNCHD_PREFIX"), fmt.Sprintf("gui/%d", os.Getuid()))
			args = []string{"kickstart", "-k", domain + "/" + label}
		case "linux":
			unit := "kitestring-" + role + ".service"
			path = filepath.Join(o.Home, ".config", "systemd", "user", unit)
			cmd = firstNonEmpty(o.Systemctl, os.Getenv("KS_SYSTEMCTL"), "systemctl")
			args = []string{"--user", "restart", unit}
		case "windows":
			path = taskFile(o.Home, role)
			cmd = firstNonEmpty(o.Schtasks, os.Getenv("KS_SCHTASKS"), "schtasks")
			// /end 让 conhost 退出，带 --service 的 ks 随之退出；再 /run 换上新版本
			args = []string{"/run", "/tn", taskName(role)}
		default:
			return restarted, nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			continue // 没注册这个服务
		}
		if !serviceUses(decodeUTF16File(data), exe) {
			fmt.Fprintf(o.Out, "! %s用的不是 %s，未重启（%s）\n", strings.TrimLeft(name, " "), exe, path)
			continue
		}
		if o.GOOS == "windows" {
			// /end 让 conhost 退出，带 --service 的 ks 随之退出；等它收尾（Hub 最多 5 秒）再 /run 换上新版本
			if _, err := o.Exec(cmd, "/end", "/tn", taskName(role)); err == nil {
				o.Pause(serviceStopWait)
			}
		}
		if out, err := o.Exec(cmd, args...); err != nil {
			return restarted, fmt.Errorf("重启%s失败：%s %s：%v %s", name, cmd, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
		}
		fmt.Fprintf(o.Out, "✓ 已重启%s\n", strings.TrimRight(name, " "))
		restarted++
	}
	return restarted, nil
}

// serviceUses 判断服务配置（plist、systemd unit 或 Windows 任务定义）启动的是不是 exe。
func serviceUses(conf, exe string) bool {
	return strings.Contains(conf, "<string>"+xmlEscape(exe)+"</string>") ||
		strings.Contains(conf, "<Arguments>--headless "+xmlEscape(winQuote(exe))+" ") ||
		strings.Contains(conf, "ExecStart="+shellQuote(exe)+" ") ||
		strings.Contains(conf, "ExecStart="+exe+" ")
}

func serviceName(role string) string {
	if role == "hub" {
		return " Hub "
	}
	return "后台服务"
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}
