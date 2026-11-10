//go:build !windows

package daemon

import (
	"bufio"
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

// 进程表的 macOS / Linux 实现：ps 读进程表，ps -E 或 /proc 读环境，lsof 查监听端口，kill 发信号。
// Windows 的实现见 procs_windows.go。

// listProcs 读一次本机进程表。
func listProcs() ([]proc, error) {
	cmd := exec.Command("ps", "-A", "-ww", "-o", "pid=,ppid=,%cpu=,rss=,stat=,lstart=,etime=,command=")
	cmd.Env = psEnv()
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var ps []proc
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 12 { // pid ppid %cpu rss stat lstart（5 段）etime 命令
			continue
		}
		if strings.HasPrefix(f[4], "Z") {
			continue // 僵尸进程已经退出，只是父进程还没收尸
		}
		pid, err1 := strconv.Atoi(f[0])
		ppid, err2 := strconv.Atoi(f[1])
		if err1 != nil || err2 != nil {
			continue
		}
		cpu, _ := strconv.ParseFloat(f[2], 64)
		rss, _ := strconv.ParseInt(f[3], 10, 64)
		ps = append(ps, proc{pid: pid, ppid: ppid, cpu: cpu, rss: rss, start: strings.Join(f[5:10], " "), age: parseEtime(f[10]), cmd: strings.Join(f[11:], " ")})
	}
	return ps, nil
}

// psEnv：启动时间按 C 的格式写（lstart 的段数固定），命令行按 UTF-8 原样输出（C 下中文会被转义成 M-x，对不上调用，R-208）。
func psEnv() []string {
	env := make([]string, 0, len(os.Environ())+3)
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "LC_ALL=") && !strings.HasPrefix(kv, "LC_TIME=") && !strings.HasPrefix(kv, "LC_CTYPE=") {
			env = append(env, kv)
		}
	}
	return append(env, "LC_TIME=C", "LC_CTYPE=en_US.UTF-8")
}

// procQuery 查一个进程的父进程号、启动时间（同 proc.start 的格式）和命令行；进程不在时 ok 为 false。
func procQuery(pid int) (ppid int, start, cmd string, ok bool) {
	c := exec.Command("ps", "-o", "ppid=,lstart=,command=", "-p", strconv.Itoa(pid))
	c.Env = append(os.Environ(), "LC_ALL=C") // 固定 lstart 格式（中文环境下段数不同）
	out, err := c.Output()
	if err != nil {
		return 0, "", "", false // ps 找不到该进程时以非零退出
	}
	f := strings.Fields(string(out))
	if len(f) < 6 { // ppid + lstart（5 段）+ 命令
		return 0, "", "", false
	}
	ppid, _ = strconv.Atoi(f[0])
	return ppid, strings.Join(f[1:6], " "), strings.Join(f[6:], " "), true
}

// readEnvs 读进程环境里的 Claude Code 标记（只认本用户的进程，读不到的跳过）。
func readEnvs(ps []proc) map[int]procEnv {
	out := map[int]procEnv{}
	switch runtime.GOOS {
	case "linux":
		for _, p := range ps {
			b, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(p.pid), "environ"))
			if err != nil || !bytes.Contains(b, []byte("CLAUDE_PID=")) {
				continue
			}
			for _, kv := range bytes.Split(b, []byte{0}) {
				envMark(out, p.pid, string(kv))
			}
		}
	default:
		// macOS：ps -E 把环境接在命令后面，用空格分开；只取认得的几个键
		cmd := exec.Command("ps", "-E", "-A", "-ww", "-o", "pid=,command=")
		cmd.Env = append(os.Environ(), "LC_ALL=C")
		b, err := cmd.Output()
		if err != nil {
			return out
		}
		sc := bufio.NewScanner(bytes.NewReader(b))
		sc.Buffer(make([]byte, 1<<20), 8<<20)
		for sc.Scan() {
			line := sc.Text()
			if !strings.Contains(line, " CLAUDE_PID=") {
				continue
			}
			f := strings.Fields(line)
			pid, err := strconv.Atoi(f[0])
			if err != nil {
				continue
			}
			for _, kv := range f[1:] {
				envMark(out, pid, kv)
			}
		}
	}
	return dropUnmarked(out)
}

// listenPorts 查这些进程在监听的 TCP 端口（lsof；没有 lsof 或查不到时返回空）。
func listenPorts(pids []int) map[int][]int {
	out := map[int][]int{}
	if len(pids) == 0 {
		return out
	}
	list := make([]string, len(pids))
	for i, p := range pids {
		list[i] = strconv.Itoa(p)
	}
	b, _ := exec.Command("lsof", "-nP", "-a", "-iTCP", "-sTCP:LISTEN", "-p", strings.Join(list, ","), "-Fpn").Output()
	cur := 0
	for _, line := range strings.Split(string(b), "\n") {
		if line == "" {
			continue
		}
		switch line[0] {
		case 'p':
			cur, _ = strconv.Atoi(line[1:])
		case 'n':
			if i := strings.LastIndex(line, ":"); i >= 0 {
				if port, err := strconv.Atoi(line[i+1:]); err == nil && cur > 0 {
					out[cur] = append(out[cur], port)
				}
			}
		}
	}
	return out
}

// stopSignalName / killSignalName 是停止时记下的信号名：先请它退出，再强制结束。
const stopSignalName, killSignalName = "SIGTERM", "SIGKILL"

// killProc 停一个进程：force 为 false 发 SIGTERM，为 true 发 SIGKILL；进程已经不在时返回 errProcGone。
func killProc(pid int, force bool) error {
	sig := syscall.SIGTERM
	if force {
		sig = syscall.SIGKILL
	}
	err := syscall.Kill(pid, sig)
	if errors.Is(err, syscall.ESRCH) {
		return errProcGone
	}
	return err
}
