package daemon

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/NekroAI/kitestring/internal/profile"
	"github.com/NekroAI/kitestring/internal/protocol"
)

// 事件里的来源只有显示名、目录、启动命令；序列化结果里不会有凭证（D13，R-240）。
func TestEventProfileHasNoSecrets(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ANTHROPIC_API_KEY", "sk-secret-123")
	t.Setenv("ANTHROPIC_BASE_URL", "https://gateway.example/secret")
	dir := home + "/.claude-x"
	if err := profile.Save(home, []profile.Dir{{Path: dir, Launcher: "claude-x"}}); err != nil {
		t.Fatal(err)
	}
	info := profile.Resolve(home, dir)
	ev := protocol.Event{Profile: info.Name, ProfileDir: info.Dir, Launcher: info.Launcher}
	b, _ := json.Marshal(ev)
	for _, bad := range []string{"sk-secret", "gateway.example", "ANTHROPIC"} {
		if strings.Contains(string(b), bad) {
			t.Fatalf("事件含 %q：%s", bad, b)
		}
	}
	if ev.Profile != "claude-x" || ev.Launcher != "claude-x" {
		t.Fatalf("%+v", ev)
	}
}
