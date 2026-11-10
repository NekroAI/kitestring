//go:build windows

package daemon

import (
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// 进程表的 Windows 实现：Toolhelp 快照拿进程号和父进程号，逐个打开进程读启动时间、CPU、内存和命令行，
// 环境从进程的 PEB 里读，监听端口用 netstat，停止用 TerminateProcess。
//
// 命令行规整成和 ps 输出相近的样子：第一段只留程序名（去掉目录和 .exe），其余参数用空格连起来，
// 这样认 shell、认 Claude Code / Codex、认 ks 自己的规则在各平台通用。

var procGetMemoryInfo = windows.NewLazySystemDLL("kernel32.dll").NewProc("K32GetProcessMemoryInfo")

// processMemoryCounters 对应 PROCESS_MEMORY_COUNTERS。
type processMemoryCounters struct {
	cb                         uint32
	PageFaultCount             uint32
	PeakWorkingSetSize         uintptr
	WorkingSetSize             uintptr
	QuotaPeakPagedPoolUsage    uintptr
	QuotaPagedPoolUsage        uintptr
	QuotaPeakNonPagedPoolUsage uintptr
	QuotaNonPagedPoolUsage     uintptr
	PagefileUsage              uintptr
	PeakPagefileUsage          uintptr
}

type winEntry struct {
	pid, ppid int
	exe       string
}

func snapshotEntries() ([]winEntry, error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(snap)
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	var out []winEntry
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		out = append(out, winEntry{pid: int(e.ProcessID), ppid: int(e.ParentProcessID), exe: windows.UTF16ToString(e.ExeFile[:])})
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return nil, err
	}
	return out, nil
}

// fmtStart 把启动时间写成和 ps lstart（LC_ALL=C）一样的格式，lstartTime 能解析回来。
func fmtStart(t time.Time) string { return t.Local().Format("Mon Jan 2 15:04:05 2006") }

// procDetail 打开进程读启动时间、CPU 时间、内存和命令行；系统进程、别的用户的进程打不开时 ok 为 false。
func procDetail(pid int, withCmd bool) (start time.Time, cpu time.Duration, rssKB int64, cmd string, ok bool) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return
	}
	defer windows.CloseHandle(h)
	var created, exited, kernel, user windows.Filetime
	if windows.GetProcessTimes(h, &created, &exited, &kernel, &user) != nil {
		return
	}
	start = time.Unix(0, created.Nanoseconds())
	cpu = time.Duration((uint64(kernel.HighDateTime)<<32|uint64(kernel.LowDateTime))+(uint64(user.HighDateTime)<<32|uint64(user.LowDateTime))) * 100
	var mc processMemoryCounters
	mc.cb = uint32(unsafe.Sizeof(mc))
	if r, _, _ := procGetMemoryInfo.Call(uintptr(h), uintptr(unsafe.Pointer(&mc)), uintptr(mc.cb)); r != 0 {
		rssKB = int64(mc.WorkingSetSize / 1024)
	}
	if withCmd {
		cmd = commandLine(h)
	}
	return start, cpu, rssKB, cmd, true
}

// commandLine 读进程的命令行（ProcessCommandLineInformation，Windows 8.1 起可用，只需要查询权限）。
func commandLine(h windows.Handle) string {
	buf := make([]byte, 4096)
	for range 3 {
		var n uint32
		err := windows.NtQueryInformationProcess(h, windows.ProcessCommandLineInformation, unsafe.Pointer(&buf[0]), uint32(len(buf)), &n)
		if err == nil {
			us := (*windows.NTUnicodeString)(unsafe.Pointer(&buf[0]))
			if us.Length == 0 || us.Buffer == nil {
				return ""
			}
			return windows.UTF16ToString(unsafe.Slice(us.Buffer, us.Length/2))
		}
		if n <= uint32(len(buf)) {
			return ""
		}
		buf = make([]byte, n)
	}
	return ""
}

// normCmd 把 Windows 命令行规整成“程序名 参数…”（见文件头）。拆不开时退回原样。
func normCmd(line, exe string) string {
	if line == "" {
		return strings.TrimSuffix(strings.TrimSuffix(exe, ".exe"), ".EXE")
	}
	args, err := windows.DecomposeCommandLine(line)
	if err != nil || len(args) == 0 {
		return line
	}
	args[0] = progName(args[0])
	return strings.Join(args, " ")
}

// listProcs 读一次本机进程表。
func listProcs() ([]proc, error) {
	ents, err := snapshotEntries()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	starts := make(map[int]time.Time, len(ents))
	ps := make([]proc, 0, len(ents))
	for _, e := range ents {
		if e.pid <= 4 { // System Idle Process 与 System
			continue
		}
		start, cpuTime, rss, line, ok := procDetail(e.pid, true)
		if !ok {
			continue
		}
		age := now.Sub(start)
		cpu := 0.0
		if age > 0 {
			cpu = float64(int(float64(cpuTime)/float64(age)*1000+0.5)) / 10 // 进程一生的平均占用（同 Linux ps）
		}
		starts[e.pid] = start
		ps = append(ps, proc{pid: e.pid, ppid: e.ppid, age: age, cmd: normCmd(line, e.exe), start: fmtStart(start), cpu: cpu, rss: rss})
	}
	// Windows 不会把孤儿进程交给 1 号进程：父进程退出后 ppid 照旧，进程号还可能被新进程复用。
	// 父进程不在、或启动得比自己晚，都当作没有父进程（同 Unix 上被系统接管）。
	for i := range ps {
		ps1, ok := starts[ps[i].ppid]
		if !ok || ps1.After(starts[ps[i].pid]) {
			ps[i].ppid = 1
		}
	}
	return ps, nil
}

// procQuery 查一个进程的父进程号、启动时间（同 proc.start 的格式）和命令行；进程不在时 ok 为 false。
func procQuery(pid int) (ppid int, start, cmd string, ok bool) {
	ents, err := snapshotEntries()
	if err != nil {
		return 0, "", "", false
	}
	var self *winEntry
	for i := range ents {
		if ents[i].pid == pid {
			self = &ents[i]
			break
		}
	}
	if self == nil {
		return 0, "", "", false
	}
	st, _, _, line, ok := procDetail(pid, true)
	if !ok {
		return 0, "", "", false
	}
	ppid = self.ppid
	if pst, _, _, _, pok := procDetail(ppid, false); !pok || pst.After(st) {
		ppid = 1 // 父进程已退出或进程号被复用（见 listProcs）
	}
	return ppid, fmtStart(st), normCmd(line, self.exe), true
}

// readEnvs 读进程环境里的 Claude Code 标记：从 PEB 找到进程参数块，再读环境块（只认本用户能打开的进程）。
func readEnvs(ps []proc) map[int]procEnv {
	out := map[int]procEnv{}
	for _, p := range ps {
		env := processEnv(p.pid)
		if !strings.Contains(env, "CLAUDE_PID=") {
			continue
		}
		for _, kv := range strings.Split(env, "\x00") {
			envMark(out, p.pid, kv)
		}
	}
	return dropUnmarked(out)
}

const maxEnvBytes = 1 << 20

// processEnv 读另一个进程的环境块，原样返回（变量之间以 \x00 分开）；读不到时返回空串。
func processEnv(pid int) string {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_VM_READ, false, uint32(pid))
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(h)
	var pbi windows.PROCESS_BASIC_INFORMATION
	if windows.NtQueryInformationProcess(h, windows.ProcessBasicInformation, unsafe.Pointer(&pbi), uint32(unsafe.Sizeof(pbi)), nil) != nil || pbi.PebBaseAddress == nil {
		return ""
	}
	var peb windows.PEB
	if !readMem(h, uintptr(unsafe.Pointer(pbi.PebBaseAddress)), unsafe.Pointer(&peb), unsafe.Sizeof(peb)) || peb.ProcessParameters == nil {
		return ""
	}
	var params windows.RTL_USER_PROCESS_PARAMETERS
	addr := uintptr(unsafe.Pointer(peb.ProcessParameters))
	// 参数块的实际长度随系统版本变化：只读到 EnvironmentSize 为止（Vista 起都有）
	need := unsafe.Offsetof(params.EnvironmentSize) + unsafe.Sizeof(params.EnvironmentSize)
	if !readMem(h, addr, unsafe.Pointer(&params), need) || params.Environment == nil {
		return ""
	}
	size := params.EnvironmentSize
	if size == 0 || size > maxEnvBytes {
		size = maxEnvBytes / 16
	}
	buf := make([]uint16, size/2)
	var n uintptr
	if err := windows.ReadProcessMemory(h, uintptr(params.Environment), (*byte)(unsafe.Pointer(&buf[0])), size, &n); err != nil && n == 0 {
		return ""
	}
	buf = buf[:n/2]
	// 环境块以连续两个 \x00 结束
	for i := 0; i+1 < len(buf); i++ {
		if buf[i] == 0 && buf[i+1] == 0 {
			buf = buf[:i]
			break
		}
	}
	return string(utf16Decode(buf))
}

func readMem(h windows.Handle, addr uintptr, dst unsafe.Pointer, size uintptr) bool {
	var n uintptr
	return windows.ReadProcessMemory(h, addr, (*byte)(dst), size, &n) == nil && n == size
}

// utf16Decode 解码 UTF-16，保留其中的 \x00（windows.UTF16ToString 遇到 \x00 就截断）。
func utf16Decode(s []uint16) []rune {
	out := make([]rune, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := rune(s[i])
		if c >= 0xd800 && c < 0xdc00 && i+1 < len(s) && s[i+1] >= 0xdc00 && s[i+1] < 0xe000 {
			c = (c-0xd800)<<10 | (rune(s[i+1]) - 0xdc00) + 0x10000
			i++
		}
		out = append(out, c)
	}
	return out
}

// listenPorts 查这些进程在监听的 TCP 端口（netstat -ano；查不到时返回空）。
// 认“对端地址端口为 0”的行（监听中），不依赖状态列的文字（随系统语言变化）。
func listenPorts(pids []int) map[int][]int {
	out := map[int][]int{}
	if len(pids) == 0 {
		return out
	}
	want := make(map[int]bool, len(pids))
	for _, p := range pids {
		want[p] = true
	}
	c := exec.Command("netstat", "-ano", "-p", "TCP")
	c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	b, _ := c.Output()
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 5 || !strings.EqualFold(f[0], "TCP") || !strings.HasSuffix(f[2], ":0") {
			continue
		}
		pid, err := strconv.Atoi(f[len(f)-1])
		if err != nil || !want[pid] {
			continue
		}
		if i := strings.LastIndex(f[1], ":"); i >= 0 {
			if port, err := strconv.Atoi(f[1][i+1:]); err == nil && port > 0 && !containsInt(out[pid], port) {
				out[pid] = append(out[pid], port)
			}
		}
	}
	return out
}

func containsInt(s []int, v int) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// Windows 没有信号：先“请它退出”和“强制结束”都是 TerminateProcess，记下的名字区分是哪一步。
const stopSignalName, killSignalName = "terminate", "terminate-force"

// killProc 结束一个进程；进程已经不在时返回 errProcGone。
func killProc(pid int, force bool) error {
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		// 刚退出的进程有时报“拒绝访问”而不是“参数错误”：进程表里没有它就算已经不在
		if _, _, _, alive := procQuery(pid); errors.Is(err, windows.ERROR_INVALID_PARAMETER) || !alive {
			return errProcGone
		}
		return err
	}
	defer windows.CloseHandle(h)
	return windows.TerminateProcess(h, 1)
}
