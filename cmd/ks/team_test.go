package main

import (
	"strings"
	"testing"
)

// R-248：识别成功时 --session 与它不一致被拒（防冒用）；一致或不填照常；识别不到时才接受 --session 并标记自报。
func TestResolveTellSender(t *testing.T) {
	if _, _, _, err := resolveTellSender("claude:other", "claude:me", ""); err == nil || !strings.Contains(err.Error(), "不一致") {
		t.Fatalf("识别成功而 --session 不一致应被拒：%v", err)
	}
	k, a, claimed, err := resolveTellSender("claude:me", "claude:me", "sub-1")
	if err != nil || k != "claude:me" || a != "sub-1" || claimed {
		t.Fatalf("一致应照常：%s %s %v %v", k, a, claimed, err)
	}
	if k, _, claimed, err = resolveTellSender("", "claude:me", ""); err != nil || k != "claude:me" || claimed {
		t.Fatalf("不填应用识别结果：%s %v %v", k, claimed, err)
	}
	if k, _, claimed, err = resolveTellSender("codex:abc", "", ""); err != nil || k != "codex:abc" || !claimed {
		t.Fatalf("识别不到时应接受 --session 并标记自报：%s %v %v", k, claimed, err)
	}
	if k, _, claimed, _ = resolveTellSender("", "", ""); k != "" || claimed {
		t.Fatalf("都没有应为空：%s %v", k, claimed)
	}
}
