package hub

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NekroAI/kitestring/internal/roles"
	"github.com/NekroAI/kitestring/internal/store"
)

// 当前安排（R-245）：用户口头交代的临时安排（「额度紧，做完这几条就停」「这次可以 push」）落成项目上的短记录 N-x。
// 任何会话都能记（ks arrange add / arrange_add），标明谁记的；用户在工作台简报下方的小条里能加能撤。
// 接任概览和压缩后的关键状态都带上生效中的安排，接任的会话不靠交接转述就能读到。

type arrangeOut struct {
	Body struct {
		Arrangement store.Arrangement `json:"arrangement"`
		Message     string            `json:"message,omitempty" doc:"给 Agent 的一句话回执"`
	}
}

type arrangeListOut struct {
	Body struct {
		Arrangements []store.Arrangement `json:"arrangements"`
	}
}

// arrangeWho：安排的署名，会话记的带上它当时的身份。
func arrangeWho(a store.Arrangement) string {
	if a.BySession == "" {
		return "用户"
	}
	who := a.ByTitle
	if who == "" {
		who = a.BySession
	}
	if a.ByRole != "" {
		return roles.Name(a.ByRole) + "「" + who + "」"
	}
	return "「" + who + "」"
}

// arrangeLine：安排在概览 / 关键状态里的一行。
func arrangeLine(a store.Arrangement) string {
	until := "撤销前一直有效"
	if a.ExpiresAt != nil {
		until = "至 " + a.ExpiresAt.Local().Format("01-02 15:04")
	}
	return fmt.Sprintf("%s「%s」（%s记，%s）", a.Code, a.Text, arrangeWho(a), until)
}

// arrangeNote：项目生效中的安排，一行一条；没有时返回空。
func (h *Hub) arrangeNote(ctx context.Context, projectID string) []string {
	if projectID == "" {
		return nil
	}
	list, err := h.store.ListArrangements(ctx, projectID, false)
	if err != nil {
		h.log.Warn("读取当前安排失败", "err", err)
		return nil
	}
	var out []string
	for _, a := range list {
		out = append(out, arrangeLine(a))
	}
	return out
}

// projectSession：Agent 调用者所在的项目会话。
func (h *Hub) projectSession(ctx context.Context, key string) (store.Session, error) {
	x, err := h.store.GetSession(ctx, key)
	if err != nil {
		return x, apiErr(err)
	}
	if x.ProjectID == "" {
		return x, huma.Error409Conflict("认不出你在哪个项目：在项目目录里的会话运行")
	}
	return x, nil
}

func (h *Hub) registerAgentArrange(api huma.API) {
	tag := []string{"agent"}
	huma.Register(api, huma.Operation{OperationID: "agent-add-arrangement", Method: http.MethodPost, Path: "/api/agent/arrangements", Tags: tag,
		Summary: "记一条当前安排（R-245）：用户口头交代的临时安排，接任的会话自动读到"},
		func(ctx context.Context, in *struct {
			Body struct {
				SessionKey string `json:"session_key" minLength:"1"`
				Text       string `json:"text" doc:"安排：一两句话，写清要怎样（如「额度紧，做完 R-12、R-13 就停」「这次可以 push」）"`
				Until      string `json:"until,omitempty" doc:"到期：时长（30m、2h、1d）或日期（2026-11-02、2026-11-02 18:00、18:00）；不填表示撤销前一直有效"`
			}
		}) (*arrangeOut, error) {
			x, err := h.projectSession(ctx, in.Body.SessionKey)
			if err != nil {
				return nil, err
			}
			exp, err := store.ParseUntil(in.Body.Until, time.Now())
			if err != nil {
				return nil, huma.Error400BadRequest(err.Error())
			}
			a, err := h.store.AddArrangement(ctx, x.ProjectID, in.Body.Text, x.Key, exp)
			if err != nil {
				return nil, huma.Error400BadRequest(err.Error())
			}
			h.broker.publish("arrangements")
			out := &arrangeOut{}
			out.Body.Arrangement = a
			out.Body.Message = fmt.Sprintf("已记下当前安排 %s，用户在工作台上能看到、能撤销；接任的会话会自动读到。不再适用时用 ks arrange revoke %s 撤销。", a.Code, a.Code)
			return out, nil
		})
	huma.Register(api, huma.Operation{OperationID: "agent-list-arrangements", Method: http.MethodGet, Path: "/api/agent/arrangements", Tags: tag,
		Summary: "列出本项目的当前安排（R-245）"},
		func(ctx context.Context, in *struct {
			SessionKey string `query:"session_key"`
			All        bool   `query:"all" doc:"true = 连已到期、已撤销的也列出（最近 50 条）"`
		}) (*arrangeListOut, error) {
			x, err := h.projectSession(ctx, in.SessionKey)
			if err != nil {
				return nil, err
			}
			list, err := h.store.ListArrangements(ctx, x.ProjectID, in.All)
			if err != nil {
				return nil, err
			}
			out := &arrangeListOut{}
			out.Body.Arrangements = list
			return out, nil
		})
	huma.Register(api, huma.Operation{OperationID: "agent-revoke-arrangement", Method: http.MethodPost, Path: "/api/agent/arrangements/{code}/revoke", Tags: tag,
		Summary: "撤销一条当前安排（R-245）"},
		func(ctx context.Context, in *struct {
			Code string `path:"code"`
			Body struct {
				SessionKey string `json:"session_key" minLength:"1"`
			}
		}) (*arrangeOut, error) {
			x, err := h.projectSession(ctx, in.Body.SessionKey)
			if err != nil {
				return nil, err
			}
			cur, err := h.store.GetArrangement(ctx, in.Code)
			if err != nil || cur.ProjectID != x.ProjectID {
				return nil, huma.Error404NotFound("本项目没有安排 " + strings.ToUpper(in.Code))
			}
			a, err := h.store.RevokeArrangement(ctx, cur.ID, x.Key)
			if err != nil {
				return nil, huma.Error409Conflict(err.Error())
			}
			h.broker.publish("arrangements")
			out := &arrangeOut{}
			out.Body.Arrangement = a
			out.Body.Message = "已撤销 " + a.Code + "。"
			return out, nil
		})
}

func (h *Hub) registerArrangeWeb(api huma.API) {
	tag := []string{"arrangements"}
	huma.Register(api, huma.Operation{OperationID: "list-arrangements", Method: http.MethodGet, Path: "/api/arrangements", Tags: tag,
		Summary: "当前安排（R-245）：默认只列生效中的"},
		func(ctx context.Context, in *struct {
			ProjectID string `query:"project_id" doc:"只列这个项目的；不填列全部项目"`
			All       bool   `query:"all" doc:"true = 连已到期、已撤销的也列出（最近 50 条）"`
		}) (*arrangeListOut, error) {
			list, err := h.store.ListArrangements(ctx, in.ProjectID, in.All)
			if err != nil {
				return nil, err
			}
			out := &arrangeListOut{}
			out.Body.Arrangements = list
			return out, nil
		})
	huma.Register(api, huma.Operation{OperationID: "add-arrangement", Method: http.MethodPost, Path: "/api/arrangements", Tags: tag,
		Summary: "用户在网页上加一条当前安排（R-245），署名「用户」"},
		func(ctx context.Context, in *struct {
			Body struct {
				ProjectID string `json:"project_id" minLength:"1"`
				Text      string `json:"text" minLength:"1"`
				Until     string `json:"until,omitempty" doc:"到期：时长（30m、2h、1d）或日期；不填表示撤销前一直有效"`
			}
		}) (*arrangeOut, error) {
			if _, err := h.store.GetProject(ctx, in.Body.ProjectID); err != nil {
				return nil, apiErr(err)
			}
			exp, err := store.ParseUntil(in.Body.Until, time.Now())
			if err != nil {
				return nil, huma.Error400BadRequest(err.Error())
			}
			a, err := h.store.AddArrangement(ctx, in.Body.ProjectID, in.Body.Text, "", exp)
			if err != nil {
				return nil, huma.Error400BadRequest(err.Error())
			}
			h.broker.publish("arrangements")
			out := &arrangeOut{}
			out.Body.Arrangement = a
			return out, nil
		})
	huma.Register(api, huma.Operation{OperationID: "revoke-arrangement", Method: http.MethodPost, Path: "/api/arrangements/{code}/revoke", Tags: tag,
		Summary: "用户撤销一条当前安排（R-245）"},
		func(ctx context.Context, in *struct {
			Code string `path:"code"`
		}) (*arrangeOut, error) {
			a, err := h.store.RevokeArrangement(ctx, in.Code, store.ArrangeByUser)
			if errors.Is(err, store.ErrNotFound) {
				return nil, huma.Error404NotFound("没有安排 " + strings.ToUpper(in.Code))
			} else if err != nil {
				return nil, huma.Error409Conflict(err.Error())
			}
			h.broker.publish("arrangements")
			out := &arrangeOut{}
			out.Body.Arrangement = a
			return out, nil
		})
}
