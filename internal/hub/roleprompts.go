package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NekroAI/kitestring/internal/protocol"
	"github.com/NekroAI/kitestring/internal/roles"
)

// 身份提示词可配置（R-98）：默认模板编译进 ks（internal/roles/*.md），用户在网页设置里改写某个身份的正文，
// 改写版存在 Hub；ks role 与会话恢复 / 压缩后的补发都优先用改写版。身份抬头固定，不可改写。
// 只有网页（登录用户）能改：Agent 侧没有任何写接口，也没有 MCP 工具。

const maxRolePromptRunes = 20000

type rolePromptRec struct {
	Body     string `json:"body"`
	BaseHash string `json:"base_hash"` // 改写时默认模板正文的指纹，用来发现“默认模板之后又更新了”
}

func rolePromptKey(role string) string { return "role_prompt:" + role }

// customRolePrompt 读取某个身份的改写版；没改写过返回 ok = false。
func (h *Hub) customRolePrompt(ctx context.Context, role string) (rolePromptRec, bool) {
	raw, err := h.store.GetSetting(ctx, rolePromptKey(role))
	if err != nil || raw == "" {
		return rolePromptRec{}, false
	}
	var r rolePromptRec
	if json.Unmarshal([]byte(raw), &r) != nil || strings.TrimSpace(r.Body) == "" {
		return rolePromptRec{}, false
	}
	return r, true
}

// rolePromptFor 返回注入给 Agent 的完整身份提示词：优先用户改写版，否则默认模板。
func (h *Hub) rolePromptFor(ctx context.Context, role string) string {
	if r, ok := h.customRolePrompt(ctx, role); ok {
		return roles.Compose(role, r.Body)
	}
	return roles.Prompt(role)
}

// customRoleBodies 是全部改写过的身份 → 正文，下发给 daemon。
func (h *Hub) customRoleBodies(ctx context.Context) map[string]string {
	m := map[string]string{}
	for _, role := range roles.All {
		if r, ok := h.customRolePrompt(ctx, role); ok {
			m[role] = r.Body
		}
	}
	return m
}

func (h *Hub) pushRolePrompts(ctx context.Context, c *deviceConn) {
	c.send(protocol.Envelope{Type: protocol.MsgRolePrompts, RolePrompts: h.customRoleBodies(ctx)})
}

func (h *Hub) broadcastRolePrompts(ctx context.Context) {
	h.mu.Lock()
	conns := make([]*deviceConn, 0, len(h.conns))
	for _, c := range h.conns {
		conns = append(conns, c)
	}
	h.mu.Unlock()
	for _, c := range conns {
		h.pushRolePrompts(ctx, c)
	}
}

// RolePromptView 是设置页上一个身份的提示词状态。
type RolePromptView struct {
	Role     string `json:"role" doc:"pm / dev / advisor / reviewer"`
	Label    string `json:"label" doc:"显示名（PM / Dev / Adv / Rev）"`
	Default  string `json:"default" doc:"当前版本内置的默认模板正文"`
	Body     string `json:"body" doc:"正在使用的正文：改写过的是改写版，否则等于默认模板"`
	Custom   bool   `json:"custom" doc:"是否已被改写"`
	Outdated bool   `json:"outdated" doc:"改写之后默认模板又更新了（可对比默认模板，或恢复默认）"`
}

func (h *Hub) rolePromptView(ctx context.Context, role string) RolePromptView {
	v := RolePromptView{Role: role, Label: roles.Label(role), Default: roles.DefaultBody(role)}
	v.Body = v.Default
	if r, ok := h.customRolePrompt(ctx, role); ok {
		v.Body, v.Custom = strings.TrimSpace(r.Body), true
		v.Outdated = r.BaseHash != roles.BodyHash(v.Default)
	}
	return v
}

func (h *Hub) registerRolePromptsWeb(api huma.API) {
	huma.Register(api, huma.Operation{OperationID: "list-role-prompts", Method: http.MethodGet, Path: "/api/role-prompts", Tags: []string{"settings"}, Summary: "四种身份的提示词（默认模板与改写版，R-98）"},
		func(ctx context.Context, _ *struct{}) (*struct{ Body []RolePromptView }, error) {
			out := &struct{ Body []RolePromptView }{Body: []RolePromptView{}}
			for _, role := range roles.All {
				out.Body = append(out.Body, h.rolePromptView(ctx, role))
			}
			return out, nil
		})
	huma.Register(api, huma.Operation{OperationID: "set-role-prompt", Method: http.MethodPut, Path: "/api/role-prompts/{role}", Tags: []string{"settings"}, Summary: "改写某个身份的提示词（只有网页能改，R-98）"},
		func(ctx context.Context, in *struct {
			Role string `path:"role" enum:"pm,dev,advisor,reviewer"`
			Body struct {
				Body string `json:"body" doc:"提示词正文（不含固定的身份抬头）；与默认模板一致时等同于恢复默认"`
			}
		}) (*struct{ Body RolePromptView }, error) {
			body := strings.TrimSpace(in.Body.Body)
			if body == "" {
				return nil, huma.Error400BadRequest("提示词不能为空；想用回默认模板请点“恢复默认”")
			}
			if utf8.RuneCountInString(body) > maxRolePromptRunes {
				return nil, huma.Error400BadRequest("提示词太长了")
			}
			if body == roles.DefaultBody(in.Role) {
				return h.restoreRolePrompt(ctx, in.Role)
			}
			b, _ := json.Marshal(rolePromptRec{Body: body, BaseHash: roles.BodyHash(roles.DefaultBody(in.Role))})
			if err := h.store.SetSetting(ctx, rolePromptKey(in.Role), string(b)); err != nil {
				return nil, apiErr(err)
			}
			h.broadcastRolePrompts(ctx)
			return &struct{ Body RolePromptView }{h.rolePromptView(ctx, in.Role)}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "reset-role-prompt", Method: http.MethodDelete, Path: "/api/role-prompts/{role}", Tags: []string{"settings"}, Summary: "恢复某个身份的默认提示词（R-98）"},
		func(ctx context.Context, in *struct {
			Role string `path:"role" enum:"pm,dev,advisor,reviewer"`
		}) (*struct{ Body RolePromptView }, error) {
			return h.restoreRolePrompt(ctx, in.Role)
		})
}

func (h *Hub) restoreRolePrompt(ctx context.Context, role string) (*struct{ Body RolePromptView }, error) {
	if err := h.store.SetSetting(ctx, rolePromptKey(role), ""); err != nil {
		return nil, apiErr(err)
	}
	h.broadcastRolePrompts(ctx)
	return &struct{ Body RolePromptView }{h.rolePromptView(ctx, role)}, nil
}
