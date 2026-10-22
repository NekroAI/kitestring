package daemon

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

func TestInstanceAlive(t *testing.T) {
	cmd := exec.Command("ps", "-o", "lstart=", "-p", strconv.Itoa(os.Getpid()))
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.Output()
	if err != nil {
		t.Skip("ps 不可用")
	}
	self := strconv.Itoa(os.Getpid()) + "@" + strings.Join(strings.Fields(string(out)), " ")
	if !instanceAlive(self) {
		t.Fatalf("自己的进程应在运行：%s", self)
	}
	if instanceAlive(strconv.Itoa(os.Getpid()) + "@Mon Jan  1 00:00:00 2001") {
		t.Fatal("启动时间不同（进程号被复用）应视为已退出")
	}
	if instanceAlive("999999@Mon Jan  1 00:00:00 2001") {
		t.Fatal("不存在的进程应视为已退出")
	}
}
