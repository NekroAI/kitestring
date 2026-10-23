package hub

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/NekroAI/kitestring/internal/store"
)

// R-56：慢客户端缓冲满了丢消息时记下欠账，下一次写出时补发一次全量重拉，不会漏。
func TestBrokerOwesResyncAfterDrop(t *testing.T) {
	b := newBroker()
	s := &sub{ch: make(chan string, 1)}
	b.subs[s] = struct{}{}
	b.publishSession(store.Session{Key: "claude:a"})
	b.publishSession(store.Session{Key: "claude:b"}) // 缓冲已满，被丢掉
	if !s.owed {
		t.Fatal("丢消息后应记下欠账")
	}
	w := httptest.NewRecorder()
	msg := <-s.ch
	w.WriteString(msg)
	s.settle(w)
	out := w.Body.String()
	if !strings.Contains(out, `"key":"claude:a"`) || !strings.Contains(out, "event: changed") || !strings.Contains(out, "sessions,requirements") {
		t.Fatalf("应先写出缓冲里的消息，再补发全量重拉：%s", out)
	}
	if s.owed {
		t.Fatal("补发后欠账清零")
	}
	if b.Rev() != 2 {
		t.Fatalf("每条消息都占一个序号：%d", b.Rev())
	}
}
