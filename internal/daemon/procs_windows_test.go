//go:build windows

package daemon

import (
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// 子进程：TestProcsHelper 由下面的测试以 KS_PROCS_HELPER=1 拉起，监听一个端口后一直等着被结束。
func TestProcsHelper(t *testing.T) {
	if os.Getenv("KS_PROCS_HELPER") != "1" {
		t.Skip("只作为子进程运行")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		os.Exit(2)
	}
	os.Stdout.WriteString(ln.Addr().String() + "\n")
	time.Sleep(time.Minute)
}

func TestWindowsProcs(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^TestProcsHelper$")
	cmd.Env = append(os.Environ(), "KS_PROCS_HELPER=1", "CLAUDE_PID=4242", "CLAUDE_CODE_SESSION_ID=sess-中文")
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	buf := make([]byte, 64)
	n, _ := out.Read(buf)
	addr := strings.TrimSpace(string(buf[:n]))
	child := cmd.Process.Pid

	ps, err := listProcs()
	if err != nil {
		t.Fatal(err)
	}
	byPid, _ := procIndex(ps)
	self, ok := byPid[os.Getpid()]
	if !ok {
		t.Fatalf("进程表里没有自己（%d 个进程）", len(ps))
	}
	if name := progName(exe); !strings.HasPrefix(self.cmd, name+" ") && self.cmd != name {
		t.Errorf("命令行没规整（应以 %s 开头）：%q", name, self.cmd)
	}
	if lstartTime(self.start).IsZero() {
		t.Errorf("启动时间解析不了：%q", self.start)
	}
	c, ok := byPid[child]
	if !ok || c.ppid != os.Getpid() {
		t.Fatalf("子进程不在或父进程不对：%+v", c)
	}
	if !under(byPid, c, os.Getpid()) {
		t.Error("under 认不出子进程")
	}
	ppid, start, qcmd, ok := procQuery(child)
	if !ok || ppid != os.Getpid() || start != c.start || qcmd != c.cmd {
		t.Errorf("procQuery 与进程表不一致：%d %q %q %v", ppid, start, qcmd, ok)
	}
	if !instanceAlive(procID(child, c.start)) {
		t.Error("instanceAlive 认为子进程不在")
	}

	envs := readEnvs(ps)
	e, ok := envs[child]
	if !ok || e.claudePid != 4242 || e.session != "sess-中文" {
		t.Errorf("读不到子进程环境：%+v %v", e, ok)
	}
	if _, ok := envs[os.Getpid()]; ok && os.Getenv("CLAUDE_PID") == "" {
		t.Error("自己没有 CLAUDE_PID 却被认出来了")
	}

	ports := listenPorts([]int{child})
	_, portStr, _ := strings.Cut(addr, ":")
	found := false
	for _, p := range ports[child] {
		if itoa(p) == portStr {
			found = true
		}
	}
	if !found {
		t.Errorf("没查到监听端口 %s：%v", addr, ports)
	}

	if err := killProc(child, false); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	if _, _, _, ok := procQuery(child); ok {
		t.Error("结束后进程还在")
	}
	if _, _, _, reused := procQuery(child); !reused {
		if err := killProc(child, true); err != errProcGone {
			t.Errorf("结束不存在的进程应返回 errProcGone，得到 %v", err)
		}
	}
}

func itoa(n int) string {
	b := []byte{}
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
