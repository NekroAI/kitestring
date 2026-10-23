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

	"github.com/NekroAI/kitestring/internal/blocks"
	"github.com/NekroAI/kitestring/internal/client"
	"github.com/NekroAI/kitestring/internal/protocol"
	"github.com/NekroAI/kitestring/internal/version"
	"github.com/NekroAI/kitestring/internal/writing"
)

const instructions = `Kitestring 是用户的多 agent 驾驶舱，用户通过它统一查看需求、回答决策。
- 会话身份：用户说“接管 PM / 做开发（Dev）/ 当顾问（Adv）/ 当审查员（Rev）”之类的话时，运行 shell 命令 ks role pm|dev|advisor|reviewer，按它输出的身份提示词工作。
- 开始一项工作前，用 req_list 看当前项目已有需求；开始做某条需求时用 session_link_req 关联本会话（草稿 / 待开始的需求会自动变为进行中）。
- 用户提出的新需求、你发现必须做的事，用 req_create 记下来，避免遗漏。你记下的需求是草稿，用户确认（改为待开始）后才会排进会话的待办；会话空闲时，排给你的待开始需求会整批送达。
- 需要用户拍板的问题用 decision_raise 提出（附选项与推荐），不要只埋在对话里。用户在 Kitestring 中作答后，答复会在用户下一次发消息时自动注入你的上下文。
- 开发完成后把需求状态设为 review（待验收）并用 evidence_attach 附上证据；done 只能由用户验收后设置。截图类证据务必附上 images。
- 写给用户读的文字（证据、阶段小结、决策）：中文、短句、先结论，不要写成技术日志。具体写法见 evidence_attach、stage_submit、decision_raise 的说明；超过字数阈值时照常保存，但返回里会提示你压缩。
- 表达块：这三个工具都可以附 blocks（验收清单、前后对比、截图、流程图、指标、改动范围、选项、小表格），纸鸢统一画出来。能用块就不写长文：验收证据给 verdict 一句结论加 checklist 逐条判定，界面类附截图或前后对比，流程类附流程图。提到需求、计划、决策时写编号（R-12 / P-3 / D-40），纸鸢会画成印记。
- 设计类需求（kind = design）只是标注，由你按情况判断：界面 / 交互有几种做法、需要用户拍板时，用 decision_raise 交 2～3 套方案并在 option_images 附上每套的效果图，用户选定后再实现；做法明确时可以直接做。用户明确说“出方案 / 先设计”时先交方案。
- 只有一个方案 / 计划需要用户点头时，用 decision_raise 的 approval = true（可在 images 附方案图）：用户会批准或拒绝，并可能附言，按附言调整。
- 计划：用户给出较大的目标时，可用 plan_create 拆成有顺序的阶段（阶段内可放多条需求），用户批准后才开工。计划开始或阶段解锁时，该阶段的需求会交给你；阶段内需求都做完（待验收）后，用 stage_submit 交阶段小结，用户验收整个阶段后下一阶段自动交给你。plan_get 查看计划进度。
- 做完一段、需要用户验收或答复才能继续时，可以用 shell 命令 ks wait 挂起等待：用户在 Kitestring 上答复、验收或留言后它会输出内容并退出，你按输出继续；超时则结束本轮。只在确实等用户时使用，不要用它做定时或自动续跑。
  - Claude Code：在后台运行（默认最长等 1 小时，可用 --timeout 调整，最长 12h；后台任务的超时要设得比它长），它结束时你会被唤醒。
  - Codex：没有“后台命令结束后唤醒”的机制，要在前台运行 ks wait（自动最多等 30 分钟，不用加 --timeout；shell 工具的超时设成 35 分钟以上）。命令还在运行时继续等它结束，不要中断、不要重复运行。超时时它会提示你收尾：照做，用一两句话说明进度和在等什么，然后结束本轮，不要再运行 ks wait 续等。前台等待期间这一轮一直开着，Codex 会定期查看命令输出，每次消耗少量额度（主要是缓存读取），所以最多 30 分钟。`

// Run 以 stdio 运行 MCP server。
func Run(ctx context.Context) error {
	s := mcp.NewServer(&mcp.Implementation{Name: "kitestring", Version: version.Version}, &mcp.ServerOptions{Instructions: instructions})
	t := &tools{c: client.New(20 * time.Second)}
	t.register(s)
	t.registerTeam(s)
	t.registerReview(s)
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

type reqGetIn struct {
	Ref  string `json:"ref" jsonschema:"需求编号（如 R-12）或 ID"`
	Full bool   `json:"full,omitempty" jsonschema:"为 true 时返回完整 JSON（含全部历史与元数据），并附上证据截图；默认只给要点"`
}

type reqCreateIn struct {
	Title      string   `json:"title" jsonschema:"需求标题，一句话；不要把编号（R-12、R-95-D 之类）写进标题，纸鸢会自己显示编号，开头的编号前缀会被去掉"`
	Detail     string   `json:"detail,omitempty" jsonschema:"背景与细节"`
	Acceptance string   `json:"acceptance,omitempty" jsonschema:"验收标准"`
	Status     string   `json:"status,omitempty" jsonschema:"初始状态，默认 draft（草稿，用户确认后才会排进会话的待办）；你当场就在做的用 in_progress"`
	Priority   string   `json:"priority,omitempty" jsonschema:"P0 到 P3，默认 P2"`
	Source     string   `json:"source,omitempty" jsonschema:"来源，如：用户对话、issue 链接、截图"`
	DocLink    string   `json:"doc_link,omitempty" jsonschema:"仓库内方案文档路径或链接"`
	Kind       string   `json:"kind,omitempty" jsonschema:"需求类型：design = 设计类（先交方案、用户选定后再实现），不填为普通需求"`
	Images     []string `json:"images,omitempty" jsonschema:"本机图片文件路径（PNG/JPEG/GIF/WebP，默认单张不超过 10 MB，上限可在 Hub 设置里调），如截图"`
}

type reqStatusIn struct {
	Ref    string `json:"ref" jsonschema:"需求编号（如 R-12）或 ID"`
	Status string `json:"status" jsonschema:"新状态：draft in_progress blocked review shelved（ready 待开始与 done 已完成只能由用户设置）"`
}

type evidenceIn struct {
	Ref     string           `json:"ref" jsonschema:"需求编号（如 R-12）或 ID"`
	Kind    string           `json:"kind" jsonschema:"证据类型：test、commit、screenshot、doc、manual 等"`
	Verdict string           `json:"verdict,omitempty" jsonschema:"一句结论，40 字以内，写用户能看到的变化。纸鸢用大字放在最上面"`
	Blocks  []map[string]any `json:"blocks,omitempty" jsonschema:"表达块（推荐，验收证据至少给一个 checklist），类型和字段见工具说明"`
	Todo    []string         `json:"todo,omitempty" jsonschema:"要你做的：只写机器验证不了、要用户动手或拍板的事，每条一句；没有就不写"`
	Details string           `json:"details,omitempty" jsonschema:"实现细节（改了哪些文件、测试命令），默认折叠"`
	Aside   []string         `json:"aside,omitempty" jsonschema:"和本需求无关的顺带改动，每条一句，默认折叠"`
	Summary string           `json:"summary,omitempty" jsonschema:"证据正文（可用 Markdown）。给了 verdict 和 blocks 时可以不写，纸鸢会拼出纯文本；只写文字时按顺序写：1. 第一句是结论，40 字以内，写用户能看到的变化（例如：登录页支持“记住我”，重开浏览器不用再登录）；2. 逐条对照验收标准，每条写“通过 / 未通过 / 需你确认”和一句依据，标上对应截图（如：图 1）；3. 你需要做的：只写机器验证不了、要用户动手或拍板的事，没有就不写；4. 风险或副作用（可选）；5. 实现细节放最后（改了哪些文件、测试命令）；和本需求无关的顺带改动单列，或另记需求。全文建议 600 字以内"`
	Link    string           `json:"link,omitempty" jsonschema:"链接或文件路径"`
	Images  []string         `json:"images,omitempty" jsonschema:"本机截图等图片文件路径（PNG/JPEG/GIF/WebP，默认单张不超过 10 MB，上限可在 Hub 设置里调），用户验收时会直接看到。已经放进 figure / compare 块的图不用再列"`
	Videos  []string         `json:"videos,omitempty" jsonschema:"本机录屏文件路径（MP4/WebM/MOV，默认单个不超过 50 MB，上限可在 Hub 设置里调）。只在用户明确要求录屏或视频时才附加，平时用截图"`
}

type decisionIn struct {
	Question       string           `json:"question" jsonschema:"需要用户决策的问题：一句话，只问一件事，单独成立、无需上下文也能看懂（建议 60 字以内）"`
	Context        string           `json:"context,omitempty" jsonschema:"说明，按“结论 / 依据 / 细节”三段写：结论是你的判断，依据是为什么，细节放最后。不要在这里埋别的待拍板的点，那些另开决策。建议 400 字以内"`
	Options        []string         `json:"options,omitempty" jsonschema:"候选选项"`
	OptionImages   []string         `json:"option_images,omitempty" jsonschema:"每个选项的效果图（本机图片路径，与 options 一一对应，没有的留空字符串）。比较设计方案时用，用户会看图点选"`
	Approval       bool             `json:"approval,omitempty" jsonschema:"请用户批准一个方案 / 计划时设为 true：选项固定为“批准 / 拒绝”，用户两种都可以附言"`
	Images         []string         `json:"images,omitempty" jsonschema:"问题附带的本机图片路径（例如待批准的方案图）"`
	Recommendation string           `json:"recommendation,omitempty" jsonschema:"你的推荐及理由"`
	Requirement    string           `json:"requirement,omitempty" jsonschema:"关联需求编号（如 R-12）"`
	Blocks         []map[string]any `json:"blocks,omitempty" jsonschema:"表达块（可选），放在说明下面帮用户看懂：方案对比用 table 或 compare，流程用 flow；类型和字段见工具说明"`
	Points         []map[string]any `json:"points,omitempty" jsonschema:"要拍板的点（可选）：一个方案里有几个点要用户分别选时用它，[{question: 一句话问题, options: [{label, desc: 一句说明, recommended: true/false, image: 本机效果图路径}]}]，每点 2～4 个选项、标出推荐，用户逐点选择，答复会逐点注入。和 approval = true 一起用就是“按以下选择批准 / 拒绝”。不用再在 options 里重复"`
	Summary        []string         `json:"summary,omitempty" jsonschema:"方案要点（可选），每条一行、以需求编号开头，如“R-82 新命令 ks role，提示词压缩后由 hook 补发”；有它时完整说明（context）会折叠起来"`
}

type planReqIn struct {
	Ref        string `json:"ref,omitempty" jsonschema:"已有需求的编号（如 R-12）；给了就不用填其它字段"`
	Title      string `json:"title,omitempty" jsonschema:"新需求标题（不给 ref 时必填），一句话说清一个具体改动；不要把编号写进标题"`
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
	Plan    string           `json:"plan" jsonschema:"计划编号（如 P-1）或 ID"`
	Stage   int              `json:"stage" jsonschema:"阶段序号（从 1 开始），只能是计划的当前阶段"`
	Summary string           `json:"summary" jsonschema:"阶段小结，用户阶段验收时看：只写跨需求的整体效果、遗留问题和你需要做的（机器验证不了、要用户动手或拍板的事）。不要重复各条需求的证据，那些用户在需求里看"`
	Blocks  []map[string]any `json:"blocks,omitempty" jsonschema:"表达块（可选），例如整体指标 metrics、跨需求的流程 flow；类型和字段见工具说明"`
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
			var out reqListView
			if err := t.c.Do(ctx, "GET", "/agent/requirements?"+q.Encode(), nil, &out); err != nil {
				return nil, nil, err
			}
			return text(out.String()), nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "req_get", Description: "读取一条需求：说明、验收标准、关联会话、证据要点，以及用户的修改意见与决策（描述和意见里的附图以图片形式返回）。需要完整历史与元数据时传 full = true。"},
		func(ctx context.Context, req *mcp.CallToolRequest, in reqGetIn) (*mcp.CallToolResult, any, error) {
			var raw json.RawMessage
			if err := t.c.Do(ctx, "GET", "/agent/requirements/"+url.PathEscape(in.Ref), nil, &raw); err != nil {
				return nil, nil, err
			}
			var res *mcp.CallToolResult
			var ids []string
			if in.Full {
				var out map[string]any
				_ = json.Unmarshal(raw, &out)
				body, _ := json.MarshalIndent(out, "", "  ")
				res, ids = text(string(body)), imageIDs(out)
			} else {
				var v reqView
				if err := json.Unmarshal(raw, &v); err != nil {
					return nil, nil, err
				}
				res, ids = text(v.String()), v.images()
			}
			for _, id := range ids {
				if data, mime, err := t.c.Fetch(ctx, "/agent/files/"+url.PathEscape(id), 10<<20); err == nil {
					res.Content = append(res.Content, &mcp.ImageContent{Data: data, MIMEType: mime})
				}
			}
			return res, nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "req_create", Description: "新建需求并自动关联当前会话。用户提出的新需求、你发现必须做的事都应记录，避免遗漏。同项目已有同名未关闭需求时照常新建，但返回里会提示，重复的请搁置。"},
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
			var out reqView
			if err := t.c.Do(ctx, "POST", "/agent/requirements", body, &out); err != nil {
				return nil, nil, err
			}
			msg := "已记下 " + out.line()
			if out.Status == "draft" {
				msg += "（用户确认后才排进待办）"
			}
			for _, n := range out.Notices {
				msg += "\n注意：" + n
			}
			return text(msg), nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "req_update_status", Description: "更新需求状态并关联当前会话。开发完成设为 review（待验收），done 只能由用户验收后设置。"},
		func(ctx context.Context, req *mcp.CallToolRequest, in reqStatusIn) (*mcp.CallToolResult, any, error) {
			body := map[string]any{"status": in.Status, "session_key": t.sessionKey(ctx, req)}
			dropEmpty(body)
			var out reqView
			if err := t.c.Do(ctx, "POST", "/agent/requirements/"+url.PathEscape(in.Ref)+"/status", body, &out); err != nil {
				return nil, nil, err
			}
			msg := fmt.Sprintf("%s「%s」→ %s", out.Code, out.Title, zh(out.Status))
			if out.Status == "review" {
				msg += fmt.Sprintf("（证据 %d 条）", out.evidenceCount())
			}
			return text(msg), nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "evidence_attach", Description: "为需求附加验收证据，用户验收时读它。推荐结构化地交：verdict 一句结论（40 字以内，写用户能看到的变化）；blocks 里放一个 checklist，逐条对照验收标准给 pass / fail / ask 和一句依据，用 see 指向证明它的块；界面类附 figure 或 compare，流程类附 flow；todo 只写机器验证不了的事，没有就不写；实现细节放 details、顺带改动放 aside，两者默认折叠。也可以只写 summary（按同样顺序写成文字）。交之前先自查截图里的文字有没有被截断、溢出或重叠。超过字数阈值照常保存，返回里会提示压缩。\n" + writing.BlocksGuide},
		func(ctx context.Context, req *mcp.CallToolRequest, in evidenceIn) (*mcp.CallToolResult, any, error) {
			body := map[string]any{"kind": in.Kind, "summary": in.Summary, "link": in.Link, "session_key": t.sessionKey(ctx, req), "verdict": in.Verdict, "details": in.Details}
			dropEmpty(body)
			if len(in.Todo) > 0 {
				body["todo"] = in.Todo
			}
			if len(in.Aside) > 0 {
				body["aside"] = in.Aside
			}
			cache := map[string]string{}
			ids, err := t.uploadCached(ctx, append(in.Images, in.Videos...), cache)
			if err != nil {
				return nil, nil, err
			}
			if len(ids) > 0 {
				body["files"] = ids
			}
			if err := t.addBlocks(ctx, body, in.Blocks, cache); err != nil {
				return nil, nil, err
			}
			var out struct {
				reqView
				BlockWarnings []string `json:"block_warnings"`
			}
			if err := t.c.Do(ctx, "POST", "/agent/requirements/"+url.PathEscape(in.Ref)+"/evidence", body, &out); err != nil {
				return nil, nil, err
			}
			msg := fmt.Sprintf("%s 已附证据（共 %d 条）", out.Code, out.evidenceCount())
			if len(ids) > 0 {
				msg += fmt.Sprintf("，本次附件 %d 个", len(ids))
			}
			msg += "；状态：" + zh(out.Status)
			msg += blockWarnings(out.BlockWarnings)
			if in.Summary != "" {
				if h := writing.EvidenceHint(in.Summary); h != "" {
					msg += "\n" + h
				}
			}
			if h := writing.PayloadHint(in.Verdict, hasChecklist(in.Blocks)); h != "" {
				msg += "\n" + h
			}
			return text(msg), nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "decision_raise", Description: "向用户提出一个需要拍板的决策，进入用户的决策收件箱并推送通知。只用于要用户回答的问题；要告诉 PM 的事用 pm_notify，不要发成决策。写法：问题一句话，每次只问一件事；互不相关的问题拆成几个决策，不要埋在说明里；说明按“结论 / 依据 / 细节”写，能用表达块（blocks）说清的就不写长文。一个方案里有几个点要用户分别拍板时，用 points 逐点给选项（每点标出推荐），不要把它们埋在说明里；方案较长时用 summary 每条需求一行，完整说明会被折叠。要用户回答才能推进的问题，先用它发到面板再等，终端里的话用户不一定看得到。超过字数阈值照常保存，返回里会提示压缩。用户作答后，答复会在用户下一次发消息时自动注入本会话上下文；在此之前可以继续推进不受影响的工作。\n" + writing.BlocksGuide},
		func(ctx context.Context, req *mcp.CallToolRequest, in decisionIn) (*mcp.CallToolResult, any, error) {
			body := map[string]any{"question": in.Question, "context": in.Context, "options": in.Options, "recommendation": in.Recommendation, "requirement": in.Requirement, "session_key": t.sessionKey(ctx, req)}
			dropEmpty(body)
			if in.Approval {
				body["approval"] = true
			}
			cache := map[string]string{}
			if len(in.Images) > 0 {
				ids, err := t.uploadCached(ctx, in.Images, cache)
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
					up, err := t.uploadCached(ctx, []string{p}, cache)
					if err != nil {
						return nil, nil, err
					}
					ids[i] = up[0]
				}
				body["option_images"] = ids
			}
			if err := t.addBlocks(ctx, body, in.Blocks, cache); err != nil {
				return nil, nil, err
			}
			if len(in.Points) > 0 {
				raw := blocks.FromMaps(in.Points)
				if err := blocks.Localize(raw, cache, func(p string) (string, error) {
					ids, err := t.upload(ctx, []string{p})
					if err != nil {
						return "", err
					}
					return ids[0], nil
				}); err != nil {
					return nil, nil, err
				}
				body["points"] = raw
			}
			if len(in.Summary) > 0 {
				body["summary"] = in.Summary
			}
			var out struct {
				Code          string   `json:"code"`
				BlockWarnings []string `json:"block_warnings"`
			}
			if err := t.c.Do(ctx, "POST", "/agent/decisions", body, &out); err != nil {
				return nil, nil, err
			}
			msg := fmt.Sprintf("%v 已进入用户的决策收件箱，作答后会自动注入本会话", out.Code)
			msg += blockWarnings(out.BlockWarnings)
			if h := writing.DecisionHint(in.Question, in.Context); h != "" {
				msg += "\n" + h
			}
			return text(msg), nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "session_link_req", Description: "开始做某条需求时调用：把当前会话关联到这条需求，草稿 / 待开始的需求会自动变为进行中，让用户在看板上看到你在做什么。"},
		func(ctx context.Context, req *mcp.CallToolRequest, in linkIn) (*mcp.CallToolResult, any, error) {
			key := t.sessionKey(ctx, req)
			if key == "" {
				return nil, nil, fmt.Errorf("无法识别当前会话")
			}
			var out reqView
			if err := t.c.Do(ctx, "POST", "/agent/sessions/"+url.PathEscape(key)+"/link", map[string]any{"requirement": in.Requirement}, &out); err != nil {
				return nil, nil, err
			}
			return text("已关联 " + out.line()), nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "plan_create", Description: "把较大的目标拆成计划：按顺序的阶段，阶段内可放多条需求（已有或新建；新建的与同项目未关闭需求同名时，直接关联已有需求并在返回里说明，已有的请用 ref 引用）。提交后进入用户的“待批准”，批准后第一阶段的需求会交给本会话。"},
		func(ctx context.Context, req *mcp.CallToolRequest, in planCreateIn) (*mcp.CallToolResult, any, error) {
			body := map[string]any{"title": in.Title, "goal": in.Goal, "stages": in.Stages, "session_key": t.sessionKey(ctx, req)}
			dropEmpty(body)
			var out planView
			if err := t.c.Do(ctx, "POST", "/agent/plans", body, &out); err != nil {
				return nil, nil, err
			}
			msg := out.Code + " 已提交，等待用户批准；批准后第一阶段的需求会交给你。\n" + out.String()
			for _, n := range out.Notices {
				msg += "\n注意：" + n
			}
			return text(msg), nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "plan_get", Description: "查看计划进度：各阶段状态、阶段内需求及其状态、阶段小结。"},
		func(ctx context.Context, req *mcp.CallToolRequest, in planRefIn) (*mcp.CallToolResult, any, error) {
			var out planView
			if err := t.c.Do(ctx, "GET", "/agent/plans/"+url.PathEscape(in.Plan), nil, &out); err != nil {
				return nil, nil, err
			}
			return text(out.String()), nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "stage_submit", Description: "计划当前阶段的需求都做完（待验收）后调用：交阶段小结，阶段进入“待阶段验收”。小结只写跨需求的整体效果、遗留问题和你需要做的，不重复各条需求的证据，可附表达块（blocks）。用户通过后下一阶段会自动交给你；退回会附意见送达。\n" + writing.BlocksGuide},
		func(ctx context.Context, req *mcp.CallToolRequest, in stageSubmitIn) (*mcp.CallToolResult, any, error) {
			body := map[string]any{"summary": in.Summary, "session_key": t.sessionKey(ctx, req)}
			if err := t.addBlocks(ctx, body, in.Blocks, map[string]string{}); err != nil {
				return nil, nil, err
			}
			var out struct {
				planView
				BlockWarnings []string `json:"block_warnings"`
			}
			path := fmt.Sprintf("/agent/plans/%s/stages/%d/submit", url.PathEscape(in.Plan), in.Stage)
			if err := t.c.Do(ctx, "POST", path, body, &out); err != nil {
				return nil, nil, err
			}
			return text(fmt.Sprintf("%s 阶段 %d 已交小结，等待用户验收。", out.Code, in.Stage) + blockWarnings(out.BlockWarnings)), nil, nil
		})
}

func text(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

// planView 是计划的精简视图：按行输出，省上下文。
type planView struct {
	Notices []string `json:"notices"`
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
// uploadCached 同 upload，但同一路径只传一次（images 与表达块里的图共用）。
func (t *tools) uploadCached(ctx context.Context, paths []string, cache map[string]string) ([]string, error) {
	var ids []string
	for _, p := range paths {
		if id, ok := cache[p]; ok {
			ids = append(ids, id)
			continue
		}
		up, err := t.upload(ctx, []string{p})
		if err != nil {
			return nil, err
		}
		cache[p] = up[0]
		ids = append(ids, up[0])
	}
	return ids, nil
}

// addBlocks 把表达块里的本机图片上传后放进请求体（R-97）。
func (t *tools) addBlocks(ctx context.Context, body map[string]any, in []map[string]any, cache map[string]string) error {
	if len(in) == 0 {
		return nil
	}
	raw := blocks.FromMaps(in)
	if err := blocks.Localize(raw, cache, func(p string) (string, error) {
		ids, err := t.upload(ctx, []string{p})
		if err != nil {
			return "", err
		}
		return ids[0], nil
	}); err != nil {
		return err
	}
	body["blocks"] = raw
	return nil
}

// blockWarnings 把 hub 退回的块提示拼进工具返回。
func blockWarnings(w []string) string {
	if len(w) == 0 {
		return ""
	}
	return "\n有块没有保存（其余内容已保存）：\n- " + strings.Join(w, "\n- ")
}

func hasChecklist(in []map[string]any) bool {
	for _, b := range in {
		if s, _ := b["type"].(string); strings.EqualFold(s, blocks.Checklist) {
			return true
		}
	}
	return false
}

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
