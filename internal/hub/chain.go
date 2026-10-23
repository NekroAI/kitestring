package hub

import (
	"context"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NekroAI/kitestring/internal/store"
)

// 决策后果链（R-154）：决策产生了哪些需求、哪些提交，决策之间的取代 / 推翻。网页决策详情与 Agent 复盘各一个入口。

// fromDecision 校验 Agent 声明的来源决策，返回规范编号（D-x）；没写时为空。
func (h *Hub) fromDecision(ctx context.Context, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", nil
	}
	d, err := h.store.GetDecision(ctx, ref)
	if err != nil || d.Kind != store.KindDecision {
		return "", huma.Error400BadRequest("from_decision 要写一条存在的决策编号（D-x）：" + ref)
	}
	return d.Code, nil
}

type chainOut struct {
	Body store.DecisionChain
}

func (h *Hub) decisionChain(ctx context.Context, in *refPath) (*chainOut, error) {
	c, err := h.store.GetDecisionChain(ctx, in.Ref)
	return &chainOut{Body: c}, apiErr(err)
}

func (h *Hub) registerChainWeb(api huma.API) {
	huma.Register(api, huma.Operation{OperationID: "get-decision-chain", Method: http.MethodGet, Path: "/api/decisions/{ref}/chain", Tags: []string{"decisions"}, Summary: "决策后果链：产生的需求与提交、取代 / 推翻关系（R-154）"}, h.decisionChain)
}

func (h *Hub) registerChainAgent(api huma.API) {
	huma.Register(api, huma.Operation{OperationID: "agent-get-decision-chain", Method: http.MethodGet, Path: "/api/agent/decisions/{ref}/chain", Tags: []string{"agent"}, Summary: "决策后果链：产生的需求与提交、取代 / 推翻关系（R-154）"}, h.decisionChain)
}
