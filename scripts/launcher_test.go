package scripts

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// R-354：deploy.sh 用 install-launcher.sh 安装 / 核对 Hub 的固定启动器：没有就装，有就绝不替换。
func TestInstallLauncher(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "build", "ks-launcher")
	dst := filepath.Join(dir, "home", "bin", "ks-launcher")
	plist := filepath.Join(dir, "hub.plist")
	write := func(p, s string, mode os.FileMode) {
		t.Helper()
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(s), mode); err != nil {
			t.Fatal(err)
		}
		os.Chmod(p, mode)
	}
	read := func(p string) string { b, _ := os.ReadFile(p); return string(b) }

	write(src, "launcher v1", 0o755)
	write(plist, "<string>/u/ks</string>", 0o644) // 还没经启动器运行

	// dry-run 不动文件
	out, code := runScript(t, nil, "install-launcher.sh", "--dry-run", src, dst, plist)
	if code != 0 || !strings.Contains(out, "[dry-run] 安装启动器") {
		t.Fatalf("%d %s", code, out)
	}
	if _, err := os.Stat(dst); err == nil {
		t.Fatal("dry-run 不应安装")
	}

	// 没有：装上，可执行；提示怎么授权、怎么切换
	out, code = runScript(t, nil, "install-launcher.sh", src, dst, plist)
	if code != 0 || read(dst) != "launcher v1" || !strings.Contains(out, "完全磁盘访问权限") || !strings.Contains(out, "ks install --hub") {
		t.Fatalf("应装好启动器：%d %s", code, out)
	}
	if st, _ := os.Stat(dst); st.Mode().Perm() != 0o755 {
		t.Fatalf("权限 %v", st.Mode())
	}

	// 已有、内容不同：不替换，只提示
	write(src, "launcher v2", 0o755)
	write(plist, "<string>"+dst+"</string><string>/u/ks</string>", 0o644) // 已经经启动器运行
	out, code = runScript(t, nil, "install-launcher.sh", src, dst, plist)
	if code != 0 || read(dst) != "launcher v1" || !strings.Contains(out, "不替换") || strings.Contains(out, "ks install --hub") {
		t.Fatalf("已有的启动器不应被替换：%d %s %q", code, out, read(dst))
	}

	// 已有但不可执行：失败，不替换
	os.Chmod(dst, 0o644)
	out, code = runScript(t, nil, "install-launcher.sh", src, dst, plist)
	if code == 0 || read(dst) != "launcher v1" || !strings.Contains(out, "chmod 755") {
		t.Fatalf("不可执行时应失败：%d %s", code, out)
	}

	// 被删了、新构建里也没有：Hub 已经经它运行时失败（替换程序后 Hub 起不来）
	os.Remove(dst)
	os.Remove(src)
	out, code = runScript(t, nil, "install-launcher.sh", src, dst, plist)
	if code == 0 || !strings.Contains(out, "部署停止") {
		t.Fatalf("应失败：%d %s", code, out)
	}
	// Hub 没经启动器运行时只提示
	write(plist, "<string>/u/ks</string>", 0o644)
	if out, code = runScript(t, nil, "install-launcher.sh", src, dst, plist); code != 0 || !strings.Contains(out, "跳过") {
		t.Fatalf("应跳过：%d %s", code, out)
	}
}

// deploy.sh --dry-run 在替换程序之前列出启动器这一步（macOS）
func TestDeployDryRunLauncher(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("启动器只用于 macOS")
	}
	home := t.TempDir()
	plist := filepath.Join(home, "hub.plist")
	os.WriteFile(plist, []byte("<string>/u/ks</string>"), 0o644)
	out, code := runScript(t, deployEnv(t, home, "KS_HUB_PLIST="+plist), "deploy.sh", "--dry-run")
	if code != 0 {
		t.Fatalf("dry-run 失败：%s", out)
	}
	want := filepath.Join(home, "bin", "ks-launcher")
	i, j := strings.Index(out, "[dry-run] 安装启动器"), strings.Index(out, "rm "+filepath.Join(home, "bin", "ks"))
	if i < 0 || !strings.Contains(out, want) || j < 0 || i > j {
		t.Fatalf("应在替换程序之前安装启动器 %s：%s", want, out)
	}
	if _, err := os.Stat(want); err == nil {
		t.Fatal("dry-run 不应安装")
	}
}
