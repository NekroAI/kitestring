package hub

import (
	"context"
	"fmt"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NekroAI/kitestring/internal/protocol"
	"github.com/NekroAI/kitestring/internal/store"
)

// 资源库的变体、取代与删除（R-380，数据见 store/assetlinks.go）。
// 变体随 asset.json 推送（每个具名变体一条，key = 「素材 id@变体名」）；取代由 Agent 用 ks asset supersede 标记；
// 真删除只在网页上做（先 GET delete-check 看连带什么，确认后 DELETE），Agent 接口不开放。

// checkVariantItem 校验变体条目：key 要是「素材 id@变体名」，素材 id 和变体名都合格。
func checkVariantItem(it protocol.AssetItem) error {
	if it.Parent == "" {
		if it.Variant != "" {
			if msg := protocol.CheckVariantName(it.Variant); msg != "" {
				return fmt.Errorf("素材 %s 的默认变体名 %s", it.Key, msg)
			}
		}
		return nil
	}
	if msg := protocol.CheckAssetID(it.Parent); msg != "" {
		return fmt.Errorf("变体 %s 所属的素材 id %s", it.Key, msg)
	}
	if msg := protocol.CheckVariantName(it.Variant); msg != "" {
		return fmt.Errorf("变体 %s 的变体名%s", it.Key, msg)
	}
	if it.Key != protocol.VariantKey(it.Parent, it.Variant) {
		return fmt.Errorf("变体的 key 要写成「素材 id@变体名」：%s", it.Key)
	}
	return nil
}

type supersedeIn struct {
	Body struct {
		Project    string                `json:"project,omitempty" doc:"项目名、ID 或 git remote；不写按当前目录、会话所在项目"`
		SessionKey string                `json:"session_key,omitempty"`
		CwdRemote  string                `json:"cwd_remote,omitempty"`
		Pairs      []store.SupersedePair `json:"pairs" minItems:"1" maxItems:"2000" doc:"取代关系：旧条目 → 取代它的素材；new 不写表示取消取代"`
		DryRun     bool                  `json:"dry_run,omitempty" doc:"只校验，不改"`
	}
}

type supersedeOut struct {
	Body struct {
		Project string                  `json:"project" doc:"项目名"`
		Applied bool                    `json:"applied" doc:"已改；有一条出错时一条都不改"`
		Results []store.SupersedeResult `json:"results"`
	}
}

type assetDeleteCheckOut struct{ Body store.AssetDeleteInfo }

type assetDeleteOut struct {
	Body struct {
		Deleted []store.AssetRef `json:"deleted" doc:"删掉的素材（含变体）"`
	}
}

func (h *Hub) registerAssetLinksWeb(api huma.API) {
	tag := []string{"assets"}
	huma.Register(api, huma.Operation{OperationID: "asset-delete-check", Method: http.MethodGet, Path: "/api/assets/{ref}/delete-check", Tags: tag,
		Summary: "删除素材前的检查（R-380）：版本、审核记录、一起删的变体、被它取代的条目、引用着它的分享"},
		func(ctx context.Context, in *refPath) (*assetDeleteCheckOut, error) {
			info, err := h.store.AssetDeleteCheck(ctx, in.Ref)
			return &assetDeleteCheckOut{Body: info}, apiErr(err)
		})
	huma.Register(api, huma.Operation{OperationID: "delete-asset", Method: http.MethodDelete, Path: "/api/assets/{ref}", Tags: tag,
		Summary: "真删除推错的素材（R-380）：连同变体、版本、审核记录、附件；只在网页上确认后调用"},
		func(ctx context.Context, in *refPath) (*assetDeleteOut, error) {
			gone, err := h.store.DeleteAsset(ctx, in.Ref)
			if err != nil {
				return nil, apiErr(err)
			}
			h.broker.publish("assets", "shares")
			out := &assetDeleteOut{}
			out.Body.Deleted = gone
			return out, nil
		})
}

func (h *Hub) registerAssetLinksAgent(api huma.API) {
	tag := []string{"agent"}
	huma.Register(api, huma.Operation{OperationID: "agent-asset-supersede", Method: http.MethodPost, Path: "/api/agent/assets/supersede", Tags: tag,
		Summary: "批量标记旧条目被某件素材取代（ks asset supersede，R-380）；有一条出错时一条都不改"},
		func(ctx context.Context, in *supersedeIn) (*supersedeOut, error) {
			project, err := h.agentAssetProject(ctx, in.Body.Project, in.Body.CwdRemote, in.Body.SessionKey)
			if err != nil {
				return nil, err
			}
			res, applied, err := h.store.SupersedeAssets(ctx, project, in.Body.Pairs, in.Body.DryRun)
			if err != nil {
				return nil, apiErr(err)
			}
			if applied {
				h.broker.publish("assets", "shares")
			}
			out := &supersedeOut{}
			out.Body.Applied, out.Body.Results = applied, res
			if project != "" {
				if p, err := h.store.GetProject(ctx, project); err == nil {
					out.Body.Project = p.Name
				}
			}
			return out, nil
		})
}
