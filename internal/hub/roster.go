package hub

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NekroAI/kitestring/internal/protocol"
	"github.com/NekroAI/kitestring/internal/roles"
	"github.com/NekroAI/kitestring/internal/store"
	"github.com/NekroAI/kitestring/internal/summary"
)

// 在线会话列表（R-249 P2）：ks team sessions / MCP team_sessions。
// 列本项目（或所有项目）的会话：身份、所用工具、待命 / 忙 / 在线 / 离线、手上的需求、能否立刻叫醒及原因。
// 全部从会话记录推（原则第 11 条）：状态用 store.Session.Availability，能否叫醒用 wakeAbility。
// 这两个函数之后的点对点消息、广播的送达状态（P3）会直接复用：wakeAbility 的 now 为真即「已送达 / 马上叫醒」，
// 为假时 why 就是要写给发送方的「等对方下次醒来 / 对方不在线」原因。

// rosterLimit：带上已结束的会话时最多列多少个（最近活跃的在前）。
const rosterLimit = 50

// 会话可用状态的中文名，按 docs/copywriting.md 5.2（statusSets.roster）：「在线」并入「空闲」，会话不用「离线」
var availLabel = map[string]string{store.AvailStandby: "待命", store.AvailBusy: "运行中", store.AvailOnline: "空闲", store.AvailOffline: "已结束"}
var availRank = map[string]int{store.AvailStandby: 0, store.AvailBusy: 1, store.AvailOnline: 2, store.AvailOffline: 3}

// wakeAbility 判断现在发给会话 x 的消息能不能立刻叫醒它，why 是一句给发送方看的原因。
// 只看会话记录：状态、是否挂着 ks wait（Parked / ParkedUntil）、工具（tool）、是否已被接任（RetiredAt）。
//   - 已结束 / 失联：离线，不能叫醒；
//   - 已被接任卸任：不再挂 ks wait、不再被唤醒；
//   - 正在跑（含等授权、在问用户、后台任务）：这一轮结束后收到；
//   - Claude / claude-x 挂着后台 ks wait、Codex 前台 ks wait 没过 ParkedUntil、DSH 插件待命：能立刻叫醒；
//   - 一轮结束没挂 ks wait：等用户下次发消息；Codex 写明它不能后台叫醒。
func wakeAbility(x store.Session) (now bool, why string) { return wakeAbilityAt(x, time.Now()) }

// wakeAbilityAt 同 wakeAbility，时间由调用方给。
func wakeAbilityAt(x store.Session, at time.Time) (now bool, why string) {
	if x.OfflineAt != nil && x.Role != "" {
		return false, "已下线，消息照常排队，接任的新会话会收到"
	}
	switch x.State {
	case store.StateEnded:
		return false, "会话已结束"
	case store.StateLost:
		return false, "会话失联（设备断开）"
	}
	if x.RetiredAt != nil {
		return false, "身份已由新会话接任，不再接收消息"
	}
	switch x.AvailabilityAt(at) {
	case store.AvailBusy:
		switch {
		case x.State == store.StateWaitApproval:
			return false, "等待授权，本轮结束后送达"
		case x.State == store.StateWaitReply:
			return false, "等待回答，本轮结束后送达"
		case x.Tool == protocol.ToolCodex:
			return false, "运行中，本轮结束后送达（Codex 需在本轮再次待命，否则等用户下次发消息）"
		}
		return false, "运行中，本轮结束后送达"
	case store.AvailStandby:
		switch x.Tool {
		case protocol.ToolCodex:
			if x.ParkedUntil != nil {
				return true, fmt.Sprintf("终端前台待命（至 %s）", x.ParkedUntil.Local().Format("15:04"))
			}
			return true, "终端前台待命"
		case protocol.ToolDSH:
			return true, "DSH 插件待命"
		}
		return true, "后台待命"
	}
	// 在线：一轮结束、没挂 ks wait
	switch x.Tool {
	case protocol.ToolCodex:
		if x.CodexWaitExpired(at) {
			return false, fmt.Sprintf("终端前台待命已到期（%s）；Codex 无法在后台接收，等用户下次发消息", x.ParkedUntil.Local().Format("15:04"))
		}
		return false, "Codex 无法在后台接收，等用户下次发消息"
	case protocol.ToolDSH:
		return false, "DSH 插件未待命，等用户下次发消息"
	}
	return false, "未待命，等用户下次发消息"
}

// RosterEntry 是在线会话列表里的一个会话。
type RosterEntry struct {
	Key          string                 `json:"key"`
	Title        string                 `json:"title"`
	Role         string                 `json:"role" doc:"身份；空为无身份"`
	RoleName     string                 `json:"role_name" doc:"身份中文名；无身份写「无身份」"`
	Harness      string                 `json:"harness" doc:"所用工具：Claude Code / 启动命令名（如 claude-x）/ Codex 终端 / Codex App / DSH"`
	Tool         string                 `json:"tool"`
	Client       string                 `json:"client"`
	Device       string                 `json:"device"`
	Project      string                 `json:"project"`
	Status       string                 `json:"status" enum:"standby,busy,online,offline" doc:"standby 待命；busy 运行中；online 空闲（没挂 ks wait）；offline 已结束（含失联）"`
	StatusLabel  string                 `json:"status_label"`
	WakeNow      bool                   `json:"wake_now" doc:"现在发消息能不能立刻叫醒它"`
	WakeWhy      string                 `json:"wake_why" doc:"能 / 不能立刻叫醒的原因"`
	Open         []store.RequirementRef `json:"open" doc:"手上的需求：待开始、进行中、受阻"`
	Me           bool                   `json:"me,omitempty" doc:"就是调用者自己"`
	LastActiveAt string                 `json:"last_active_at"`
}

type rosterOut struct {
	Body struct {
		ProjectID   string        `json:"project_id,omitempty"`
		ProjectName string        `json:"project_name,omitempty"`
		AllProjects bool          `json:"all_projects"`
		Sessions    []RosterEntry `json:"sessions" doc:"待命 → 运行中 → 空闲 → 已结束，同状态最近活跃的在前"`
		Omitted     int           `json:"omitted" doc:"带上已结束的会话时，超出上限没列出的更早的会话数"`
		Text        string        `json:"text" doc:"给 Agent 读的列表（ks team sessions 与 team_sessions 输出同一段）"`
	}
}

func toRosterEntry(x store.Session, me string, at time.Time) RosterEntry {
	e := RosterEntry{Key: x.Key, Title: x.Title, Role: x.Role, RoleName: roles.Name(x.Role), Harness: x.Harness(), Tool: x.Tool, Client: x.Client,
		Device: x.DeviceName, Project: x.ProjectName, Status: x.AvailabilityAt(at), Open: []store.RequirementRef{}, Me: x.Key == me,
		LastActiveAt: x.LastActiveAt.UTC().Format("2006-01-02T15:04:05Z")}
	if e.RoleName == "" {
		e.RoleName = "无身份"
	}
	if e.Title == "" {
		e.Title = x.LastPrompt
	}
	e.StatusLabel = availLabel[e.Status]
	e.WakeNow, e.WakeWhy = wakeAbilityAt(x, at)
	for _, r := range x.Requirements {
		if r.Status == store.ReqReady || r.Status == store.ReqInProgress || r.Status == store.ReqBlocked {
			e.Open = append(e.Open, r)
		}
	}
	return e
}

// roster 列会话。sessionKey 为调用者：不跨项目时用它定项目；跨项目时可以为空。
// role 为身份（认中文名和别称），none / 无身份 只列没身份的；withEnded 为真时也列已结束的。
func (h *Hub) roster(ctx context.Context, sessionKey, role string, allProjects, withEnded bool) (*rosterOut, error) {
	out := &rosterOut{}
	out.Body.AllProjects = allProjects
	projectID := ""
	if sessionKey != "" {
		me, err := h.store.GetSession(ctx, sessionKey)
		if err != nil && !allProjects {
			return nil, apiErr(err)
		}
		if err == nil {
			out.Body.ProjectID, out.Body.ProjectName = me.ProjectID, me.ProjectName
		}
	}
	if !allProjects {
		if sessionKey == "" {
			return nil, huma.Error400BadRequest("认不出当前会话；要看所有项目的会话加 all_projects")
		}
		if out.Body.ProjectID == "" {
			return nil, huma.Error400BadRequest("当前会话不属于任何项目；要看所有项目的会话加 all_projects")
		}
		projectID = out.Body.ProjectID
	}
	filter, none := "", false
	if r := strings.TrimSpace(role); r != "" {
		v, ok := roles.Resolve(r)
		if !ok {
			return nil, huma.Error400BadRequest("不认识的身份：" + r + "（可填 aide / advisor / reviewer / pm / dev，也认中文名和别称；none 只看无身份的会话）")
		}
		filter, none = v, v == ""
	}
	limit := 0
	if withEnded {
		limit = rosterLimit
	}
	list, total, err := h.store.SessionRoster(ctx, projectID, withEnded, limit)
	if err != nil {
		return nil, err
	}
	out.Body.Omitted = total - len(list)
	at := time.Now()
	out.Body.Sessions = []RosterEntry{}
	for _, x := range list {
		if (none && x.Role != "") || (filter != "" && x.Role != filter) {
			continue
		}
		out.Body.Sessions = append(out.Body.Sessions, toRosterEntry(x, sessionKey, at))
	}
	sort.SliceStable(out.Body.Sessions, func(i, j int) bool {
		return availRank[out.Body.Sessions[i].Status] < availRank[out.Body.Sessions[j].Status]
	})
	out.Body.Text = rosterText(out, role, withEnded)
	return out, nil
}

// rosterText 把列表写成给 Agent 读的几行字。
func rosterText(out *rosterOut, role string, withEnded bool) string {
	b := out.Body
	var sb strings.Builder
	scope := "所有项目"
	if !b.AllProjects {
		scope = "项目「" + b.ProjectName + "」"
	}
	kind := "没结束的会话"
	if withEnded {
		kind = "会话，含已结束的"
	}
	if role != "" {
		kind = "身份为「" + role + "」的" + kind
	}
	if len(b.Sessions) == 0 {
		fmt.Fprintf(&sb, "%s没有%s。", scope, kind)
		return sb.String()
	}
	counts := map[string]int{}
	for _, e := range b.Sessions {
		counts[e.Status]++
	}
	var parts []string
	for _, st := range []string{store.AvailStandby, store.AvailBusy, store.AvailOnline, store.AvailOffline} {
		if counts[st] > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", availLabel[st], counts[st]))
		}
	}
	fmt.Fprintf(&sb, "%s：%d 个%s（%s）\n", scope, len(b.Sessions), kind, strings.Join(parts, " · "))
	for _, e := range b.Sessions {
		sb.WriteString("- " + e.Key)
		if e.Me {
			sb.WriteString("（你）")
		}
		if t := summary.Truncate(strings.Join(strings.Fields(e.Title), " "), 30); t != "" {
			sb.WriteString("「" + t + "」")
		}
		sb.WriteString(" " + e.RoleName + " · " + e.Harness)
		if e.Device != "" {
			sb.WriteString("@" + e.Device)
		}
		if b.AllProjects && e.Project != "" {
			sb.WriteString(" · 项目 " + e.Project)
		}
		fmt.Fprintf(&sb, " · %s · ", e.StatusLabel)
		if e.WakeNow {
			sb.WriteString("能立刻叫醒：" + e.WakeWhy)
		} else {
			sb.WriteString("不能立刻叫醒：" + e.WakeWhy)
		}
		if len(e.Open) == 0 {
			sb.WriteString(" · 手上没有需求")
		} else {
			var rs []string
			for _, r := range e.Open {
				rs = append(rs, r.Code+"「"+summary.Truncate(r.Title, 20)+"」")
			}
			sb.WriteString(" · 手上：" + strings.Join(rs, "、"))
		}
		sb.WriteString("\n")
	}
	if b.Omitted > 0 {
		fmt.Fprintf(&sb, "另有 %d 个更早的会话没列出（最多列 %d 个）。\n", b.Omitted, rosterLimit)
	}
	return strings.TrimRight(sb.String(), "\n")
}

type rosterIn struct {
	SessionKey  string `query:"session_key" doc:"调用者会话键；不跨项目时用它定项目"`
	Role        string `query:"role" doc:"只看某个身份（aide / advisor / reviewer / pm / dev，也认中文名和别称）；none 只看无身份的会话"`
	AllProjects bool   `query:"all_projects" doc:"列所有项目的会话"`
	All         bool   `query:"all" doc:"也列已结束的会话（最近活跃的 50 个）"`
}

func (h *Hub) registerAgentRoster(api huma.API) {
	huma.Register(api, huma.Operation{OperationID: "agent-team-sessions", Method: http.MethodGet, Path: "/api/agent/team/sessions", Tags: []string{"agent"},
		Summary:     "在线会话列表（R-249）：本项目会话的身份、工具、状态、手上的需求、能否立刻叫醒",
		Description: "状态与能否叫醒只从会话记录推：Claude / claude-x 挂着后台 ks wait、Codex 前台 ks wait 未到期、DSH 插件待命能立刻叫醒；正在跑的这一轮结束后收到；Codex 没挂 wait 不能后台叫醒，等用户下次发消息；已结束或失联为离线。"},
		func(ctx context.Context, in *rosterIn) (*rosterOut, error) {
			return h.roster(ctx, in.SessionKey, in.Role, in.AllProjects, in.All)
		})
}
