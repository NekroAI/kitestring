package main

import (
	"strings"
	"testing"
)

// R-248：ks team tell 不接受 --session，发送方不能被冒用。
func TestTeamTellRejectsSessionFlag(t *testing.T) {
	err := runTeam([]string{"tell", "你好", "--to", "advisor", "--session", "claude:someone-else"})
	if err == nil || !strings.Contains(err.Error(), "不接受 --session") {
		t.Fatalf("tell 带 --session 应被拒绝：%v", err)
	}
}
