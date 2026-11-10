package main

import (
	"strings"
	"testing"
)

// R-329：ks hub password 当 passwd 用；敲错子命令时报错列出可用的子命令
func TestHubSubcommand(t *testing.T) {
	for in, want := range map[string]string{"passwd": "passwd", "password": "passwd", "serve": "serve", "device-rm": "device-rm"} {
		got, err := hubSubcommand(in)
		if err != nil || got != want {
			t.Errorf("hubSubcommand(%q) = %q, %v；想要 %q", in, got, err, want)
		}
	}
	_, err := hubSubcommand("pasword")
	if err == nil {
		t.Fatal("未知子命令应报错")
	}
	for _, s := range hubSubcommands {
		if !strings.Contains(err.Error(), s) {
			t.Errorf("报错 %q 没列出 %s", err, s)
		}
	}
}

// 未知子命令在开数据库之前就报错
func TestRunHubUnknownBeforeStore(t *testing.T) {
	t.Setenv("KS_HOME", t.TempDir())
	err := runHub([]string{"nope", "--data", "/nonexistent/dir/hub.db"})
	if err == nil || !strings.Contains(err.Error(), "未知的 hub 子命令：nope（可用：") {
		t.Fatalf("得到 %v", err)
	}
}
