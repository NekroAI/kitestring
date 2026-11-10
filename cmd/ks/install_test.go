package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// 命令行入口层的隔离测试：临时 HOME + 假 claude 命令，绝不碰真实配置、launchd 或线上 Hub。
func installEnv(t *testing.T) (home, codexHooks, plist string) {
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // Windows 的家目录看 USERPROFILE
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	bin := t.TempDir()
	script, ext := "#!/bin/sh\nexit 0\n", ""
	if runtime.GOOS == "windows" {
		script, ext = "@exit /b 0\r\n", ".cmd"
	}
	// 假 claude / launchctl / systemctl / schtasks，万一走到服务注册也不会碰真实服务
	for _, n := range []string{"claude", "launchctl", "systemctl", "schtasks", "codex"} {
		if err := os.WriteFile(filepath.Join(bin, n+ext), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	codexHooks = filepath.Join(home, ".codex", "hooks.json")
	plist = filepath.Join(home, "Library", "LaunchAgents", "ai.nekro.kitestring.daemon.plist")
	switch runtime.GOOS {
	case "linux":
		plist = filepath.Join(home, ".config", "systemd", "user", "kitestring-daemon.service")
	case "windows":
		plist = filepath.Join(home, ".kitestring", "services", "daemon.xml")
	}
	return
}

func write(t *testing.T, p, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return "<missing>"
	}
	return string(b)
}

const ksHook = `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"/opt/ks hook claude Stop"}]}]}}`

func TestUninstallClaudeDirOnlyTouchesThatDir(t *testing.T) {
	home, codex, plist := installEnv(t)
	x := filepath.Join(home, ".claude-x")
	def := filepath.Join(home, ".claude", "settings.json")
	write(t, filepath.Join(x, "settings.json"), ksHook)
	write(t, def, ksHook)
	write(t, codex, `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"/opt/ks hook codex Stop"}]}]}}`)
	write(t, plist, "plist")
	beforeCodex, beforeDef := read(t, codex), read(t, def)

	if err := runInstall([]string{"--claude-dir", x}, true); err != nil {
		t.Fatal(err)
	}
	if read(t, codex) != beforeCodex {
		t.Fatal("Codex 配置被动了")
	}
	if read(t, def) != beforeDef {
		t.Fatal("默认 Claude 目录被动了")
	}
	if read(t, plist) != "plist" {
		t.Fatal("daemon plist 被动了")
	}
	if read(t, filepath.Join(x, "settings.json")) == ksHook {
		t.Fatal("指定目录应被清掉")
	}
}

func TestUninstallWithoutArgsCleansEverything(t *testing.T) {
	home, codex, plist := installEnv(t)
	write(t, plist, "plist")
	def := filepath.Join(home, ".claude", "settings.json")
	write(t, def, ksHook)
	write(t, codex, `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"/opt/ks hook codex Stop"}]}]}}`)
	beforeCodex := read(t, codex)
	if err := runInstall([]string{"--dry-run"}, true); err != nil {
		t.Fatal(err)
	}
	// dry-run 不写
	if read(t, codex) != beforeCodex || read(t, def) != ksHook {
		t.Fatal("dry-run 不应改文件")
	}
	_ = runInstall(nil, true)
	if read(t, def) == ksHook {
		t.Fatal("默认 Claude 目录应被清掉")
	}
	if read(t, codex) == beforeCodex {
		t.Fatal("不带参数时 Codex 照旧要清掉")
	}
	if read(t, plist) != "<missing>" {
		t.Fatal("不带参数时 daemon 服务照旧要移除")
	}
}

func TestInstallClaudeDirAddsToListAndKeepsDefaults(t *testing.T) {
	home, codex, plist := installEnv(t)
	x := filepath.Join(home, ".claude-x")
	if err := runInstall([]string{"--claude-dir", x}, false); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{x, filepath.Join(home, ".claude")} {
		if read(t, filepath.Join(d, "settings.json")) == "<missing>" {
			t.Fatalf("%s 应被写入", d)
		}
	}
	if read(t, codex) == "<missing>" {
		t.Fatal("install 照旧装 Codex")
	}
	if read(t, plist) == "<missing>" {
		t.Fatal("install 照旧装 daemon 服务")
	}
}
