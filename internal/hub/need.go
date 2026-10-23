package hub

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NekroAI/kitestring/internal/notify"
	"github.com/NekroAI/kitestring/internal/roles"
)

// 没有可用开发时提醒你（R-87）：PM 判断需要开发会话、工人池里又没有在线的开发时，记在项目上并推送给你；
// 推送点开是项目页，写着原因和接入用的那句话。在线开发数够了就自动清除。

const needRenotify = 10 * time.Minute // 同一项目 10 分钟内不重复推送

// JoinPhrase 是开开发会话时对 Agent 说的那句话。
func JoinPhrase(project string) string {
	return fmt.Sprintf("连接到纸鸢，做「%s」的开发（运行 ks role dev）", project)
}

// requestDevs 记下需要 n 个开发并推送提醒。
func (h *Hub) requestDevs(ctx context.Context, projectID string, n int, reason string) error {
	p, err := h.store.GetProject(ctx, projectID)
	if err != nil {
		return err
	}
	if err := h.store.SetNeedDevs(ctx, projectID, n, reason); err != nil {
		return err
	}
	h.broker.publish("projects")
	dedup := "need|" + projectID
	h.mu.Lock()
	t, seen := h.notified[dedup]
	if seen && time.Since(t) < needRenotify && p.NeedDevs == n {
		h.mu.Unlock()
		return nil
	}
	h.notified[dedup] = time.Now()
	h.mu.Unlock()
	h.sendNotify(projectID, notify.Message{
		Event: notify.EventNeedDevs,
		Title: fmt.Sprintf("需要 %d 个开发 · %s", n, p.Name),
		Body:  fmt.Sprintf("%s\n在项目目录打开 Claude Code，说：%s", reason, JoinPhrase(p.Name)),
		Group: "kitestring",
	}, "/projects/"+projectID)
	return nil
}

// checkNeed：项目的在线开发够数了，清除提醒。
func (h *Hub) checkNeed(ctx context.Context, projectID string) {
	if projectID == "" {
		return
	}
	p, err := h.store.GetProject(ctx, projectID)
	if err != nil || p.NeedDevs == 0 {
		return
	}
	list, err := h.store.RoleSessions(ctx, projectID)
	if err != nil {
		return
	}
	online := 0
	for _, x := range list {
		if x.Role == roles.Dev && x.Presence() != "offline" {
			online++
		}
	}
	if online >= p.NeedDevs {
		if err := h.store.SetNeedDevs(ctx, projectID, 0, ""); err == nil {
			h.broker.publish("projects")
		}
	}
}

func (h *Hub) registerAgentNeed(api huma.API) {
	huma.Register(api, huma.Operation{OperationID: "agent-need-devs", Method: http.MethodPost, Path: "/api/agent/team/need", Tags: []string{"agent"}, Summary: "PM 请用户开开发会话（R-87）"},
		func(ctx context.Context, in *struct {
			Body struct {
				SessionKey string `json:"session_key" minLength:"1" doc:"调用者（必须是本项目的 PM）"`
				Count      int    `json:"count" minimum:"0" maximum:"5" doc:"还需要几个开发会话；0 表示撤销提醒"`
				Reason     string `json:"reason,omitempty" doc:"为什么需要：哪些需求在等、为什么不适合 subagent"`
			}
		}) (*okBody, error) {
			me, err := h.store.GetSession(ctx, in.Body.SessionKey)
			if err != nil {
				return nil, apiErr(err)
			}
			if me.Role != roles.PM || me.ProjectID == "" {
				return nil, huma.Error403Forbidden("只有项目的 PM 能请用户开开发会话")
			}
			if in.Body.Count == 0 {
				return ok(), apiErr(h.dismissNeed(ctx, me.ProjectID))
			}
			if strings.TrimSpace(in.Body.Reason) == "" {
				return nil, huma.Error400BadRequest("请写明为什么需要开发会话，用户会在推送里看到")
			}
			if err := h.requestDevs(ctx, me.ProjectID, in.Body.Count, in.Body.Reason); err != nil {
				return nil, apiErr(err)
			}
			return ok(), nil
		})
}

// dismissNeed 撤销提醒（PM 不再需要，或你在项目页点“不需要了”）。
func (h *Hub) dismissNeed(ctx context.Context, projectID string) error {
	if err := h.store.SetNeedDevs(ctx, projectID, 0, ""); err != nil {
		return err
	}
	h.broker.publish("projects")
	return nil
}

func (h *Hub) registerNeedWeb(api huma.API) {
	huma.Register(api, huma.Operation{OperationID: "dismiss-need-devs", Method: http.MethodDelete, Path: "/api/projects/{id}/need", Tags: []string{"projects"}, Summary: "撤销“需要开发会话”的提醒（R-87）"},
		func(ctx context.Context, in *struct {
			ID string `path:"id"`
		}) (*okBody, error) {
			return ok(), apiErr(h.dismissNeed(ctx, in.ID))
		})
}
