package hub

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NekroAI/kitestring/internal/store"
)

// 状态变更历史（R-153）的查询接口：按需求 / 项目 / 时间段查。网页与 Agent（例如参谋复盘）各一个入口，查法相同。

type statusQueryIn struct {
	Requirement string `query:"requirement" doc:"需求编号（R-12）或 ID"`
	ProjectID   string `query:"project_id"`
	Since       string `query:"since" doc:"起始时间（RFC 3339，含）"`
	Until       string `query:"until" doc:"结束时间（RFC 3339，含）"`
	Limit       int    `query:"limit" doc:"最多返回多少条，0 = 不限"`
}

type statusChangesOut struct {
	Body struct {
		Changes []store.StatusChange `json:"changes"`
	}
}

func (h *Hub) statusChanges(ctx context.Context, in *statusQueryIn) (*statusChangesOut, error) {
	q := store.StatusQuery{ProjectID: in.ProjectID, Limit: in.Limit}
	if ref := strings.TrimSpace(in.Requirement); ref != "" {
		r, err := h.store.GetRequirement(ctx, ref)
		if err != nil {
			return nil, apiErr(err)
		}
		q.RequirementID = r.ID
	}
	var err error
	if q.Since, err = parseQueryTime(in.Since, "since"); err != nil {
		return nil, err
	}
	if q.Until, err = parseQueryTime(in.Until, "until"); err != nil {
		return nil, err
	}
	out := &statusChangesOut{}
	out.Body.Changes, err = h.store.ListStatusChanges(ctx, q)
	return out, apiErr(err)
}

func parseQueryTime(s, name string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return t, huma.Error400BadRequest(name + " 要写成 RFC 3339 时间，例如 2026-10-24T08:00:00+08:00")
	}
	return t, nil
}

func (h *Hub) registerStatusWeb(web huma.API) {
	huma.Register(web, huma.Operation{OperationID: "list-status-changes", Method: http.MethodGet, Path: "/api/status-changes", Tags: []string{"requirements"}, Summary: "需求状态变更历史：按需求 / 项目 / 时间段查（R-153）"}, h.statusChanges)
}

func (h *Hub) registerStatusAgent(agent huma.API) {
	huma.Register(agent, huma.Operation{OperationID: "agent-list-status-changes", Method: http.MethodGet, Path: "/api/agent/status-changes", Tags: []string{"agent"}, Summary: "需求状态变更历史：按需求 / 项目 / 时间段查（R-153）"}, h.statusChanges)
}
