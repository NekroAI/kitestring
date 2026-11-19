// Package scripts 只放脚本的测试（R-354）：deploy.sh 的备份目录、migrate-storage.sh 的拷贝与核对。
package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func runScript(t *testing.T, env []string, name string, args ...string) (string, int) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("脚本只在 macOS / Linux 上跑")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("没有 bash")
	}
	cmd := exec.Command("bash", append([]string{name}, args...)...)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return string(out), code
}

// deployEnv 是演示环境：数据、安装路径都在临时目录，launchctl 换成 true
func deployEnv(t *testing.T, home string, extra ...string) []string {
	bin := filepath.Join(home, "bin", "ks")
	return append([]string{"KS_HOME=" + home, "KS_BIN=" + bin, "KS_HUB_URL=http://127.0.0.1:1", "LAUNCHD_PREFIX=gui/0", "KS_LAUNCHCTL=true", "KS_BACKUP_DIR="}, extra...)
}

// R-354：指定的备份目录不可用（外置硬盘没挂上）时，deploy.sh 在构建前失败退出，不建目录、不动程序
func TestDeployBackupDirUnavailable(t *testing.T) {
	home := t.TempDir()
	missing := filepath.Join(home, "Volumes", "SSD", "backups")
	bin := filepath.Join(home, "bin", "ks")
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	cases := map[string][]string{
		"环境变量":       deployEnv(t, home, "KS_BACKUP_DIR="+missing),
		"backup-dir": deployEnv(t, home),
	}
	if err := os.WriteFile(filepath.Join(home, "backup-dir"), []byte(missing+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, env := range cases {
		for _, args := range [][]string{{"--dry-run"}, {}} {
			out, code := runScript(t, env, "deploy.sh", args...)
			if code == 0 || !strings.Contains(out, "备份目录不可用") || !strings.Contains(out, "没有改任何东西") {
				t.Fatalf("%s %v：应失败并说明（退出码 %d）：%s", name, args, code, out)
			}
			if strings.Contains(out, "目标提交") {
				t.Fatalf("%s %v：不该走到构建：%s", name, args, out)
			}
			for _, p := range []string{missing, filepath.Join(home, "backups")} {
				if _, err := os.Stat(p); err == nil {
					t.Fatalf("%s %v：不该建出 %s", name, args, p)
				}
			}
			if b, _ := os.ReadFile(bin); string(b) != "old" {
				t.Fatalf("%s %v：程序被动了", name, args)
			}
		}
	}
}

// R-354：backup-dir 指到可用目录时 dry-run 用它备份、清理旧备份也在它里面；不配置时用默认目录
func TestDeployBackupDirConfigured(t *testing.T) {
	home := t.TempDir()
	ext := filepath.Join(t.TempDir(), "ssd backups")
	if err := os.Mkdir(ext, 0o755); err != nil {
		t.Fatal(err)
	}
	out, code := runScript(t, deployEnv(t, home), "deploy.sh", "--dry-run")
	if code != 0 || !strings.Contains(out, "备份目录："+filepath.Join(home, "backups")+"（默认）") {
		t.Fatalf("默认目录（退出码 %d）：%s", code, out)
	}
	for i := 0; i < 12; i++ {
		if err := os.WriteFile(filepath.Join(ext, "ks-20260101-0000"+string(rune('a'+i))), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(home, "backup-dir"), []byte(ext+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, code = runScript(t, deployEnv(t, home), "deploy.sh", "--dry-run")
	if code != 0 || !strings.Contains(out, "备份目录："+ext+"（来自 "+filepath.Join(home, "backup-dir")+"）") {
		t.Fatalf("backup-dir（退出码 %d）：%s", code, out)
	}
	if !strings.Contains(out, "删除旧备份 "+ext+"/ks-20260101-0000a") || strings.Contains(out, "ks-20260101-0000c") {
		t.Fatalf("应只清理新目录里最旧的两份：%s", out)
	}
	// 环境变量优先于 backup-dir
	env2 := t.TempDir()
	out, code = runScript(t, deployEnv(t, home, "KS_BACKUP_DIR="+env2), "deploy.sh", "--dry-run")
	if code != 0 || !strings.Contains(out, "备份目录："+env2+"（来自 环境变量 KS_BACKUP_DIR）") {
		t.Fatalf("环境变量优先（退出码 %d）：%s", code, out)
	}
}

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for p, c := range files {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// R-354：迁移拷贝后核对通过才提示下一步；内容不一致时核对不通过、不删旧目录；删除要显式确认
func TestMigrateStorage(t *testing.T) {
	ks := t.TempDir()
	src := filepath.Join(t.TempDir(), "files")
	writeTree(t, src, map[string]string{"a.png": "A", "sub/b c.txt": "B", "proto/x/index.html": "<p>"})
	vol := t.TempDir()
	dst := filepath.Join(vol, "files")
	env := []string{"KS_HOME=" + ks}

	// 上一层不存在（硬盘没挂上）：拒绝
	out, code := runScript(t, env, "migrate-storage.sh", "copy", src, filepath.Join(vol, "nope", "files"))
	if code == 0 || !strings.Contains(out, "上一层不存在") {
		t.Fatalf("挂载点不在应拒绝（%d）：%s", code, out)
	}
	// dry-run 不拷贝
	out, code = runScript(t, env, "migrate-storage.sh", "copy", src, dst, "--dry-run")
	if _, err := os.Stat(dst); code != 0 || err == nil {
		t.Fatalf("dry-run 不该建目录（%d）：%s", code, out)
	}
	// 拷贝并核对通过
	out, code = runScript(t, env, "migrate-storage.sh", "copy", src, dst)
	if code != 0 || !strings.Contains(out, "结果：通过") || !strings.Contains(out, "旧目录文件数：3") || !strings.Contains(out, "下一步") {
		t.Fatalf("拷贝应通过（%d）：%s", code, out)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "sub", "b c.txt")); string(b) != "B" {
		t.Fatalf("没拷过去")
	}
	recs, _ := filepath.Glob(filepath.Join(ks, "migrations", "*-files.txt"))
	if len(recs) == 0 {
		t.Fatalf("没有核对记录")
	}
	// 新目录多出文件（切换后新传的）不算不通过
	writeTree(t, dst, map[string]string{"new.png": "N"})
	if out, code = runScript(t, env, "migrate-storage.sh", "verify", src, dst); code != 0 || !strings.Contains(out, "新目录多出：1") {
		t.Fatalf("多出文件应通过（%d）：%s", code, out)
	}
	// 不写确认参数：不删
	if out, code = runScript(t, env, "migrate-storage.sh", "delete-old", src, dst); code == 0 || !strings.Contains(out, "--confirm-delete-old") {
		t.Fatalf("没确认应拒绝（%d）：%s", code, out)
	}
	// 内容被改：核对不通过，带确认也不删
	writeTree(t, dst, map[string]string{"a.png": "X"})
	os.Remove(filepath.Join(dst, "proto", "x", "index.html"))
	out, code = runScript(t, env, "migrate-storage.sh", "delete-old", src, dst, "--confirm-delete-old")
	if code == 0 || !strings.Contains(out, "结果：不通过") || !strings.Contains(out, "不一致\t./a.png") || !strings.Contains(out, "缺少\t./proto/x/index.html") {
		t.Fatalf("应核对不通过（%d）：%s", code, out)
	}
	if _, err := os.Stat(filepath.Join(src, "a.png")); err != nil {
		t.Fatalf("核对不通过时旧目录被删了")
	}
	// 重新拷贝后：dry-run 不删，确认后删除旧目录
	if out, code = runScript(t, env, "migrate-storage.sh", "copy", src, dst); code != 0 {
		t.Fatalf("重拷应通过（%d）：%s", code, out)
	}
	if out, code = runScript(t, env, "migrate-storage.sh", "delete-old", src, dst, "--confirm-delete-old", "--dry-run"); code != 0 {
		t.Fatalf("dry-run 删除（%d）：%s", code, out)
	}
	if _, err := os.Stat(src); err != nil {
		t.Fatalf("dry-run 把旧目录删了")
	}
	if out, code = runScript(t, env, "migrate-storage.sh", "delete-old", src, dst, "--confirm-delete-old"); code != 0 || !strings.Contains(out, "已删除旧目录") {
		t.Fatalf("确认后应删除（%d）：%s", code, out)
	}
	if _, err := os.Stat(src); err == nil {
		t.Fatalf("旧目录还在")
	}
}

// R-382：hub 的 plist 缺 ProcessType 时，dry-run 说明会先备份再补上、hub 改用 bootout + bootstrap 重启；已有的不动，照旧 kickstart
func TestDeployHubPlistProcessType(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("只在 macOS 上补 plist")
	}
	home := t.TempDir()
	plist := filepath.Join(t.TempDir(), "ai.nekro.kitestring.hub.plist")
	write := func(extra string) {
		c := "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<plist version=\"1.0\">\n<dict>\n\t<key>Label</key>\n\t<string>ai.nekro.kitestring.hub</string>\n" + extra + "</dict>\n</plist>\n"
		if err := os.WriteFile(plist, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("")
	out, code := runScript(t, deployEnv(t, home, "KS_HUB_PLIST="+plist), "deploy.sh", "--dry-run")
	if code != 0 || !strings.Contains(out, "补 ProcessType Interactive") || !strings.Contains(out, "hub-plist-") ||
		!strings.Contains(out, "bootout gui/0/ai.nekro.kitestring.hub") || !strings.Contains(out, "bootstrap gui/0 "+plist) ||
		strings.Contains(out, "kickstart -k gui/0/ai.nekro.kitestring.hub\n") {
		t.Fatalf("缺 ProcessType 时应备份、补上并 bootout + bootstrap（退出码 %d）：%s", code, out)
	}
	if b, _ := os.ReadFile(plist); strings.Contains(string(b), "ProcessType") {
		t.Fatal("dry-run 不该改 plist")
	}
	write("\t<key>ProcessType</key>\n\t<string>Interactive</string>\n")
	out, code = runScript(t, deployEnv(t, home, "KS_HUB_PLIST="+plist), "deploy.sh", "--dry-run")
	if code != 0 || !strings.Contains(out, "hub 的 plist 不用改") || strings.Contains(out, "bootout") ||
		!strings.Contains(out, "kickstart -k gui/0/ai.nekro.kitestring.hub") {
		t.Fatalf("已有 ProcessType 时不动、照旧 kickstart（退出码 %d）：%s", code, out)
	}
}
