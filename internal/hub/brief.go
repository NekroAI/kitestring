package hub

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NekroAI/kitestring/internal/roles"
)

// PM 简报（R-113）：PM 用工具写一段给你看的话（你离开期间发生了什么、在等你什么），显示在工作台顶部。
// 每次写入覆盖上一份；写空文本清除。只有项目的 PM 能写。

const (
	briefMax       = 2000 // 防滥用的硬上限；建议长度见下，超建议照常保存并提示压缩（R-212）
	headlineAdv    = 40   // 一句话结论建议字数
	lookAdv        = 3    // 要先看的建议条数
	briefTextAdv   = 200  // 简报 / 总述正文建议字数
	morningTextAdv = 200
)

// shapeHints：一句话结论、要先看的、正文超出建议长度时给写的人一句提示（照常保存，不截断，R-212）。
func shapeHints(headline string, look []string, text string, textAdv int) string {
	var hs []string
	if n := utf8.RuneCountInString(headline); n > headlineAdv {
		hs = append(hs, fmt.Sprintf("一句话结论 %d 字，建议压到 %d 字以内（常驻条一行放不下会被截断）", n, headlineAdv))
	}
	if len(look) > lookAdv {
		hs = append(hs, fmt.Sprintf("要先看的 %d 条，建议只留最要紧的 %d 条以内", len(look), lookAdv))
	}
	if n := utf8.RuneCountInString(text); n > textAdv {
		hs = append(hs, fmt.Sprintf("正文 %d 字，建议压到 %d 字以内，细节放进证据 / 需求里", n, textAdv))
	}
	if len(hs) == 0 {
		return ""
	}
	return "提示：" + strings.Join(hs, "；") + "。已照常保存。"
}

// cleanLook：去掉空行、首尾空白。
func cleanLook(in []string) []string {
	var out []string
	for _, l := range in {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

func (h *Hub) registerAgentBrief(api huma.API) {
	huma.Register(api, huma.Operation{OperationID: "agent-set-brief", Method: http.MethodPost, Path: "/api/agent/brief", Tags: []string{"agent"}, Summary: "PM 写给用户的简报（R-113）"},
		func(ctx context.Context, in *struct {
			Body struct {
				SessionKey string   `json:"session_key" minLength:"1" doc:"调用者（必须是本项目的 PM）"`
				Text       string   `json:"text" doc:"简报正文：一段话，先说结论，提到需求 / 计划 / 决策时写编号；建议 200 字以内，超出照常保存并提示压缩；headline、look、text 都为空表示清除"`
				Headline   string   `json:"headline,omitempty" doc:"一句话结论（建议 40 字以内）：常驻条上只显示它（R-212）；不写时网页取正文第一句"`
				Look       []string `json:"look,omitempty" doc:"要用户先看的几条（建议 3 条以内，每条一句，写编号）：展开后排在最前（R-212）"`
			}
		}) (*briefOut, error) {
			me, err := h.store.GetSession(ctx, in.Body.SessionKey)
			if err != nil {
				return nil, apiErr(err)
			}
			if me.Role != roles.PM || me.ProjectID == "" {
				return nil, huma.Error403Forbidden("只有项目的 PM 能写简报")
			}
			text := strings.TrimSpace(in.Body.Text)
			headline := strings.TrimSpace(in.Body.Headline)
			look := cleanLook(in.Body.Look)
			if utf8.RuneCountInString(text) > briefMax {
				text = string([]rune(text)[:briefMax])
			}
			if err := h.store.SetBrief(ctx, me.ProjectID, text, me.Key, headline, look); err != nil {
				return nil, apiErr(err)
			}
			h.broker.publish("projects")
			out := &briefOut{}
			out.Body.OK = true
			out.Body.Hint = shapeHints(headline, look, text, briefTextAdv)
			return out, nil
		})
}

type briefOut struct {
	Body struct {
		OK   bool   `json:"ok"`
		Hint string `json:"hint,omitempty" doc:"一句话结论 / 要先看的 / 正文超出建议长度时的提示（照常保存）"`
	}
}
