package daemon

import (
	"sync"
	"testing"
	"time"
)

func newTestDaemon() *Daemon {
	d := &Daemon{toolUses: map[string]toolUse{}}
	d.toolCond = sync.NewCond(&d.mu)
	return d
}

func TestLookupToolUse(t *testing.T) {
	d := newTestDaemon()
	d.rememberToolUse("toolu_1", toolUse{session: "claude:current", agent: "a1"})
	if got := d.lookupToolUse("toolu_1", time.Second); got.session != "claude:current" || got.agent != "a1" {
		t.Fatalf("应直接命中，得到 %+v", got)
	}
	// hook 稍晚到达：查询方等待后命中。
	go func() {
		time.Sleep(80 * time.Millisecond)
		d.rememberToolUse("toolu_2", toolUse{session: "claude:later"})
	}()
	start := time.Now()
	if got := d.lookupToolUse("toolu_2", time.Second); got.session != "claude:later" {
		t.Fatalf("等待后应命中，得到 %+v", got)
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Fatal("命中后应立即返回")
	}
	// 一直等不到：超时返回空，由调用方兜底。
	start = time.Now()
	if got := d.lookupToolUse("missing", 150*time.Millisecond); got.session != "" || time.Since(start) < 140*time.Millisecond {
		t.Fatalf("超时应返回空，得到 %+v", got)
	}
}

// ks team tell 按 ppid 认会话时，取这个会话最新一条命令的子任务并认领掉（R-191）。
func TestClaimTell(t *testing.T) {
	d := newTestDaemon()
	d.rememberToolUse("t1", toolUse{session: "claude:pm", agent: "sub-old", tell: true})
	time.Sleep(5 * time.Millisecond)
	d.rememberToolUse("t2", toolUse{session: "claude:pm", agent: "sub-new", tell: true})
	d.rememberToolUse("t3", toolUse{session: "claude:other", agent: "x", tell: true})
	d.rememberToolUse("m1", toolUse{session: "claude:pm", agent: "mcp"})
	d.mu.Lock()
	defer d.mu.Unlock()
	if got := d.claimTell("claude:pm"); got != "sub-new" {
		t.Fatalf("应取最新一条，得到 %q", got)
	}
	if got := d.claimTell("claude:pm"); got != "sub-old" {
		t.Fatalf("认领后应轮到上一条，得到 %q", got)
	}
	if got := d.claimTell("claude:pm"); got != "" {
		t.Fatalf("MCP 调用不算 ks team tell，得到 %q", got)
	}
}
