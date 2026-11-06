package hub

import (
	"context"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NekroAI/kitestring/internal/protocol"
	"github.com/NekroAI/kitestring/internal/store"
)

// 会话详情页按轮的时间线（R-273）。
// 一轮 = 主线程的一次提问（UserPromptSubmit）到下一次提问之前：你说的话（终端 / 网页）或纸鸢送达的消息 →
// 这一轮里纸鸢的动作（只认明确记录：需求状态、证据、决策、团队消息、留言、派子任务、简报、交接）→ 工具调用（收成一行）→ 它的回复。
// 这一轮结束（Stop）之后、下一轮开始之前发生的（多半是子任务做的）单独放在 after。

// Turn 是时间线上的一轮。
type Turn struct {
	ID    string     `json:"id" doc:"轮的标识：turn_id；没有时用提问事件的 ID"`
	Start time.Time  `json:"start"`
	End   *time.Time `json:"end,omitempty" doc:"这一轮结束（Stop）的时间；运行中时省略"`
	// Source：terminal 你在终端说的；web 你在网页上发的话送达并开启了这一轮；wake 会话被自动唤醒（来源见 Wake）；
	// 空 = 没有提问记录（老会话、或这一页最早的半轮）
	Source string `json:"source" enum:",terminal,web,wake"`
	Prompt string `json:"prompt,omitempty" doc:"你在终端说的话（截断）；纸鸢送达的消息开启的一轮为空"`
	// Wake：source = wake 时是谁唤醒了会话（R-287）。kite 待命期间纸鸢送达消息；timeout 待命到时；
	// bg 后台命令结束或监视有输出；agent 子任务交回结果；output 终端 ! 命令 / 斜杠命令的输出。
	// 早于 R-287 的记录按待命结束、子任务结束的记录推断，推不出的记 bg。
	Wake     string `json:"wake,omitempty" enum:",kite,timeout,bg,agent,output"`
	WakeNote string `json:"wake_note,omitempty" doc:"后台命令 / 监视 / 子任务的描述（会话自己写的），没有时为空"`
	// 提问 / 回复事件的 ID：对象索引里 p:<ID> / r:<ID> 指的就是它们
	PromptID string     `json:"prompt_id,omitempty"`
	ReplyID  string     `json:"reply_id,omitempty"`
	Inbox    []TurnItem `json:"inbox" doc:"这一轮收到的消息：网页上发的话、团队消息、你的答复、交给它的需求……"`
	Items    []TurnItem `json:"items" doc:"这一轮里纸鸢的动作，时间正序"`
	Tools    ToolSum    `json:"tools"`
	Reply    string     `json:"reply,omitempty" doc:"它在这一轮最后的回复（Markdown）"`
	// ReplyKind：full 完整记录；brief 只有摘要用的前 1500 字（早于每轮回复记录上线）；none 没有记录
	ReplyKind string     `json:"reply_kind" enum:"full,brief,none"`
	After     []TurnItem `json:"after" doc:"这一轮结束后、下一轮开始前发生的动作（多半是子任务）"`
	AfterTool ToolSum    `json:"after_tools" doc:"这一轮结束后子任务调用的工具"`
	Waits     []TurnWait `json:"waits,omitempty" doc:"这一轮里挂 ks wait 的记录"`
}

// TurnWait 是一次 ks wait。
type TurnWait struct {
	At    time.Time  `json:"at"`
	Until *time.Time `json:"until,omitempty"`
}

// ToolSum 是一轮工具调用的汇总：次数、按名字计数（多的在前）、逐条（截断到 200 条）。
type ToolSum struct {
	N     int        `json:"n"`
	Sub   int        `json:"sub" doc:"其中子任务调用的次数"`
	Names []ToolName `json:"names"`
	List  []ToolCall `json:"list"`
}

type ToolName struct {
	Name string `json:"name"`
	N    int    `json:"n"`
}

type ToolCall struct {
	Name  string    `json:"name"`
	Brief string    `json:"brief,omitempty"`
	At    time.Time `json:"at"`
	Sub   bool      `json:"sub,omitempty" doc:"子任务调用的"`
}

// TurnItem 是时间线上的一个节点：纸鸢记录（store.Act），或会话事件里明确的几种（压缩上下文、提问工具在问你）。
type TurnItem struct {
	store.Act
	Refs  []string `json:"refs" doc:"相关的需求 / 决策编号"`
	Model string   `json:"model,omitempty" doc:"派子任务：用量最多的模型 id"`
	// 收到的纸鸢提醒的类别（「待验收」「订阅提醒」……），text 已整理成一句话（D-1171）
	Label string `json:"label,omitempty"`
	// 网页上发的话的送达状态（只有 web 为真时）
	Delivery *WebDelivery `json:"delivery,omitempty"`
}

// 会话事件里的两种节点
const (
	ItemCompact = "compact" // 压缩了上下文
	ItemAsk     = "ask"     // 用提问工具在问你（AskUserQuestion / 计划待批准）
)

// WebDelivery 是网页上发的一句话的送达状态。
// woke 已唤醒，它在处理；queued 排到下一轮（它在跑 / 没挂 ks wait）；offline 已下线或已结束；delivered 已送进某一轮；closed 已关闭不再送。
type WebDelivery struct {
	Status  string `json:"status" enum:"woke,queued,offline,delivered,closed"`
	Reason  string `json:"reason"`
	Command string `json:"command,omitempty" doc:"已下线、要先接任时新会话接任的命令（ks takeover <身份>）"`
}

// SessionObject 是对象索引里的一项：这个会话的纸鸢记录里出现过的需求 / 决策；或子任务、消息、简报的计数。
type SessionObject struct {
	Code    string    `json:"code" doc:"R-x / D-x；计数项为 agents / messages / briefs"`
	Kind    string    `json:"kind" enum:"req,dec,agents,messages,briefs"`
	Title   string    `json:"title,omitempty"`
	Status  string    `json:"status,omitempty"`
	Pending bool      `json:"pending,omitempty" doc:"它提的、还在等你答的决策"`
	Items   []string  `json:"items" doc:"相关条目的 ID：纸鸢记录的 ID，以及 p:<轮>（提问提到）、r:<轮>（回复提到）"`
	Last    time.Time `json:"last"`
}

type turnsOut struct {
	Body struct {
		Turns   []Turn            `json:"turns" doc:"时间正序"`
		More    bool              `json:"more" doc:"更早还有轮"`
		Before  string            `json:"before,omitempty" doc:"往上加载更早一页时传的 before"`
		Objects []SessionObject   `json:"objects" doc:"对象索引：这个会话动过的需求、决策（待你答的在前，其余按最近动过排序），以及子任务 / 消息 / 简报的计数"`
		Pending []TurnItem        `json:"pending" doc:"网页上发的、还没送进某一轮的话（带送达状态）；只在最新一页"`
		Models  map[string]string `json:"agent_models" doc:"子任务用量最多的模型，键是 agent_id"`
		Running []RunTask         `json:"running" doc:"在跑的子任务 / 后台命令：名字、模型、开始时间，不含命令原文"`
	}
}

const turnsDefault, turnsMax = 12, 50

var refRe = regexp.MustCompile(`(?:^|[^A-Za-z0-9_-])([RD]-\d+)(?:[^A-Za-z0-9_-]|$)`)

func textRefs(s string) []string {
	var out []string
	for _, m := range refRe.FindAllStringSubmatch(s, -1) {
		out = append(out, m[1])
	}
	return out
}

func actRefs(a store.Act) []string {
	var out []string
	if a.Req != "" {
		out = append(out, a.Req)
	}
	if a.Dec != "" {
		out = append(out, a.Dec)
	}
	return out
}

// webDelivery 推出网页上发的一句话现在的送达状态（只从记录推，同团队消息的 deliveryOf）。
func webDelivery(a store.Act, x store.Session, turnOpen bool) *WebDelivery {
	switch {
	case a.Status == store.DecClosed:
		return &WebDelivery{Status: "closed", Reason: "已关闭，不再送达"}
	case a.Status == store.DecDelivered && turnOpen:
		return &WebDelivery{Status: "woke", Reason: "已送达，处理中"}
	case a.Status == store.DecDelivered:
		return &WebDelivery{Status: "delivered", Reason: "已送达"}
	case x.OfflineAt != nil && x.StoppedAt != nil && x.Role == "":
		return &WebDelivery{Status: "offline", Reason: "会话已停下，消息已排队"}
	case x.OfflineAt != nil && x.Role != "":
		return &WebDelivery{Status: "offline", Reason: "会话已下线，消息已排队，新会话接任时转交", Command: takeoverCmd(x.Role)}
	case x.RetiredAt != nil:
		return &WebDelivery{Status: "offline", Reason: "身份已由新会话接任，这个会话不会再收到"}
	case sessionOffline(x):
		return &WebDelivery{Status: "offline", Reason: offlineWhy(x) + "，会话恢复后才会收到"}
	}
	now, why := wakeAbility(x)
	if now {
		return &WebDelivery{Status: "woke", Reason: "已送达（" + why + "）"}
	}
	return &WebDelivery{Status: "queued", Reason: why}
}

func (h *Hub) sessionTurns(ctx context.Context, key string, before time.Time, limit int) (*turnsOut, error) {
	x, err := h.store.GetSession(ctx, key)
	if err != nil {
		return nil, apiErr(err)
	}
	if limit <= 0 {
		limit = turnsDefault
	}
	limit = min(limit, turnsMax)
	starts, err := h.store.TurnStarts(ctx, key, before, limit+1)
	if err != nil {
		return nil, err
	}
	out := &turnsOut{}
	var lo time.Time
	if len(starts) > limit {
		out.Body.More = true
		lo = starts[limit-1]
		out.Body.Before = lo.UTC().Format(time.RFC3339Nano)
	}
	// 往前多取一段：这一页最早一轮是自动唤醒时，要看它前面的待命结束、子任务结束记录才知道是谁唤醒的
	from := lo
	if !from.IsZero() {
		from = from.Add(-wakeLookback)
	}
	evs, err := h.store.EventsBetween(ctx, key, from, before)
	if err != nil {
		return nil, err
	}
	replies, err := h.store.TurnReplies(ctx, key, lo, before)
	if err != nil {
		return nil, err
	}
	acts, err := h.store.SessionActs(ctx, key)
	if err != nil {
		return nil, err
	}
	if out.Body.Models, err = h.store.AgentModels(ctx, key); err != nil {
		return nil, err
	}

	// 1. 事件 → 轮
	var turns []*Turn
	cur := func(at time.Time) *Turn {
		if len(turns) == 0 {
			turns = append(turns, &Turn{ID: "pre", Start: at, ReplyKind: "none"})
		}
		return turns[len(turns)-1]
	}
	addTool := func(sum *ToolSum, ev protocol.Event, sub bool) {
		sum.N++
		if sub {
			sum.Sub++
		}
		if len(sum.List) < 200 {
			sum.List = append(sum.List, ToolCall{Name: strings.TrimPrefix(ev.ToolName, "mcp__"), Brief: ev.ToolBrief, At: ev.Time, Sub: sub})
		}
	}
	asks := func(tool string) bool {
		return tool == "AskUserQuestion" || tool == "request_user_input" || tool == "ExitPlanMode"
	}
	var wk wakeTrack
	for _, ev := range evs {
		sub := ev.AgentID != ""
		if !lo.IsZero() && ev.Time.Before(lo) {
			wk.see(ev) // 这一页之前的，只用来判断唤醒来源
			continue
		}
		switch {
		case ev.Name == "UserPromptSubmit" && !sub:
			t := &Turn{ID: ev.TurnID, Start: ev.Time, Source: "terminal", Prompt: ev.Prompt, PromptID: ev.ID, ReplyKind: "none"}
			if t.ID == "" {
				t.ID = ev.ID
			}
			if ev.Source == protocol.SourceAuto {
				t.Source, t.Prompt = "wake", ""
				t.Wake, t.WakeNote = wk.kind(ev)
			}
			turns = append(turns, t)
		case ev.Name == "PreToolUse":
			t := cur(ev.Time)
			if asks(ev.ToolName) && !sub {
				t.Items = append(t.Items, TurnItem{Act: store.Act{ID: ev.ID, Kind: ItemAsk, At: ev.Time, Text: ev.ToolBrief, Via: ev.ToolName}, Refs: textRefs(ev.ToolBrief)})
				continue
			}
			if t.End != nil {
				addTool(&t.AfterTool, ev, sub)
			} else {
				addTool(&t.Tools, ev, sub)
			}
		case ev.Name == "Stop" && !sub:
			t := cur(ev.Time)
			end := ev.Time
			t.End, t.ReplyID = &end, ev.ID
			if r, ok := replies[ev.ID]; ok {
				t.Reply, t.ReplyKind = r, "full"
			} else if ev.LastReply != "" {
				t.Reply, t.ReplyKind = ev.LastReply, "brief"
			}
		case ev.Name == "SessionStart" && ev.Source == "compact":
			t := cur(ev.Time)
			t.Items = append(t.Items, TurnItem{Act: store.Act{ID: ev.ID, Kind: ItemCompact, At: ev.Time}})
		case ev.Name == protocol.EventWait:
			t := cur(ev.Time)
			t.Waits = append(t.Waits, TurnWait{At: ev.Time, Until: ev.Until})
		}
		wk.see(ev)
	}
	// 最新一轮没有记下回复时，用会话上存的最后回复（它就是这一轮的）
	if n := len(turns); n > 0 && turns[n-1].Reply == "" && turns[n-1].End != nil && before.IsZero() {
		if lr, err := h.store.GetLastReply(ctx, key); err == nil && lr != nil && !lr.At.Before(turns[n-1].Start) {
			turns[n-1].Reply, turns[n-1].ReplyKind = lr.Text, "full"
		}
	}

	// 2. 纸鸢记录按时间并入
	inWindow := func(at time.Time) bool {
		return (lo.IsZero() || !at.Before(lo)) && (before.IsZero() || at.Before(before))
	}
	find := func(at time.Time) *Turn {
		var t *Turn
		for _, x := range turns {
			if x.Start.After(at) {
				break
			}
			t = x
		}
		return t
	}
	byID := map[string]*Turn{}
	for _, t := range turns {
		byID[t.ID] = t
	}
	const slack = 3 * time.Second
	for _, a := range acts {
		it := TurnItem{Act: a, Refs: actRefs(a)}
		if a.Kind == store.ActAgent {
			it.Model = out.Body.Models[a.Agent]
		}
		if a.Kind == store.ActTellOut {
			it.Text = sentBody(a.Text, a.Req)
		}
		if a.Kind == store.ActInbox {
			// 收到的消息落在送达的那一轮：送达时记了轮 ID 的按 ID，经 ks wait 送达的落在送达之后开始的第一轮
			var t *Turn
			if a.DeliveredAt != nil {
				if bt, ok := byID[a.DeliveredTurn]; ok {
					t = bt
				} else {
					d := *a.DeliveredAt
					if !inWindow(d.Add(slack)) && !inWindow(d) {
						continue
					}
					// 先找送达之后开始的那一轮（经 ks wait 送达，醒来的提问紧跟其后）；再放宽到送达前几秒（两边时钟有偏差）
					for _, lead := range []time.Duration{0, slack} {
						for _, x := range turns {
							if !x.Start.Before(d.Add(-lead)) && x.Start.Before(d.Add(2*time.Minute)) {
								t = x
								break
							}
						}
						if t != nil {
							break
						}
					}
					if t == nil {
						t = find(d)
					}
				}
			}
			if t == nil {
				// 还没送进任何一轮：网页上发的话放进 pending（只在最新一页），其余不显示
				if a.Web && before.IsZero() {
					it.Delivery = webDelivery(a, x, a.Status == store.DecDelivered)
					out.Body.Pending = append(out.Body.Pending, it)
				}
				continue
			}
			if a.Web {
				it.Delivery = webDelivery(a, x, t.End == nil && t == turns[len(turns)-1])
				if t.Source == "wake" {
					t.Source = "web"
				}
			}
			it.Label, it.Text = inboxText(a)
			t.Inbox = append(t.Inbox, it)
			continue
		}
		if !inWindow(a.At) {
			continue
		}
		t := find(a.At)
		if t == nil {
			// 会话最早的提问之前（只在第一页）：放进最前面那段没有提问的
			if len(turns) == 0 || turns[0].ID != "pre" {
				turns = append([]*Turn{{ID: "pre", Start: a.At, ReplyKind: "none"}}, turns...)
			}
			t = turns[0]
		}
		if t.End != nil && a.At.After(t.End.Add(slack)) {
			t.After = append(t.After, it)
		} else {
			t.Items = append(t.Items, it)
		}
	}
	for _, t := range turns {
		sortItems(t.Items)
		sortItems(t.After)
		t.Tools.Names = toolNames(t.Tools.List)
		t.AfterTool.Names = toolNames(t.AfterTool.List)
		if t.Inbox == nil {
			t.Inbox = []TurnItem{}
		}
		if t.Items == nil {
			t.Items = []TurnItem{}
		}
		if t.After == nil {
			t.After = []TurnItem{}
		}
		out.Body.Turns = append(out.Body.Turns, *t)
	}
	if out.Body.Turns == nil {
		out.Body.Turns = []Turn{}
	}
	if out.Body.Pending == nil {
		out.Body.Pending = []TurnItem{}
	}
	if before.IsZero() {
		if out.Body.Running, err = h.runningTasks(ctx, x, acts, out.Body.Models); err != nil {
			return nil, err
		}
	}
	if out.Body.Running == nil {
		out.Body.Running = []RunTask{}
	}
	// 3. 对象索引（全会话）
	texts, err := h.store.SessionTexts(ctx, key)
	if err != nil {
		return nil, err
	}
	out.Body.Objects = sessionObjects(acts, texts)
	return out, nil
}

func sortItems(a []TurnItem) {
	sort.SliceStable(a, func(i, j int) bool { return a[i].At.Before(a[j].At) })
}

func toolNames(list []ToolCall) []ToolName {
	m := map[string]int{}
	var order []string
	for _, c := range list {
		if m[c.Name] == 0 {
			order = append(order, c.Name)
		}
		m[c.Name]++
	}
	out := make([]ToolName, 0, len(order))
	for _, n := range order {
		out = append(out, ToolName{Name: n, N: m[n]})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].N > out[j].N })
	return out
}

// sessionObjects 算对象索引：记录里出现过的需求 / 决策（以记录为准，文字里提到的只用来点亮相关的轮），
// 待你答的决策排最前，其余按最近动过排序；再加子任务 / 消息 / 简报的计数。
func sessionObjects(acts []store.Act, texts []store.SessionText) []SessionObject {
	idx := map[string]*SessionObject{}
	var order []*SessionObject
	get := func(code, kind string) *SessionObject {
		if o, ok := idx[code]; ok {
			return o
		}
		o := &SessionObject{Code: code, Kind: kind, Items: []string{}}
		idx[code] = o
		order = append(order, o)
		return o
	}
	counts := map[string]*SessionObject{}
	count := func(kind, id string, at time.Time) {
		o := counts[kind]
		if o == nil {
			o = &SessionObject{Code: kind, Kind: kind, Items: []string{}}
			counts[kind] = o
		}
		o.Items = append(o.Items, id)
		if at.After(o.Last) {
			o.Last = at
		}
	}
	for _, a := range acts {
		id := a.ID
		if a.Req != "" {
			o := get(a.Req, "req")
			o.Items = append(o.Items, id)
			if a.ReqTitle != "" {
				o.Title = a.ReqTitle
			}
			if a.ReqStatus != "" {
				o.Status = a.ReqStatus
			}
			if a.At.After(o.Last) {
				o.Last = a.At
			}
		}
		if a.Dec != "" {
			o := get(a.Dec, "dec")
			o.Items = append(o.Items, id)
			o.Title, o.Status = a.DecTitle, a.DecStatus
			o.Pending = a.DecStatus == store.DecPending
			if a.At.After(o.Last) {
				o.Last = a.At
			}
		}
		switch {
		case a.Kind == store.ActAgent:
			count("agents", id, a.At)
		case a.Kind == store.ActTellOut, a.Kind == store.ActInbox && a.Via == store.KindTell:
			count("messages", id, a.At)
		case a.Kind == store.ActBrief:
			count("briefs", id, a.At)
		}
	}
	// 提问 / 回复里提到的编号：只点亮已在索引里的对象
	for _, t := range texts {
		for _, code := range textRefs(t.Text) {
			o, ok := idx[code]
			if !ok {
				continue
			}
			id := "r:" + t.EventID
			if t.Prompt {
				id = "p:" + t.EventID
			}
			if len(o.Items) == 0 || o.Items[len(o.Items)-1] != id {
				o.Items = append(o.Items, id)
			}
		}
	}
	sort.SliceStable(order, func(i, j int) bool {
		if order[i].Pending != order[j].Pending {
			return order[i].Pending
		}
		return order[i].Last.After(order[j].Last)
	})
	out := make([]SessionObject, 0, len(order)+3)
	for _, o := range order {
		out = append(out, *o)
	}
	for _, k := range []string{"agents", "messages", "briefs"} {
		if o := counts[k]; o != nil {
			out = append(out, *o)
		}
	}
	return out
}

type replyOut struct {
	Body struct {
		ID       string      `json:"id" doc:"这句话的记录 ID"`
		Code     string      `json:"code" doc:"记录编号（D-x）"`
		Delivery WebDelivery `json:"delivery"`
	}
}

type repliesOut struct {
	Body struct {
		Replies []TurnItem `json:"replies" doc:"最近在网页上发给它的话（新的在前，最多 20 条），带送达状态"`
	}
}

func (h *Hub) registerSessionTurns(api huma.API) {
	huma.Register(api, huma.Operation{OperationID: "session-turns", Method: http.MethodGet, Path: "/api/sessions/{key}/turns", Tags: []string{"sessions"},
		Summary: "会话按轮的时间线（R-273）：每轮你说的话 / 纸鸢送达的消息、纸鸢动作、工具汇总、回复；附对象索引"},
		func(ctx context.Context, in *struct {
			Key    string `path:"key"`
			Before string `query:"before" doc:"只要这个时间之前开始的轮（上一页返回的 before）；不传是最新一页"`
			Limit  int    `query:"limit" doc:"最多几轮，默认 12、最多 50"`
		}) (*turnsOut, error) {
			var before time.Time
			if in.Before != "" {
				t, err := time.Parse(time.RFC3339Nano, in.Before)
				if err != nil {
					return nil, huma.Error400BadRequest("before 不是合法时间")
				}
				before = t
			}
			return h.sessionTurns(ctx, in.Key, before, in.Limit)
		})
	huma.Register(api, huma.Operation{OperationID: "session-replies", Method: http.MethodGet, Path: "/api/sessions/{key}/replies", Tags: []string{"sessions"},
		Summary: "网页上发给会话的话和它们的送达状态（R-273）"},
		func(ctx context.Context, in *keyPath) (*repliesOut, error) {
			x, err := h.store.GetSession(ctx, in.Key)
			if err != nil {
				return nil, apiErr(err)
			}
			acts, err := h.store.SessionActs(ctx, in.Key)
			if err != nil {
				return nil, err
			}
			out := &repliesOut{}
			out.Body.Replies = []TurnItem{}
			for i := len(acts) - 1; i >= 0 && len(out.Body.Replies) < 20; i-- {
				if a := acts[i]; a.Kind == store.ActInbox && a.Web {
					out.Body.Replies = append(out.Body.Replies, TurnItem{Act: a, Refs: []string{}, Delivery: webDelivery(a, x, false)})
				}
			}
			return out, nil
		})
}

// RunTask 是一个在跑的子任务或后台命令（R-273 验收第 5 条：名字 · 模型 · 时长，不显示原始命令）。
type RunTask struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind" enum:"agent,shell"`
	Name      string    `json:"name" doc:"子任务的任务描述 / 后台命令的描述；没有描述时为空（不拿命令原文顶替）"`
	Type      string    `json:"type,omitempty" doc:"子任务类型（Explore 等）或后台命令的类别（build / deploy / service / poll / other）"`
	Model     string    `json:"model,omitempty"`
	StartedAt time.Time `json:"started_at"`
}

// runningTasks：会话在跑的后台任务。子任务的名字取派子任务时记下的描述（lane_agents），后台命令取 run_in_background 的描述，
// 没写描述（描述就是命令本身）时只给类别。
func (h *Hub) runningTasks(ctx context.Context, x store.Session, acts []store.Act, models map[string]string) ([]RunTask, error) {
	if x.State == store.StateEnded {
		return nil, nil
	}
	names := map[string]store.Act{}
	for _, a := range acts {
		if a.Kind == store.ActAgent {
			names[a.Agent] = a
		}
	}
	shells, err := h.store.RunningShells(ctx, x.Key)
	if err != nil {
		return nil, err
	}
	byTask := map[string]protocol.ShellCmd{}
	for _, c := range shells {
		if c.Task != "" {
			byTask[c.Task] = c
		}
	}
	shellName := func(c protocol.ShellCmd) string {
		if c.Label == "" || c.Label == c.Cmd || strings.HasPrefix(c.Cmd, strings.TrimSuffix(c.Label, "…")) {
			return ""
		}
		return c.Label
	}
	var out []RunTask
	seen := map[string]bool{}
	for _, t := range x.Tasks {
		r := RunTask{ID: t.ID, Kind: t.Kind, StartedAt: t.StartedAt}
		if t.Kind == "agent" {
			a := names[t.ID]
			r.Name, r.Type, r.Model = a.Text, t.AgentType, models[t.ID]
			if r.Name == "" {
				r.Name = t.Label
			}
			if r.Type == "" {
				r.Type = a.Note
			}
		} else if c, ok := byTask[t.ID]; ok {
			r.Name, r.Type = shellName(c), c.Kind
			seen[c.Ref] = true
		} else {
			r.Type = "other" // 旧版 daemon 只报了命令：不显示命令原文
		}
		out = append(out, r)
	}
	for _, c := range shells {
		if seen[c.Ref] || (c.Task != "" && slicesHasTask(x.Tasks, c.Task)) {
			continue
		}
		out = append(out, RunTask{ID: c.Ref, Kind: "shell", Name: shellName(c), Type: c.Kind, StartedAt: time.UnixMilli(c.Start).UTC()})
	}
	return out, nil
}

func slicesHasTask(ts []store.BgTask, id string) bool {
	for _, t := range ts {
		if t.ID == id {
			return true
		}
	}
	return false
}

// subWakeGap：老记录里子任务结束后多久之内的自动唤醒算子任务回报。
const subWakeGap = 30 * time.Second

// wakeLookback：判断一页最早一轮的唤醒来源时往前多看的时长。
const wakeLookback = 30 * time.Minute

// wakeTrack 顺着事件记下「上一轮之后」发生的待命结束、子任务结束，用来判断下一条自动注入的消息是谁送来的（R-287）。
// ks wait 在 Claude Code 里是后台命令，它结束时会话收到的是一条普通的后台命令通知，只有对上 KsWaitEnd 才知道是纸鸢送达。
type wakeTrack struct {
	waitEnd  string // 上一轮开始后最近一次待命结束的原因：delivered / timeout / canceled
	subDone  time.Time // 主线程上一轮结束后最近一次子任务结束（早于 R-287 的记录靠它认子任务回报）
	mainIdle bool   // 主线程上一轮已结束
}

func (w *wakeTrack) see(ev protocol.Event) {
	sub := ev.AgentID != ""
	switch {
	case ev.Name == "UserPromptSubmit" && !sub:
		*w = wakeTrack{}
	case ev.Name == "Stop" && !sub:
		w.mainIdle, w.subDone = true, time.Time{}
	case ev.Name == "SubagentStop" && w.mainIdle && ev.AgentType != "":
		// 只认派出的子任务（带类型）；Claude Code 自己的内部子代理结束时也发 SubagentStop，但不带类型、也不会唤醒会话
		w.subDone = ev.Time
	case ev.Name == protocol.EventWaitEnd:
		w.waitEnd = ev.Reason
	}
}

// kind 给一条自动注入的提问判来源。
func (w *wakeTrack) kind(ev protocol.Event) (string, string) {
	if ev.Wake == "" || ev.Wake == protocol.WakeBg {
		switch w.waitEnd {
		case "delivered":
			return "kite", ""
		case "timeout":
			return "timeout", ""
		}
	}
	if ev.Wake != "" {
		return ev.Wake, ev.WakeNote
	}
	// 子任务交回结果时会话马上被唤醒；隔得久的是别的通知
	if !w.subDone.IsZero() && ev.Time.Sub(w.subDone) < subWakeGap {
		return protocol.WakeAgent, ""
	}
	return protocol.WakeBg, ""
}
