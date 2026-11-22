package install

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NekroAI/kitestring/internal/protocol"
)

func TestAgyInstallUninstallAndIdempotence(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	// 预先在 settings.json 放入一条用户自定义权限，确认不会丢且不会被重复添加。
	settingsPath := agySettingsPath(home)
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsPath, []byte(`{"permissions":{"allow":["command(git status)"]},"theme":"dark"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	opts := Options{
		KS:     "/usr/local/bin/ks",
		Agy:    true,
		AgySet: true,
		Out:    &out,
	}
	if err := Run(opts, false); err != nil {
		t.Fatalf("install --agy failed: %v\n%s", err, out.String())
	}

	pDir := agyPluginDir(home)
	for _, rel := range []string{"plugin.json", "hooks.json", "mcp_config.json", filepath.Join("rules", "AGENTS.md")} {
		if _, err := os.Stat(filepath.Join(pDir, rel)); err != nil {
			t.Fatalf("expected plugin file %s: %v", rel, err)
		}
	}

	// 验证 hooks.json 扁平事件与匹配器事件格式正确
	hooksRoot, err := readJSON(filepath.Join(pDir, "hooks.json"))
	if err != nil {
		t.Fatal(err)
	}
	ksHooks := hooksRoot[agyPluginName].(map[string]any)
	if list := ksHooks["Stop"].([]any); len(list) != 1 || list[0].(map[string]any)["command"] != "/usr/local/bin/ks hook agy Stop" {
		t.Fatalf("unexpected flat Stop hook: %#v", ksHooks["Stop"])
	}
	if groups := ksHooks["PreToolUse"].([]any); len(groups) != 1 || groups[0].(map[string]any)["matcher"] != "*" {
		t.Fatalf("unexpected grouped PreToolUse hook: %#v", ksHooks["PreToolUse"])
	}

	// 验证 settings.json 写入 command(ks) + 全部 42 个 mcp(kitestring/*) 且保留用户字段
	settingsRoot, err := readJSON(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if settingsRoot["theme"] != "dark" {
		t.Fatalf("expected custom settings key preserved, got %#v", settingsRoot["theme"])
	}
	allow := settingsRoot["permissions"].(map[string]any)["allow"].([]any)
	wantCount := 1 + 1 + len(protocol.KitestringMCPTools)
	if len(allow) != wantCount {
		t.Fatalf("expected %d allow entries (1 custom + command(ks) + 42 mcp), got %d", wantCount, len(allow))
	}

	// 再安装一次，确认幂等，allow 数量不变
	if err := Run(opts, false); err != nil {
		t.Fatal(err)
	}
	settingsRoot2, _ := readJSON(settingsPath)
	allow2 := settingsRoot2["permissions"].(map[string]any)["allow"].([]any)
	if len(allow2) != wantCount {
		t.Fatalf("idempotent reinstall changed allow count: got %d want %d", len(allow2), wantCount)
	}

	// 验证 status 报告正确
	var statusBuf bytes.Buffer
	stOpts := Options{KS: "/usr/local/bin/ks", Status: true, Out: &statusBuf}
	if err := Run(stOpts, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(statusBuf.String(), "[Antigravity") || !strings.Contains(statusBuf.String(), "5/5 个事件") {
		t.Fatalf("unexpected agy status output:\n%s", statusBuf.String())
	}

	// 如果本机装了 agy CLI，顺便跑一次官方严格校验
	if agyBin, err := exec.LookPath("agy"); err == nil {
		cmd := exec.Command(agyBin, "plugin", "validate", pDir, "--strict")
		if valOut, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("agy plugin validate --strict failed: %v\n%s", err, string(valOut))
		}
	}

	// 卸载：插件目录被删，settings.json 只剩用户的 command(git status)
	if err := Run(opts, true); err != nil {
		t.Fatalf("uninstall --agy failed: %v", err)
	}
	if _, err := os.Stat(pDir); !os.IsNotExist(err) {
		t.Fatalf("expected plugin dir removed, stat err=%v", err)
	}
	afterRoot, err := readJSON(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	afterAllow := afterRoot["permissions"].(map[string]any)["allow"].([]any)
	b, _ := json.Marshal(afterAllow)
	if string(b) != `["command(git status)"]` {
		t.Fatalf("expected only user allow entry left after uninstall, got %s", string(b))
	}
}

func TestAgyDryRunTouchesNoFiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	var out bytes.Buffer
	err := Run(Options{KS: "/usr/local/bin/ks", Agy: true, AgySet: true, DryRun: true, Out: &out}, false)
	if err != nil {
		t.Fatal(err)
	}
	if agyInstalled(home) {
		t.Fatal("dry-run must not write plugin files")
	}
	if !strings.Contains(out.String(), "plugin.json") || !strings.Contains(out.String(), "hooks.json") {
		t.Fatalf("dry-run output missing agy files:\n%s", out.String())
	}
}
