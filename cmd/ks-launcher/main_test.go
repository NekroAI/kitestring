package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

// 构建一次启动器，跑真实的子进程：退出码原样返回、停止信号转给子进程、程序不存在时返回 127。
func buildLauncher(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("启动器只用于 macOS；Windows 上没有这些信号")
	}
	bin := filepath.Join(t.TempDir(), "ks-launcher")
	out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput()
	if err != nil {
		t.Fatalf("构建启动器失败：%v %s", err, out)
	}
	return bin
}

func TestLauncher(t *testing.T) {
	bin := buildLauncher(t)

	t.Run("退出码原样返回，参数和环境变量照传", func(t *testing.T) {
		cmd := exec.Command(bin, "/bin/sh", "-c", `printf '%s|%s' "$0" "$KS_T"; exit 3`, "arg0")
		cmd.Env = append(os.Environ(), "KS_T=v")
		out, err := cmd.Output()
		if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 3 {
			t.Fatalf("应返回子进程的退出码 3：%v", err)
		}
		if string(out) != "arg0|v" {
			t.Fatalf("参数或环境变量没照传：%q", out)
		}
	})

	t.Run("SIGTERM 转给子进程，子进程收尾后的退出码原样返回", func(t *testing.T) {
		dir := t.TempDir()
		ready := filepath.Join(dir, "ready")
		done := filepath.Join(dir, "done")
		script := `trap 'touch "` + done + `"; exit 7' TERM; touch "` + ready + `"; while :; do sleep 0.05; done`
		cmd := exec.Command(bin, "/bin/sh", "-c", script)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		waitFile(t, ready)
		_ = cmd.Process.Signal(syscall.SIGTERM)
		err := cmd.Wait()
		if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 7 {
			t.Fatalf("应返回子进程收尾后的退出码 7：%v", err)
		}
		if _, err := os.Stat(done); err != nil {
			t.Fatal("子进程没有收到 SIGTERM")
		}
	})

	t.Run("子进程被信号杀掉时返回 128 + 信号值", func(t *testing.T) {
		err := exec.Command(bin, "/bin/sh", "-c", "kill -KILL $$").Run()
		if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 128+9 {
			t.Fatalf("应返回 137：%v", err)
		}
	})

	t.Run("程序不存在返回 127，没有参数返回 2", func(t *testing.T) {
		out, err := exec.Command(bin, "/no/such/ks").CombinedOutput()
		if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 127 || !strings.Contains(string(out), "/no/such/ks") {
			t.Fatalf("%v %s", err, out)
		}
		if err := exec.Command(bin).Run(); err == nil || err.(*exec.ExitError).ExitCode() != 2 {
			t.Fatalf("%v", err)
		}
	})
}

func waitFile(t *testing.T, p string) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if _, err := os.Stat(p); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("等不到 %s", p)
}
