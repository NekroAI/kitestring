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
	if f := parseSecretField("X"); f["env"] != "X" || f["label"] != nil {
		t.Fatalf("%v", f)
	}
}

// --plain 声明不敏感的值（R-318）：只标写到的那个；写了没有的值时报错。
func TestSecretFieldsPlain(t *testing.T) {
	fl, err := secretFields([]string{"ADMIN_USERNAME|用户名", "ADMIN_PASSWORD|密码"}, []string{" ADMIN_USERNAME "})
	if err != nil || fl[0]["plain"] != true || fl[1]["plain"] != nil {
		t.Fatalf("%v %v", fl, err)
	}
	if _, err := secretFields([]string{"A"}, []string{"B"}); err == nil {
		t.Fatal("--plain 写了没有的值应报错")
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

// 程序解析（D-1450 审查第 3 条）：裸程序名按 PATH 找成绝对路径（viaPath = true）；写了路径的照写的算（viaPath = false）；找不到报错。
func TestResolveProgram(t *testing.T) {
	p, via, err := resolveProgram("sh")
	if err != nil || !filepath.IsAbs(p) || !via {
		t.Fatalf("sh → %q %v %v", p, via, err)
	}
	dir := t.TempDir()
	exe := filepath.Join(dir, "wrangler")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if p, via, err := resolveProgram(exe); err != nil || p != exe || via {
		t.Fatalf("写了路径：%q %v %v", p, via, err)
	}
	t.Chdir(dir)
	if p, via, err := resolveProgram("./wrangler"); err != nil || !filepath.IsAbs(p) || via {
		t.Fatalf("./wrangler：%q %v %v", p, via, err)
	}
	if _, _, err := resolveProgram("no-such-cmd-295"); err == nil {
		t.Fatal("找不到的命令应报错")
	}
}

// R-320：ks secret amend 只发写了的项（没写 = 不改）。
func TestSecretAmendBody(t *testing.T) {
	b := secretAmendBody("k", "n", "r", "", -1, false, false, false, nil, []string{"sh -c *"}, []string{"sh -c x"}, nil, nil)
	if _, ok := b["replace"]; ok {
		t.Fatal("没写 --replace 不发 replace")
	}
	if _, ok := b["days"]; ok {
		t.Fatal("没写 --days 不发 days（到期时间不变）")
	}
	if _, ok := b["disk"]; ok {
		t.Fatal("没写 --disk / --no-disk 不发 disk")
	}
	if b["add_allowed"].([]string)[0] != "sh -c *" || b["remove_allowed"].([]string)[0] != "sh -c x" {
		t.Fatalf("增删命令：%v", b)
	}
	b = secretAmendBody("k", "n", "r", "", -1, false, false, false, nil, nil, nil, nil, []string{"GITHUB_TOKEN|GitHub → Settings | Tokens", " A_ID "})
	if rs := b["replace"].([]map[string]string); len(rs) != 2 || rs[0]["env"] != "GITHUB_TOKEN" || rs[0]["how"] != "GitHub → Settings | Tokens" || rs[1]["env"] != "A_ID" || rs[1]["how"] != "" {
		t.Fatalf("替换值（R-325）：%v", b["replace"])
	}
	b = secretAmendBody("k", "n", "r", "R-1", 0, true, false, true, nil, nil, nil, []string{"p"}, nil)
	if b["days"] != 0 || b["disk"] != false || b["any_command"] != true || b["projects"].([]string)[0] != "p" {
		t.Fatalf("长期、不允许写入磁盘、不限命令、项目：%v", b)
	}
}
