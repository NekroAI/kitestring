package daemon

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// 动作简述的命令上限放宽到 200 字（R-216）：工作台动作列按实际宽度用省略号截断，悬停看全文。
func TestToolBriefBashLimit(t *testing.T) {
	short := "go test ./internal/hub -run TestTell -count=1"
	if got := toolBrief("Bash", map[string]any{"command": short}); got != short {
		t.Fatalf("短命令应原样保留：%q", got)
	}
	long := strings.Repeat("a", 150) + "   " + strings.Repeat("b", 150)
	got := toolBrief("Bash", map[string]any{"command": long})
	if n := utf8.RuneCountInString(got); n != 201 || !strings.HasSuffix(got, "…") {
		t.Fatalf("超过 200 字应截成 200 字加省略号，得到 %d 字：%q", n, got)
	}
	if !strings.Contains(got, strings.Repeat("a", 150)+" b") {
		t.Fatalf("多个空白应折成一个：%q", got)
	}
	arr := toolBrief("shell", map[string]any{"command": []any{"bash", "-lc", strings.Repeat("c", 260)}})
	if n := utf8.RuneCountInString(arr); n != 201 {
		t.Fatalf("数组形式的命令同样截到 200 字：%d", n)
	}
}

func TestToolBriefAgyAndMCP(t *testing.T) {
	cases := []struct {
		name string
		tool string
		in   map[string]any
		want string
	}{
		{"run_command", "run_command", map[string]any{"CommandLine": "go test ./...", "toolSummary": "Run tests"}, "go test ./..."},
		{"run_command fallback summary", "run_command", map[string]any{"toolSummary": "Build bundle"}, "Build bundle"},
		{"view_file", "view_file", map[string]any{"AbsolutePath": "/a/b/daemon.go"}, "daemon.go"},
		{"replace_file_content", "replace_file_content", map[string]any{"TargetFile": "/a/b/agy.go"}, "agy.go"},
		{"view_code_item", "view_code_item", map[string]any{"File": "/a/b/agy.go", "NodePath": "Daemon.buildEvent"}, "agy.go · Daemon.buildEvent"},
		{"grep_search", "grep_search", map[string]any{"Query": "toolBrief", "SearchPath": "/a/b"}, "toolBrief"},
		{"list_dir", "list_dir", map[string]any{"DirectoryPath": "/a/b/internal/daemon"}, "daemon"},
		{"read_url_content", "read_url_content", map[string]any{"Url": "https://example.com/docs"}, "example.com"},
		{"enter_plan_mode", "enter_plan_mode", map[string]any{"reason": "Need user architecture approval"}, "Need user architecture approval"},
		{"exit_plan_mode plan heading", "exit_plan_mode", map[string]any{"plan": "# Complete Integration Plan\n\nDetails..."}, "Complete Integration Plan"},
		{"manage_task", "manage_task", map[string]any{"Action": "status", "TaskId": "3965713b-d88b-4f70-9a75-15f3a158ceb4/task-543"}, "status · task-543"},
		{"schedule wait", "schedule", map[string]any{"DurationSeconds": 60, "Prompt": "check deploy"}, "60s · check deploy"},
		{"schedule cron", "schedule", map[string]any{"CronExpression": "*/5 * * * *", "Prompt": "poll"}, "*/5 * * * *"},
		{"send_message uuid", "send_message", map[string]any{"Recipient": "35ea7bc9-6062-4a2b-9a99-30f5ee8c6083"}, "35ea7bc9"},
		{"swarm", "swarm", map[string]any{"action": "add", "name": "worker-1"}, "add · worker-1"},
		{"mcp req_update_status", "mcp__kitestring__req_update_status", map[string]any{"id": "R-12", "status": "done"}, "R-12 · done"},
		{"mcp req_assign", "mcp__kitestring__req_assign", map[string]any{"requirement": "R-12", "to": "dev"}, "R-12 · dev"},
		{"mcp team_tell omits body", "mcp__kitestring__team_tell", map[string]any{"to": "pm", "text": "SUPER_SECRET_BODY"}, "pm"},
		{"mcp decision_raise", "mcp__kitestring__decision_raise", map[string]any{"question": "Deploy now?"}, "Deploy now?"},
		{"mcp secret_list empty", "mcp__kitestring__secret_list", map[string]any{}, ""},
	}
	for _, c := range cases {
		if got := toolBrief(c.tool, c.in); got != c.want {
			t.Errorf("%s (%s): got %q, want %q", c.name, c.tool, got, c.want)
		}
	}
}
