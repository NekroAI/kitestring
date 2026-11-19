package mcpserver

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/NekroAI/kitestring/internal/roles"
)

// R-359：MCP 说明缩成短入口和身份列表，流程规矩只随身份提示词下发。
func TestInstructionsShortEntry(t *testing.T) {
	ins := instructionsFor("")
	if n := utf8.RuneCountInString(ins); n > 1500 {
		t.Fatalf("MCP 说明 %d 字，应缩成短入口（1500 字以内）", n)
	}
	for _, want := range []string{"ks role aide|advisor|reviewer|pm|dev", "ks role lite", "ks help", "decision_raise", "secret_request", roles.Team()} {
		if !strings.Contains(ins, want) {
			t.Fatalf("MCP 说明缺 %q", want)
		}
	}
	for _, bad := range []string{"req_create 记下来", "evidence_attach 附上证据", "team_broadcast", "订阅事件（--on）"} {
		if strings.Contains(ins, bad) {
			t.Fatalf("流程规矩 %q 应挪进身份提示词", bad)
		}
	}
	if !strings.HasPrefix(instructionsFor("dsh"), dshInstructionsNote) {
		t.Fatal("DSH 仍先说明待命由插件负责")
	}
}
