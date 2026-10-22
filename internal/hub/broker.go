package hub

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// broker 向 Web 端广播“某类数据已变化”的信号，前端据此重新拉取（SSE）。
type broker struct {
	mu   sync.Mutex
	subs map[chan string]struct{}
}

func newBroker() *broker { return &broker{subs: map[chan string]struct{}{}} }

func (b *broker) publish(topics ...string) {
	msg := strings.Join(topics, ",")
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- msg:
		default: // 慢客户端丢弃信号即可，下次信号或重连时会全量刷新
		}
	}
}

func (b *broker) serve(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "不支持流式响应", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	ch := make(chan string, 16)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		delete(b.subs, ch)
		b.mu.Unlock()
	}()
	fmt.Fprint(w, "retry: 3000\nevent: hello\ndata: {}\n\n")
	fl.Flush()
	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case msg := <-ch:
			fmt.Fprintf(w, "event: changed\ndata: %q\n\n", msg)
			fl.Flush()
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			fl.Flush()
		}
	}
}
