package hub

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NekroAI/kitestring/internal/protocol"
	"github.com/NekroAI/kitestring/internal/roles"
	"github.com/NekroAI/kitestring/internal/store"
	"github.com/NekroAI/kitestring/internal/writing"
)

// 会话通信（R-249 P3）：点对点消息的送达状态、项目内广播、查自己发过的消息。
// 送达状态只从记录推（原则第 11 条）：消息行已标送达 → delivered；收件会话已结束 / 失联 → offline；
// 其余 → pending_wake，原因用 wakeAbility 写（马上唤醒 / 等这一轮结束 / Codex 等用户下次发消息……）。

// 送达状态的取值。
const (
	DeliveredSt   = "delivered"    // 已送达：对方会话已收到（delivered_at）
	PendingWakeSt = "pending_wake" // 待送达：已入队，对方挂着 ks wait 时马上唤醒，否则等它这一轮结束 / 下次用户发消息
	OfflineSt     = "offline"      // 无法送达：会话已结束或失联；点对点报错退回，广播跳过
)

// Delivery 是一条团队消息对一个收件人的送达状态。
type Delivery struct {
	Code        string     `json:"code,omitempty" doc:"这一行消息的编号（D-x）；广播跳过的离线会话没有"`
	To          string     `json:"to" doc:"收件会话键"`
	Title       string     `json:"title,omitempty" doc:"收件会话标题"`
	Role        string     `json:"role,omitempty" doc:"收件会话的身份；空为无身份"`
	Tool        string     `json:"tool" doc:"收件会话所用工具：Claude Code / claude-x / Codex 终端 / Codex App / DSH"`
	Status      string     `json:"status" enum:"delivered,pending_wake,offline" doc:"delivered 已送达；pending_wake 待送达；offline 无法送达"`
	StatusLabel string     `json:"status_label"`
	Reason      string     `json:"reason" doc:"送达状态的原因：已送达时间、等什么、为什么离线"`
	DeliveredAt *time.Time `json:"delivered_at,omitempty"`
}

// sessionOffline：会话已结束、失联或被用户标为已完成，消息不会被它处理。
func sessionOffline(x store.Session) bool {
	return x.State == store.StateEnded || x.State == store.StateLost || x.DoneAt != nil
}

// offlineWhy 是离线会话的原因。
func offlineWhy(x store.Session) string {
	if x.DoneAt != nil && x.State != store.StateEnded && x.State != store.StateLost {
		return "会话已归档"
	}
	_, why := wakeAbility(x)
	return why
}

// deliveryOf 推出消息 d 对收件会话 x（ok = 会话存在）现在的送达状态。
func deliveryOf(d store.Decision, x store.Session, ok bool, at time.Time) Delivery {
	out := Delivery{Code: d.Code, To: d.SessionKey}
	if ok {
		out.Title, out.Role, out.Tool = x.Title, x.Role, x.Harness()
		if out.Title == "" {
			out.Title = x.LastPrompt
		}
	}
	switch {
	case d.Status == store.DecDelivered:
		out.Status, out.DeliveredAt = DeliveredSt, d.DeliveredAt
		out.Reason = "已送达"
		if d.DeliveredAt != nil {
			out.Reason = "已送达（" + d.DeliveredAt.Local().Format("01-02 15:04") + "）"
		}
	case d.Status == store.DecClosed:
		out.Status, out.Reason = OfflineSt, "已关闭，不再送达"
	case !ok:
		out.Status, out.Reason = OfflineSt, "收件会话不存在"
	case x.OfflineAt != nil && x.Role != "":
		out.Status, out.Reason = PendingWakeSt, "会话已下线，消息照常排队，用户开启新会话接任（"+takeoverCmd(x.Role)+"）时转交"
	case sessionOffline(x):
		out.Status, out.Reason = OfflineSt, offlineWhy(x)+"；会话恢复后才会收到"
	default:
		now, why := wakeAbilityAt(x, at)
		out.Status = PendingWakeSt
		switch {
		case d.FYI && now:
			out.Reason = "仅告知，不唤醒（会话" + why + "），下次唤醒或用户发消息时一并送达"
		case d.FYI:
			out.Reason = "仅告知，不唤醒：" + why
		case now:
			out.Reason = "立即送达（" + why + "）"
		default:
			out.Reason = why
		}
	}
	out.StatusLabel = deliveryLabel[out.Status]
	return out
}

// deliveries 逐条推出消息的送达状态（按收件会话查一次记录）。
func (h *Hub) deliveries(ctx context.Context, list []store.Decision) []Delivery {
	at := time.Now()
	out := make([]Delivery, 0, len(list))
	for _, d := range list {
		x, err := h.store.GetSession(ctx, d.SessionKey)
		out = append(out, deliveryOf(d, x, err == nil, at))
	}
	return out
}

// deliveryLine 是一个收件人的一行：「- D-12 → 顾问 claude:x（Claude Code）：待送达——正在跑，这一轮结束后收到」。
func deliveryLine(e Delivery) string {
	who := roles.Name(e.Role)
	if who == "" {
		who = "无身份会话"
	}
	head := "- "
	if e.Code != "" {
		head += e.Code + " → "
	}
	tool := ""
	if e.Tool != "" {
		tool = "（" + e.Tool + "）"
	}
	return fmt.Sprintf("%s%s %s%s：%s——%s", head, who, e.To, tool, e.StatusLabel, e.Reason)
}

// broadcastSenders 是能发「要动手」广播的身份（D-1027）；其余身份只能发只告知的。
var broadcastSenders = map[string]bool{roles.PM: true, roles.Aide: true}

// parseRoleFilter 解析广播的身份过滤：逗号 / 顿号 / 空格分隔，认中文名和别称；none / 无身份 表示没身份的会话。
// 返回的 map 为空表示不过滤；键 "" 表示无身份。
func parseRoleFilter(s string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == '，' || r == '、' || r == ' ' }) {
		r, ok := roles.Resolve(f)
		if !ok {
			return nil, huma.Error400BadRequest("不认识的身份：" + f + "（可填 aide / advisor / reviewer / pm / dev，也认中文名和别称；none 表示没身份的会话）")
		}
		out[r] = true
	}
	return out, nil
}

type broadcastOut struct {
	Body struct {
		BroadcastID string     `json:"broadcast_id,omitempty" doc:"这次广播的 ID：拆给每个收件人的各行共用"`
		Codes       []string   `json:"codes" doc:"拆出的各行消息编号（D-x）"`
		Sent        int        `json:"sent" doc:"送出的收件人数"`
		Skipped     int        `json:"skipped" doc:"跳过的离线会话数"`
		Recipients  []Delivery `json:"recipients" doc:"逐个收件人的送达状态，含跳过的离线会话"`
		Message     string     `json:"message"`
	}
}

type sentOut struct {
	Body struct {
		Messages []sentMsg `json:"messages" doc:"你最近发的团队消息，最近的在前；一次广播算一条"`
		Text     string    `json:"text" doc:"给 Agent 读的几行字（ks team sent 与 team_sent 输出同一段）"`
	}
}

type sentMsg struct {
	Code        string     `json:"code" doc:"消息编号；广播是第一行的编号"`
	Broadcast   bool       `json:"broadcast"`
	BroadcastID string     `json:"broadcast_id,omitempty"`
	FYI         bool       `json:"fyi"`
	Requirement string     `json:"requirement,omitempty"`
	Text        string     `json:"text"`
	CreatedAt   time.Time  `json:"created_at"`
	Recipients  []Delivery `json:"recipients"`
}

// sentLimit：ks team sent 默认列最近几条（一次广播算一条）。
const sentLimit = 10

func (h *Hub) registerAgentBroadcast(api huma.API) {
	huma.Register(api, huma.Operation{OperationID: "agent-team-broadcast", Method: http.MethodPost, Path: "/api/agent/team/broadcast", Tags: []string{"agent"},
		Summary:     "项目内广播（R-249）：发给本项目没结束的会话，可按身份过滤，逐个收件人返回送达状态",
		Description: "范围：本项目没结束的会话（不含桌面应用的后台任务会话、不含你自己），roles 按身份过滤（逗号分隔，none 表示没身份的会话）。一次广播拆成每个收件人一条团队消息（D 号），共用 broadcast_id。fyi = true 是「只告知」，任何会话都能发；不填是「要动手」，只有主调度、主助理能发，别的身份发会被拒绝并提示改用 fyi。失联 / 已标完成的会话跳过，在 recipients 里标 offline。送达状态：delivered 已送达、pending_wake 待送达（写明原因）、offline 无法送达。消息只做通知，超过 120 字照常发送并提示压缩。"},
		func(ctx context.Context, in *struct {
			Body struct {
				SessionKey     string `json:"session_key" minLength:"1" doc:"调用者（发送方）"`
				Text           string `json:"text" minLength:"1" doc:"要说的话：编号加一句话"`
				Roles          string `json:"roles,omitempty" doc:"只发给这些身份：逗号分隔（如 dev,reviewer），认中文名和别称；none 表示没身份的会话。不填发给全体"`
				Requirement    string `json:"requirement,omitempty" doc:"相关需求编号（如 R-12）"`
				FYI            bool   `json:"fyi,omitempty" doc:"只告知：不唤醒对方挂着的 ks wait。任何会话都能发；要动手的广播（不填）只有主调度、主助理能发"`
				SessionClaimed bool   `json:"session_claimed,omitempty" doc:"session_key 是用 --session 自报的（自动识别不到当前会话时，R-248）"`
				AgentID        string `json:"agent_id,omitempty" doc:"发广播的子任务的 agent_id；主线程发时为空"`
			}
		}) (*broadcastOut, error) {
			me, err := h.store.GetSession(ctx, in.Body.SessionKey)
			if err != nil {
				return nil, apiErr(err)
			}
			if in.Body.SessionClaimed && (sessionOffline(me) || me.ProjectID == "") {
				return nil, huma.Error409Conflict("--session " + me.Key + " 不是一个没结束、属于某个项目的会话，不能据此认定发送方，广播没有发出")
			}
			if me.ProjectID == "" {
				return nil, huma.Error400BadRequest("当前会话不属于任何项目，没有可以广播的会话")
			}
			text := strings.TrimSpace(in.Body.Text)
			if text == "" {
				return nil, huma.Error400BadRequest("请写下要说的话")
			}
			if !in.Body.FYI && !broadcastSenders[me.Role] {
				who := roles.Name(me.Role)
				if who == "" {
					who = "没有身份的会话"
				}
				return nil, huma.Error403Forbidden("「要动手」的广播只有主调度、主助理能发，你是" + who + "，广播没有发出；只是告知请加 --fyi（MCP 填 fyi = true），要某个人动手请用 ks team tell --to 点对点发")
			}
			filter, err := parseRoleFilter(in.Body.Roles)
			if err != nil {
				return nil, err
			}
			reqID := ""
			if ref := strings.TrimSpace(in.Body.Requirement); ref != "" {
				r, err := h.store.GetRequirement(ctx, ref)
				if err != nil {
					return nil, apiErr(err)
				}
				reqID = r.ID
				text = fmt.Sprintf("%s「%s」%s", r.Code, brief(r.Title, 40), text)
			}
			list, _, err := h.store.SessionRoster(ctx, me.ProjectID, false, 0) // 没结束的、不含后台任务会话
			if err != nil {
				return nil, err
			}
			agent := strings.TrimSpace(in.Body.AgentID)
			out := &broadcastOut{}
			out.Body.Codes, out.Body.Recipients = []string{}, []Delivery{}
			bid := protocol.NewID()
			question := "团队消息"
			if in.Body.FYI {
				question = quietTellQ // 只作显示；是否只告知看 fyi 列
			}
			answer := h.senderName(ctx, me, agent) + " 广播：" + text
			at := time.Now()
			var sent []store.Decision
			for _, x := range list {
				if x.Key == me.Key || (len(filter) > 0 && !filter[x.Role]) {
					continue
				}
				if sessionOffline(x) {
					out.Body.Skipped++
					out.Body.Recipients = append(out.Body.Recipients, Delivery{To: x.Key, Title: x.Title, Role: x.Role, Tool: x.Harness(),
						Status: OfflineSt, StatusLabel: deliveryLabel[OfflineSt], Reason: offlineWhy(x) + "，已跳过"})
					continue
				}
				d, err := h.store.CreateDecision(ctx, store.Decision{Kind: store.KindTell, SessionKey: x.Key, ProjectID: me.ProjectID, RequirementID: reqID,
					Question: question, Answer: answer, AssignedBy: me.Key, FromRole: me.Role, FromAgent: agent, FYI: in.Body.FYI, BroadcastID: bid})
				if err != nil {
					return nil, apiErr(err)
				}
				h.decisionsChanged(ctx, d)
				sent = append(sent, d)
				out.Body.Codes = append(out.Body.Codes, d.Code)
				out.Body.Recipients = append(out.Body.Recipients, deliveryOf(d, x, true, at))
			}
			out.Body.Sent = len(sent)
			kind := "要动手"
			if in.Body.FYI {
				kind = "只告知"
			}
			scope := "全体"
			if r := strings.TrimSpace(in.Body.Roles); r != "" {
				scope = "身份为「" + r + "」的会话"
			}
			var b strings.Builder
			if len(sent) == 0 {
				fmt.Fprintf(&b, "本项目没有可以收这条广播的%s（不含你自己和后台任务会话），没有发出。", scope)
				out.Body.BroadcastID = ""
			} else {
				out.Body.BroadcastID = bid
				codes := out.Body.Codes[0]
				if len(sent) > 1 {
					codes += "～" + out.Body.Codes[len(sent)-1]
				}
				fmt.Fprintf(&b, "已按「%s」广播给%s %d 个会话（%s）", kind, scope, len(sent), codes)
				if out.Body.Skipped > 0 {
					fmt.Fprintf(&b, "，跳过离线 %d 个", out.Body.Skipped)
				}
				b.WriteString("；用户不会收到。对方回复时送回你；之后用 ks team sent " + out.Body.Codes[0] + "（MCP team_sent）查送达。")
			}
			for _, e := range out.Body.Recipients {
				b.WriteString("\n" + deliveryLine(e))
			}
			if in.Body.SessionClaimed {
				b.WriteString("\n（自动识别不到当前会话，已按 --session " + me.Key + " 认定发送方）")
			}
			if hint := writing.MessageHint(in.Body.Text); hint != "" {
				b.WriteString("\n" + hint)
			}
			out.Body.Message = b.String()
			return out, nil
		})

	huma.Register(api, huma.Operation{OperationID: "agent-team-sent", Method: http.MethodGet, Path: "/api/agent/team/sent", Tags: []string{"agent"},
		Summary:     "查自己最近发的团队消息现在的送达状态（R-249，ks team sent / team_sent）",
		Description: "默认列你最近发的 10 条（一次广播算一条，逐个收件人列状态）；ref 填 D-x 只看那一条（广播的任意一行都会带出整次广播）。只能查自己发的。送达状态只从记录推：delivered 已送达（带时间）、pending_wake 待送达（写明原因，如 Codex 要等用户下次发消息）、offline 无法送达（对方已结束或失联）。"},
		func(ctx context.Context, in *struct {
			SessionKey string `query:"session_key" required:"true" doc:"调用者（发送方）"`
			Ref        string `query:"ref" doc:"只看这一条（D-x）"`
			Limit      int    `query:"limit" doc:"最多列几条（一次广播算一条），默认 10、最多 50"`
		}) (*sentOut, error) {
			me, err := h.store.GetSession(ctx, in.SessionKey)
			if err != nil {
				return nil, apiErr(err)
			}
			out := &sentOut{}
			out.Body.Messages = []sentMsg{}
			var groups [][]store.Decision
			if ref := strings.TrimSpace(in.Ref); ref != "" {
				d, err := h.store.GetDecision(ctx, ref)
				if err != nil {
					return nil, apiErr(err)
				}
				if d.Kind != store.KindTell || d.AssignedBy != me.Key {
					return nil, huma.Error400BadRequest(d.Code + " 不是你发的团队消息，只能查自己发的")
				}
				g := []store.Decision{d}
				if d.BroadcastID != "" {
					if g, err = h.store.BroadcastTells(ctx, d.BroadcastID); err != nil {
						return nil, err
					}
				}
				groups = append(groups, g)
			} else {
				limit := in.Limit
				if limit <= 0 {
					limit = sentLimit
				}
				if limit > 50 {
					limit = 50
				}
				list, err := h.store.SentTells(ctx, me.Key, 500)
				if err != nil {
					return nil, err
				}
				idx := map[string]int{}
				for _, d := range list { // 最近的在前
					if d.BroadcastID != "" {
						if i, ok := idx[d.BroadcastID]; ok {
							groups[i] = append([]store.Decision{d}, groups[i]...) // 组内按编号升序
							continue
						}
						if len(groups) >= limit {
							continue
						}
						idx[d.BroadcastID] = len(groups)
					} else if len(groups) >= limit {
						continue
					}
					groups = append(groups, []store.Decision{d})
				}
			}
			for _, g := range groups {
				first := g[0]
				m := sentMsg{Code: first.Code, Broadcast: first.BroadcastID != "", BroadcastID: first.BroadcastID, FYI: first.FYI,
					Requirement: first.RequirementCode, Text: sentText(first.Answer, me.Key), CreatedAt: first.CreatedAt, Recipients: h.deliveries(ctx, g)}
				out.Body.Messages = append(out.Body.Messages, m)
			}
			out.Body.Text = sentTextAll(out.Body.Messages, strings.TrimSpace(in.Ref) != "")
			return out, nil
		})
}

// sentText 取消息正文：去掉开头的「发送方「标题」（会话键） 留言 / 广播 / 回复 D-x：」。会话键里没有全角字符，按它定位，标题里的冒号不影响。
func sentText(answer, key string) string {
	rest := answer
	if i := strings.Index(answer, "（"+key+"）"); i >= 0 {
		rest = answer[i+len("（"+key+"）"):]
	}
	if j := strings.Index(rest, "："); j >= 0 {
		return rest[j+len("："):]
	}
	return answer
}

func sentTextAll(ms []sentMsg, one bool) string {
	if len(ms) == 0 {
		return "你还没有发过团队消息。"
	}
	var b strings.Builder
	if !one {
		fmt.Fprintf(&b, "你最近发的 %d 条团队消息（最近的在前；送达状态只从记录推）：\n", len(ms))
	}
	for i, m := range ms {
		if i > 0 {
			b.WriteString("\n")
		}
		kind := "要动手"
		if m.FYI {
			kind = "只告知"
		}
		head := m.Code + " 点对点"
		if m.Broadcast {
			head = fmt.Sprintf("%s 广播（%d 个收件人）", m.Code, len(m.Recipients))
		}
		fmt.Fprintf(&b, "%s · %s · %s「%s」", head, kind, m.CreatedAt.Local().Format("01-02 15:04"), brief(m.Text, 40))
		counts := map[string]int{}
		for _, e := range m.Recipients {
			counts[e.Status]++
		}
		if m.Broadcast {
			var parts []string
			for _, st := range []string{DeliveredSt, PendingWakeSt, OfflineSt} {
				if counts[st] > 0 {
					parts = append(parts, fmt.Sprintf("%s %d", deliveryLabel[st], counts[st]))
				}
			}
			b.WriteString("：" + strings.Join(parts, " · "))
		}
		for _, e := range m.Recipients {
			b.WriteString("\n  " + deliveryLine(e))
		}
	}
	return b.String()
}

// roleOrNone 是身份中文名，没有身份写「无身份」。
func roleOrNone(role string) string {
	if n := roles.Name(role); n != "" {
		return n
	}
	return "无身份"
}
