package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const oldScript = "#!/bin/sh\necho 'ks 0.2.0 (old)'\n"

// release 是假的发布源：tag 为空表示还没有发布版本。
type release struct {
	tag     string
	script  string // 包里 ks 的内容
	badSum  bool
	noSum   bool
	missing bool // 包本身 404
}

func tgz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		tw.Write([]byte(body))
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func serve(t *testing.T, r release) *httptest.Server {
	t.Helper()
	v := strings.TrimPrefix(r.tag, "v")
	name := fmt.Sprintf("kitestring-%s-linux-amd64.tar.gz", v)
	pkg := tgz(t, map[string]string{"./ks": r.script, "./LICENSE": "MIT"})
	sum := sha256.Sum256(pkg)
	hexSum := hex.EncodeToString(sum[:])
	if r.badSum {
		hexSum = strings.Repeat("0", 64)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/NekroAI/kitestring/releases/latest", func(w http.ResponseWriter, req *http.Request) {
		if r.tag == "" {
			http.Redirect(w, req, "/NekroAI/kitestring/releases", http.StatusFound)
			return
		}
		http.Redirect(w, req, "/NekroAI/kitestring/releases/tag/"+r.tag, http.StatusFound)
	})
	base := "/NekroAI/kitestring/releases/download/" + r.tag + "/"
	mux.HandleFunc(base+"SHA256SUMS.txt", func(w http.ResponseWriter, req *http.Request) {
		fmt.Fprintf(w, "%s  kitestring-%s-darwin-arm64.tar.gz\n", strings.Repeat("a", 64), v)
		if !r.noSum {
			fmt.Fprintf(w, "%s  %s\n", hexSum, name)
		}
	})
	mux.HandleFunc(base+name, func(w http.ResponseWriter, req *http.Request) {
		if r.missing {
			http.NotFound(w, req)
			return
		}
		w.Write(pkg)
	})
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

// setup 在临时目录放一个旧版 ks，返回它的路径。
func setup(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("假 ks 是 sh 脚本；Windows 的替换见 TestUpdateWindowsZip")
	}
	exe := filepath.Join(t.TempDir(), "ks")
	if err := os.WriteFile(exe, []byte(oldScript), 0o755); err != nil {
		t.Fatal(err)
	}
	return exe
}

func opts(s *httptest.Server, exe, cur string, out *bytes.Buffer, restarted *string) Options {
	return Options{
		Base: s.URL, Current: cur, Exe: exe, GOOS: "linux", GOARCH: "amd64", Out: out,
		Restart: func(e string) error { *restarted = e; return nil },
	}
}

func assertUnchanged(t *testing.T, exe string) {
	t.Helper()
	b, err := os.ReadFile(exe)
	if err != nil || string(b) != oldScript {
		t.Fatalf("旧版 ks 被改动：%q %v", b, err)
	}
	ents, _ := os.ReadDir(filepath.Dir(exe))
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".ks.update-") {
			t.Fatalf("留下了临时文件 %s", e.Name())
		}
	}
}

func TestUpdateReplacesAndRestarts(t *testing.T) {
	s := serve(t, release{tag: "v0.3.0", script: "#!/bin/sh\necho 'ks 0.3.0 (new)'\n"})
	exe := setup(t)
	var out bytes.Buffer
	var restarted string
	if err := Run(context.Background(), opts(s, exe, "0.2.0", &out, &restarted)); err != nil {
		t.Fatal(err, out.String())
	}
	b, _ := os.ReadFile(exe)
	if !strings.Contains(string(b), "0.3.0") {
		t.Fatalf("没换成新版：%q", b)
	}
	if st, _ := os.Stat(exe); st.Mode().Perm() != 0o755 {
		t.Fatalf("权限 %v", st.Mode())
	}
	if restarted != exe {
		t.Fatalf("没有重启服务：%q", restarted)
	}
	if !strings.Contains(out.String(), "已更新到 ks 0.3.0（原 0.2.0）") {
		t.Fatal(out.String())
	}
}

func TestAlreadyLatest(t *testing.T) {
	s := serve(t, release{tag: "v0.2.0", script: oldScript})
	for _, cur := range []string{"0.2.0", "0.10.0"} {
		exe := setup(t)
		var out bytes.Buffer
		var restarted string
		if err := Run(context.Background(), opts(s, exe, cur, &out, &restarted)); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "无需更新") || restarted != "" {
			t.Fatalf("%s：%s", cur, out.String())
		}
		assertUnchanged(t, exe)
	}
}

func TestCheckOnly(t *testing.T) {
	s := serve(t, release{tag: "v0.3.0", script: "#!/bin/sh\necho 'ks 0.3.0 (new)'\n"})
	exe := setup(t)
	var out bytes.Buffer
	var restarted string
	o := opts(s, exe, "0.2.0", &out, &restarted)
	o.Check = true
	if err := Run(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "有新版本 0.3.0（当前 0.2.0）") {
		t.Fatal(out.String())
	}
	assertUnchanged(t, exe)
}

func TestDevBuildNeedsForce(t *testing.T) {
	s := serve(t, release{tag: "v0.3.0", script: "#!/bin/sh\necho 'ks 0.3.0 (new)'\n"})
	for _, cur := range []string{"dev", "0.2.0-5-gabc1234-dirty", "c43102a"} {
		exe := setup(t)
		var out bytes.Buffer
		var restarted string
		err := Run(context.Background(), opts(s, exe, cur, &out, &restarted))
		if err == nil || !strings.Contains(err.Error(), "开发构建") || !strings.Contains(err.Error(), "--force") {
			t.Fatalf("%s：%v", cur, err)
		}
		assertUnchanged(t, exe)
	}
	exe := setup(t)
	var out bytes.Buffer
	var restarted string
	o := opts(s, exe, "dev", &out, &restarted)
	o.Force = true
	if err := Run(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(exe); !strings.Contains(string(b), "0.3.0") {
		t.Fatal("--force 没有替换")
	}
}

func TestFailuresKeepOldBinary(t *testing.T) {
	newScript := "#!/bin/sh\necho 'ks 0.3.0 (new)'\n"
	cases := []struct {
		name string
		r    release
		want string
	}{
		{"校验失败", release{tag: "v0.3.0", script: newScript, badSum: true}, "校验失败"},
		{"清单里没有", release{tag: "v0.3.0", script: newScript, noSum: true}, "SHA256SUMS.txt 里没有"},
		{"包不存在", release{tag: "v0.3.0", script: newScript, missing: true}, "HTTP 404"},
		{"新版跑不起来", release{tag: "v0.3.0", script: "#!/bin/sh\nexit 1\n"}, "无法在本机运行"},
		{"版本不符", release{tag: "v0.3.0", script: "#!/bin/sh\necho 'ks 0.2.9 (x)'\n"}, "版本不符"},
		{"还没有发布", release{tag: ""}, "还没有发布版本"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := serve(t, c.r)
			exe := setup(t)
			var out bytes.Buffer
			var restarted string
			err := Run(context.Background(), opts(s, exe, "0.2.0", &out, &restarted))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("错误应含 %q：%v", c.want, err)
			}
			if restarted != "" {
				t.Fatal("失败时不应重启服务")
			}
			assertUnchanged(t, exe)
		})
	}
}

func TestRenameFailureKeepsOld(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("假 ks 是 sh 脚本")
	}
	s := serve(t, release{tag: "v0.3.0", script: "#!/bin/sh\necho 'ks 0.3.0 (new)'\n"})
	// 让目标是一个非空目录：rename 必然失败
	dir := t.TempDir()
	exe := filepath.Join(dir, "ks")
	os.MkdirAll(filepath.Join(exe, "keep"), 0o755)
	var out bytes.Buffer
	var restarted string
	err := Run(context.Background(), opts(s, exe, "0.2.0", &out, &restarted))
	if err == nil || !strings.Contains(err.Error(), "替换") || !strings.Contains(err.Error(), "当前版本未改动") {
		t.Fatal(err)
	}
	if st, _ := os.Stat(filepath.Join(exe, "keep")); st == nil || restarted != "" {
		t.Fatal("原文件被动过或重启了服务")
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 {
		t.Fatalf("留下了临时文件：%v", ents)
	}
}

func TestNoWritePermission(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root 不受目录权限限制")
	}
	s := serve(t, release{tag: "v0.3.0", script: "#!/bin/sh\necho 'ks 0.3.0 (new)'\n"})
	exe := setup(t)
	dir := filepath.Dir(exe)
	os.Chmod(dir, 0o555)
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	var out bytes.Buffer
	var restarted string
	err := Run(context.Background(), opts(s, exe, "0.2.0", &out, &restarted))
	if err == nil || !strings.Contains(err.Error(), "没有写入") {
		t.Fatal(err)
	}
	assertUnchanged(t, exe)
}

func TestNetworkFailure(t *testing.T) {
	s := serve(t, release{tag: "v0.3.0"})
	url := s.URL
	s.Close()
	exe := setup(t)
	var out bytes.Buffer
	err := Run(context.Background(), Options{Base: url, Current: "0.2.0", Exe: exe, GOOS: "linux", GOARCH: "amd64", Out: &out})
	if err == nil || !strings.Contains(err.Error(), "检查网络") {
		t.Fatal(err)
	}
	assertUnchanged(t, exe)
}

func TestUnsupportedPlatform(t *testing.T) {
	err := Run(context.Background(), Options{Current: "0.2.0", GOOS: "freebsd", GOARCH: "amd64"})
	if err == nil || !strings.Contains(err.Error(), "暂不支持") {
		t.Fatal(err)
	}
}

// TestMain：测试程序带 KS_FAKE_VERSION 运行时充当发布包里的 ks.exe（ks version 打印这个版本）。
func TestMain(m *testing.M) {
	if v := os.Getenv("KS_FAKE_VERSION"); v != "" && len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Printf("ks %s (fake)\n", v)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// Windows 的发布包是 zip（包内 ks.exe）；正在运行的 ks.exe 不能覆盖，先挪成 .old-* 再换上，没人用时当场删掉。
// 用 GOOS=windows 的流程跑，任何系统上都能测；假 ks.exe 是测试程序自己。
func TestUpdateWindowsZip(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("ks.exe")
	w.Write(bin)
	w, _ = zw.Create("LICENSE")
	w.Write([]byte("MIT"))
	zw.Close()
	pkg := buf.Bytes()
	sum := sha256.Sum256(pkg)
	name := "kitestring-0.3.0-windows-amd64.zip"
	mux := http.NewServeMux()
	mux.HandleFunc("/NekroAI/kitestring/releases/latest", func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, "/NekroAI/kitestring/releases/tag/v0.3.0", http.StatusFound)
	})
	mux.HandleFunc("/NekroAI/kitestring/releases/download/v0.3.0/SHA256SUMS.txt", func(w http.ResponseWriter, req *http.Request) {
		fmt.Fprintf(w, "%s  %s\n", hex.EncodeToString(sum[:]), name)
	})
	mux.HandleFunc("/NekroAI/kitestring/releases/download/v0.3.0/"+name, func(w http.ResponseWriter, req *http.Request) { w.Write(pkg) })
	s := httptest.NewServer(mux)
	defer s.Close()
	t.Setenv("KS_FAKE_VERSION", "0.3.0")

	dir := t.TempDir()
	exe := filepath.Join(dir, "ks.exe")
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	stale := exe + ".old-1"
	if err := os.WriteFile(stale, []byte("older"), 0o755); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	var restarted string
	o := Options{Base: s.URL, Current: "0.2.0", Exe: exe, GOOS: "windows", GOARCH: "amd64", Out: &out,
		Restart: func(e string) error { restarted = e; return nil }}
	if err := Run(context.Background(), o); err != nil {
		t.Fatal(err, out.String())
	}
	got, _ := os.ReadFile(exe)
	if !bytes.Equal(got, bin) || restarted != exe {
		t.Fatalf("没换成新版或没重启：%d 字节，重启 %q", len(got), restarted)
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 {
		var names []string
		for _, e := range ents {
			names = append(names, e.Name())
		}
		t.Fatalf("应只剩 ks.exe（旧文件、上次留下的 .old-*、临时文件都清掉）：%v", names)
	}
}
