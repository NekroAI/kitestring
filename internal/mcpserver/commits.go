package mcpserver

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// 提交、回滚与 CI（R-156）：commit_link 手动关联提交 / 上报 CI；req_get 附上需求关联的提交与提示。
// 纸鸢只记录与展示，不改仓库、不触发 CI。

type commitView struct {
	Link       string `json:"link"`
	Summary    string `json:"summary"`
	Source     string `json:"source"`
	Reverts    string `json:"reverts"`
	RevertedBy string `json:"reverted_by"`
	CI         *struct {
		State string `json:"state"`
		URL   string `json:"url"`
	} `json:"ci"`
}

type reqCommitsView struct {
	Code    string       `json:"code"`
	Commits []commitView `json:"commits"`
	Hints   []string     `json:"hints"`
}

var ciLabel = map[string]string{"pending": "CI 进行中", "success": "CI 通过", "failure": "CI 未通过"}

func shortSHA(s string) string {
	if len(s) > 7 && strings.Trim(strings.ToLower(s), "0123456789abcdef") == "" {
		return s[:7]
	}
	return s
}

// String 列出提交（每条一行）与提示；没有提交时为空。
func (v reqCommitsView) String() string {
	if len(v.Commits) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "提交（%d）：", len(v.Commits))
	for _, c := range v.Commits {
		fmt.Fprintf(&b, "\n- %s %s", shortSHA(c.Link), clip(c.Summary, 80))
		var tags []string
		if c.Reverts != "" {
			tags = append(tags, "回滚了 "+shortSHA(c.Reverts))
		}
		if c.RevertedBy != "" {
			tags = append(tags, "已被 "+shortSHA(c.RevertedBy)+" 回滚")
		}
		if c.CI != nil && ciLabel[c.CI.State] != "" {
			tags = append(tags, ciLabel[c.CI.State])
		}
		if len(tags) > 0 {
			b.WriteString("（" + strings.Join(tags, "；") + "）")
		}
	}
	for _, h := range v.Hints {
		b.WriteString("\n提示：" + h)
	}
	return b.String()
}

func (t *tools) reqCommitsText(ctx context.Context, ref string) string {
	var v reqCommitsView
	if err := t.c.Do(ctx, "GET", "/agent/requirements/"+url.PathEscape(ref)+"/commits", nil, &v); err != nil {
		return ""
	}
	return v.String()
}

type commitLinkIn struct {
	SHA         string `json:"sha" jsonschema:"提交哈希（7～40 位）"`
	Requirement string `json:"requirement,omitempty" jsonschema:"关联到哪条需求（R-x）；只上报 CI 时可不填"`
	Subject     string `json:"subject,omitempty" jsonschema:"提交标题（纸鸢还没从本地 git log 采集到这个提交时用来显示）"`
	CI          string `json:"ci,omitempty" jsonschema:"上报这个提交的 CI 结果：pending 进行中 / success 通过 / failure 未通过"`
	CIURL       string `json:"ci_url,omitempty" jsonschema:"CI 结果链接"`
}

func (t *tools) registerCommits(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{Name: "commit_link", Description: "把一个提交手动关联到需求，或上报提交的 CI 结果（需求时间线上显示）。提交标题末尾写了（R-x）的提交纸鸢会从本地 git log 自动关联、认出 revert，不用再调；没写编号的、或要补 CI 结果时才用。纸鸢只记录，不会替你 revert / reset / push，也不会触发 CI。"},
		func(ctx context.Context, req *mcp.CallToolRequest, in commitLinkIn) (*mcp.CallToolResult, any, error) {
			body := map[string]any{"sha": in.SHA, "requirement": in.Requirement, "subject": in.Subject, "ci": in.CI, "ci_url": in.CIURL, "session_key": t.sessionKey(ctx, req)}
			dropEmpty(body)
			var out reqCommitsView
			if err := t.c.Do(ctx, "POST", "/agent/commits", body, &out); err != nil {
				return nil, nil, err
			}
			var parts []string
			if in.Requirement != "" {
				parts = append(parts, fmt.Sprintf("已把 %s 关联到 %s", shortSHA(in.SHA), out.Code))
			}
			if in.CI != "" {
				parts = append(parts, fmt.Sprintf("已记下 %s 的 %s", shortSHA(in.SHA), ciLabel[in.CI]))
			}
			msg := strings.Join(parts, "；")
			if s := out.String(); s != "" {
				msg += "\n" + s
			}
			return text(msg), nil, nil
		})
}
