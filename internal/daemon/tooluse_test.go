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
	d.rememberToolUse("toolu_1", "claude:current")
	if got := d.lookupToolUse("toolu_1", time.Second); got != "claude:current" {
		t.Fatalf("应直接命中，得到 %q", got)
	}
	// hook 稍晚到达：查询方等待后命中。
	go func() {
		time.Sleep(80 * time.Millisecond)
		d.rememberToolUse("toolu_2", "claude:later")
	}()
	start := time.Now()
	if got := d.lookupToolUse("toolu_2", time.Second); got != "claude:later" {
		t.Fatalf("等待后应命中，得到 %q", got)
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Fatal("命中后应立即返回")
	}
	// 一直等不到：超时返回空，由调用方兜底。
	start = time.Now()
	if got := d.lookupToolUse("missing", 150*time.Millisecond); got != "" || time.Since(start) < 140*time.Millisecond {
		t.Fatalf("超时应返回空，得到 %q", got)
	}
}
