package protocol

import "strings"

// IsAbsPath 判断设备上的路径是不是绝对路径：/… 或 Windows 的 C:\…、C:/…、\\server\…（Hub 不按本机系统判断）。
func IsAbsPath(p string) bool {
	return strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\\`) ||
		len(p) >= 3 && p[1] == ':' && (p[2] == '\\' || p[2] == '/') && (p[0]|0x20 >= 'a' && p[0]|0x20 <= 'z')
}

// PathBase 取设备上一个路径的最后一段。Hub 和设备可能是不同的系统：/ 与 \ 都当作分隔符（Windows 设备的 C:\…）。
func PathBase(p string) string {
	p = strings.TrimRight(p, `/\`)
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		return p[i+1:]
	}
	return p
}
