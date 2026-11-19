package mcpserver

import (
	"context"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/NekroAI/kitestring/internal/assetpush"
	"github.com/NekroAI/kitestring/internal/client"
	"github.com/NekroAI/kitestring/internal/protocol"
)

// 资源库（R-351）：asset_push 与 ks asset push 同一套整理逻辑（internal/assetpush），交给 daemon 推送或排队补传。

type assetPushIn struct {
	Manifest    string   `json:"manifest,omitempty" jsonschema:"manifest.json 的本机路径；只推独立的图时可不填"`
	Previews    []string `json:"previews,omitempty" jsonschema:"预览图目录（本机路径），可多个"`
	Batch       string   `json:"batch,omitempty" jsonschema:"批次，如 G11e1；不填用 manifest 里的 batch"`
	Category    string   `json:"category,omitempty" jsonschema:"没写类别的素材用它"`
	Status      string   `json:"status,omitempty" jsonschema:"推送后的状态：pending 待审（默认）/ wip 制作中"`
	Requirement string   `json:"requirement,omitempty" jsonschema:"关联需求 R-x"`
	Project     string   `json:"project,omitempty" jsonschema:"记进哪个项目（项目名或 git remote）；不填记进会话所在项目"`
	Branch      string   `json:"branch,omitempty" jsonschema:"分支；不填从 git 读"`
	Commit      string   `json:"commit,omitempty" jsonschema:"提交；不填从 git 读"`
}

func (t *tools) registerAssets(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{Name: "asset_push", Description: "把一批素材推进纸鸢的资源库，用户在网页上集中浏览和审核（与 shell 命令 ks asset push 相同）。manifest 里的 GLB 是零件，预览目录里文件名以零件 id 开头的图挂到零件上，其余图是独立的图（目录图、概念图、对比图，文件名去扩展名当 id，末尾 _A / _B 的候选成组）；面数、尺寸从 manifest 读。同一批次里同一 id 内容有变的重推记为新版本，旧版本保留；内容相同的不新增版本；Hub 已有的文件不重传。未连接 Hub 时排队，连上后自动补传。GLB 超过设置里的上限、附件存储不可用时拒收并说明原因。"},
		func(ctx context.Context, req *mcp.CallToolRequest, in assetPushIn) (*mcp.CallToolResult, any, error) {
			p, notes, err := assetpush.Build(assetpush.Options{Manifest: in.Manifest, Previews: in.Previews, Batch: in.Batch, Category: in.Category, Status: in.Status,
				Branch: in.Branch, Commit: in.Commit, Project: in.Project, Requirement: in.Requirement, SessionKey: t.sessionKey(ctx, req)})
			if err != nil {
				return nil, nil, err
			}
			var out protocol.AssetPushReply
			if err := client.New(15*time.Minute).Do(ctx, "POST", "/assets/push", p, &out); err != nil {
				return nil, nil, err
			}
			return text(assetpush.Text(p, out, notes)), nil, nil
		})
}
