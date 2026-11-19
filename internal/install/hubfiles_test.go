package install

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// R-354：三种服务定义都能原样读回启动参数（含空格、引号、&、反斜杠）
func TestParseServiceArgvRoundTrip(t *testing.T) {
	cases := [][]string{
		{"/bin/ks", "hub", "serve", "--addr", "0.0.0.0:7310"},
		{"/Users/a b/ks", "hub", "serve", "--addr", "0.0.0.0:7310", "--files-dir", "/Volumes/Data SSD/it's & <ks>"},
		{`C:\Program Files\ks.exe`, "hub", "serve", "--files-dir", `D:\ks files\`, "--service"},
		{"/bin/ks", "hub", "serve", "--files-dir", `/a/"q"\b`},
	}
	for _, argv := range cases {
		if got := parseServiceArgv("darwin", plistContent("x.hub", argv, "/tmp/hub.log", true)); !slices.Equal(got, argv) {
			t.Errorf("plist：%q -> %q", argv, got)
		}
		if got := parseServiceArgv("linux", unitContent("hub", argv)); !slices.Equal(got, argv) {
			t.Errorf("systemd：%q -> %q", argv, got)
		}
		got := parseServiceArgv("windows", decodeUTF16File(utf16File(taskXML("hub", `PC\me`, `C:\Windows\System32\conhost.exe`, argv, true))))
		if len(got) == 0 || got[0] != "--headless" || !slices.Equal(got[1:], argv) {
			t.Errorf("任务计划：%q -> %q", argv, got)
		}
	}
}

// writeHubConf 在 home 下写一份 goos 平台的 hub 服务定义
func writeHubConf(t *testing.T, goos, home string, argv []string) {
	t.Helper()
	var data []byte
	switch goos {
	case "darwin":
		data = []byte(plistContent(launchLabel+".hub", argv, "/tmp/hub.log", true))
	case "linux":
		data = []byte(unitContent("hub", argv))
	case "windows":
		data = utf16File(taskXML("hub", `PC\me`, "conhost.exe", append(argv, "--service"), true))
	}
	p := serviceConfPath(goos, home, "hub")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// R-354：不写 --hub-files-dir 时沿用已安装服务里的目录；显式 default 改回默认；写了新目录用新目录
func TestResolveHubFilesDir(t *testing.T) {
	ext := "/Volumes/Data SSD/ks files"
	for _, goos := range []string{"darwin", "linux", "windows"} {
		home := t.TempDir()
		// 没装过：默认
		o := Options{}
		if note := resolveHubFilesDir(&o, goos, home); o.HubFilesDir != "" || note != "" {
			t.Errorf("%s 没装过：%q %q", goos, o.HubFilesDir, note)
		}
		writeHubConf(t, goos, home, []string{"/bin/ks", "hub", "serve", "--addr", "0.0.0.0:7310", "--files-dir", ext})
		// 不写：沿用
		o = Options{}
		note := resolveHubFilesDir(&o, goos, home)
		if o.HubFilesDir != ext || !strings.Contains(note, "沿用") {
			t.Errorf("%s 沿用：%q %q", goos, o.HubFilesDir, note)
		}
		// 显式改回默认
		o = Options{HubFilesDirDefault: true}
		note = resolveHubFilesDir(&o, goos, home)
		if o.HubFilesDir != "" || !strings.Contains(note, "改回默认") {
			t.Errorf("%s 改回默认：%q %q", goos, o.HubFilesDir, note)
		}
		if got := hubArgv(o); slices.Contains(got, "--files-dir") {
			t.Errorf("%s 改回默认后启动参数仍带 --files-dir：%q", goos, got)
		}
		// 写了新目录
		o = Options{HubFilesDir: "/mnt/new"}
		note = resolveHubFilesDir(&o, goos, home)
		if o.HubFilesDir != "/mnt/new" || !strings.Contains(note, ext) {
			t.Errorf("%s 新目录：%q %q", goos, o.HubFilesDir, note)
		}
	}
	// 装过但没配置附件目录：默认
	home := t.TempDir()
	writeHubConf(t, "darwin", home, []string{"/bin/ks", "hub", "serve", "--addr", "0.0.0.0:7310"})
	o := Options{}
	if resolveHubFilesDir(&o, "darwin", home); o.HubFilesDir != "" {
		t.Errorf("没配置过：%q", o.HubFilesDir)
	}
}

// R-354：ks install --hub --dry-run 不写 --hub-files-dir 时，要写的服务定义沿用原来的附件目录
func TestRunHubKeepsFilesDir(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("只在 macOS / Linux 上跑")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeHubConf(t, runtime.GOOS, home, []string{"/bin/ks", "hub", "serve", "--addr", "0.0.0.0:7310", "--files-dir", "/Volumes/X/files"})
	var out bytes.Buffer
	if err := Run(Options{KS: "/bin/ks", Hub: true, HubAddr: "0.0.0.0:7310", DryRun: true, Out: &out}, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "沿用已安装服务的配置：/Volumes/X/files") {
		t.Fatalf("输出没说明沿用：%s", out.String())
	}
}
