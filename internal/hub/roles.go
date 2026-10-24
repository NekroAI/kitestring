package hub

import (
	"context"
	"fmt"
	"net/http"

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
		Demoted    []string `json:"demoted" doc:"因本次接任主调度 / 主助理被降为无身份的会话"`
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
			demoted, err := h.setRole(ctx, in.Body.SessionKey, role)
			if err != nil {
				return nil, apiErr(err)
			}
			out.Body.Label, out.Body.Name, out.Body.Prompt, out.Body.Demoted = roles.Label(role), roles.Name(role), h.rolePromptFor(ctx, role), demoted
			if out.Body.Demoted == nil {
				out.Body.Demoted = []string{}
			}
			return out, nil
		})
}

const roleUsage = "身份只能是 aide（主助理）、advisor（顾问）、reviewer（评审）、pm（主调度）、dev（开发）之一，也认中文名和别称（清除身份用 none）"

// setRole 设置会话身份；接任主调度 / 主助理时通知被降级的旧会话，并把身份下发给相关设备。
func (h *Hub) setRole(ctx context.Context, key, role string) ([]string, error) {
	if !roles.Valid(role) {
		return nil, huma.Error400BadRequest(roleUsage)
	}
	x, err := h.store.GetSession(ctx, key)
	if err != nil {
		return nil, err
	}
	demoted, err := h.store.SetRole(ctx, key, role)
	if err != nil {
		return nil, err
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
		d, err := h.store.CreateDecision(ctx, store.Decision{
			Kind: store.KindRole, SessionKey: k, ProjectID: old.ProjectID, Question: roles.Display(role) + " 已被接任",
			Answer: fmt.Sprintf("你在项目「%s」的%s身份已由「%s」接任，你现在是没有身份的普通会话：不必再常驻挂 ks wait，把手上的事收个尾、说明进度后结束本轮即可；%s", old.ProjectName, roles.Display(role), name, handover[role]),
		})
		if err != nil {
			h.log.Warn("记录 PM 降级通知失败", "err", err)
			continue
		}
		h.decisionsChanged(ctx, d)
		h.sessionChanged(k)
	}
	for _, k := range demoted {
		h.revokeGrantsOf(ctx, k, "原来的主助理已卸任") // 授权窗口跟着主助理走：卸任即失效，请用户给新主助理重新开（R-190）
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
	return demoted, nil
}

// handover：卸任时交代谁接手。
var handover = map[string]string{
	roles.PM:   "派出去的需求由新主调度接手跟进。",
	roles.Aide: "代答、放行、验收建议、离开期间小结由新主助理接手；你名下的授权窗口已失效。",
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
