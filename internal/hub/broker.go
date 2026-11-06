package hub

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/NekroAI/kitestring/internal/store"
)

// broker 向 Web 端推送变化（SSE）。两种消息：
//   - changed：某类数据变了，网页重新拉取（需求、决策等低频变化）；
//   - session：一个会话的最新完整数据，网页直接替换缓存里那一条，不再整表重拉（R-56，Agent 事件是高频的）。
//
// 每条消息带一个递增序号 rev。会话列表接口返回查询开始时的 rev，网页把列表请求在途期间收到的、
// 序号更新的会话推送重新叠加上去，旧的列表结果不会盖掉新的推送。
// 慢客户端的缓冲区满了丢消息时，记下“欠账”，缓过来后补发一次全量重拉，不会漏。
type broker struct {
	mu     sync.Mutex
	rev    int64
	subs   map[*sub]struct{}
	guests map[*guestSub]struct{} // 分享访客的实时通道（R-281）：只收「有变化」的信号，不带数据
}

// guestSub 是一个访客的实时连接：任何变化都只往 ch 里放一个信号（满了就是已有信号待处理，天然合并），
// 访客页收到后重新拉取它看得到的内容；gone 关闭表示链接被撤回 / 分享失效，连接要立刻断开。
type guestSub struct {
	shareID, linkID string
	ch              chan struct{}
	gone            chan struct{}
	once            sync.Once
}

func (g *guestSub) close() { g.once.Do(func() { close(g.gone) }) }

type sub struct {
	ch    chan string // 已编码好的 SSE 消息
	owed  bool        // 丢过消息，要补发一次全量重拉
	owedM sync.Mutex
}

// settle：丢过消息就补发一次全量重拉。
func (s *sub) settle(w http.ResponseWriter) {
	s.owedM.Lock()
	owed := s.owed
	s.owed = false
	s.owedM.Unlock()
	if owed {
		data, _ := json.Marshal(allTopics)
		fmt.Fprintf(w, "event: changed\ndata: %s\n\n", data)
	}
}

// allTopics 是全量重拉时让网页失效的全部数据类型。
const allTopics = "sessions,requirements,decisions,projects,devices,plans,settings,lanes,shares,secrets"

func newBroker() *broker { return &broker{subs: map[*sub]struct{}{}, guests: map[*guestSub]struct{}{}} }

// Rev 返回当前序号（会话列表在查询前读取）。
func (b *broker) Rev() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.rev
}

func (b *broker) send(msg func(rev int64) string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.rev++
	m := msg(b.rev)
	for s := range b.subs {
		select {
		case s.ch <- m:
		default:
			s.owedM.Lock()
			s.owed = true
			s.owedM.Unlock()
		}
	}
	for g := range b.guests {
		select {
		case g.ch <- struct{}{}:
		default:
		}
	}
}

func (b *broker) addGuest(g *guestSub) {
	b.mu.Lock()
	b.guests[g] = struct{}{}
	b.mu.Unlock()
}

func (b *broker) removeGuest(g *guestSub) {
	b.mu.Lock()
	delete(b.guests, g)
	b.mu.Unlock()
}

// dropGuests 断开分享（linkID 为空时是整个分享）的访客实时连接：撤回、改密码、删除后立刻生效。
func (b *broker) dropGuests(shareID, linkID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for g := range b.guests {
		if g.shareID == shareID && (linkID == "" || g.linkID == linkID) {
			g.close()
		}
	}
}

func (b *broker) publish(topics ...string) {
	data, _ := json.Marshal(strings.Join(topics, ","))
	b.send(func(int64) string { return fmt.Sprintf("event: changed\ndata: %s\n\n", data) })
}

// publishSession 推送一个会话的最新完整数据。
func (b *broker) publishSession(x store.Session) {
	b.send(func(rev int64) string {
		data, _ := json.Marshal(struct {
			Rev     int64         `json:"rev"`
			Session store.Session `json:"session"`
		}{rev, x})
		return fmt.Sprintf("event: session\ndata: %s\n\n", data)
	})
}

// clock：心跳里的服务端时间（毫秒时间戳）。
func clock() string { return fmt.Sprintf(`{"now":%d}`, time.Now().UnixMilli()) }

func (b *broker) serve(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "不支持流式响应", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	s := &sub{ch: make(chan string, 64)}
	b.mu.Lock()
	b.subs[s] = struct{}{}
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		delete(b.subs, s)
		b.mu.Unlock()
	}()
	// hello / ping 带上服务端时间，网页据此对齐时钟（工作台时间线按服务端时间画，R-170）
	fmt.Fprintf(w, "retry: 3000\nevent: hello\ndata: %s\n\n", clock())
	fl.Flush()
	// 心跳用具名事件而不是注释：网页能看到它，据此发现“连接还开着、其实已经断了”的情况（R-65）
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case msg := <-s.ch:
			fmt.Fprint(w, msg)
			s.settle(w)
			fl.Flush()
		case <-ping.C:
			fmt.Fprintf(w, "event: ping\ndata: %s\n\n", clock())
			s.settle(w)
			fl.Flush()
		}
	}
}
