package hub

import (
	"context"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NekroAI/kitestring/internal/roles"
)

// PM 简报（R-113）：PM 用工具写一段给你看的话（你离开期间发生了什么、在等你什么），显示在工作台顶部。
// 每次写入覆盖上一份；写空文本清除。只有项目的 PM 能写。

const briefMax = 600 // 简报只是一段话，超长截断

func (h *Hub) registerAgentBrief(api huma.API) {
	huma.Register(api, huma.Operation{OperationID: "agent-set-brief", Method: http.MethodPost, Path: "/api/agent/brief", Tags: []string{"agent"}, Summary: "PM 写给用户的简报（R-113）"},
		func(ctx context.Context, in *struct {
			Body struct {
				SessionKey string `json:"session_key" minLength:"1" doc:"调用者（必须是本项目的 PM）"`
				Text       string `json:"text" doc:"简报正文：一段话，先说结论，提到需求 / 计划 / 决策时写编号；空字符串表示清除"`
			}
		}) (*okBody, error) {
			me, err := h.store.GetSession(ctx, in.Body.SessionKey)
			if err != nil {
				return nil, apiErr(err)
			}
			if me.Role != roles.PM || me.ProjectID == "" {
				return nil, huma.Error403Forbidden("只有项目的 PM 能写简报")
			}
			text := strings.TrimSpace(in.Body.Text)
			if utf8.RuneCountInString(text) > briefMax {
				text = string([]rune(text)[:briefMax])
			}
			if err := h.store.SetBrief(ctx, me.ProjectID, text, me.Key); err != nil {
				return nil, apiErr(err)
			}
			h.broker.publish("projects")
			return ok(), nil
		})
}
