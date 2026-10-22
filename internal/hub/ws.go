package hub

import (
	"context"
	"net/http"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/NekroAI/kitestring/internal/notify"
	"github.com/NekroAI/kitestring/internal/protocol"
	"github.com/NekroAI/kitestring/internal/store"
)

// deviceConn 是一条 daemon 的 WebSocket 连接；写操作串行化到 out 通道。
type deviceConn struct {
	dev    store.Device
	out    chan protocol.Envelope
	done   <-chan struct{}
	cancel context.CancelFunc
}

func (c *deviceConn) send(e protocol.Envelope) {
	select {
	case <-c.done:
	case c.out <- e:
	default: // 发送队列满说明连接已不健康，丢弃；重连后会全量重推
	}
}

// serveDeviceWS 处理 daemon 的长连接：上行事件与送达回执，下行回执、决策与配置。
func (h *Hub) serveDeviceWS(w http.ResponseWriter, r *http.Request) {
	dev, ok := deviceFrom(r.Context())
	if !ok {
		http.Error(w, "未授权", http.StatusUnauthorized)
		return
	}
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	ws.SetReadLimit(4 << 20)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c := &deviceConn{dev: dev, out: make(chan protocol.Envelope, 64), done: ctx.Done(), cancel: cancel}
	h.mu.Lock()
	if old := h.conns[dev.ID]; old != nil {
		old.cancel() // 同一设备的新连接顶替旧连接
	}
	h.conns[dev.ID] = c
	if t := h.lostTmr[dev.ID]; t != nil {
		t.Stop()
		delete(h.lostTmr, dev.ID)
	}
	h.mu.Unlock()
	h.log.Info("设备已连接", "device", dev.Name)

	go func() {
		ping := time.NewTicker(20 * time.Second)
		defer ping.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case e := <-c.out:
				wctx, wcancel := context.WithTimeout(ctx, 10*time.Second)
				err := wsjson.Write(wctx, ws, e)
				wcancel()
				if err != nil {
					cancel()
					return
				}
			case <-ping.C:
				pctx, pcancel := context.WithTimeout(ctx, 10*time.Second)
				err := ws.Ping(pctx)
				pcancel()
				if err != nil {
					cancel()
					return
				}
			}
		}
	}()

	if s, err := h.Settings(ctx); err == nil {
		cfg := h.deviceConfig(s)
		c.send(protocol.Envelope{Type: protocol.MsgConfig, Config: &cfg})
	}
	h.pushDecisions(ctx, dev.ID)

	for {
		var e protocol.Envelope
		if err := wsjson.Read(ctx, ws, &e); err != nil {
			break
		}
		h.handleDeviceMsg(ctx, c, e)
	}
	ws.Close(websocket.StatusNormalClosure, "")
	h.deviceGone(c)
}

func (h *Hub) handleDeviceMsg(ctx context.Context, c *deviceConn, e protocol.Envelope) {
	switch e.Type {
	case protocol.MsgHello:
		if err := h.store.SetDeviceOnline(ctx, c.dev.ID, true, e.Hello); err != nil {
			h.log.Warn("更新设备信息失败", "err", err)
		}
		h.broker.publish("devices", "sessions")
	case protocol.MsgEvents:
		acks := make([]string, 0, len(e.Events))
		for _, ev := range e.Events {
			if err := h.ingest(ctx, c.dev, ev); err != nil {
				h.log.Warn("处理事件失败", "device", c.dev.Name, "event", ev.Name, "err", err)
				continue // 不 ack，daemon 会重传
			}
			acks = append(acks, ev.ID)
		}
		_ = h.store.SetDeviceOnline(ctx, c.dev.ID, true, nil)
		c.send(protocol.Envelope{Type: protocol.MsgAck, Ack: acks})
	case protocol.MsgDelivered:
		changed := false
		for _, d := range e.Delivered {
			ok, err := h.store.MarkDelivered(ctx, d.DecisionID, d.SessionKey, d.TurnID, d.Time)
			if err != nil {
				h.log.Warn("标记决策已接收失败", "err", err)
				continue
			}
			changed = changed || ok
		}
		c.send(protocol.Envelope{Type: protocol.MsgAck, Ack: deliveryIDs(e.Delivered)})
		if changed {
			h.broker.publish("decisions", "sessions")
			h.pushDecisions(ctx, c.dev.ID)
		}
	}
}

func deliveryIDs(ds []protocol.Delivery) []string {
	out := make([]string, len(ds))
	for i, d := range ds {
		out[i] = "delivered:" + d.DecisionID
	}
	return out
}

// deviceGone 在连接断开后进入宽限期，超时仍未重连则标记设备离线、会话失联。
func (h *Hub) deviceGone(c *deviceConn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.conns[c.dev.ID] != c {
		return // 已被新连接顶替
	}
	delete(h.conns, c.dev.ID)
	h.log.Info("设备已断开", "device", c.dev.Name)
	h.scheduleLostLocked(c.dev)
}

// scheduleLostLocked 启动失联计时器，调用方须持有 h.mu。
func (h *Hub) scheduleLostLocked(dev store.Device) {
	if t := h.lostTmr[dev.ID]; t != nil {
		t.Stop()
	}
	h.lostTmr[dev.ID] = time.AfterFunc(lostGrace, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		h.mu.Lock()
		_, back := h.conns[dev.ID]
		delete(h.lostTmr, dev.ID)
		h.mu.Unlock()
		if back {
			return
		}
		_ = h.store.SetDeviceOnline(ctx, dev.ID, false, nil)
		keys, err := h.store.MarkDeviceLost(ctx, dev.ID)
		if err != nil {
			h.log.Warn("标记失联失败", "err", err)
		}
		h.broker.publish("devices", "sessions")
		if len(keys) > 0 {
			h.sendNotify("", notify.Message{Event: notify.EventDeviceLost, Title: "设备失联 · " + dev.Name, Body: "有未结束的会话", Group: "kitestring"}, "/")
		}
	})
}
