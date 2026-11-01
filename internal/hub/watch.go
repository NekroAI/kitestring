package hub

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NekroAI/kitestring/internal/blocks"
	"github.com/NekroAI/kitestring/internal/roles"
	"github.com/NekroAI/kitestring/internal/store"
)

// 订阅需求（R-249 P4，D-1027）：会话 watch 一条需求，需求有变动时收到一句话提醒，细节自己去需求下看。
//   - 订阅者 = 显式订阅 ∪ 关联这条需求且没结束的会话（隐式，不写行），去掉退订的；显式退订优先于隐式订阅。
//   - 两档（R-227）：要动手的立刻叫醒（验收标准被改、评审不通过、关联决策被作答、需求被退回或设为受阻）；
//     其余只告知（别的状态变化、描述修改、留言、证据、评审通过），不叫醒。mode = action 的只收要动手那一档。
//   - 自己引起的不通知自己；同一次操作里已经被 tellEdited / onReview / notifyAssigner / 用户留言 / ks wait --on 订阅事件
//     通知过的会话不重复送（记在请求的 told 集合里）；同一需求、同一收件人 60 秒内的只告知合并成一条。
//   - 统一入口 reqEvent，送达沿用团队消息（KindTell，fyi 列分档）。

// 订阅事件类型。
const (
	evStatus     = "status"      // 状态变化（只告知）
	evReturned   = "returned"    // 被退回（要动手）
	evBlocked    = "blocked"     // 设为受阻（要动手）
	evDetail     = "detail"      // 改了标题 / 描述 / 优先级 / 类型（只告知）
	evAcceptance = "acceptance"  // 改了验收标准（要动手）
	evComment    = "comment"     // 新留言（只告知）
	evEvidence   = "evidence"    // 附了证据（只告知）
	evReviewPass = "review_pass" // 评审通过（只告知）
	evReviewFail = "review_fail" // 评审不通过（要动手）
	evDecision   = "decision"    // 关联决策被作答（要动手）
)

// watchAction 是「要动手」那一档的事件。
var watchAction = map[string]bool{evReturned: true, evBlocked: true, evAcceptance: true, evReviewFail: true, evDecision: true}

// watchTellQ 是订阅提醒的标题（只用于显示；分档看 fyi 列）。
const watchTellQ = "需求订阅"

// watchMergeWindow：同一需求、同一收件人的只告知在这段时间内合并成一条。
const watchMergeWindow = 60 * time.Second

// watchMerge 记一条还没送达、可以并进新变动的只告知提醒。
type watchMerge struct {
	id    string
	at    time.Time
	items []string
}

// told 集合：一次请求里已经因这条需求收到过通知的会话（reqID|key），reqEvent 据此去重。
type toldCtxKey struct{}

type toldSet struct {
	mu sync.Mutex
	m  map[string]bool
}

// withTold 给 ctx 装上 told 集合（已经有就原样返回）。Handler 给每个请求装一个。
func withTold(ctx context.Context) context.Context {
	if _, ok := ctx.Value(toldCtxKey{}).(*toldSet); ok {
		return ctx
	}
	return context.WithValue(ctx, toldCtxKey{}, &toldSet{m: map[string]bool{}})
}

// markTold 记下 keys 已经因需求 reqID 收到过通知。
func markTold(ctx context.Context, reqID string, keys ...string) {
	s, ok := ctx.Value(toldCtxKey{}).(*toldSet)
	if !ok || reqID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range keys {
		if k != "" {
			s.m[reqID+"|"+k] = true
		}
	}
}

func wasTold(ctx context.Context, reqID, key string) bool {
	s, ok := ctx.Value(toldCtxKey{}).(*toldSet)
	if !ok {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m[reqID+"|"+key]
}

// withToldHandler 给每个 HTTP 请求装一个 told 集合。
func withToldHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(withTold(r.Context())))
	})
}

// watchWho 是提醒里写的「谁改的」：用户在网页上的操作没有会话，写「用户」。
func (h *Hub) watchWho(ctx context.Context, byKey string) string {
	if byKey == "" {
		return "用户"
	}
	x, err := h.store.GetSession(ctx, byKey)
	if err != nil {
		return "会话 " + byKey
	}
	who := roles.Name(x.Role)
	if who == "" {
		who = "会话"
	}
	return who + " " + sessionName(x)
}

// watchText 拼订阅提醒：一句话，带需求编号、变动和谁改的，请对方去需求下看。
func watchText(r store.Requirement, action bool, items []string) string {
	tier := "只告知"
	if action {
		tier = "要动手"
	}
	return fmt.Sprintf("你订阅的需求有变动（%s）：%s「%s」%s。细节见 ks req get %s；不想再收：ks req unwatch %s。", tier, r.Code, brief(r.Title, 30), strings.Join(items, "；"), r.Code, r.Code)
}

// reqEvent 是需求变动通知的统一入口：kind 是事件类型，byKey 是引起变动的会话（用户在网页上操作时为空），
// text 是变动本身（如「状态从进行中改为待验收」）。在其它通知之后调用，已被通知过的会话不重复送。
func (h *Hub) reqEvent(ctx context.Context, r store.Requirement, kind, byKey, text string) {
	if r.ID == "" {
		return
	}
	list, err := h.store.Watchers(ctx, r.ID)
	if err != nil {
		h.log.Warn("查需求订阅者失败", "err", err)
		return
	}
	action := watchAction[kind]
	item := text + "（" + h.watchWho(ctx, byKey) + "）"
	for _, w := range list {
		if w.SessionKey == byKey || wasTold(ctx, r.ID, w.SessionKey) || (w.Mode == store.WatchAction && !action) {
			continue
		}
		if action {
			if _, err := h.watchSend(ctx, r, w.SessionKey, false, watchText(r, true, []string{item})); err != nil {
				h.log.Warn("送需求订阅提醒失败", "err", err, "to", w.SessionKey)
			}
			continue
		}
		if h.watchMergeInto(ctx, r, w.SessionKey, item) {
			continue
		}
		d, err := h.watchSend(ctx, r, w.SessionKey, true, watchText(r, false, []string{item}))
		if err != nil {
			h.log.Warn("送需求订阅提醒失败", "err", err, "to", w.SessionKey)
			continue
		}
		h.mu.Lock()
		h.watchMerges[r.ID+"|"+w.SessionKey] = &watchMerge{id: d.ID, at: time.Now(), items: []string{item}}
		h.mu.Unlock()
	}
}

// watchMergeInto 把只告知的 item 并进 60 秒内发给 key、还没送达的那条提醒；并进去了返回 true。
func (h *Hub) watchMergeInto(ctx context.Context, r store.Requirement, key, item string) bool {
	h.mu.Lock()
	m := h.watchMerges[r.ID+"|"+key]
	if m == nil || time.Since(m.at) > watchMergeWindow {
		delete(h.watchMerges, r.ID+"|"+key)
		h.mu.Unlock()
		return false
	}
	items := append(append([]string(nil), m.items...), item)
	h.mu.Unlock()
	ok, err := h.store.MergeTell(ctx, m.id, watchText(r, false, items))
	if err != nil || !ok {
		h.mu.Lock()
		delete(h.watchMerges, r.ID+"|"+key) // 已送达：之后的另起一条
		h.mu.Unlock()
		return false
	}
	h.mu.Lock()
	m.items = items
	h.mu.Unlock()
	if d, err := h.store.GetDecision(ctx, m.id); err == nil {
		h.decisionsChanged(ctx, d) // 待送的内容变了，重新下发
	}
	return true
}

// watchSend 送一条订阅提醒（纸鸢自动发的，没有发送方，不能回复）。fyi = 只告知，不叫醒。
func (h *Hub) watchSend(ctx context.Context, r store.Requirement, to string, fyi bool, text string) (store.Decision, error) {
	d, err := h.store.CreateDecision(ctx, store.Decision{Kind: store.KindTell, SessionKey: to, ProjectID: r.ProjectID, RequirementID: r.ID, Question: watchTellQ, Answer: text, FYI: fyi})
	if err != nil {
		return d, err
	}
	h.decisionsChanged(ctx, d)
	return d, nil
}

// statusEvent 把状态变化归到订阅事件：退回、受阻要动手，其余只告知。
func statusEvent(before, after string) (kind, text string) {
	text = fmt.Sprintf("状态从%s改为%s", reqStatusZh[before], reqStatusZh[after])
	switch {
	case after == store.ReqBlocked:
		return evBlocked, "被设为受阻（原为" + reqStatusZh[before] + "）"
	case before == store.ReqReview && (after == store.ReqInProgress || after == store.ReqReady || after == store.ReqDraft):
		return evReturned, "被退回（待验收改为" + reqStatusZh[after] + "）"
	}
	return evStatus, text
}

// reqStatusEvent：需求 r 的状态从 before 变了，通知订阅者。
func (h *Hub) reqStatusEvent(ctx context.Context, r store.Requirement, before, byKey string) {
	if r.Status == before || r.ID == "" {
		return
	}
	kind, text := statusEvent(before, r.Status)
	h.reqEvent(ctx, r, kind, byKey, text)
}

// reviewFails 数一份审查意见里不通过的条目。
func reviewFails(p *blocks.Payload) int {
	n := 0
	if p == nil {
		return 0
	}
	if c := p.Checklist(); c != nil {
		for _, it := range c.Items {
			if it.Result == blocks.Fail {
				n++
			}
		}
	}
	return n
}

// watchDecisionAnswered：挂在需求上的决策被作答（用户答、主助理代答、用户推翻代答），通知订阅者。
// 提问方从答复本身知道结果，不重复送；改状态 / 开会话这类请求不算（状态变化另有通知）。
func (h *Hub) watchDecisionAnswered(ctx context.Context, d store.Decision, byKey string) {
	if d.Kind != store.KindDecision || d.RequirementID == "" || d.RequestKind != "" {
		return
	}
	r, err := h.store.GetRequirement(ctx, d.RequirementID)
	if err != nil {
		return
	}
	markTold(ctx, r.ID, d.SessionKey)
	what := "已作答"
	if byKey != "" {
		what = "已代答"
	}
	h.reqEvent(ctx, r, evDecision, byKey, fmt.Sprintf("关联决策 %s「%s」%s", d.Code, brief(d.Question, 30), what))
}

type watchOut struct {
	Body struct {
		Message  string `json:"message"`
		Watching bool   `json:"watching" doc:"现在是否订阅着"`
		Mode     string `json:"mode" enum:"all,action" doc:"all 两档都收；action 只收要动手的"`
		Linked   bool   `json:"linked" doc:"会话关联了这条需求（不显式订阅也会因关联而订阅）"`
	}
}

type watchingOut struct {
	Body struct {
		Text     string        `json:"text" doc:"写给会话读的一段：订阅了哪些需求、哪些是因关联而订阅、退订了哪些"`
		Watching []store.Watch `json:"watching"`
		Off      []store.Watch `json:"off" doc:"已退订的"`
	}
}

func (h *Hub) registerAgentWatch(api huma.API) {
	huma.Register(api, huma.Operation{OperationID: "agent-watch", Method: http.MethodPost, Path: "/api/agent/requirements/{ref}/watch", Tags: []string{"agent"},
		Summary:     "订阅 / 退订一条需求（R-249 P4）：需求有变动时收到一句话提醒",
		Description: "off = false 订阅（mode = action 只收要动手的：验收标准被改、评审不通过、关联决策被作答、被退回或设为受阻；默认 all 两档都收，只告知的不叫醒）；off = true 退订。关联这条需求、或正在做它的会话默认订阅，退订优先于这种默认订阅。自己引起的变动不通知自己。订阅者永远是当前会话（session_key）。"},
		func(ctx context.Context, in *struct {
			Ref  string `path:"ref"`
			Body struct {
				SessionKey     string `json:"session_key" minLength:"1" doc:"订阅的会话（调用者）"`
				Off            bool   `json:"off,omitempty" doc:"true = 退订"`
				Mode           string `json:"mode,omitempty" enum:"all,action," doc:"all（默认）两档都收；action 只收要动手的"`
				SessionClaimed bool   `json:"session_claimed,omitempty" doc:"session_key 是调用方用 --session 自报的（自动识别不到当前会话时，R-248）"`
			}
		}) (*watchOut, error) {
			b := in.Body
			me, err := h.store.GetSession(ctx, b.SessionKey)
			if err != nil {
				return nil, apiErr(err)
			}
			if b.SessionClaimed && sessionOffline(me) {
				return nil, huma.Error409Conflict("--session " + me.Key + " 不是一个没结束的会话，不能替它订阅")
			}
			r, err := h.store.GetRequirement(ctx, in.Ref)
			if err != nil {
				return nil, apiErr(err)
			}
			w, err := h.store.SetWatch(ctx, r.ID, me.Key, b.Mode, b.Off)
			if err != nil {
				return nil, apiErr(err)
			}
			out := &watchOut{}
			out.Body.Watching, out.Body.Mode, out.Body.Linked = !b.Off, w.Mode, w.Linked
			name := fmt.Sprintf("%s「%s」", r.Code, brief(r.Title, 30))
			if b.Off {
				out.Body.Message = "已退订 " + name + "：之后它的变动不再通知你。"
				if w.Linked {
					out.Body.Message += "你关联了它，退订优先于因关联而来的默认订阅；派活、交验收这类跟你职责有关的通知照常送。"
				}
				out.Body.Message += "要重新订阅用 ks req watch " + r.Code + "（MCP req_watch）。"
			} else {
				tier := "两档都收：要动手的（验收标准被改、评审不通过、关联决策被作答、被退回或受阻）立刻叫醒，只告知的（别的状态变化、描述、留言、证据、评审通过）等你下次醒来一并带上"
				if w.Mode == store.WatchAction {
					tier = "只收要动手的：验收标准被改、评审不通过、关联决策被作答、被退回或受阻，立刻叫醒"
				}
				out.Body.Message = "已订阅 " + name + "，" + tier + "；自己引起的变动不通知你。退订用 ks req unwatch " + r.Code + "（MCP req_watch off = true），看订阅了哪些用 ks req watching。"
			}
			if b.SessionClaimed {
				out.Body.Message += "（自动识别不到当前会话，已按 --session " + me.Key + " 订阅）"
			}
			return out, nil
		})
	huma.Register(api, huma.Operation{OperationID: "agent-watching", Method: http.MethodGet, Path: "/api/agent/watching", Tags: []string{"agent"},
		Summary: "列出当前会话订阅的需求（R-249 P4）：显式订阅的、因关联而订阅的、退订掉的"},
		func(ctx context.Context, in *struct {
			SessionKey string `query:"session_key" required:"true"`
		}) (*watchingOut, error) {
			if _, err := h.store.GetSession(ctx, in.SessionKey); err != nil {
				return nil, apiErr(err)
			}
			on, off, err := h.store.Watching(ctx, in.SessionKey)
			if err != nil {
				return nil, apiErr(err)
			}
			out := &watchingOut{}
			out.Body.Watching, out.Body.Off = on, off
			if out.Body.Watching == nil {
				out.Body.Watching = []store.Watch{}
			}
			if out.Body.Off == nil {
				out.Body.Off = []store.Watch{}
			}
			out.Body.Text = watchingText(on, off)
			return out, nil
		})
}

// watchingText 是 ks req watching / MCP req_watching 的输出。已完成、已搁置的因关联而订阅只计数不列。
func watchingText(on, off []store.Watch) string {
	var b strings.Builder
	var shown []store.Watch
	hidden := 0
	for _, w := range on {
		if w.Implicit && (w.Status == store.ReqDone || w.Status == store.ReqShelved) {
			hidden++
			continue
		}
		shown = append(shown, w)
	}
	if len(on) == 0 {
		b.WriteString("你没有订阅任何需求。")
	} else {
		fmt.Fprintf(&b, "你订阅了 %d 条需求：", len(on))
	}
	for _, w := range shown {
		how := "显式订阅"
		if w.Implicit {
			how = "因关联而订阅"
		}
		mode := "两档都收"
		if w.Mode == store.WatchAction {
			mode = "只收要动手的"
		}
		fmt.Fprintf(&b, "\n- %s「%s」（%s）· %s · %s", w.Code, brief(w.Title, 30), reqStatusZh[w.Status], how, mode)
	}
	if hidden > 0 {
		fmt.Fprintf(&b, "\n另有 %d 条已完成 / 已搁置的需求因关联而订阅，不逐条列出。", hidden)
	}
	for i, w := range off {
		if i == 0 {
			b.WriteString("\n已退订：")
		} else {
			b.WriteString("、")
		}
		b.WriteString(w.Code)
		if w.Linked {
			b.WriteString("（关联了，已退掉默认订阅）")
		}
	}
	b.WriteString("\n订阅：ks req watch R-x [--action-only]（MCP req_watch）；退订：ks req unwatch R-x（MCP req_watch off = true）。")
	return b.String()
}
