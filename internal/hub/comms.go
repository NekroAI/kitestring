package hub

import (
	"context"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NekroAI/kitestring/internal/store"
)

// 通讯页（R-249 P5，D-1063 方案 B）：网页上只读地看本项目会话之间的消息、广播和送达状态。
// 左栏是在线会话（同 ks team sessions，加今天收发几条）；右栏按时间列消息，一次广播合成一条、逐个收件人带送达状态。
// 纸鸢自动发的提醒（没有发送方的团队消息）标 system，网页默认收起。
// 没送到的分两组：等对方醒来（还会变，置顶、导航上标数字）和对方已结束 / 失联（永远送不到，单独折叠，D-1071）。
// 送达状态只从记录推，和 ks team sent 同一套（deliveryOf）。

// commsLimit：一次最多列多少条消息（广播按收件人算）。
const commsLimit = 2000

// CommsPeer 是消息的一方：会话（身份、标题、工具），子任务发的再带子任务名。
type CommsPeer struct {
	Key       string `json:"key"`
	Role      string `json:"role" doc:"身份；空为无身份"`
	Title     string `json:"title"`
	Tool      string `json:"tool,omitempty" doc:"所用工具：Claude Code / claude-x / Codex 终端 / Codex App / DSH"`
	Agent     string `json:"agent,omitempty" doc:"发消息的子任务 agent_id；会话主线程发的为空"`
	AgentName string `json:"agent_name,omitempty" doc:"子任务的名字"`
	Ended     bool   `json:"ended,omitempty" doc:"会话已结束、失联或被标为已完成"`
}

// CommsMsg 是通讯页上的一条消息；一次广播合成一条，Recipients 逐个列出。
type CommsMsg struct {
	Code             string     `json:"code" doc:"消息编号（D-x）；广播是第一行的编号"`
	Codes            []string   `json:"codes" doc:"广播拆出的各行编号"`
	CreatedAt        time.Time  `json:"created_at"`
	System           bool       `json:"system" doc:"纸鸢自动发的订阅 / 状态提醒（没有发送方）"`
	SystemLabel      string     `json:"system_label,omitempty" doc:"纸鸢提醒的类别，如「交验收」「用户改需求状态」"`
	From             *CommsPeer `json:"from,omitempty" doc:"发送方；纸鸢提醒为空"`
	Recipients       []Delivery `json:"recipients" doc:"收件人与各自的送达状态"`
	Broadcast        bool       `json:"broadcast"`
	FYI              bool       `json:"fyi" doc:"只告知；否则要动手"`
	Requirement      string     `json:"requirement,omitempty"`
	RequirementTitle string     `json:"requirement_title,omitempty"`
	ReplyTo          string     `json:"reply_to,omitempty" doc:"回复的是哪条（D-x）"`
	Text             string     `json:"text" doc:"正文：去掉了发送方、需求编号这类前缀"`
	Full             string     `json:"full" doc:"原文"`
}

// CommsSession 是左栏的一个会话：在线会话列表的一项，加今天收发几条、有几条在等它醒来。
type CommsSession struct {
	RosterEntry
	In           int        `json:"in" doc:"今天收到的会话消息（不含纸鸢提醒）"`
	Out          int        `json:"out" doc:"今天发出的会话消息（一次广播算一条）"`
	Waiting      int        `json:"waiting" doc:"发给它、还在等它醒来的消息数"`
	WaitingSince *time.Time `json:"waiting_since,omitempty" doc:"最早那条的发送时间"`
}

type commsOut struct {
	Body struct {
		ProjectID   string         `json:"project_id"`
		ProjectName string         `json:"project_name"`
		Sessions    []CommsSession `json:"sessions" doc:"待命 → 忙 → 在线 → 离线"`
		Messages    []CommsMsg     `json:"messages" doc:"时间范围内的消息，新的在前（含纸鸢提醒）"`
		Waiting     []CommsMsg     `json:"waiting" doc:"还在等对方醒来的消息（不论等了多久），新的在前"`
		Offline     []CommsMsg     `json:"offline" doc:"没送到、对方已结束或失联的消息，新的在前"`
		Today       int            `json:"today" doc:"今天的会话消息条数（一次广播算一条，不含纸鸢提醒）"`
		TodayCasts  int            `json:"today_broadcasts" doc:"今天的广播次数"`
		TodaySystem int            `json:"today_system" doc:"今天的纸鸢提醒条数"`
		Truncated   bool           `json:"truncated" doc:"时间范围内的消息超过上限，只列了最近的"`
	}
}

type commsPendingOut struct {
	Body struct {
		Count int `json:"count" doc:"所有项目里还在等对方醒来的消息数（导航上的数字；一次广播算一条）"`
	}
}

// sysHead 是纸鸢提醒开头的「你订阅的事件（review · 交验收）：」这类前缀。
var sysHead = regexp.MustCompile(`^[^（：]{2,12}（[a-z_]+ · ([^）]+)）：`)

// watchHead 是 R-249 订阅提醒的前缀「你订阅的需求有变动（只告知）：」。
var watchHead = regexp.MustCompile(`^你订阅的需求有变动（[^）]*）：`)

// sysTails 是纸鸢提醒结尾写给 Agent 的操作提示，网页上不显示。
var sysTails = []string{"细节见 ks req get", " 只是告诉你", "需要细节用 req_get", "等用户验收，不必回复", "不必回复；"}

// keyParen 是正文里写给 Agent 的会话键「（claude:xxxx）」，网页上发送方已单独显示，去掉。
var keyParen = regexp.MustCompile(`（[a-z][a-z-]*:[0-9A-Za-z-]{6,}）`)

// shrinkReqs 把正文里的「R-12「标题」」缩成「R-12」：网页上编号会画成印记，标题在悬停里。
func shrinkReqs(text string) string {
	var b strings.Builder
	for {
		loc := reqRef.FindStringIndex(text)
		if loc == nil {
			b.WriteString(text)
			return b.String()
		}
		b.WriteString(text[:loc[1]-len("「")])
		rest := text[loc[1]-len("「"):]
		end := quoteEnd(rest)
		if end < 0 {
			b.WriteString(rest)
			return b.String()
		}
		text = rest[end:]
		if text != "" && !strings.HasPrefix(text, "，") && !strings.HasPrefix(text, "。") && !strings.HasPrefix(text, "）") {
			b.WriteString(" ")
		}
	}
}

var reqRef = regexp.MustCompile(`R-\d+「`)

// sysText 拆出纸鸢提醒的类别和正文。
func sysText(d store.Decision) (label, text string) {
	a := strings.TrimSpace(d.Answer)
	switch {
	case sysHead.MatchString(a):
		m := sysHead.FindStringSubmatchIndex(a)
		label, text = a[m[2]:m[3]], a[m[1]:]
	case watchHead.MatchString(a):
		label, text = "订阅提醒", a[len(watchHead.FindString(a)):]
	case d.Question == watchTellQ:
		label, text = "订阅提醒", a
	default:
		label, text = "通知", a
	}
	for _, t := range sysTails {
		if i := strings.Index(text, t); i > 0 {
			text = text[:i]
		}
	}
	return label, strings.TrimSpace(text)
}

// stripReq 去掉正文开头的「R-12「标题」」：需求单独显示成印记。标题里可能套着「」，按层数找收尾。
func stripReq(text, code string) string {
	if code == "" || !strings.HasPrefix(text, code+"「") {
		return text
	}
	if end := quoteEnd(text[len(code):]); end > 0 {
		return strings.TrimLeft(strings.TrimSpace(text[len(code)+end:]), "：:，, ")
	}
	return text
}

// quoteEnd：s 以「开头，返回配对的」之后的位置；截断过的标题以「…」」收尾，里面没配对的「不再算层数。找不到返回 -1。
func quoteEnd(s string) int {
	depth, prev := 0, rune(0)
	for i, r := range s {
		switch r {
		case '「':
			depth++
		case '」':
			if depth--; depth == 0 || prev == '…' {
				return i + len("」")
			}
		}
		prev = r
	}
	return -1
}

// commsBuilder 把消息行拼成通讯页的条目，会话、子任务名、回复编号都按需查一次。
type commsBuilder struct {
	h        *Hub
	at       time.Time
	sessions map[string]*store.Session
	agents   map[string]string
	replies  map[string]string
}

func (h *Hub) newCommsBuilder() *commsBuilder {
	return &commsBuilder{h: h, at: time.Now(), sessions: map[string]*store.Session{}, agents: map[string]string{}, replies: map[string]string{}}
}

func (b *commsBuilder) session(ctx context.Context, key string) *store.Session {
	if x, ok := b.sessions[key]; ok {
		return x
	}
	var p *store.Session
	if x, err := b.h.store.GetSession(ctx, key); err == nil {
		p = &x
	}
	b.sessions[key] = p
	return p
}

func (b *commsBuilder) delivery(ctx context.Context, d store.Decision) Delivery {
	x := b.session(ctx, d.SessionKey)
	if x == nil {
		return deliveryOf(d, store.Session{}, false, b.at)
	}
	return deliveryOf(d, *x, true, b.at)
}

// build 把消息行（新的在前）拼成条目；同一次广播的各行合成一条。
func (b *commsBuilder) build(ctx context.Context, list []store.Decision) []CommsMsg {
	var ids []string
	for _, d := range list {
		if d.ReplyTo != "" {
			if _, ok := b.replies[d.ReplyTo]; !ok {
				ids = append(ids, d.ReplyTo)
			}
		}
	}
	if len(ids) > 0 {
		if m, err := b.h.store.DecisionCodes(ctx, ids); err == nil {
			for k, v := range m {
				b.replies[k] = v
			}
		}
	}
	out := []CommsMsg{}
	casts := map[string]int{}
	for _, d := range list {
		if d.BroadcastID != "" {
			if i, ok := casts[d.BroadcastID]; ok {
				m := &out[i]
				m.Codes = append([]string{d.Code}, m.Codes...) // 组内按编号升序
				m.Code = d.Code
				m.Recipients = append([]Delivery{b.delivery(ctx, d)}, m.Recipients...)
				if d.CreatedAt.Before(m.CreatedAt) {
					m.CreatedAt = d.CreatedAt
				}
				continue
			}
			casts[d.BroadcastID] = len(out)
		}
		out = append(out, b.msg(ctx, d))
	}
	return out
}

func (b *commsBuilder) msg(ctx context.Context, d store.Decision) CommsMsg {
	m := CommsMsg{Code: d.Code, Codes: []string{d.Code}, CreatedAt: d.CreatedAt, Broadcast: d.BroadcastID != "", FYI: d.FYI,
		Requirement: d.RequirementCode, RequirementTitle: d.RequirementTitle, ReplyTo: b.replies[d.ReplyTo], Full: d.Answer,
		Recipients: []Delivery{b.delivery(ctx, d)}}
	if d.AssignedBy == "" {
		m.System = true
		m.SystemLabel, m.Text = sysText(d)
	} else {
		p := &CommsPeer{Key: d.AssignedBy, Role: d.FromRole, Agent: d.FromAgent}
		if x := b.session(ctx, d.AssignedBy); x != nil {
			p.Title, p.Tool, p.Ended = x.Title, x.Harness(), sessionOffline(*x)
			if p.Title == "" {
				p.Title = x.LastPrompt
			}
			if p.Role == "" {
				p.Role = x.Role
			}
		}
		if d.FromAgent != "" {
			k := d.AssignedBy + "|" + d.FromAgent
			name, ok := b.agents[k]
			if !ok {
				name = b.h.store.AgentLabel(ctx, d.AssignedBy, d.FromAgent)
				b.agents[k] = name
			}
			p.AgentName = name
		}
		m.From = p
		m.Text = stripReq(strings.TrimSpace(sentText(d.Answer, d.AssignedBy)), d.RequirementCode)
	}
	m.Text = strings.TrimSpace(shrinkReqs(keyParen.ReplaceAllString(m.Text, "")))
	return m
}

func (h *Hub) registerCommsWeb(api huma.API) {
	tag := []string{"comms"}
	huma.Register(api, huma.Operation{OperationID: "get-comms", Method: http.MethodGet, Path: "/api/comms", Tags: tag,
		Summary:     "通讯页（R-249）：项目里的在线会话、会话之间的消息与广播、送达状态（只读）",
		Description: "messages 是 [since, until) 之间的消息，新的在前，含纸鸢自动发的提醒（system = true）；一次广播合成一条，recipients 逐个列送达状态。waiting 是还在等对方醒来的消息（不论多久），offline 是发给已结束 / 失联会话、永远送不到的消息。sessions 带今天（day_start 起）收发几条。"},
		func(ctx context.Context, in *struct {
			ProjectID string `query:"project_id" required:"true"`
			Since     string `query:"since" doc:"开始时间（RFC3339），不填为 24 小时前"`
			Until     string `query:"until" doc:"结束时间（RFC3339），不填为现在"`
			DayStart  string `query:"day_start" doc:"今天从几点算（浏览器本地零点，RFC3339），不填为 24 小时前"`
			Ended     bool   `query:"ended" doc:"左栏也列已结束的会话（最近 50 个）"`
		}) (*commsOut, error) {
			p, err := h.store.GetProject(ctx, in.ProjectID)
			if err != nil {
				return nil, apiErr(err)
			}
			now := time.Now()
			parse := func(s string, def time.Time) (time.Time, error) {
				if strings.TrimSpace(s) == "" {
					return def, nil
				}
				t, err := time.Parse(time.RFC3339, s)
				if err != nil {
					return t, huma.Error400BadRequest("时间格式不对：" + s)
				}
				return t, nil
			}
			since, err := parse(in.Since, now.Add(-24*time.Hour))
			if err != nil {
				return nil, err
			}
			until, err := parse(in.Until, time.Time{})
			if err != nil {
				return nil, err
			}
			day, err := parse(in.DayStart, now.Add(-24*time.Hour))
			if err != nil {
				return nil, err
			}
			out := &commsOut{}
			out.Body.ProjectID, out.Body.ProjectName = p.ID, p.Name
			b := h.newCommsBuilder()

			rows, err := h.store.ProjectTells(ctx, p.ID, since, until, commsLimit)
			if err != nil {
				return nil, err
			}
			out.Body.Truncated = len(rows) >= commsLimit
			out.Body.Messages = b.build(ctx, rows)

			unsent, err := h.store.UnsentTells(ctx, p.ID, 500)
			if err != nil {
				return nil, err
			}
			out.Body.Waiting, out.Body.Offline = []CommsMsg{}, []CommsMsg{}
			waitingTo := map[string][]time.Time{}
			for _, m := range b.build(ctx, unsent) {
				var w, off bool
				for _, r := range m.Recipients {
					switch r.Status {
					case PendingWakeSt:
						w = true
						waitingTo[r.To] = append(waitingTo[r.To], m.CreatedAt)
					case OfflineSt:
						off = true
					}
				}
				if w {
					out.Body.Waiting = append(out.Body.Waiting, m)
				} else if off {
					out.Body.Offline = append(out.Body.Offline, m)
				}
			}

			// 今天的收发：不含纸鸢提醒；一次广播对发送方算一条、对每个收件人各算一条
			todayRows := rows
			if day.Before(since) || (!until.IsZero() && until.Before(now)) {
				if todayRows, err = h.store.ProjectTells(ctx, p.ID, day, time.Time{}, commsLimit); err != nil {
					return nil, err
				}
			}
			in1, out1 := map[string]int{}, map[string]int{}
			seenCast := map[string]bool{}
			for _, d := range todayRows {
				if d.CreatedAt.Before(day) {
					continue
				}
				if d.AssignedBy == "" {
					out.Body.TodaySystem++
					continue
				}
				in1[d.SessionKey]++
				if d.BroadcastID != "" {
					if seenCast[d.BroadcastID] {
						continue
					}
					seenCast[d.BroadcastID] = true
					out.Body.TodayCasts++
				}
				out1[d.AssignedBy]++
				out.Body.Today++
			}

			list, _, err := h.store.SessionRoster(ctx, p.ID, in.Ended, map[bool]int{true: rosterLimit}[in.Ended])
			if err != nil {
				return nil, err
			}
			out.Body.Sessions = []CommsSession{}
			for _, x := range list {
				e := CommsSession{RosterEntry: toRosterEntry(x, "", b.at), In: in1[x.Key], Out: out1[x.Key]}
				if ts := waitingTo[x.Key]; len(ts) > 0 {
					e.Waiting = len(ts)
					sort.Slice(ts, func(i, j int) bool { return ts[i].Before(ts[j]) })
					first := ts[0]
					e.WaitingSince = &first
				}
				out.Body.Sessions = append(out.Body.Sessions, e)
			}
			sort.SliceStable(out.Body.Sessions, func(i, j int) bool {
				return availRank[out.Body.Sessions[i].Status] < availRank[out.Body.Sessions[j].Status]
			})
			return out, nil
		})

	huma.Register(api, huma.Operation{OperationID: "get-comms-pending", Method: http.MethodGet, Path: "/api/comms/pending", Tags: tag,
		Summary: "所有项目里还在等对方醒来的团队消息数（R-249，导航「通讯」旁的数字）；发给已结束 / 失联会话的不算"},
		func(ctx context.Context, _ *struct{}) (*commsPendingOut, error) {
			unsent, err := h.store.UnsentTells(ctx, "", 1000)
			if err != nil {
				return nil, err
			}
			out := &commsPendingOut{}
			for _, m := range h.newCommsBuilder().build(ctx, unsent) { // 一次广播算一条，和通讯页置顶的条数一致
				for _, r := range m.Recipients {
					if r.Status == PendingWakeSt {
						out.Body.Count++
						break
					}
				}
			}
			return out, nil
		})
}
