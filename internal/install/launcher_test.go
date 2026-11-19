package install

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// R-354：hub 服务经固定启动器运行，plist 的 ProgramArguments 以启动器开头，沿用附件目录。
func TestHubArgvWithLauncher(t *testing.T) {
	o := Options{KS: "/u/.local/bin/ks", HubAddr: "0.0.0.0:7310", HubFilesDir: "/Volumes/D/ks files", HubLauncher: "/u/.kitestring/bin/ks-launcher"}
	want := []string{"/u/.kitestring/bin/ks-launcher", "/u/.local/bin/ks", "hub", "serve", "--addr", "0.0.0.0:7310", "--files-dir", "/Volumes/D/ks files"}
	argv := hubArgv(o)
	if !slices.Equal(argv, want) {
		t.Fatalf("%v", argv)
	}
	plist := plistContent(launchLabel+".hub", argv, "/u/.kitestring/logs/hub.log")
	if got := parseServiceArgv("darwin", plist); !slices.Equal(got, want) {
		t.Fatalf("plist 里的启动参数：%v", got)
	}
	// 重跑 ks install 时附件目录照常读回；部署时照常认出这是 ks 的服务、能重启
	if v := flagValue(parseServiceArgv("darwin", plist), "--files-dir"); v != "/Volumes/D/ks files" {
		t.Fatalf("读回附件目录：%q", v)
	}
	if !serviceUses(plist, "/u/.local/bin/ks") {
		t.Fatal("RestartServices 应认出用的是这个 ks")
	}
}

func launcherEnv(t *testing.T) (home, ksDir string, out *bytes.Buffer) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("KS_HOME", filepath.Join(home, ".kitestring"))
	ksDir = filepath.Join(home, "bin")
	if err := os.MkdirAll(ksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	return home, ksDir, &bytes.Buffer{}
}

func TestEnsureLauncherInstallsWhenMissing(t *testing.T) {
	home, ksDir, out := launcherEnv(t)
	if err := os.WriteFile(filepath.Join(ksDir, LauncherName), []byte("new launcher"), 0o755); err != nil {
		t.Fatal(err)
	}
	o := Options{KS: filepath.Join(ksDir, "ks"), Out: out}
	dst := filepath.Join(home, ".kitestring", "bin", LauncherName)

	// --dry-run 只说要装，不动文件
	o.DryRun = true
	if got := ensureLauncher(o, home); got != dst || !strings.Contains(out.String(), "将安装启动器") {
		t.Fatalf("%q %s", got, out)
	}
	if _, err := os.Stat(dst); err == nil {
		t.Fatal("dry-run 不应安装")
	}
	o.DryRun = false
	if got := ensureLauncher(o, home); got != dst {
		t.Fatalf("应装到 %s：%q %s", dst, got, out)
	}
	st, err := os.Stat(dst)
	if err != nil || st.Mode().Perm() != 0o755 {
		t.Fatalf("应装好且可执行：%v %v", st, err)
	}
	if b, _ := os.ReadFile(dst); string(b) != "new launcher" {
		t.Fatalf("内容：%q", b)
	}
	if !strings.Contains(out.String(), "完全磁盘访问") {
		t.Fatalf("首次安装应说明怎么授权：%s", out)
	}
}

func TestEnsureLauncherNeverReplaces(t *testing.T) {
	home, ksDir, out := launcherEnv(t)
	dst := LauncherPath(home)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("authorized launcher"), 0o755); err != nil {
		t.Fatal(err)
	}
	old, _ := os.Stat(dst)
	if err := os.WriteFile(filepath.Join(ksDir, LauncherName), []byte("different launcher"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := ensureLauncher(Options{KS: filepath.Join(ksDir, "ks"), Out: out}, home); got != dst {
		t.Fatalf("%q %s", got, out)
	}
	if b, _ := os.ReadFile(dst); string(b) != "authorized launcher" {
		t.Fatalf("已有的启动器不应被替换：%q", b)
	}
	if st, _ := os.Stat(dst); !st.ModTime().Equal(old.ModTime()) {
		t.Fatal("已有的启动器不应被改动")
	}
	// copyExecutable 本身也不覆盖已有文件（两个安装同时跑）
	if err := copyExecutable(filepath.Join(ksDir, LauncherName), dst); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dst); string(b) != "authorized launcher" {
		t.Fatalf("copyExecutable 不应覆盖：%q", b)
	}
}

func TestEnsureLauncherUnavailable(t *testing.T) {
	home, ksDir, out := launcherEnv(t)
	o := Options{KS: filepath.Join(ksDir, "ks"), Out: out}
	// ks 旁边没有启动器：不经启动器运行，并说明后果和办法
	if got := ensureLauncher(o, home); got != "" || !strings.Contains(out.String(), "make build") {
		t.Fatalf("%q %s", got, out)
	}
	// 已有但不可执行：不用它，也不替换它
	dst := LauncherPath(home)
	os.MkdirAll(filepath.Dir(dst), 0o755)
	os.WriteFile(dst, []byte("x"), 0o644)
	os.WriteFile(filepath.Join(ksDir, LauncherName), []byte("y"), 0o755)
	out.Reset()
	if got := ensureLauncher(o, home); got != "" || !strings.Contains(out.String(), "chmod 755") {
		t.Fatalf("%q %s", got, out)
	}
	if b, _ := os.ReadFile(dst); string(b) != "x" {
		t.Fatal("不可执行的启动器也不应被替换")
	}
}

type exitErr int

func (e exitErr) Error() string { return "exit status " + strconv.Itoa(int(e)) }
func (e exitErr) ExitCode() int { return int(e) }

func fakeLaunchctl(t *testing.T, fn func(args []string) ([]byte, error)) *[][]string {
	t.Helper()
	var calls [][]string
	oldL, oldP := launchctl, launchdPause
	launchctl = func(args ...string) ([]byte, error) {
		calls = append(calls, args)
		return fn(args)
	}
	launchdPause = func(time.Duration) {}
	t.Cleanup(func() { launchctl, launchdPause = oldL, oldP })
	return &calls
}

func countCmd(calls [][]string, sub string) int {
	n := 0
	for _, c := range calls {
		if c[0] == sub {
			n++
		}
	}
	return n
}

// R-367：bootout 还没完成时 bootstrap 报 5，等待并重试
func TestBootstrapRetries(t *testing.T) {
	prints, boots := 0, 0
	calls := fakeLaunchctl(t, func(args []string) ([]byte, error) {
		switch args[0] {
		case "print": // 旧实例前两次还在
			prints++
			if prints <= 2 {
				return []byte("state = running"), nil
			}
			return nil, exitErr(113)
		case "bootstrap":
			boots++
			if boots <= 2 {
				return []byte("Bootstrap failed: 5: Input/output error"), exitErr(5)
			}
		}
		return nil, nil
	})
	if err := bootstrap(Options{Out: &bytes.Buffer{}}, "gui/501", "ai.nekro.kitestring.hub", "/x.plist"); err != nil {
		t.Fatalf("重试后应成功：%v", err)
	}
	if countCmd(*calls, "print") != 3 || countCmd(*calls, "bootstrap") != 3 {
		t.Fatalf("应先等旧实例卸载再重试 bootstrap：%v", *calls)
	}
}

func TestBootstrapGivesUp(t *testing.T) {
	calls := fakeLaunchctl(t, func(args []string) ([]byte, error) {
		if args[0] == "print" {
			return nil, exitErr(113)
		}
		return []byte("Bootstrap failed: 5: Input/output error"), exitErr(5)
	})
	err := bootstrap(Options{Out: &bytes.Buffer{}}, "gui/501", "l", "/x.plist")
	if err == nil || !strings.Contains(err.Error(), "Input/output error") || countCmd(*calls, "bootstrap") != bootstrapAttempts {
		t.Fatalf("一直报 5 时重试 %d 次后报错：%v %v", bootstrapAttempts, err, *calls)
	}
	// 其它错误（如 plist 写错）不重试
	for _, e := range []error{exitErr(1), errors.New("launchctl 不存在")} {
		calls := fakeLaunchctl(t, func(args []string) ([]byte, error) {
			if args[0] == "print" {
				return nil, exitErr(113)
			}
			return []byte("bad plist"), e
		})
		if err := bootstrap(Options{Out: &bytes.Buffer{}}, "gui/501", "l", "/x.plist"); err == nil || countCmd(*calls, "bootstrap") != 1 {
			t.Fatalf("非 5 / 37 的错误不应重试：%v %v", err, *calls)
		}
	}
}

// launchd 注册 hub：plist 写进 home 下、bootstrap 经重试；启动器在 ProgramArguments 里
func TestLaunchdWritesLauncherPlist(t *testing.T) {
	home := t.TempDir()
	fakeLaunchctl(t, func(args []string) ([]byte, error) {
		if args[0] == "print" {
			return nil, exitErr(113)
		}
		return nil, nil
	})
	o := Options{KS: "/u/ks", HubAddr: "0.0.0.0:7310", HubLauncher: "/u/.kitestring/bin/ks-launcher", Out: &bytes.Buffer{}}
	if err := launchd(o, home, "hub", hubArgv(o), false); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(home, "Library", "LaunchAgents", launchLabel+".hub.plist"))
	if err != nil {
		t.Fatal(err)
	}
	if got := parseServiceArgv("darwin", string(b)); len(got) < 2 || got[0] != o.HubLauncher || got[1] != "/u/ks" {
		t.Fatalf("%v", got)
	}
}
