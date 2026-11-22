package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestTakeoverArgv_Agy(t *testing.T) {
	argv, err := takeoverArgv("agy", "pm")
	if err != nil {
		t.Fatalf("takeoverArgv(agy, pm) failed: %v", err)
	}
	if len(argv) != 3 || argv[0] != "agy" || argv[1] != "-i" || !strings.Contains(argv[2], "ks role") {
		t.Fatalf("unexpected agy takeover argv: %v", argv)
	}
	if _, err := takeoverArgv("unknown", "pm"); err == nil {
		t.Fatal("expected error for unknown tool")
	}
}

func TestCliSession_AgyEnvFallback(t *testing.T) {
	t.Setenv("DSH_KITESTRING_SESSION", "")
	t.Setenv("ANTIGRAVITY_CONVERSATION_ID", "cid-cli-1")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")

	got := cliSession("")
	if !reflect.DeepEqual(got, "agy:cid-cli-1") {
		t.Fatalf("expected agy:cid-cli-1, got %q", got)
	}

	t.Setenv("CLAUDE_CODE_SESSION_ID", "claude-1")
	if gotClaude := cliSession(""); gotClaude != "claude:claude-1" {
		t.Fatalf("CLAUDE_CODE_SESSION_ID should take precedence over host AGY env, got %q", gotClaude)
	}

	t.Setenv("DSH_KITESTRING_SESSION", "dsh:sess-9")
	if gotDSH := cliSession(""); gotDSH != "dsh:sess-9" {
		t.Fatalf("DSH should take precedence over all, got %q", gotDSH)
	}
}
