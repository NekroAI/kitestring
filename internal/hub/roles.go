package hub

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NekroAI/kitestring/internal/protocol"
	"github.com/NekroAI/kitestring/internal/roles"
	"github.com/NekroAI/kitestring/internal/store"
)

// 会话身份（R-82、R-190）：Agent 用 ks role 声明自己是主助理 / 顾问 / 评审 / 主调度 / 开发。
// Hub 记下身份并下发给会话所在设备的 daemon，由 daemon 在会话恢复 / 压缩后补发身份提示词。

type roleOut struct {
	Body struct {
		SessionKey string   `json:"session_key"`
		Role       string   `json:"role"`
		Label      string   `json:"label" doc:"身份英文简写（Aide / Adv / Rev / PM / Dev）；无身份时为空"`
		Name       string   `json:"name" doc:"身份中文名（主助理 / 顾问 / 评审 / 主调度 / 开发）；无身份时为空"`
		Prompt     string   `json:"prompt" doc:"身份提示词，ks role 直接打印给 Agent"`
		Demoted    []string `json:"demoted" doc:"因本次接任主调度 / 主助理被降为无身份的会话（R-229）"`
		Handoff    bool     `json:"handoff" doc:"接任了上一任写的交接（R-224）：交接内容已附在 prompt 末尾"`
	}
}

func (h *Hub) registerAgentRoles(api huma.API) {
	huma.Register(api, huma.Operation{OperationID: "agent-set-role", Method: http.MethodPost, Path: "/api/agent/sessions/role", Tags: []string{"agent"}},
		func(ctx context.Context, in *struct {
			Body struct {
				SessionKey string `json:"session_key" minLength:"1"`
				Role       string `json:"role" doc:"aide / advisor / reviewer / pm / dev，也认中文名、英文简写和别称（如 主调度、参谋、Aide）；空为清除身份"`
			}
		}) (*roleOut, error) {
			out := &roleOut{}
			role, ok := roles.Resolve(in.Body.Role)
			if !ok {
				return nil, huma.Error400BadRequest(roleUsage)
			}
			out.Body.SessionKey, out.Body.Role = in.Body.SessionKey, role
			demoted, handoff, backlog, err := h.setRole(ctx, in.Body.SessionKey, role)
			if err != nil {
				return nil, apiErr(err)
			}
			ov := ""
			if x, err := h.store.GetSession(ctx, in.Body.SessionKey); err == nil && role != "" {
				ov = h.overview(ctx, x, role, overviewHead, gitWait) // 接手概览（R-245）：按身份从记录生成，交接只写纸鸢里没有的
			}
			out.Body.Label, out.Body.Name, out.Body.Prompt, out.Body.Demoted = roles.Label(role), roles.Name(role), h.rolePromptFor(ctx, role)+h.roleNotes(ctx, in.Body.SessionKey)+ov+handoff+offlineBacklogNote(backlog), demoted
			out.Body.Handoff = handoff != ""
			if out.Body.Demoted == nil {
				out.Body.Demoted = []string{}
			}
			return out, nil
		})
}

const roleUsage = "身份只能是 aide（主助理）、advisor（顾问）、reviewer（评审）、pm（主调度）、dev（开发）之一，也认中文名和别称（清除身份用 none）"

// setRole 设置会话身份；接任主调度 / 主助理时通知被降级的旧会话，并把身份下发给相关设备。
// 同项目同身份有上一任写好的交接时一并接任（R-224）：返回要注入的交接内容，上一任交出身份。
// 上一任是空等下线的（R-272）：它积压的点名消息和未答决策转给本会话，返回转了几条。
func (h *Hub) setRole(ctx context.Context, key, role string) ([]string, string, int, error) {
	if !roles.Valid(role) {
		return nil, "", 0, huma.Error400BadRequest(roleUsage)
	}
	x, err := h.store.GetSession(ctx, key)
	if err != nil {
		return nil, "", 0, err
	}
	// 先记下接任前各个下线会话（SetRole 会清掉被接任者的身份）
	offline := map[string]store.Session{}
	if role != "" && x.ProjectID != "" {
		if list, err := h.store.RoleSessions(ctx, x.ProjectID); err == nil {
			for _, o := range list {
				if o.Key != key && o.Role == role && (o.OfflineAt != nil || o.StoppedAt != nil) {
					offline[o.Key] = o
				}
			}
		}
	}
	demoted, err := h.store.SetRole(ctx, key, role)
	if err != nil {
		return nil, "", 0, err
	}
	if role != "" && x.RetiredAt != nil {
		_ = h.store.SetRetired(ctx, key, false) // 交出身份的会话重新声明身份：回到正常
	}
	if _, err := h.store.SetOffline(ctx, key, nil); err != nil { // 空等下线的会话重新声明身份：回到在线，空等从头算（R-272）
		h.log.Warn("清除下线标记失败", "err", err)
	}
	_ = h.store.ResetIdleWait(ctx, key, time.Time{})
	h.clearIdle(ctx, key) // 重新声明身份：待命超时从头算（R-229）
	handoff, demoted := h.takeOver(ctx, x, role, demoted)
	backlog := 0
	for _, k := range demoted {
		if o, ok := offline[k]; ok {
			backlog += h.takeOverOffline(ctx, o, x)
		}
	}
	devices := map[string]bool{x.DeviceID: true}
	name := x.Title
	if name == "" {
		name = x.DeviceName + " 上的 " + x.Tool + " 会话"
	}
	for _, k := range demoted {
		old, err := h.store.GetSession(ctx, k)
		if err != nil {
			continue
		}
		devices[old.DeviceID] = true
		// 被接任一律交出身份（R-229）：有没有写交接都走 R-224 同一处标记——ks wait 直接返回、订阅事件不再送，重新 ks role 时恢复
		handed := old.RetiredAt != nil
		if !handed {
			if err := h.store.SetRetired(ctx, k, true); err != nil {
				h.log.Warn("标记旧会话已交接失败", "err", err)
			}
		}
		answer := retiredNote(old.ProjectName, role, name, handed, h.pendingCodes(ctx, k))
		d, err := h.store.CreateDecision(ctx, store.Decision{
			Kind: store.KindRole, SessionKey: k, ProjectID: old.ProjectID, Question: roles.Display(role) + " 已被接任",
			Answer: answer,
		})
		if err != nil {
			h.log.Warn("记录 PM 降级通知失败", "err", err)
			continue
		}
		h.decisionsChanged(ctx, d)
		h.sessionChanged(k)
	}
	for _, k := range demoted {
		h.revokeGrantsOf(ctx, k, "原来的主助理已被接任") // 授权窗口跟着主助理走：交出身份即失效，请用户给新主助理重新开（R-190）
	}
	if x.Role == roles.Aide && role != roles.Aide {
		h.revokeGrantsOf(ctx, key, "这个会话不再是主助理")
	}
	for dev := range devices {
		h.pushRoles(ctx, dev)
	}
	if role == roles.Dev {
		h.devUp(ctx, key, x.Role != roles.Dev) // 新声明的开发报上线；够数时清除 need_devs 提醒并告诉 PM（R-116）
	}
	h.sessionChanged(key)
	return demoted, handoff, backlog, nil
}

// pendingCodes 列出会话提的、还等用户答复的决策编号（交出身份通知里写明它们的去向，R-229）。
func (h *Hub) pendingCodes(ctx context.Context, key string) []string {
	decs, err := h.store.ListDecisions(ctx, []string{store.DecPending}, -1)
	if err != nil {
		return nil
	}
	var out []string
	for _, d := range decs {
		if d.SessionKey == key && d.Code != "" && d.Kind != store.KindTell && d.Kind != store.KindRole && d.Kind != store.KindNote {
			out = append(out, d.Code)
		}
	}
	return out
}

// handover：交出身份时交代谁接手。
var handover = map[string]string{
	roles.PM:   "派出去的需求由新主调度接手跟进。",
	roles.Aide: "代答、放行、验收建议、离开期间小结由新主助理接手；你名下的授权窗口已失效。",
}

// pushInstances 把设备上未结束会话的终端实例下发给 daemon：daemon 重启后内存里的实例表是空的，
// 重启期间（或之前）退出的进程靠它才能继续被盯住并补发结束（R-204）。
func (h *Hub) pushInstances(ctx context.Context, deviceID string) {
	h.mu.Lock()
	c := h.conns[deviceID]
	h.mu.Unlock()
	if c == nil {
		return
	}
	list, err := h.store.InstancesForDevice(ctx, deviceID)
	if err != nil {
		h.log.Warn("查询会话终端实例失败", "err", err)
		return
	}
	if len(list) > 0 {
		c.send(protocol.Envelope{Type: protocol.MsgSessions, Sessions: list})
	}
}

// pushRoles 把设备上有身份的会话全集下发给 daemon。
func (h *Hub) pushRoles(ctx context.Context, deviceID string) {
	h.mu.Lock()
	c := h.conns[deviceID]
	h.mu.Unlock()
	if c == nil {
		return
	}
	m, err := h.store.RolesForDevice(ctx, deviceID)
	if err != nil {
		h.log.Warn("查询会话身份失败", "err", err)
		return
	}
	c.send(protocol.Envelope{Type: protocol.MsgRoles, Roles: m})
}
