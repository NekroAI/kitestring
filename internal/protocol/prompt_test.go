package protocol

import "testing"

func TestCleanPrompt(t *testing.T) {
	cases := []struct {
		in, want string
		auto     bool
	}{
		{"<task-notification>\n<task-id>bjd</task-id>\n<output-file>/tmp/x</output-file>\n</task-notification>", "", true},
		{"  <bash-stdout>ok</bash-stdout>", "", true},
		{"<command-message>review</command-message>\n<command-name>/code-review</command-name>\n<command-args>high</command-args>", "/code-review high", false},
		{"<command-name>clear</command-name>", "/clear", false},
		{"<bash-input>git status</bash-input>", "! git status", false},
		{"修一下登录页<system-reminder>内部提示</system-reminder>", "修一下登录页", false},
		{"普通消息", "普通消息", false},
	}
	for _, c := range cases {
		got, auto := CleanPrompt(c.in)
		if got != c.want || auto != c.auto {
			t.Errorf("CleanPrompt(%q) = %q, %v；期望 %q, %v", c.in, got, auto, c.want, c.auto)
		}
	}
}
