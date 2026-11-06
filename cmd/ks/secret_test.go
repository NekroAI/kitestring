package main

import (
	"os"
	"path/filepath"
	"testing"
)

// ks secret run（R-295）：值只加进子进程的环境；退出码原样带回；启动失败为 127。
func TestRunWithEnv(t *testing.T) {
	if code := runWithEnv([]string{"sh", "-c", `test "$KS_T_TOKEN" = "v 1&<x>" && test "$KS_T_ID" = acct`}, map[string]string{"KS_T_TOKEN": "v 1&<x>", "KS_T_ID": "acct"}); code != 0 {
		t.Fatalf("子进程应拿到环境变量：%d", code)
	}
	if os.Getenv("KS_T_TOKEN") != "" {
		t.Fatal("值不应进 ks 自己的环境")
	}
	if code := runWithEnv([]string{"sh", "-c", "exit 3"}, nil); code != 3 {
		t.Fatalf("退出码应原样带回：%d", code)
	}
	if code := runWithEnv([]string{"/no/such/cmd"}, nil); code != 127 {
		t.Fatalf("启动失败应为 127：%d", code)
	}
}

func TestParseSecretField(t *testing.T) {
	f := parseSecretField("CLOUDFLARE_API_TOKEN | API Token | 控制台 → API 令牌 | 选模板")
	if f["env"] != "CLOUDFLARE_API_TOKEN" || f["label"] != "API Token" || f["how"] != "控制台 → API 令牌 | 选模板" {
		t.Fatalf("%v", f)
	}
	if f := parseSecretField("X"); f["env"] != "X" || f["label"] != "" {
		t.Fatalf("%v", f)
	}
}

// 写入磁盘的文件权限 0600，已存在的也收紧。
func TestWritePrivate(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a", ".dev.vars")
	if err := writePrivate(p, "A=1\n"); err != nil {
		t.Fatal(err)
	}
	_ = os.Chmod(p, 0o644)
	if err := writePrivate(p, "A=2\n"); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(p)
	b, _ := os.ReadFile(p)
	if st.Mode().Perm() != 0o600 || string(b) != "A=2\n" {
		t.Fatalf("%v %q", st.Mode(), b)
	}
}
