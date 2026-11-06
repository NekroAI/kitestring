package hub

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NekroAI/kitestring/internal/store"
)

// 最近动态（R-296）：工作台和项目页共用，口径见 store/activity.go。

type activityIn struct {
	ProjectID string `query:"project_id" doc:"只看某个项目；空 = 全部项目"`
	Mine      bool   `query:"mine" doc:"只看你做的"`
	Limit     int    `query:"limit" doc:"最多几条，默认 20，最多 200"`
}

type activityOut struct {
	Body struct {
		Items []store.Activity `json:"items" doc:"新的在前"`
	}
}

func (h *Hub) registerActivityWeb(web huma.API) {
	huma.Register(web, huma.Operation{OperationID: "list-activity", Method: http.MethodGet, Path: "/api/activity", Tags: []string{"requirements"}, Summary: "最近动态：提交证据、提交验收、提决策、受阻与解除、留言、指派、提交评审、审查意见、你的答复与验收（R-296）"},
		func(ctx context.Context, in *activityIn) (*activityOut, error) {
			items, err := h.store.ListActivity(ctx, store.ActivityQuery{ProjectID: in.ProjectID, Mine: in.Mine, Limit: in.Limit})
			if err != nil {
				return nil, apiErr(err)
			}
			out := &activityOut{}
			out.Body.Items = items
			return out, nil
		})
}
