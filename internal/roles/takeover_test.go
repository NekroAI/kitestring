package roles

import (
	"strings"
	"testing"
)

// R-224：ks takeover 带的接任提示要让新会话声明对应身份，并读完交接回一句确认。
func TestTakeoverPrompt(t *testing.T) {
	for role, want := range map[string]string{PM: "ks role pm", Aide: "ks role aide", Dev: "ks role dev"} {
		p := TakeoverPrompt(role)
		if !strings.Contains(p, want) || !strings.Contains(p, Display(role)) || !strings.Contains(p, "确认") {
			t.Fatalf("%s 的接任提示不对：%s", role, p)
		}
	}
}
