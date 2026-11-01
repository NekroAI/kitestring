package main

import (
	"reflect"
	"testing"
)

// R-249 P4：ks req watch / watch --action-only / unwatch 的请求体，与 MCP req_watch 发的一致（见 mcpserver 的 TestWatchTools）。
func TestWatchBody(t *testing.T) {
	cases := []struct {
		off, action bool
		want        map[string]any
	}{
		{false, false, map[string]any{"session_key": "codex:me", "mode": "all"}},
		{false, true, map[string]any{"session_key": "codex:me", "mode": "action"}},
		{true, false, map[string]any{"session_key": "codex:me", "off": true}},
	}
	for _, c := range cases {
		if got := watchBody("codex:me", c.off, c.action, false); !reflect.DeepEqual(got, c.want) {
			t.Errorf("off=%v action=%v：%v", c.off, c.action, got)
		}
	}
	if got := watchBody("codex:me", false, false, true); got["session_claimed"] != true {
		t.Errorf("自报会话要带 session_claimed：%v", got)
	}
}
