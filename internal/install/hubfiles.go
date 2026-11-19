package install

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// R-354：重跑 ks install --hub 不写 --hub-files-dir 时，沿用已注册的 hub 服务里配置的附件目录，
// 不悄悄改回默认（附件搬到外置硬盘后改回默认，旧附件就看不到了、新附件又写回系统盘）。
// 要改回默认须显式写 --hub-files-dir default（Options.HubFilesDirDefault）。

// serviceConfPath 是 ks install 为 role 写下的服务定义文件。
func serviceConfPath(goos, home, role string) string {
	switch goos {
	case "darwin":
		return filepath.Join(home, "Library", "LaunchAgents", launchLabel+"."+role+".plist")
	case "linux":
		return filepath.Join(home, ".config", "systemd", "user", "kitestring-"+role+".service")
	case "windows":
		return taskFile(home, role)
	}
	return ""
}

// installedHubFilesDir 读已注册的 hub 服务定义，返回启动参数里 --files-dir 的值；没注册或没写返回空。
func installedHubFilesDir(goos, home string) string {
	path := serviceConfPath(goos, home, "hub")
	if path == "" {
		return ""
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return flagValue(parseServiceArgv(goos, decodeUTF16File(b)), "--files-dir")
}

// resolveHubFilesDir 定这次写进 hub 服务的附件目录，返回给用户看的一句说明（没有变化时为空）。
func resolveHubFilesDir(o *Options, goos, home string) string {
	old := installedHubFilesDir(goos, home)
	switch {
	case o.HubFilesDirDefault:
		o.HubFilesDir = ""
		if old != "" {
			return fmt.Sprintf("hub 附件目录改回默认（数据库旁的 files）；原来是 %s，那里的附件不会搬回来，需要时自行拷贝", old)
		}
		return ""
	case o.HubFilesDir != "":
		if old != "" && old != o.HubFilesDir {
			return fmt.Sprintf("hub 附件目录：%s（原来是 %s）", o.HubFilesDir, old)
		}
		return ""
	case old != "":
		o.HubFilesDir = old
		return fmt.Sprintf("hub 附件目录沿用已安装服务的配置：%s（改目录用 --hub-files-dir <目录>，改回默认用 --hub-files-dir default）", old)
	}
	return ""
}

// flagValue 取 argv 里 name 的值（支持 "--x v" 和 "--x=v"），没有返回空。
func flagValue(argv []string, name string) string {
	for i, a := range argv {
		if a == name && i+1 < len(argv) {
			return argv[i+1]
		}
		if v, ok := strings.CutPrefix(a, name+"="); ok {
			return v
		}
	}
	return ""
}

var (
	plistArgsRe  = regexp.MustCompile(`(?s)<key>ProgramArguments</key>\s*<array>(.*?)</array>`)
	plistStrRe   = regexp.MustCompile(`(?s)<string>(.*?)</string>`)
	taskArgsRe   = regexp.MustCompile(`(?s)<Arguments>(.*?)</Arguments>`)
	xmlUnescaper = strings.NewReplacer("&lt;", "<", "&gt;", ">", "&quot;", `"`, "&apos;", "'", "&amp;", "&")
)

// parseServiceArgv 从服务定义里读回启动参数：plistContent、unitContent、taskXML 的反向。
// Windows 的参数前面有 conhost 的 --headless。
func parseServiceArgv(goos, conf string) []string {
	switch goos {
	case "darwin":
		m := plistArgsRe.FindStringSubmatch(conf)
		if m == nil {
			return nil
		}
		var argv []string
		for _, s := range plistStrRe.FindAllStringSubmatch(m[1], -1) {
			argv = append(argv, xmlUnescaper.Replace(s[1]))
		}
		return argv
	case "linux":
		for _, line := range strings.Split(conf, "\n") {
			if v, ok := strings.CutPrefix(strings.TrimSpace(line), "ExecStart="); ok {
				return splitShell(v)
			}
		}
	case "windows":
		if m := taskArgsRe.FindStringSubmatch(conf); m != nil {
			return splitWindows(xmlUnescaper.Replace(m[1]))
		}
	}
	return nil
}

// splitShell 按 shell 规则拆分（单引号原样、双引号、反斜杠转义），对应 shellQuote。
func splitShell(s string) []string {
	var out []string
	var cur strings.Builder
	in := false // 当前有一个参数在拼
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\'':
			in = true
			j := strings.IndexByte(s[i+1:], '\'')
			if j < 0 {
				cur.WriteString(s[i+1:])
				i = len(s)
				break
			}
			cur.WriteString(s[i+1 : i+1+j])
			i += j + 1
		case c == '"':
			in = true
			for i++; i < len(s) && s[i] != '"'; i++ {
				if s[i] == '\\' && i+1 < len(s) {
					i++
				}
				cur.WriteByte(s[i])
			}
		case c == '\\' && i+1 < len(s):
			in = true
			i++
			cur.WriteByte(s[i])
		case c == ' ' || c == '\t':
			if in {
				out = append(out, cur.String())
				cur.Reset()
				in = false
			}
		default:
			in = true
			cur.WriteByte(c)
		}
	}
	if in {
		out = append(out, cur.String())
	}
	return out
}

// splitWindows 按 CommandLineToArgvW 的规则拆分，对应 winQuote。
func splitWindows(s string) []string {
	var out []string
	var cur strings.Builder
	in, quoted := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\\':
			n := 0
			for i < len(s) && s[i] == '\\' {
				n++
				i++
			}
			if i < len(s) && s[i] == '"' {
				cur.WriteString(strings.Repeat(`\`, n/2))
				if n%2 == 1 {
					cur.WriteByte('"')
				} else {
					quoted = !quoted
				}
			} else {
				cur.WriteString(strings.Repeat(`\`, n))
				i--
			}
			in = true
		case c == '"':
			quoted = !quoted
			in = true
		case (c == ' ' || c == '\t') && !quoted:
			if in {
				out = append(out, cur.String())
				cur.Reset()
				in = false
			}
		default:
			cur.WriteByte(c)
			in = true
		}
	}
	if in {
		out = append(out, cur.String())
	}
	return out
}
