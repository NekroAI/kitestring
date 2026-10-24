package hookcli

import (
	"encoding/json"
	"strings"
	"testing"
)

// D13：hook 只上报白名单环境变量；凭证与网关地址任何情况下都不进上报内容（R-240）。
func TestCollectEnvNeverReportsCredentials(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-secret-123")
	t.Setenv("ANTHROPIC_BASE_URL", "https://gateway.example/secret")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "tok-456")
	t.Setenv("CLAUDE_CONFIG_DIR", "/home/u/.claude-x")
	t.Setenv("TERM_PROGRAM", "ghostty")
	env := collectEnv()
	b, _ := json.Marshal(env)
	for _, bad := range []string{"sk-secret-123", "gateway.example", "tok-456", "ANTHROPIC"} {
		if strings.Contains(string(b), bad) {
			t.Fatalf("上报内容含 %q：%s", bad, b)
		}
	}
	if env["CLAUDE_CONFIG_DIR"] != "/home/u/.claude-x" || env["TERM_PROGRAM"] != "ghostty" {
		t.Fatalf("应上报配置目录：%v", env)
	}
}
