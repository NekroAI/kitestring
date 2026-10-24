package daemon

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/NekroAI/kitestring/internal/protocol"
)

func sessionEnds(t *testing.T, d *Daemon) map[string]string {
	evs, _, err := d.q.pending(100)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, e := range evs {
		if e.Name == "SessionEnd" {
			out[protocol.SessionKey(e.Tool, e.SessionID)] = e.Reason + "|" + e.Instance
		}
	}
	return out
}

// R-204：daemon 重启后实例表是空的，Hub 下发的实例里进程已退出的会话要补发结束；还活着的（含挂着 ks wait 的）不动。
func TestSeededInstancesEndedWhenProcessGone(t *testing.T) {
	d := newQDaemon(t, t.TempDir())
	defer d.q.close()
	d.seedInstances([]protocol.SessionInstance{
		{Key: "claude:dead1", Instance: "8787@Fri Oct 23 21:28:04 2026"},
		{Key: "claude:dead2", Instance: "8787@Fri Oct 23 21:28:04 2026"},   // 同一进程里 /clear 出的第二个会话
		{Key: "claude:waiting", Instance: "5389@Sat Oct 24 16:16:30 2026"}, // 进程还在，ks wait 挂着
		{Key: "claude:old", Instance: "5389@Sat Oct 24 16:16:30 2026", PrevInstance: "1@x"},
	})
	d.checkInstances(func(inst string) bool { return strings.HasPrefix(inst, "5389@") })
	got := sessionEnds(t, d)
	if len(got) != 3 || got["claude:dead1"] == "" || got["claude:dead2"] == "" || got["claude:old"] != "process_gone|1@x" {
		t.Fatalf("得到 %v", got)
	}
	if _, ok := got["claude:waiting"]; ok {
		t.Fatal("进程还在的会话（ks wait）不该被结束")
	}
	// 结束过的不再重复补发：此后进程也没了的 waiting 才补发，其余仍是原来的三条
	d.checkInstances(func(string) bool { return false })
	if g := sessionEnds(t, d); len(g) != 4 || g["claude:waiting"] == "" {
		t.Fatalf("重复补发或漏补：%v", g)
	}
}

// pid 被复用（进程号相同、启动时间不同）按已退出处理；真实进程（启动时间对得上）不误判。
func TestInstanceAlivePidReuse(t *testing.T) {
	pid := strconv.Itoa(os.Getpid())
	cmd := exec.Command("ps", "-o", "lstart=", "-p", pid)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.Output()
	if err != nil {
		t.Skip("没有 ps")
	}
	start := strings.Join(strings.Fields(string(out)), " ")
	if !instanceAlive(pid + "@" + start) {
		t.Fatal("启动时间对得上，应判定还在")
	}
	if instanceAlive(pid + "@Mon Jan  1 00:00:00 2001") {
		t.Fatal("pid 相同但启动时间不同（pid 被复用），应判定已退出")
	}
}
