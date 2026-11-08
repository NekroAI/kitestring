package hub

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/NekroAI/kitestring/internal/protocol"
	"github.com/NekroAI/kitestring/internal/roles"
	"github.com/NekroAI/kitestring/internal/store"
	"github.com/NekroAI/kitestring/internal/summary"
)

// 身份上下线通知（R-319，D-1496）：有身份的会话接任、不再担任身份、下线（空等下线、停下后下线、会话结束）时，
// 纸鸢给本项目其他在线会话发一条「只告知」的团队消息（谁、什么身份、上线还是下线），不唤醒对方的 ks wait。
// 无身份会话的上下线不发。走团队消息的只告知队列（tellQuiet 同一种记录：kind = tell、fyi），一次通知共用 broadcast_id，
// 通讯页合成一条。下线类消息记下说的是哪个会话、哪个身份（about_key / about_role）：送达时那个身份
// （主调度 / 主助理，每项目唯一）已由别的会话接任的，消息末尾注明「已由 X 接任」，免得收件方以为身份还空着。

const (
	presenceUp   = "上线"
	presenceDown = "下线"
)

// presenceText 拼通知全文：前缀「身份变动（presence · 上线）：」供通讯页拆出类别（comms.go 的 sysHead），
// 第一句是不含工具名的事实（copywriting.md 1.3），结尾的工具指引网页上不显示（sysTails 的「 只是告知」）。
func presenceText(kind, body string) string {
	return "身份变动（presence · " + kind + "）：" + body + " 只是告知，不必回复；本项目在线会话见 ks team sessions。"
}

// presenceOnline：会话现在在线、还在担任它的身份——没结束 / 失联 / 归档，没下线、没在停下、没被接任。
func presenceOnline(x store.Session) bool {
	return !sessionOffline(x) && x.OfflineAt == nil && x.StoppedAt == nil && x.RetiredAt == nil
}

// announcePresence 把身份变动通知发给本项目其他在线会话（不含 subject 自己、skip 里的、后台任务会话），
// extra 是另外要收的会话（如空等下线时原来就通知的另一位常驻成员，它可能已下线但仍占着身份，消息排队转给接任者）。
// down 为真时记下 about_key / about_role，送达时据此注明接任情况。返回收件会话键。
func (h *Hub) announcePresence(ctx context.Context, subject store.Session, role, kind, body string, extra []string, skip map[string]bool) []string {
	if subject.ProjectID == "" || role == "" {
		return nil
	}
	list, _, err := h.store.SessionRoster(ctx, subject.ProjectID, false, 0)
	if err != nil {
		h.log.Warn("查询在线会话失败", "err", err)
		return nil
	}
	var to []string
	seen := map[string]bool{subject.Key: true}
	for k := range skip {
		seen[k] = true
	}
	for _, x := range list {
		if seen[x.Key] || !presenceOnline(x) {
			continue
		}
		seen[x.Key] = true
		to = append(to, x.Key)
	}
	for _, k := range extra {
		if k != "" && !seen[k] {
			seen[k] = true
			to = append(to, k)
		}
	}
	if len(to) == 0 {
		return nil
	}
	text := presenceText(kind, body)
	bid := protocol.NewID()
	d := store.Decision{Kind: store.KindTell, ProjectID: subject.ProjectID, Question: quietTellQ, Answer: text, FYI: true, BroadcastID: bid}
	if kind == presenceDown {
		d.AboutKey, d.AboutRole = subject.Key, role
	}
	for _, k := range to {
		d.SessionKey = k
		out, err := h.store.CreateDecision(ctx, d)
		if err != nil {
			h.log.Warn("发送身份变动通知失败", "err", err, "to", k)
			continue
		}
		h.decisionsChanged(ctx, out)
	}
	return to
}

// seatTakenBy：主调度 / 主助理这个身份现在由 except 以外的哪个在线会话担任；不是唯一身份或没有时 ok 为假。
func (h *Hub) seatTakenBy(ctx context.Context, projectID, role, except string) (store.Session, bool) {
	if projectID == "" || !roles.Unique(role) {
		return store.Session{}, false
	}
	list, err := h.store.RoleSessions(ctx, projectID)
	if err != nil {
		return store.Session{}, false
	}
	for _, x := range list {
		if x.Key != except && x.Role == role && presenceOnline(x) {
			return x, true
		}
	}
	return store.Session{}, false
}

// takenNote：说某个身份下线的消息还没送达、那个身份已由别的会话接任时，附在消息末尾的一句（R-319）。
func (h *Hub) takenNote(ctx context.Context, d store.Decision) string {
	if d.Kind != store.KindTell || d.AboutRole == "" || d.Status == store.DecDelivered {
		return ""
	}
	x, ok := h.seatTakenBy(ctx, d.ProjectID, d.AboutRole, d.AboutKey)
	if !ok {
		return ""
	}
	if x.Key == d.SessionKey {
		return "\n（" + roles.Name(d.AboutRole) + "已由你接任，以上下线情况已过时。）"
	}
	return "\n（" + roles.Name(d.AboutRole) + "已由 " + sessionName(x) + " 接任，以上下线情况已过时。）"
}

// onRoleChange：setRole 之后按身份变化发通知。prev 是改之前的会话记录，demoted 是因此交接的旧会话。
func (h *Hub) onRoleChange(ctx context.Context, prev store.Session, role string, demoted []store.Session) {
	if prev.ProjectID == "" {
		return
	}
	active := prev.RetiredAt == nil && prev.OfflineAt == nil && prev.StoppedAt == nil
	skip := map[string]bool{}
	for _, o := range demoted {
		skip[o.Key] = true // 被接任的旧会话另有「已被接任」的纸鸢提醒
	}
	switch {
	case role != "" && (prev.Role != role || !active):
		var b strings.Builder
		fmt.Fprintf(&b, "%s 接任%s", sessionName(prev), roles.Name(role))
		if prev.Role != "" && prev.Role != role {
			fmt.Fprintf(&b, "（不再担任%s）", roles.Name(prev.Role))
		}
		b.WriteString("，已上线。")
		for _, o := range demoted {
			fmt.Fprintf(&b, "原%s%s 已交接。", roles.Name(role), sessionName(o))
		}
		if role == roles.Dev && prev.Role != roles.Dev {
			if pm := h.projectPM(ctx, prev.ProjectID); pm != "" {
				skip[pm] = true // 开发上线另有要动手的「开发已上线待命」消息给主调度（R-116）
			}
		}
		h.announcePresence(ctx, prev, role, presenceUp, b.String(), nil, skip)
	case role == "" && prev.Role != "" && active:
		body := fmt.Sprintf("%s 不再担任%s，现为无身份会话。", sessionName(prev), roles.Name(prev.Role))
		h.announcePresence(ctx, prev, prev.Role, presenceDown, body, nil, skip)
	}
}

// onSessionEnded：有身份、还在担任身份的会话结束了（终端关闭 / SessionEnd），告诉本项目其他在线会话。
// 已空等下线、停下后下线、被接任的不再重复通知。
func (h *Hub) onSessionEnded(ctx context.Context, prev *store.Session, next store.Session) {
	if prev == nil || next.Background || next.Role == "" || next.ProjectID == "" {
		return
	}
	if sessionOffline(*prev) || next.State != store.StateEnded {
		return
	}
	if next.OfflineAt != nil || next.StoppedAt != nil || next.RetiredAt != nil || next.DoneAt != nil {
		return
	}
	body := fmt.Sprintf("%s%s 的会话已结束。", roles.Name(next.Role), sessionName(next))
	if roles.Unique(next.Role) {
		body += fmt.Sprintf("用户开新会话（%s）接任时会再通知。", takeoverCmd(next.Role))
	}
	h.announcePresence(ctx, next, next.Role, presenceDown, body, nil, nil)
}

// onlineSection 是接手概览里的「当前在线」：本项目在线的会话和身份（不含后台任务会话），有身份的在前；
// 主调度 / 主助理没有在线会话时注明。me 是接任的会话自己。
func (h *Hub) onlineSection(ctx context.Context, projectID, me string) ovSection {
	sec := ovSection{title: "当前在线（本项目没结束的会话；有变动时纸鸢会发只告知消息，最新情况用 ks team sessions 查）"}
	list, _, err := h.store.SessionRoster(ctx, projectID, false, 0)
	if err != nil {
		return sec
	}
	at := time.Now()
	var withRole, without []string
	has := map[string]bool{}
	for _, x := range list {
		if !presenceOnline(x) {
			continue
		}
		e := toRosterEntry(x, me, at)
		line := e.RoleName + " " + x.Key
		if e.Me {
			line += "（你）"
		}
		if t := summary.Truncate(strings.Join(strings.Fields(e.Title), " "), 30); t != "" {
			line += "「" + t + "」"
		}
		line += " · " + e.Harness
		if e.Device != "" {
			line += "@" + e.Device
		}
		line += " · " + e.StatusLabel
		if x.Role != "" {
			has[x.Role] = true
			withRole = append(withRole, line)
		} else {
			without = append(without, line)
		}
	}
	sec.lines = append(withRole, without...)
	var missing []string
	for _, r := range []string{roles.PM, roles.Aide} {
		if !has[r] {
			missing = append(missing, roles.Name(r))
		}
	}
	if len(missing) > 0 {
		sec.lines = append(sec.lines, "（没有在线的"+strings.Join(missing, "、")+"）")
	}
	return sec
}
