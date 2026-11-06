package install

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRestartServicesLaunchd(t *testing.T) {
	home := t.TempDir()
	agents := filepath.Join(home, "Library", "LaunchAgents")
	os.MkdirAll(agents, 0o755)
	exe := "/opt/ks bin/ks"
	os.WriteFile(filepath.Join(agents, launchLabel+".hub.plist"), []byte("<string>"+exe+"</string>\n<string>hub</string>"), 0o644)
	os.WriteFile(filepath.Join(agents, launchLabel+".daemon.plist"), []byte("<string>"+exe+"</string>\n<string>daemon</string>"), 0o644)
	var calls []string
	var out bytes.Buffer
	n, err := RestartServices(exe, RestartOptions{Home: home, GOOS: "darwin", Launchctl: "fakectl", Domain: "gui/501", Out: &out,
		Exec: func(name string, args ...string) ([]byte, error) {
			calls = append(calls, name+" "+strings.Join(args, " "))
			return nil, nil
		}})
	if err != nil || n != 2 {
		t.Fatal(n, err)
	}
	want := []string{"fakectl kickstart -k gui/501/ai.nekro.kitestring.hub", "fakectl kickstart -k gui/501/ai.nekro.kitestring.daemon"}
	if strings.Join(calls, "|") != strings.Join(want, "|") {
		t.Fatalf("%q", calls)
	}
	if !strings.Contains(out.String(), "✓ 已重启 Hub\n") || !strings.Contains(out.String(), "✓ 已重启后台服务\n") {
		t.Fatal(out.String())
	}
}

func TestRestartServicesSkipsOtherBinaryAndMissing(t *testing.T) {
	home := t.TempDir()
	agents := filepath.Join(home, "Library", "LaunchAgents")
	os.MkdirAll(agents, 0o755)
	os.WriteFile(filepath.Join(agents, launchLabel+".daemon.plist"), []byte("<string>/elsewhere/ks</string>"), 0o644)
	var out bytes.Buffer
	n, err := RestartServices("/tmp/x/ks", RestartOptions{Home: home, GOOS: "darwin", Out: &out,
		Exec: func(string, ...string) ([]byte, error) { t.Fatal("不应调用"); return nil, nil }})
	if err != nil || n != 0 || !strings.Contains(out.String(), "后台服务用的不是 /tmp/x/ks，未重启") {
		t.Fatal(n, err, out.String())
	}
}

func TestRestartServicesSystemdAndError(t *testing.T) {
	home := t.TempDir()
	units := filepath.Join(home, ".config", "systemd", "user")
	os.MkdirAll(units, 0o755)
	exe := "/home/u/.local/bin/ks"
	os.WriteFile(filepath.Join(units, "kitestring-daemon.service"), []byte("[Service]\nExecStart="+exe+" daemon\n"), 0o644)
	var calls []string
	n, err := RestartServices(exe, RestartOptions{Home: home, GOOS: "linux", Systemctl: "sc", Out: &bytes.Buffer{},
		Exec: func(name string, args ...string) ([]byte, error) {
			calls = append(calls, name+" "+strings.Join(args, " "))
			return nil, nil
		}})
	if err != nil || n != 1 || calls[0] != "sc --user restart kitestring-daemon.service" {
		t.Fatal(n, err, calls)
	}
	_, err = RestartServices(exe, RestartOptions{Home: home, GOOS: "linux", Out: &bytes.Buffer{},
		Exec: func(string, ...string) ([]byte, error) { return []byte("boom"), errors.New("exit 1") }})
	if err == nil || !strings.Contains(err.Error(), "重启后台服务失败") {
		t.Fatal(err)
	}
}
