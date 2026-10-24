package mcpserver

import (
	"context"
	"fmt"
	"net/url"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// 审查意见（R-88）：审查员会话用 review_submit 把审查意见挂到需求上，不改需求状态、不拦截验收。

type reviewIn struct {
	Ref     string   `json:"ref" jsonschema:"需求编号（如 R-12）或 ID"`
	Summary string   `json:"summary" jsonschema:"审查意见（可用 Markdown）：结论在前，问题按严重程度排序，每条写清依据（文件:行号、复现步骤）和建议；确认过的和推测的分开写"`
	Images  []string `json:"images,omitempty" jsonschema:"本机截图等图片文件路径（可选），例如标出问题的界面截图"`
}

func (t *tools) registerReview(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{Name: "review_submit", Description: "评审（Rev）用：把审查意见挂到需求上（用户在需求详情里看到，标明「评审」）。不改需求状态、不拦截验收；只有身份为评审（ks role reviewer）的会话能交，任意工具的会话都能担任评审。"},
		func(ctx context.Context, req *mcp.CallToolRequest, in reviewIn) (*mcp.CallToolResult, any, error) {
			key := t.sessionKey(ctx, req)
			if key == "" {
				return nil, nil, fmt.Errorf("无法识别当前会话")
			}
			body := map[string]any{"session_key": key, "summary": in.Summary}
			ids, err := t.upload(ctx, in.Images)
			if err != nil {
				return nil, nil, err
			}
			if len(ids) > 0 {
				body["files"] = ids
			}
			var out reqView
			if err := t.c.Do(ctx, "POST", "/agent/requirements/"+url.PathEscape(in.Ref)+"/reviews", body, &out); err != nil {
				return nil, nil, err
			}
			return text(fmt.Sprintf("%s 已挂上审查意见（共 %d 条）；需求状态仍是%s，验收由用户决定。", out.Code, len(out.Reviews), zh(out.Status))), nil, nil
		})
}
