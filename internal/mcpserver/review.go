package mcpserver

import (
	"context"
	"fmt"
	"net/url"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/NekroAI/kitestring/internal/blocks"
	"github.com/NekroAI/kitestring/internal/writing"
)

// 审查意见（R-88）：审查员会话用 review_submit 把审查意见挂到需求上，不改需求状态。
// R-232 起评审还做验证：结果用 verdict + checklist 块 + 带标题和框的截图交，和证据同样的结构；
// 提交评审的待验收需求，这时才进用户的验收卡，有不通过的纸鸢通知主调度（R-237）。

type reviewIn struct {
	Ref     string           `json:"ref" jsonschema:"需求编号（如 R-12）或 ID"`
	Verdict string           `json:"verdict,omitempty" jsonschema:"一句结论，40 字以内（例如：4 条验收点 3 条通过，空表时表头没冻结）"`
	Blocks  []map[string]any `json:"blocks,omitempty" jsonschema:"表达块：验证结果放一个 checklist，按验收标准逐条给 result（pass / fail / ask）和 why（一句依据：怎么验的、看到了什么），用 see 指向证明它的截图块；也可以放 figure / compare 截图。字段同 evidence_attach"`
	Summary string           `json:"summary,omitempty" jsonschema:"审查意见正文（可用 Markdown）：结论在前，问题按严重程度排序，每条写清依据（文件:行号、复现步骤）和建议；确认过的和推测的分开写。给了 verdict 和 blocks 时可以不写"`
	Details string           `json:"details,omitempty" jsonschema:"怎么验的：跑了哪些测试、演示环境怎么起的（默认折叠）"`
	Images  []any            `json:"images,omitempty" jsonschema:"截图：{path: 本机路径, title: 一句话标题, boxes: [{x, y, w, h, label, criterion}]}，boxes 框出问题或改动处，坐标是图片原始尺寸的比例（0～1）；只写路径字符串也行"`
}

func (t *tools) registerReview(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{Name: "review_submit", Description: "评审（Rev）用：把审查意见和验证结果挂到需求上（用户在需求详情和验收卡上看到，标明「评审」）。验证结果推荐结构化地交：verdict 一句结论，blocks 里放一个 checklist 按验收标准逐条 pass / fail / ask 加一句依据，images 附截图（每张写标题、用 boxes 框出问题处）。只验证：不改代码、不改需求状态；有不通过的纸鸢自动通知主调度，验收由用户决定。只有身份为评审（ks role reviewer）的会话能交，任意工具的会话都能担任评审；这个工具没有 blocks / images 字段时（旧版 MCP），用命令行 ks req review <R-编号> --verdict … --blocks 清单.json --image 截图.png。\n" + writing.BlocksGuide},
		func(ctx context.Context, req *mcp.CallToolRequest, in reviewIn) (*mcp.CallToolResult, any, error) {
			key := t.sessionKey(ctx, req)
			if key == "" {
				return nil, nil, fmt.Errorf("无法识别当前会话")
			}
			body := map[string]any{"session_key": key, "summary": in.Summary, "verdict": in.Verdict, "details": in.Details}
			dropEmpty(body)
			cache := map[string]string{}
			if len(in.Images) > 0 {
				if err := blocks.LocalizeImages(in.Images, cache, func(p string) (string, error) {
					up, err := t.upload(ctx, []string{p})
					if err != nil {
						return "", err
					}
					return up[0], nil
				}); err != nil {
					return nil, nil, err
				}
				body["images"] = in.Images
			}
			if err := t.addBlocks(ctx, body, in.Blocks, cache); err != nil {
				return nil, nil, err
			}
			var out struct {
				reqView
				Review *struct {
					State string `json:"state"`
				} `json:"review"`
				BlockWarnings []string `json:"block_warnings"`
				ImageHints    []string `json:"image_hints"`
			}
			if err := t.c.Do(ctx, "POST", "/agent/requirements/"+url.PathEscape(in.Ref)+"/reviews", body, &out); err != nil {
				return nil, nil, err
			}
			msg := fmt.Sprintf("%s 已挂上审查意见（共 %d 条）；需求状态仍是%s，验收由用户决定。", out.Code, len(out.Reviews), zh(out.Status))
			if out.Review != nil && out.Review.State == "done" {
				msg += "这条需求已进用户的验收卡，卡上带你的结论；有不通过的已通知主调度。"
			}
			msg += blockWarnings(out.BlockWarnings)
			msg += imageHints(out.ImageHints)
			return text(msg), nil, nil
		})
}
