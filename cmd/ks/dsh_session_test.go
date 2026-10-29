package main

import "testing"

// R-251：从 Claude Code 终端里启动的 DSH 会继承 CLAUDE_CODE_SESSION_ID；在 DSH 的 shell 里
// ks 要认 DSH 根会话，子代理里也一样（插件注入根会话键，DSH_SESSION_ID 此时是子会话）。
func TestDSHSessionWinsOverInheritedClaude(t *testing.T) {
	t.Setenv("CLAUDE_CODE_SESSION_ID", "claude-parent")
	t.Setenv("DSH_SESSION_ID", "child")
	t.Setenv(envDSHRootSession, "dsh:root")
	t.Setenv(envDSHAgent, "child")
	if got := cliSession(""); got != "dsh:root" {
		t.Fatalf("cliSession 应认 DSH 根会话，得到 %q", got)
	}
	if got := currentSession(""); got != "dsh:root" {
		t.Fatalf("currentSession 应认 DSH 根会话，得到 %q", got)
	}
	if key, agent := currentTeller(); key != "dsh:root" || agent != "child" {
		t.Fatalf("currentTeller 应带子代理，得到 %q %q", key, agent)
	}
	if got := cliSession("claude:explicit"); got != "claude:explicit" {
		t.Fatalf("显式参数仍然优先，得到 %q", got)
	}
}

// 老插件没有注入根会话键：退回 DSH_SESSION_ID，仍排在继承来的 Claude 变量前面；格式不对的根会话键不认。
func TestDSHSessionFallback(t *testing.T) {
	t.Setenv("CLAUDE_CODE_SESSION_ID", "claude-parent")
	t.Setenv("DSH_SESSION_ID", "s1")
	t.Setenv(envDSHRootSession, "claude:oops")
	t.Setenv(envDSHAgent, "")
	if got := cliSession(""); got != "dsh:s1" {
		t.Fatalf("应退回 DSH_SESSION_ID，得到 %q", got)
	}
	t.Setenv("DSH_SESSION_ID", "")
	t.Setenv(envDSHRootSession, "")
	if got := cliSession(""); got != "claude:claude-parent" {
		t.Fatalf("不在 DSH 里时照旧认 Claude，得到 %q", got)
	}
}
