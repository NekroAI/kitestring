package install

import (
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf16"
)

// Windows 的常驻服务用“任务计划程序”的用户任务（不需要管理员权限）：
// 任务名 “Kitestring daemon” / “Kitestring hub”，以当前用户在登录的桌面会话里运行，
// 由 conhost.exe --headless 承载 ks（控制台程序直接启动会弹出窗口），退出时一分钟后重启。
// 开机自启（登录时启动）可关：关掉时任务仍注册着，ks install / ks update 照样用它启动、重启，只是登录时不自动运行。
// ks 带 --service 运行：日志写进 ~/.kitestring/logs/<role>.log，conhost 退出时（schtasks /end）ks 跟着退出。
// 任务定义另存一份在 ~/.kitestring/services/<role>.xml，RestartServices 据此判断任务用的是哪个 ks。

// serviceStopWait 是停掉任务后等 ks 退出的时间（Hub 关闭连接最多 5 秒）。
const serviceStopWait = 6 * time.Second

func taskName(role string) string { return "Kitestring " + role }

func taskFile(home, role string) string {
	return filepath.Join(home, ".kitestring", "services", role+".xml")
}

// winQuote 按 Windows 命令行规则给一个参数加引号（CommandLineToArgvW 能还原）。
func winQuote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\"") {
		return s
	}
	var b strings.Builder
	b.WriteByte('"')
	slashes := 0
	for _, r := range s {
		switch r {
		case '\\':
			slashes++
		case '"':
			b.WriteString(strings.Repeat(`\`, slashes*2+1))
			slashes = 0
			b.WriteRune(r)
			continue
		default:
			slashes = 0
		}
		b.WriteRune(r)
	}
	b.WriteString(strings.Repeat(`\`, slashes))
	b.WriteByte('"')
	return b.String()
}

// taskXML 生成任务定义。userID 是 DOMAIN\user；autostart 为 false 时不加登录触发器。
func taskXML(role, userID, conhost string, argv []string, autostart bool) string {
	args := []string{"--headless"}
	for _, a := range argv {
		args = append(args, winQuote(a))
	}
	trigger := ""
	if autostart {
		trigger = fmt.Sprintf(`
  <Triggers>
    <LogonTrigger>
      <Enabled>true</Enabled>
      <UserId>%s</UserId>
    </LogonTrigger>
  </Triggers>`, xmlEscape(userID))
	}
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo>
    <Description>Kitestring %s（由 ks install 管理）</Description>
  </RegistrationInfo>%s
  <Principals>
    <Principal id="Author">
      <UserId>%s</UserId>
      <LogonType>InteractiveToken</LogonType>
      <RunLevel>LeastPrivilege</RunLevel>
    </Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <AllowHardTerminate>true</AllowHardTerminate>
    <StartWhenAvailable>false</StartWhenAvailable>
    <IdleSettings>
      <StopOnIdleEnd>false</StopOnIdleEnd>
      <RestartOnIdle>false</RestartOnIdle>
    </IdleSettings>
    <AllowStartOnDemand>true</AllowStartOnDemand>
    <Enabled>true</Enabled>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
    <RestartOnFailure>
      <Interval>PT1M</Interval>
      <Count>999</Count>
    </RestartOnFailure>
  </Settings>
  <Actions Context="Author">
    <Exec>
      <Command>%s</Command>
      <Arguments>%s</Arguments>
    </Exec>
  </Actions>
</Task>
`, role, trigger, xmlEscape(userID), xmlEscape(conhost), xmlEscape(strings.Join(args, " ")))
}

// utf16File 把文本编码成带 BOM 的 UTF-16LE（schtasks /xml 只认这种）。
func utf16File(s string) []byte {
	u := utf16.Encode([]rune(strings.ReplaceAll(s, "\n", "\r\n")))
	b := make([]byte, 2, 2+len(u)*2)
	b[0], b[1] = 0xff, 0xfe
	for _, c := range u {
		b = append(b, byte(c), byte(c>>8))
	}
	return b
}

// decodeUTF16File 是 utf16File 的反向（读回 RestartServices 存下的任务定义）；不是 UTF-16 时原样返回。
func decodeUTF16File(b []byte) string {
	if len(b) < 2 || b[0] != 0xff || b[1] != 0xfe {
		return string(b)
	}
	u := make([]uint16, 0, len(b)/2)
	for i := 2; i+1 < len(b); i += 2 {
		u = append(u, uint16(b[i])|uint16(b[i+1])<<8)
	}
	return string(utf16.Decode(u))
}

func conhostPath() string {
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	return filepath.Join(root, "System32", "conhost.exe")
}

func schtask(o Options, home, role string, argv []string, remove bool) error {
	name := taskName(role)
	path := taskFile(home, role)
	// 先停掉正在运行的（没注册或没在运行时报错，忽略），等它收尾
	if err := run(Options{DryRun: o.DryRun, Out: io.Discard}, "schtasks", "/end", "/tn", name); err == nil && !o.DryRun {
		time.Sleep(serviceStopWait)
	}
	if remove {
		_ = run(o, "schtasks", "/delete", "/tn", name, "/f")
		if o.DryRun {
			fmt.Fprintf(o.Out, "  将删除 %s\n", path)
		} else {
			_ = os.Remove(path)
			_ = os.Remove(filepath.Dir(path)) // 空了才删得掉
		}
		fmt.Fprintf(o.Out, "✓ 已移除 %s 服务\n", role)
		return nil
	}
	u, err := user.Current()
	if err != nil {
		return err
	}
	xml := taskXML(role, u.Username, conhostPath(), append(argv, "--service"), !o.NoAutostart)
	if err := writeFile(o, path, utf16File(xml), 0o600); err != nil {
		return err
	}
	if err := run(o, "schtasks", "/create", "/tn", name, "/xml", path, "/f"); err != nil {
		return err
	}
	if err := run(o, "schtasks", "/run", "/tn", name); err != nil {
		return err
	}
	auto := "登录时自动启动"
	if o.NoAutostart {
		auto = "不随登录启动，需要时运行 schtasks /run /tn \"" + name + "\""
	}
	fmt.Fprintf(o.Out, "✓ %s 服务（任务计划程序：%s，%s）\n", role, name, auto)
	return nil
}
