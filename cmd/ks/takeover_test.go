package main

import (
	"strings"
	"testing"
)

// R-224：ks takeover 在项目目录启动新会话，首条消息是接任提示；--print 输出能直接粘贴的一行。
func TestTakeoverArgv(t *testing.T) {
	argv, err := takeoverArgv("claude", "pm")
	if err != nil || len(argv) != 2 || argv[0] != "claude" || !strings.Contains(argv[1], "ks role pm") {
		t.Fatalf("claude 命令不对：%v %v", argv, err)
	}
	if argv, _ := takeoverArgv("codex", "aide"); argv[0] != "codex" || !strings.Contains(argv[1], "ks role aide") {
		t.Fatalf("codex 命令不对：%v", argv)
	}
	if _, err := takeoverArgv("vim", "pm"); err == nil {
		t.Fatal("不认识的工具应报错")
	}
	if got := shellJoin([]string{"claude", "it's 接任"}); got != `claude 'it'\''s 接任'` {
		t.Fatalf("引号处理不对：%s", got)
	}
}
