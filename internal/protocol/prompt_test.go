package protocol

import "testing"

func TestCleanPrompt(t *testing.T) {
	cases := []struct {
		in, want string
		auto     bool
	}{
		{"<task-notification>\n<task-id>bjd</task-id>\n<output-file>/tmp/x</output-file>\n</task-notification>", "", true},
		{"  <bash-stdout>ok</bash-stdout>", "", true},
		{"<agent-message from=\"a934d12\">\n[Subagent hand-back] 做完了</agent-message>", "", true},
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

// R-179：Codex 桌面版「项目建议」后台任务只认第一条提示词的固定开头。
func TestIsBackgroundPrompt(t *testing.T) {
	cases := []struct {
		name, tool, prompt string
		want               bool
	}{
		{"项目建议任务（实际抓到的开头）", "codex", "# Overview\n\nGenerate 0 to 3 hyperpersonalized suggestions for what this user can do with Codex in this local project: /Users/miose/Documents/Obsidian Vault\n\n\nGet an understanding of the user's intent …", true},
		{"换行 / 缩进不同也认", "codex", "  # Overview\r\n    Generate 0 to 3   hyperpersonalized suggestions for what this user can do with Codex", true},
		{"用户的对话：检查空间占用（Obsidian Vault 里那条）", "codex", "检查一下当前设备的空间占用情况", false},
		{"同一对话的后续消息", "codex", "深度分析看看给我清理建议", false},
		{"用户自己引用这段文字，但不在开头", "codex", "帮我看看这个提示词是什么意思：# Overview Generate 0 to 3 hyperpersonalized suggestions for what this user can do with Codex", false},
		{"只有 # Overview 开头", "codex", "# Overview\n\n这个项目是做什么的？", false},
		{"空", "codex", "", false},
		{"别的工具不认", "claude", "# Overview\n\nGenerate 0 to 3 hyperpersonalized suggestions for what this user can do with Codex", false},
	}
	for _, c := range cases {
		if got := IsBackgroundPrompt(c.tool, c.prompt); got != c.want {
			t.Errorf("%s：得到 %v，期望 %v", c.name, got, c.want)
		}
	}
}

func TestAutoKind(t *testing.T) {
	cases := []struct{ in, kind, note string }{
		{"<task-notification>\n<task-id>b1</task-id>\n<status>completed</status>\n<summary>Background command \"Wait for Kitestring events\" completed (exit code 0)</summary>\n</task-notification>", WakeBg, "Wait for Kitestring events"},
		{"<task-notification>\n<task-id>b2</task-id>\n<summary>Monitor event: \"CI run\"</summary>\n<event>ok</event>\n</task-notification>", WakeBg, "CI run"},
		{"<task-notification>\n<summary>Agent \"实现 R-273 会话详情页\" finished</summary></task-notification>", WakeAgent, "实现 R-273 会话详情页"},
		{"<task-notification><status>stopped</status><summary>Background shell command didn't finish</summary></task-notification>", WakeBg, ""},
		{"<agent-message from=\"a7d\">\n[Subagent hand-back] ...", WakeAgent, ""},
		{"<local-command-stdout>ok</local-command-stdout>", WakeOutput, ""},
		{"<bash-stdout>hi</bash-stdout>", WakeOutput, ""},
		{"帮我看看", "", ""},
	}
	for _, c := range cases {
		k, n := AutoKind(c.in)
		if k != c.kind || n != c.note {
			t.Errorf("AutoKind(%q) = %q, %q；期望 %q, %q", c.in, k, n, c.kind, c.note)
		}
	}
}
