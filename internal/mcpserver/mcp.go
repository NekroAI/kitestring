// Package mcpserver 实现 ks mcp：agent 用来读写需求、提出决策的 stdio MCP server。
// 所有调用经本机 daemon 转发到 Hub；会话按 D17 精确识别。
package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/NekroAI/kitestring/internal/client"
	"github.com/NekroAI/kitestring/internal/protocol"
	"github.com/NekroAI/kitestring/internal/version"
)

const instructions = `Kitestring 是用户的多 agent 驾驶舱，用户通过它统一查看需求、回答决策。
- 开始一项工作前，用 req_list 看当前项目已有需求；开始做某条需求时用 session_link_req 关联本会话（草稿 / 待开始的需求会自动变为进行中）。
- 用户提出的新需求、你发现必须做的事，用 req_create 记下来，避免遗漏。
- 需要用户拍板的问题用 decision_raise 提出（附选项与推荐），不要只埋在对话里。用户在 Kitestring 中作答后，答复会在用户下一次发消息时自动注入你的上下文。
- 开发完成后把需求状态设为 review（待验收）并用 evidence_attach 附上证据；done 只能由用户验收后设置。截图类证据务必附上 images。
- 设计类需求（kind = design，或用户说“出方案 / 设计”）：先用 decision_raise 交 2～3 套方案，并在 option_images 附上每套的效果图，用户看图选定后再实现。
- 只有一个方案 / 计划需要用户点头时，用 decision_raise 的 approval = true（可在 images 附方案图）：用户会批准或拒绝，并可能附言，按附言调整。
- 计划：用户给出较大的目标时，可用 plan_create 拆成有顺序的阶段（阶段内可放多条需求），用户批准后才开工。计划开始或阶段解锁时，该阶段的需求会交给你；阶段内需求都做完（待验收）后，用 stage_submit 交阶段小结，用户验收整个阶段后下一阶段自动交给你。plan_get 查看计划进度。
- 做完一段、需要用户验收或答复才能继续时，可以在后台运行 shell 命令 ks wait（默认最长等 1 小时，可用 --timeout 调整，最长 12h；后台任务的超时要设得比它长）。用户在 Kitestring 上答复、验收或留言后它会输出内容并退出，你随即被唤醒，按输出继续；超时则结束本轮。只在确实等用户时使用，不要用它做定时或自动续跑。`

// Run 以 stdio 运行 MCP server。
func Run(ctx context.Context) error {
	s := mcp.NewServer(&mcp.Implementation{Name: "kitestring", Version: version.Version}, &mcp.ServerOptions{Instructions: instructions})
	t := &tools{c: client.New(20 * time.Second)}
	t.register(s)
	return s.Run(ctx, &mcp.StdioTransport{})
}

type tools struct {
	c *client.Client
}

// sessionKey 识别调用来自哪个会话（D17）：
//   - Codex：请求 _meta.sessionId；
//   - Claude：用 _meta 里的 toolUseId 向 daemon 查询（与 PreToolUse hook 对齐）；
//     进程环境里的 CLAUDE_CODE_SESSION_ID 只作兜底——同一进程内恢复或清空会话后它会过期。
func (t *tools) sessionKey(ctx context.Context, req *mcp.CallToolRequest) string {
	if req.Params != nil && req.Params.Meta != nil {
		if id, ok := req.Params.Meta["claudecode/toolUseId"].(string); ok && id != "" {
			var out struct {
				SessionKey string `json:"session_key"`
			}
			if t.c.Do(ctx, "GET", "/session?tool_use_id="+url.QueryEscape(id), nil, &out) == nil && out.SessionKey != "" {
				return out.SessionKey
			}
		}
	}
	return sessionKeyFromMeta(req)
}

func sessionKeyFromMeta(req *mcp.CallToolRequest) string {
	tool := ""
	if p := req.Session.InitializeParams(); p != nil && p.ClientInfo != nil {
		name := strings.ToLower(p.ClientInfo.Name)
		switch {
		case strings.Contains(name, "codex"):
			tool = protocol.ToolCodex
		case strings.Contains(name, "claude"):
			tool = protocol.ToolClaude
		}
	}
	if req.Params != nil && req.Params.Meta != nil {
		if sid, ok := req.Params.Meta["sessionId"].(string); ok && sid != "" {
			if tool == "" {
				tool = protocol.ToolCodex
			}
			return protocol.SessionKey(tool, sid)
		}
	}
	if sid := os.Getenv("CLAUDE_CODE_SESSION_ID"); sid != "" {
		return protocol.SessionKey(protocol.ToolClaude, sid)
	}
	return ""
}

type reqListIn struct {
	All    bool     `json:"all,omitempty" jsonschema:"为 true 时列出所有项目的需求；默认只列当前会话所在项目"`
	Status []string `json:"status,omitempty" jsonschema:"按状态过滤：draft ready in_progress blocked review done shelved"`
}

type refIn struct {
	Ref string `json:"ref" jsonschema:"需求编号（如 R-12）或 ID"`
}

type reqCreateIn struct {
	Title      string   `json:"title" jsonschema:"需求标题，一句话"`
	Detail     string   `json:"detail,omitempty" jsonschema:"背景与细节"`
	Acceptance string   `json:"acceptance,omitempty" jsonschema:"验收标准"`
	Status     string   `json:"status,omitempty" jsonschema:"初始状态，默认 draft；已明确可做的用 ready，正在做的用 in_progress"`
	Priority   string   `json:"priority,omitempty" jsonschema:"P0 到 P3，默认 P2"`
	Source     string   `json:"source,omitempty" jsonschema:"来源，如：用户对话、issue 链接、截图"`
	DocLink    string   `json:"doc_link,omitempty" jsonschema:"仓库内方案文档路径或链接"`
	Kind       string   `json:"kind,omitempty" jsonschema:"需求类型：design = 设计类（先交方案、用户选定后再实现），不填为普通需求"`
	Images     []string `json:"images,omitempty" jsonschema:"本机图片文件路径（PNG/JPEG/GIF/WebP，单张不超过 10 MB），如截图"`
}

type reqStatusIn struct {
	Ref    string `json:"ref" jsonschema:"需求编号（如 R-12）或 ID"`
	Status string `json:"status" jsonschema:"新状态：draft ready in_progress blocked review shelved（done 只能由用户设置）"`
}

type evidenceIn struct {
	Ref     string   `json:"ref" jsonschema:"需求编号（如 R-12）或 ID"`
	Kind    string   `json:"kind" jsonschema:"证据类型：test、commit、screenshot、doc、manual 等"`
	Summary string   `json:"summary" jsonschema:"证据摘要，例如：单元测试 42 个全部通过"`
	Link    string   `json:"link,omitempty" jsonschema:"链接或文件路径"`
	Images  []string `json:"images,omitempty" jsonschema:"本机截图等图片文件路径（PNG/JPEG/GIF/WebP，单张不超过 10 MB），用户验收时会直接看到"`
	Videos  []string `json:"videos,omitempty" jsonschema:"本机录屏文件路径（MP4/WebM，单个不超过 50 MB）。只在用户明确要求录屏或视频时才附加，平时用截图"`
}

type decisionIn struct {
	Question       string   `json:"question" jsonschema:"需要用户决策的问题，单独成立、无需上下文也能看懂"`
	Context        string   `json:"context,omitempty" jsonschema:"背景：为什么需要决策、各选项的取舍"`
	Options        []string `json:"options,omitempty" jsonschema:"候选选项"`
	OptionImages   []string `json:"option_images,omitempty" jsonschema:"每个选项的效果图（本机图片路径，与 options 一一对应，没有的留空字符串）。比较设计方案时用，用户会看图点选"`
	Approval       bool     `json:"approval,omitempty" jsonschema:"请用户批准一个方案 / 计划时设为 true：选项固定为“批准 / 拒绝”，用户两种都可以附言"`
	Images         []string `json:"images,omitempty" jsonschema:"问题附带的本机图片路径（例如待批准的方案图）"`
	Recommendation string   `json:"recommendation,omitempty" jsonschema:"你的推荐及理由"`
	Requirement    string   `json:"requirement,omitempty" jsonschema:"关联需求编号（如 R-12）"`
}

type planReqIn struct {
	Ref        string `json:"ref,omitempty" jsonschema:"已有需求的编号（如 R-12）；给了就不用填其它字段"`
	Title      string `json:"title,omitempty" jsonschema:"新需求标题（不给 ref 时必填），一句话说清一个具体改动"`
	Detail     string `json:"detail,omitempty" jsonschema:"新需求的背景与细节"`
	Acceptance string `json:"acceptance,omitempty" jsonschema:"新需求的验收标准"`
	Priority   string `json:"priority,omitempty" jsonschema:"P0 到 P3，默认 P2"`
	Kind       string `json:"kind,omitempty" jsonschema:"design = 设计类需求，不填为普通需求"`
}

type planStageIn struct {
	Title        string      `json:"title" jsonschema:"阶段名称，如：方案、数据与接口、网页"`
	Requirements []planReqIn `json:"requirements" jsonschema:"这一阶段的需求（可并行做），可以是已有需求或新需求"`
}

type planCreateIn struct {
	Title  string        `json:"title" jsonschema:"计划标题"`
	Goal   string        `json:"goal,omitempty" jsonschema:"计划目标：做完后达到什么效果"`
	Stages []planStageIn `json:"stages" jsonschema:"按先后顺序排列的阶段；前一阶段验收通过后才开始下一阶段"`
}

type planRefIn struct {
	Plan string `json:"plan" jsonschema:"计划编号（如 P-1）或 ID"`
}

type stageSubmitIn struct {
	Plan    string `json:"plan" jsonschema:"计划编号（如 P-1）或 ID"`
	Stage   int    `json:"stage" jsonschema:"阶段序号（从 1 开始），只能是计划的当前阶段"`
	Summary string `json:"summary" jsonschema:"阶段小结：这一阶段做了什么、怎么验证的，用户验收时会看到"`
}

type linkIn struct {
	Requirement string `json:"requirement" jsonschema:"需求编号（如 R-12）或 ID"`
}

func (t *tools) register(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{Name: "req_list", Description: "列出需求（默认当前会话所在项目），返回编号、标题、状态、轮到谁、待决决策数。"},
		func(ctx context.Context, req *mcp.CallToolRequest, in reqListIn) (*mcp.CallToolResult, any, error) {
			q := url.Values{}
			if k := t.sessionKey(ctx, req); k != "" {
				q.Set("session_key", k)
			}
			if in.All {
				q.Set("all", "true")
			}
			for _, st := range in.Status {
				q.Add("status", st)
			}
			var out map[string]any
			err := t.c.Do(ctx, "GET", "/agent/requirements?"+q.Encode(), nil, &out)
			return nil, out, err
		})
	mcp.AddTool(s, &mcp.Tool{Name: "req_get", Description: "读取一条需求的完整信息，含验收标准、关联会话、证据，以及用户的修改意见（附图会以图片形式返回）。"},
		func(ctx context.Context, req *mcp.CallToolRequest, in refIn) (*mcp.CallToolResult, any, error) {
			var out map[string]any
			if err := t.c.Do(ctx, "GET", "/agent/requirements/"+url.PathEscape(in.Ref), nil, &out); err != nil {
				return nil, nil, err
			}
			text, _ := json.MarshalIndent(out, "", "  ")
			res := &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(text)}}}
			for _, id := range imageIDs(out) {
				if data, mime, err := t.c.Fetch(ctx, "/agent/files/"+url.PathEscape(id), 10<<20); err == nil {
					res.Content = append(res.Content, &mcp.ImageContent{Data: data, MIMEType: mime})
				}
			}
			return res, nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "req_create", Description: "新建需求并自动关联当前会话。用户提出的新需求、你发现必须做的事都应记录，避免遗漏。"},
		func(ctx context.Context, req *mcp.CallToolRequest, in reqCreateIn) (*mcp.CallToolResult, any, error) {
			body := map[string]any{"title": in.Title, "detail": in.Detail, "acceptance": in.Acceptance, "status": in.Status, "priority": in.Priority, "source": in.Source, "doc_link": in.DocLink, "kind": in.Kind, "session_key": t.sessionKey(ctx, req)}
			dropEmpty(body)
			ids, err := t.upload(ctx, in.Images)
			if err != nil {
				return nil, nil, err
			}
			if len(ids) > 0 {
				body["files"] = ids
			}
			var out map[string]any
			err = t.c.Do(ctx, "POST", "/agent/requirements", body, &out)
			return nil, out, err
		})
	mcp.AddTool(s, &mcp.Tool{Name: "req_update_status", Description: "更新需求状态并关联当前会话。开发完成设为 review（待验收），done 只能由用户验收后设置。"},
		func(ctx context.Context, req *mcp.CallToolRequest, in reqStatusIn) (*mcp.CallToolResult, any, error) {
			body := map[string]any{"status": in.Status, "session_key": t.sessionKey(ctx, req)}
			dropEmpty(body)
			var out map[string]any
			err := t.c.Do(ctx, "POST", "/agent/requirements/"+url.PathEscape(in.Ref)+"/status", body, &out)
			return nil, out, err
		})
	mcp.AddTool(s, &mcp.Tool{Name: "evidence_attach", Description: "为需求附加验收证据（测试结果、提交、截图路径等）。"},
		func(ctx context.Context, req *mcp.CallToolRequest, in evidenceIn) (*mcp.CallToolResult, any, error) {
			body := map[string]any{"kind": in.Kind, "summary": in.Summary, "link": in.Link, "session_key": t.sessionKey(ctx, req)}
			dropEmpty(body)
			ids, err := t.upload(ctx, append(in.Images, in.Videos...))
			if err != nil {
				return nil, nil, err
			}
			if len(ids) > 0 {
				body["files"] = ids
			}
			var out map[string]any
			err = t.c.Do(ctx, "POST", "/agent/requirements/"+url.PathEscape(in.Ref)+"/evidence", body, &out)
			return nil, out, err
		})
	mcp.AddTool(s, &mcp.Tool{Name: "decision_raise", Description: "向用户提出一个需要拍板的决策，进入用户的决策收件箱并推送通知。用户作答后，答复会在用户下一次发消息时自动注入本会话上下文；在此之前可以继续推进不受影响的工作。"},
		func(ctx context.Context, req *mcp.CallToolRequest, in decisionIn) (*mcp.CallToolResult, any, error) {
			body := map[string]any{"question": in.Question, "context": in.Context, "options": in.Options, "recommendation": in.Recommendation, "requirement": in.Requirement, "session_key": t.sessionKey(ctx, req)}
			dropEmpty(body)
			if in.Approval {
				body["approval"] = true
			}
			if len(in.Images) > 0 {
				ids, err := t.upload(ctx, in.Images)
				if err != nil {
					return nil, nil, err
				}
				body["images"] = ids
			}
			if len(in.OptionImages) > 0 {
				ids := make([]string, len(in.OptionImages))
				for i, p := range in.OptionImages {
					if strings.TrimSpace(p) == "" {
						continue
					}
					up, err := t.upload(ctx, []string{p})
					if err != nil {
						return nil, nil, err
					}
					ids[i] = up[0]
				}
				body["option_images"] = ids
			}
			var out map[string]any
			if err := t.c.Do(ctx, "POST", "/agent/decisions", body, &out); err != nil {
				return nil, nil, err
			}
			return nil, map[string]any{"decision": out["code"], "status": "已进入用户的决策收件箱，作答后会自动注入本会话"}, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "session_link_req", Description: "开始做某条需求时调用：把当前会话关联到这条需求，草稿 / 待开始的需求会自动变为进行中，让用户在看板上看到你在做什么。"},
		func(ctx context.Context, req *mcp.CallToolRequest, in linkIn) (*mcp.CallToolResult, any, error) {
			key := t.sessionKey(ctx, req)
			if key == "" {
				return nil, nil, fmt.Errorf("无法识别当前会话")
			}
			var out map[string]any
			err := t.c.Do(ctx, "POST", "/agent/sessions/"+url.PathEscape(key)+"/link", map[string]any{"requirement": in.Requirement}, &out)
			return nil, out, err
		})
	mcp.AddTool(s, &mcp.Tool{Name: "plan_create", Description: "把较大的目标拆成计划：按顺序的阶段，阶段内可放多条需求（已有或新建）。提交后进入用户的“待批准”，批准后第一阶段的需求会交给本会话。"},
		func(ctx context.Context, req *mcp.CallToolRequest, in planCreateIn) (*mcp.CallToolResult, any, error) {
			body := map[string]any{"title": in.Title, "goal": in.Goal, "stages": in.Stages, "session_key": t.sessionKey(ctx, req)}
			dropEmpty(body)
			var out planView
			if err := t.c.Do(ctx, "POST", "/agent/plans", body, &out); err != nil {
				return nil, nil, err
			}
			return text(out.Code + " 已提交，等待用户批准；批准后第一阶段的需求会交给你。\n" + out.String()), nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "plan_get", Description: "查看计划进度：各阶段状态、阶段内需求及其状态、阶段小结。"},
		func(ctx context.Context, req *mcp.CallToolRequest, in planRefIn) (*mcp.CallToolResult, any, error) {
			var out planView
			if err := t.c.Do(ctx, "GET", "/agent/plans/"+url.PathEscape(in.Plan), nil, &out); err != nil {
				return nil, nil, err
			}
			return text(out.String()), nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "stage_submit", Description: "计划当前阶段的需求都做完（待验收）后调用：交阶段小结，阶段进入“待阶段验收”。用户通过后下一阶段会自动交给你；退回会附意见送达。"},
		func(ctx context.Context, req *mcp.CallToolRequest, in stageSubmitIn) (*mcp.CallToolResult, any, error) {
			var out planView
			path := fmt.Sprintf("/agent/plans/%s/stages/%d/submit", url.PathEscape(in.Plan), in.Stage)
			if err := t.c.Do(ctx, "POST", path, map[string]any{"summary": in.Summary, "session_key": t.sessionKey(ctx, req)}, &out); err != nil {
				return nil, nil, err
			}
			return text(fmt.Sprintf("%s 阶段 %d 已交小结，等待用户验收。", out.Code, in.Stage)), nil, nil
		})
}

func text(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

// planView 是计划的精简视图：按行输出，省上下文。
type planView struct {
	Code   string `json:"code"`
	Title  string `json:"title"`
	Goal   string `json:"goal"`
	Status string `json:"status"`
	Paused bool   `json:"paused"`
	Stages []struct {
		Seq          int    `json:"seq"`
		Title        string `json:"title"`
		Status       string `json:"status"`
		Summary      string `json:"summary"`
		Requirements []struct {
			Code   string `json:"code"`
			Title  string `json:"title"`
			Status string `json:"status"`
		} `json:"requirements"`
	} `json:"stages"`
}

var planStatusZh = map[string]string{"draft": "待开始 / 待批准", "active": "进行中", "done": "已完成", "shelved": "已搁置"}
var stageStatusZh = map[string]string{"locked": "未解锁", "active": "进行中", "review": "待阶段验收", "passed": "已通过"}

func (p planView) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s「%s」%s", p.Code, p.Title, planStatusZh[p.Status])
	if p.Paused {
		b.WriteString("（暂停：已通过阶段里有需求被重开）")
	}
	b.WriteString("\n")
	if p.Goal != "" {
		fmt.Fprintf(&b, "目标：%s\n", p.Goal)
	}
	for _, st := range p.Stages {
		fmt.Fprintf(&b, "阶段 %d「%s」%s\n", st.Seq, st.Title, stageStatusZh[st.Status])
		for _, r := range st.Requirements {
			fmt.Fprintf(&b, "  - %s %s [%s]\n", r.Code, r.Title, r.Status)
		}
		if st.Summary != "" {
			fmt.Fprintf(&b, "  小结：%s\n", st.Summary)
		}
	}
	return b.String()
}

// upload 上传本机图片或录屏，返回附件 ID。
func (t *tools) upload(ctx context.Context, paths []string) ([]string, error) {
	var ids []string
	for _, p := range paths {
		var ref struct {
			ID string `json:"id"`
		}
		if err := t.c.Upload(ctx, "/agent/files", p, &ref); err != nil {
			return nil, fmt.Errorf("上传 %s 失败：%w", p, err)
		}
		ids = append(ids, ref.ID)
	}
	return ids, nil
}

// imageIDs 收集需求详情里所有附图（不含录屏）：描述、证据（未作废）、修改意见；最多 8 张。
func imageIDs(r map[string]any) []string {
	var ids []string
	add := func(v any) {
		list, _ := v.([]any)
		for _, it := range list {
			if m, ok := it.(map[string]any); ok {
				if mime, _ := m["mime"].(string); mime != "" && !strings.HasPrefix(mime, "image/") {
					continue
				}
				if id, ok := m["id"].(string); ok && len(ids) < 8 {
					ids = append(ids, id)
				}
			}
		}
	}
	add(r["files"])
	for _, key := range []string{"feedback", "evidence"} {
		list, _ := r[key].([]any)
		for _, it := range list {
			m, _ := it.(map[string]any)
			if m == nil || m["voided"] == true {
				continue
			}
			add(m["answer_files"])
			add(m["files"])
		}
	}
	return ids
}

func dropEmpty(m map[string]any) {
	for k, v := range m {
		switch x := v.(type) {
		case string:
			if x == "" {
				delete(m, k)
			}
		case []string:
			if len(x) == 0 {
				delete(m, k)
			}
		}
	}
}
