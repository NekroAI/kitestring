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

// 会话身份（R-82）：Agent 用 ks role 声明自己是 PM / 开发 / 顾问 / 审查员。
// Hub 记下身份并下发给会话所在设备的 daemon，由 daemon 在会话恢复 / 压缩后补发身份提示词。

type roleOut struct {
	Body struct {
		SessionKey string   `json:"session_key"`
		Role       string   `json:"role"`
		Label      string   `json:"label" doc:"身份中文名；无身份时为空"`
		Prompt     string   `json:"prompt" doc:"身份提示词，ks role 直接打印给 Agent"`
		Demoted    []string `json:"demoted" doc:"因本次接管 PM 被降为无身份的会话"`
	}
}

func (h *Hub) registerAgentRoles(api huma.API) {
	huma.Register(api, huma.Operation{OperationID: "agent-set-role", Method: http.MethodPost, Path: "/api/agent/sessions/role", Tags: []string{"agent"}},
		func(ctx context.Context, in *struct {
			Body struct {
				SessionKey string `json:"session_key" minLength:"1"`
				Role       string `json:"role" enum:",pm,dev,advisor,reviewer" doc:"pm / dev / advisor / reviewer；空为清除身份"`
			}
		}) (*roleOut, error) {
			out := &roleOut{}
			out.Body.SessionKey, out.Body.Role = in.Body.SessionKey, in.Body.Role
			demoted, err := h.setRole(ctx, in.Body.SessionKey, in.Body.Role)
			if err != nil {
				return nil, apiErr(err)
			}
			out.Body.Label, out.Body.Prompt, out.Body.Demoted = roles.Label(in.Body.Role), roles.Prompt(in.Body.Role), demoted
			if out.Body.Demoted == nil {
				out.Body.Demoted = []string{}
			}
			return out, nil
		})
}

// setRole 设置会话身份；接管 PM 时通知被降级的旧 PM，并把身份下发给相关设备。
func (h *Hub) setRole(ctx context.Context, key, role string) ([]string, error) {
	if !roles.Valid(role) {
		return nil, huma.Error400BadRequest("身份只能是 pm、dev、advisor、reviewer 之一（清除身份用 none）")
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
			Kind: store.KindRole, SessionKey: k, ProjectID: old.ProjectID, Question: "PM 已被接管",
			Answer: fmt.Sprintf("你在项目「%s」的 PM 身份已由「%s」接管，你现在是没有身份的普通会话：不必再常驻挂 ks wait，把手上的事收个尾、说明进度后结束本轮即可；派出去的需求由新 PM 接手跟进。", old.ProjectName, name),
		})
		if err != nil {
			h.log.Warn("记录 PM 降级通知失败", "err", err)
			continue
		}
		h.decisionsChanged(ctx, d)
		h.sessionChanged(k)
	}
	for dev := range devices {
		h.pushRoles(ctx, dev)
	}
	h.sessionChanged(key)
	return demoted, nil
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
