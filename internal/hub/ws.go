package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
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
	pushMu sync.Mutex // 决策全集「查库 → 下发」串行（R-413）
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
		ping := time.NewTicker(devicePingEvery)
		defer ping.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case e := <-c.out:
				wctx, wcancel := context.WithTimeout(ctx, 10*time.Second)
				// 下行内容（答复、团队消息……）里出现的已知凭据值同样隐去（R-295）
				b, err := json.Marshal(e)
				if err == nil {
					err = ws.Write(wctx, websocket.MessageText, h.redactBytes(b))
				}
				wcancel()
				if err != nil {
					cancel()
					return
				}
			case <-ping.C:
				pctx, pcancel := context.WithTimeout(ctx, devicePongWait)
				err := ws.Ping(pctx)
				pcancel()
				if err != nil {
					cancel()
					return
				}
			}
		}
	}()

	// 读循环只管收消息，处理交给下面的处理协程（R-374）：处理要读写数据库，数据库慢时会卡住；
	// 读循环一停，daemon 回的 Pong 就没人收，写协程的 Ping 10 秒等不到回应就断开连接，事件处理到一半被取消，重连后又重来。
	// 处理用不随连接取消的 context：连接断了，正在处理的那条照样做完，不留半截状态；还没轮到的丢掉，daemon 会重发没回执的。
	inbox := make(chan protocol.Envelope, deviceInbox)
	worked := make(chan struct{})
	go func() {
		defer close(worked)
		pctx := context.WithoutCancel(ctx)
		h.deviceHello(pctx, c)
		for e := range inbox {
			if ctx.Err() != nil {
				continue
			}
			mctx, mcancel := context.WithTimeout(pctx, deviceMsgTimeout)
			h.handleDeviceMsg(mctx, c, e)
			mcancel()
		}
	}()

	for {
		var e protocol.Envelope
		if err := wsjson.Read(ctx, ws, &e); err != nil {
			break
		}
		select {
		case inbox <- e:
		default: // 积压太多：丢掉这条，daemon 没收到回执会重发
			h.log.Warn("设备消息积压，丢弃一条待重发", "device", dev.Name, "type", e.Type)
		}
	}
	cancel()
	close(inbox)
	ws.Close(websocket.StatusNormalClosure, "")
	h.deviceGone(c)
	<-worked
}

// deviceInbox 是每条设备连接待处理消息的上限；deviceMsgTimeout 是单条消息最长处理时间（R-374）。
const (
	deviceInbox      = 256
	deviceMsgTimeout = 2 * time.Minute
)

// 每 devicePingEvery 发一次 Ping，devicePongWait 内收不到 Pong 就断开（测试里调短）。
var (
	devicePingEvery = 20 * time.Second
	devicePongWait  = 10 * time.Second
)

// deviceHello：设备刚连上时下发配置、待注入决策、身份、终端实例和身份提示词。
func (h *Hub) deviceHello(ctx context.Context, c *deviceConn) {
	if s, err := h.Settings(ctx); err == nil {
		cfg := h.deviceConfig(s)
		c.send(protocol.Envelope{Type: protocol.MsgConfig, Config: &cfg})
	}
	h.pushDecisions(ctx, c.dev.ID)
	h.pushRoles(ctx, c.dev.ID)
	h.pushInstances(ctx, c.dev.ID)
	h.pushRolePrompts(ctx, c)
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
	case protocol.MsgOrphans:
		h.setOrphans(c.dev.ID, e.Orphans)
	case protocol.MsgUsage:
		if h.laneUsage(ctx, e.Usage, e.Colds) && e.Batch != "" {
			c.send(protocol.Envelope{Type: protocol.MsgUsageAck, Ack: []string{e.Batch}})
		}
	case protocol.MsgShells:
		if h.laneShells(ctx, c.dev.ID, e.Shells) && e.Batch != "" {
			c.send(protocol.Envelope{Type: protocol.MsgShellsAck, Ack: []string{e.Batch}})
		}
	case protocol.MsgStopResult:
		if e.StopResult != nil {
			h.stopDone(*e.StopResult)
		}
	case protocol.MsgGitResult:
		if e.GitResult != nil {
			h.gitDone(*e.GitResult)
		}
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
		if changed {
			h.broker.publish("decisions", "sessions")
			h.pushDecisions(ctx, c.dev.ID)
		}
		// 回执放在新全集之后（R-413）：daemon 先收到「已送达」的全集再删本地回执，回执还在时收到的旧全集不会被当成重置
		c.send(protocol.Envelope{Type: protocol.MsgAck, Ack: deliveryIDs(e.Delivered)})
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
			h.sendNotify("", notify.Message{Event: notify.EventDeviceLost, Title: pushDeviceLostTitle(dev.Name), Body: pushDeviceLostBody, Group: "kitestring"}, "/")
		}
	})
}
