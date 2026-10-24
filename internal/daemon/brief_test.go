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
