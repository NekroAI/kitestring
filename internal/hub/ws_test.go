package hub

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/NekroAI/kitestring/internal/protocol"
	"github.com/NekroAI/kitestring/internal/store"
)

// R-374：事件处理慢（数据库被占住）时，daemon 的连接不能因为 Pong 没人收而断开；放开后事件照常处理并回执。
func TestDeviceWSStaysUpWhileProcessingIsSlow(t *testing.T) {
	oldEvery, oldWait := devicePingEvery, devicePongWait
	devicePingEvery, devicePongWait = 50*time.Millisecond, 200*time.Millisecond
	t.Cleanup(func() { devicePingEvery, devicePongWait = oldEvery, oldWait })

	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	h := New(st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	srv := httptest.NewServer(h.Handler(nil))
	t.Cleanup(srv.Close)
	tok := NewDeviceToken()
	if _, err := st.CreateDevice(ctx, "mac", HashToken(tok)); err != nil {
		t.Fatal(err)
	}

	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/api/device/ws",
		&websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + tok}}})
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	ws.SetReadLimit(16 << 20)
	acks := make(chan []string, 16)
	closed := make(chan error, 1)
	go func() { // 和 daemon 一样一直在读：Pong 由读循环回
		for {
			var e protocol.Envelope
			if err := wsjson.Read(ctx, ws, &e); err != nil {
				closed <- err
				return
			}
			if e.Type == protocol.MsgAck {
				acks <- e.Ack
			}
		}
	}()

	// 占住写连接：事件写入会一直等着
	tx, err := st.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	ev := protocol.Event{ID: protocol.NewID(), Time: time.Now().UTC(), Tool: "claude", Name: "PreToolUse", SessionID: "s1"}
	if err := wsjson.Write(ctx, ws, protocol.Envelope{Type: protocol.MsgEvents, Events: []protocol.Event{ev}}); err != nil {
		t.Fatal(err)
	}
	// 处理卡住的这段时间里 Ping 发了二十来次，连接要一直在
	select {
	case err := <-closed:
		t.Fatalf("处理慢时连接被断开：%v", err)
	case <-time.After(1200 * time.Millisecond):
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-acks:
		if len(got) != 1 || got[0] != ev.ID {
			t.Fatalf("回执不对：%v", got)
		}
	case err := <-closed:
		t.Fatalf("连接断开：%v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("放开后迟迟没收到回执")
	}
	if _, err := st.GetSession(ctx, protocol.SessionKey("claude", "s1")); err != nil {
		t.Fatalf("事件没处理完：%v", err)
	}
}
