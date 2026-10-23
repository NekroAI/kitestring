package hub

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NekroAI/kitestring/internal/store"
)

// 需求池盘面（R-133）：分布、池子指标、每条需求进入当前状态的时间。口径见 store/pool.go。

type poolIn struct {
	ProjectID string `query:"project_id" doc:"只看某个项目；空 = 全部项目"`
	TZOffset  string `query:"tz_offset" doc:"浏览器时区相对 UTC 的分钟数（东八区 = 480），决定「昨天」「近 7 天」按哪天算；不填按 Hub 所在时区"`
}

type poolOut struct {
	Body store.Pool
}

func (h *Hub) requirementPool(ctx context.Context, in *poolIn) (*poolOut, error) {
	loc := time.Local
	if s := strings.TrimSpace(in.TZOffset); s != "" {
		m, err := strconv.Atoi(s)
		if err != nil || m < -14*60 || m > 14*60 {
			return nil, huma.Error400BadRequest("tz_offset 要写成 -840 ~ 840 之间的整数分钟")
		}
		loc = time.FixedZone("client", m*60)
	}
	p, err := h.store.RequirementPool(ctx, in.ProjectID, time.Now(), loc)
	if err != nil {
		return nil, apiErr(err)
	}
	return &poolOut{Body: p}, nil
}

func (h *Hub) registerPoolWeb(web huma.API) {
	huma.Register(web, huma.Operation{OperationID: "requirement-pool", Method: http.MethodGet, Path: "/api/requirement-pool", Tags: []string{"requirements"}, Summary: "需求池盘面指标：各状态条数、昨天新增 / 完成、草稿积压、在流转、你提出 / Agent 记下、近 7 天每日完成（R-133）"}, h.requirementPool)
}
