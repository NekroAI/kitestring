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
	"github.com/NekroAI/kitestring/internal/roles"
	"github.com/NekroAI/kitestring/internal/version"
	"github.com/NekroAI/kitestring/internal/writing"
)

// instructions 是 MCP 说明；{{ROLES}} 换成各身份的职责与别称（R-190，来自 roles.Team）。
var instructions = strings.Replace(instructionsTmpl, "{{ROLES}}", roles.Team(), 1)

// dshInstructionsNote 放在 DSH 会话的说明最前面：DSH 的待命由 Host 插件负责，
// 与 Claude / Codex 自己挂 ks wait 的方式不同，先说清楚再让模型读通用说明。
const dshInstructionsNote = `你在 DeepSeek Harness（DSH）中运行，纸鸢 Host 插件负责待命：这一轮结束后插件会自动保持在线，用户在纸鸢上答复、验收或派工时，会在同一个会话里唤醒你。不要运行 ks wait，也不要为了等用户而让回合保持不结束；下面关于 ks wait 的说明对 DSH 不适用。

`

// instructionsFor 按调用方返回 MCP 说明。client 为空表示 Claude / Codex 等自挂等待的客户端。
func instructionsFor(client string) string {
	if client == "dsh" {
		return dshInstructionsNote + instructions
	}
	return instructions
}

const instructionsTmpl = `Kitestring 是用户的多 agent 驾驶舱，用户通过它统一查看需求、回答决策。
- 会话身份：用户让你担任某个身份时（如“接管 PM”“当参谋”“做主助理”“当顾问”“做开发”“当评审”），运行 shell 命令 ks role aide|advisor|reviewer|pm|dev（也认中文名和别称，如 ks role 参谋），按它输出的身份提示词工作。各身份的职责与别称：
{{ROLES}}
- 开始一项工作前，用 req_list 看当前项目已有需求；开始做某条需求时用 session_link_req 关联本会话（待开始的需求会自动变为进行中；草稿只有主调度 / 主助理关联、且项目开了「允许主调度 / 主助理放行草稿」才会记一次放行并变为进行中；开发、顾问等其他身份或没开时只关联、仍是草稿，要请主调度或用户放行）。
- 用户提出的新需求、你发现必须做的事，用 req_create 记下来，避免遗漏；用户在对话里提的每条需求、反馈都要记。你记下的需求是草稿，用户确认（改为待开始）后才会排进会话的待办；会话空闲时，排给你的待开始需求会整批送达。
- 需要用户决策的问题用 decision_raise 提出（附选项与推荐），不要只埋在对话里。提错了或要修订，用 decision_withdraw 撤回（可用 replaced_by 指向新决策），不要并排再挂一条。用户在 Kitestring 中作答后，答复会在用户下一次发消息时自动注入你的上下文。
- 决策后果链：按用户在某条决策里的答复新建需求时，req_create 带 from_decision（D-x）；接着某条决策往下问、或要推翻它的结论时，decision_raise 带 from_decision（推翻时再加 overturns = true）。
- 开发完成后把需求状态设为 review（待验收）并用 evidence_attach 附上证据；done 只能由用户验收后设置。截图类证据务必附上 images。界面类证据的截图用真实数据，改到证据 / 决策显示时新旧两种格式都要截图。
- 写给用户读的文字（证据、阶段小结、决策）：中文、短句、先结论，不要写成技术日志。具体写法见 evidence_attach、stage_submit、decision_raise 的说明；超过字数阈值时照常保存，但返回里会提示你压缩。
- 会话之间：细节用 req_comment 写进需求留言，team_tell（旧名 pm_notify）的消息只放编号加一句话请对方去看；本项目有哪些会话、谁现在能立即送达，用 team_sessions（ks team sessions）查。点对点用 team_tell（to 填身份或会话键），好几个会话都要知道用 team_broadcast（要动手的广播只有主调度、主助理能发，别人用 fyi = true 只告知）；发出后用 team_sent 看已送达 / 待送达 / 无法送达。关联或在做的需求默认订阅，有变动时收到一句话提醒（自己引起的不通知）；别的需求要跟进用 req_watch 订阅，不想收用 req_watch off = true 退订，req_watching 看订阅了哪些。
- 表达块：这三个工具都可以附 blocks（验收清单、前后对比、截图、流程图、指标、改动范围、选项、小表格），纸鸢统一画出来。能用块就不写长文：验收证据给 verdict 一句结论加 checklist 逐条判定，界面类附截图或前后对比，流程类附流程图。提到需求、计划、决策时写编号（R-12 / P-3 / D-40），纸鸢会画成印记。
- 设计类需求（kind = design）只是标注，由你按情况判断：界面 / 交互有几种做法、需要用户决策时，用 decision_raise 交 2～3 套方案并在 option_images 附上每套的效果图（要试交互的用 option_prototypes 附可交互原型），用户选定后再实现；做法明确时可以直接做。用户明确说“出方案 / 先设计”时先交方案。
- 只有一个方案 / 计划需要用户点头时，用 decision_raise 的 approval = true（可在 images 附方案图）：用户会批准或拒绝，并可能附言，按附言调整。
- 第三方凭据（API Token、账户 ID 等）：需要时用 secret_request（或 ks secret request）申请，写清用途、关联需求、建议有效期、允许的命令，以及每个值的获取方式（按服务写清步骤，指导用户快速取到）；用户在纸鸢上填值并授权后结果送到你这里。之后用 shell 命令 ks secret run <凭据> -- <命令> 运行，值只作为这条命令的环境变量出现；secret_list 看本项目能用的凭据。不要让用户在终端里输入或粘贴凭据，不要打印、回显值或写进文件（确需落盘时申请里说明，用户单独授权后用 ks secret write）。
- 计划：用户给出较大的目标时，可用 plan_create 拆成有顺序的阶段（阶段内可放多条需求），用户批准后才开始。计划开始或阶段解锁时，该阶段的需求会指派给你；阶段内需求都做完（待验收）后，用 stage_submit 交阶段小结，用户验收整个阶段后下一阶段自动指派给你。计划被用户退回（附意见）时用 plan_update 修改并重新提交。plan_get 查看计划进度。
- 做完一段、需要用户验收或答复才能继续时，可以用 shell 命令 ks wait 挂起等待：用户在 Kitestring 上答复、验收或留言后它会输出内容并退出，你按输出继续；超时则结束本轮。只在确实等用户时使用，不要用它做定时或自动续跑。
  - Claude Code：在后台运行（默认、也最多等 55 分钟，醒来时提示缓存还有效；项目设置可调得更短，--timeout 超过上限按上限截断；后台任务的超时要设得比它长）。主调度 / 主助理 / 开发连续空等累计到项目设置的时长（默认 4 小时）时，醒来会收到写交接下线的指示：handoff_write 写交接后结束本轮，不再挂 ks wait，它结束时你会被唤醒。
  - Codex：没有“后台命令结束后唤醒”的机制，要在前台运行 ks wait（自动最多等 30 分钟，不用加 --timeout；shell 工具的超时设成 35 分钟以上）。命令还在运行时继续等它结束，不要中断、不要重复运行。超时时它会提示你收尾：照做，用一两句话说明进度和在等什么，然后结束本轮，不要再运行 ks wait 续等。前台等待期间这一轮一直开着，Codex 会定期查看命令输出，每次消耗少量额度（主要是缓存读取），所以最多 30 分钟。
  - 订阅事件（--on）：主助理 / 主调度挂 ks wait 时，还会在本项目别的会话提决策（decision）、交验收（review）、把需求设为受阻（blocked）、交阶段小结（stage）时被唤醒，收到一句说明；用户在网页上改项目设置（放行开关、自动派发、授权窗口）时是 settings，改需求状态时是 status（status 之外，指派需求的主调度和关联在线会话本来就会收到，不用订阅）。不加 --on 沿用上次，从没设过按身份默认：主助理订阅 decision,review,settings，主调度订阅 review,blocked,settings，顾问、开发等其它身份不订阅；例：ks wait --on decision,review,blocked，--on none 不订阅，--on default 恢复默认。这只是告诉你，不是指派；不进用户收件箱、不推送。`

// Run 以 stdio 运行 MCP server。clientName 是调用方（目前只区分 dsh），影响呈现给模型的说明。
func Run(ctx context.Context, clientName string) error {
	s := mcp.NewServer(&mcp.Implementation{Name: "kitestring", Version: version.Version}, &mcp.ServerOptions{Instructions: instructionsFor(clientName)})
	t := &tools{c: client.New(20 * time.Second)}
	t.register(s)
	t.registerTeam(s)
	t.registerReview(s)
	t.registerCommits(s)
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
	if key := metaSessionKey(req); key != "" {
		return key
	}
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

// caller 和 sessionKey 一样识别会话，顺带返回发起调用的子任务 agent_id（主线程为空，R-191）。
func (t *tools) caller(ctx context.Context, req *mcp.CallToolRequest) (key, agent string) {
	if key := metaSessionKey(req); key != "" {
		return key, metaAgentID(req)
	}
	if req.Params != nil && req.Params.Meta != nil {
		if id, ok := req.Params.Meta["claudecode/toolUseId"].(string); ok && id != "" {
			var out struct {
				SessionKey string `json:"session_key"`
				AgentID    string `json:"agent_id"`
			}
			if t.c.Do(ctx, "GET", "/session?tool_use_id="+url.QueryEscape(id), nil, &out) == nil && out.SessionKey != "" {
				return out.SessionKey, out.AgentID
			}
		}
	}
	return sessionKeyFromMeta(req), ""
}

// metaSessionKey 读调用方插件直接注入的完整会话键（如 dsh:session-xxx）。
// DSH 的 MCP 桥由 Host 插件持有，没有 hook 进程可对齐，按每次调用带身份最准确（D17 的扩展）。
func metaSessionKey(req *mcp.CallToolRequest) string {
	if req.Params == nil || req.Params.Meta == nil {
		return ""
	}
	key, _ := req.Params.Meta["kitestring/session"].(string)
	return strings.TrimSpace(key)
}

// metaAgentID 读调用方插件带上的子任务标识（主线程为空）。
func metaAgentID(req *mcp.CallToolRequest) string {
	if req.Params == nil || req.Params.Meta == nil {
		return ""
	}
	id, _ := req.Params.Meta["kitestring/agent"].(string)
	return strings.TrimSpace(id)
}

func sessionKeyFromMeta(req *mcp.CallToolRequest) string {
	if key := metaSessionKey(req); key != "" {
		return key
	}
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
	Title        string   `json:"title" jsonschema:"需求标题，一句话；不要把编号（R-12、R-95-D 之类）写进标题，纸鸢会自己显示编号，开头的编号前缀会被去掉"`
	Detail       string   `json:"detail,omitempty" jsonschema:"背景与细节"`
	Acceptance   string   `json:"acceptance,omitempty" jsonschema:"验收标准"`
	Status       string   `json:"status,omitempty" jsonschema:"初始状态，默认 draft（草稿，用户确认后才会排进会话的待办）；你当场就在做的用 in_progress"`
	Priority     string   `json:"priority,omitempty" jsonschema:"P0 到 P3，默认 P2"`
	Source       string   `json:"source,omitempty" jsonschema:"来源，如：用户对话、issue 链接、截图"`
	DocLink      string   `json:"doc_link,omitempty" jsonschema:"仓库内方案文档路径或链接"`
	Kind         string   `json:"kind,omitempty" jsonschema:"需求类型：design = 设计类（先交方案、用户选定后再实现），不填为普通需求"`
	FromDecision string   `json:"from_decision,omitempty" jsonschema:"产生这条需求的决策编号（如 D-142）：用户在某条决策里作答后由此新建的需求要填，决策详情里会显示它产生了这条需求"`
	Images       []string `json:"images,omitempty" jsonschema:"本机图片文件路径（PNG/JPEG/GIF/WebP，默认单张不超过 10 MB，上限可在 Hub 设置里调），如截图"`
}

type reqStatusIn struct {
	Ref    string `json:"ref" jsonschema:"需求编号（如 R-12）或 ID"`
	Status string `json:"status" jsonschema:"新状态：draft in_progress blocked review shelved；ready（放行）只有主调度 / 主助理在项目开了放行时能设，且必须写 reason（没开放行时用 req_request_status 申请用户批准）；done 只能由用户验收后设置"`
	Reason string `json:"reason,omitempty" jsonschema:"设为 ready（放行草稿）时必填：一句理由，说明为什么做法明确、可以直接排期；设为 shelved（搁置）时必填：一句话说明为什么搁置；改回草稿（撤回放行）时可写原因"`
	// ReasonKind 是搁置原因的类别（R-157）
	ReasonKind string `json:"reason_kind,omitempty" jsonschema:"搁置时的原因类别：duplicate 重复 / changed 方向变了 / dropped 不做了 / other 其他（默认）"`
	// WaitingOn：受阻时在等什么（R-243）
	WaitingOn string `json:"waiting_on,omitempty" jsonschema:"设为 blocked 时在等的需求或决策编号（R-x / D-x）。等别的需求定稿 / 验收 / 做完、等某条决策答复时写上：顺着等待关系落到用户身上的那一条会在「待处理」写明「这条在等它」。不写时这条受阻需求本身进用户的「待处理」；reason 写一句在等什么"`
}

type reqRequestStatusIn struct {
	Ref    string `json:"ref" jsonschema:"需求编号（如 R-12）或 ID"`
	To     string `json:"to" jsonschema:"目标状态：ready（草稿 / 已搁置 → 待开始）或 draft（已搁置 → 草稿）；其它状态不能申请"`
	Reason string `json:"reason" jsonschema:"一句理由：为什么现在可以改，用户在审批里看到"`
}

type secretFieldArg struct {
	Env   string `json:"env" jsonschema:"环境变量名，例如 CLOUDFLARE_API_TOKEN"`
	Label string `json:"label,omitempty" jsonschema:"显示名，例如 API Token"`
	How   string `json:"how" jsonschema:"获取方式：按服务写清步骤（在哪个页面、点什么、选什么权限 / 模板），指导用户快速取到"`
}

type secretRequestIn struct {
	Name        string           `json:"name" jsonschema:"凭据名，例如「Cloudflare 部署」；ks secret run 按它取用。已有同名凭据时是对它重新申请（补值、续期、扩大范围）"`
	Purpose     string           `json:"purpose" jsonschema:"用途：拿它做什么，用户在申请里看到"`
	Fields      []secretFieldArg `json:"fields" jsonschema:"要的每个值；一条凭据可以含多个值（如 API Token 和 Account ID），一起授权、一次取用"`
	Requirement string           `json:"requirement,omitempty" jsonschema:"关联需求 R-x"`
	Days        *int             `json:"days,omitempty" jsonschema:"建议的有效期（天），默认 7；0 = 长期"`
	Allowed     []string         `json:"allowed,omitempty" jsonschema:"建议允许的命令，如 \"wrangler pages deploy *\"（* 单独放最后 = 后面可跟任意参数，放在中间匹配一个参数）；不写 = 不限命令。范围越小越容易获批"`
}

type reqEditIn struct {
	Ref          string  `json:"ref" jsonschema:"需求编号（如 R-12）或 ID"`
	Title        *string `json:"title,omitempty" jsonschema:"新标题；不要把编号写进标题，开头的编号前缀会被去掉；不传表示不改"`
	Detail       *string `json:"detail,omitempty" jsonschema:"新描述（整段替换）；不传表示不改"`
	Acceptance   *string `json:"acceptance,omitempty" jsonschema:"新验收标准（整段替换）；需求进行中 / 受阻 / 待验收时要同时给 approval；不传表示不改"`
	Priority     *string `json:"priority,omitempty" jsonschema:"新优先级 P0 到 P3；不传表示不改"`
	Kind         *string `json:"kind,omitempty" jsonschema:"新类型：design = 设计类，feature = 普通；不传表示不改"`
	Reason       string  `json:"reason,omitempty" jsonschema:"为什么改，一句话，会显示在时间线上"`
	Approval     string  `json:"approval,omitempty" jsonschema:"进行中之后改验收标准时必填：这条需求上用户已批准的批准类决策编号（D-x，用 decision_raise approval = true 发起）"`
	FromDecision string  `json:"from_decision,omitempty" jsonschema:"这次修改是按哪条决策的答复改的（D-x）：时间线上会连在那条决策下面，用户看得到「因为这条决策改了验收标准」；带了 approval 时默认就是它"`
}

type evidenceIn struct {
	Ref        string           `json:"ref" jsonschema:"需求编号（如 R-12）或 ID"`
	Kind       string           `json:"kind" jsonschema:"证据类型：test、commit、screenshot、doc、manual 等"`
	Verdict    string           `json:"verdict,omitempty" jsonschema:"一句结论，40 字以内，写用户能看到的变化。纸鸢用大字放在最上面"`
	Blocks     []map[string]any `json:"blocks,omitempty" jsonschema:"表达块（推荐，验收证据至少给一个 checklist），类型和字段见工具说明"`
	Todo       []string         `json:"todo,omitempty" jsonschema:"要你做的：只写机器验证不了、要用户动手或决策的事，每条一句；没有就不写"`
	Details    string           `json:"details,omitempty" jsonschema:"实现细节（改了哪些文件、测试命令），默认折叠"`
	Aside      []string         `json:"aside,omitempty" jsonschema:"和本需求无关的顺带改动，每条一句，默认折叠"`
	Summary    string           `json:"summary,omitempty" jsonschema:"证据正文（可用 Markdown）。给了 verdict 和 blocks 时可以不写，纸鸢会拼出纯文本；只写文字时按顺序写：1. 第一句是结论，40 字以内，写用户能看到的变化（例如：登录页支持“记住我”，重开浏览器不用再登录）；2. 逐条对照验收标准，每条写“通过 / 未通过 / 需你确认”和一句依据，标上对应截图（如：图 1）；3. 你需要做的：只写机器验证不了、要用户动手或决策的事，没有就不写；4. 风险或副作用（可选）；5. 实现细节放最后（改了哪些文件、测试命令）；和本需求无关的顺带改动单列，或另记需求。全文建议 600 字以内"`
	Link       string           `json:"link,omitempty" jsonschema:"链接或文件路径"`
	Images     []any            `json:"images,omitempty" jsonschema:"截图（PNG/JPEG/GIF/WebP，默认单张不超过 10 MB，上限可在 Hub 设置里调），用户验收时按这里的顺序全部显示：重点看的放前面，补充、测试一类的放后面。每项写成 {path: 本机路径, title: 一句话标题（20 字以内，如：工作台 · 带鱼屏 · 没有待办时）, boxes: [{x, y, w, h, label: 一句标签, criterion: 对应第几条验收点（可选）}]}；boxes 用矩形框标出这次改动的地方，坐标是图片原始尺寸的比例（0～1，左上角为原点），看图时画在图上、可一键隐藏；写了 criterion 的框会裁成小图放在那条验收点旁边。只写路径字符串也行，但网页只能用文件名当标题。已经放进 figure / compare 块的图不用再列"`
	Prototypes []any            `json:"prototypes,omitempty" jsonschema:"可交互原型（R-197）：Agent 做的 HTML 原型 / 效果图页面，用户在纸鸢里直接打开（隔离运行，读不到纸鸢的接口和登录态）。每项写成 {path: 本机目录、单个 .html 或 .zip, title: 一句话标题, cover: 封面截图路径（推荐，没有时显示占位图）, entry: 入口页面（不写时找 index.html）, version_of: 原型 ID（可选）}，只写路径也行。同一需求下同标题重交即为新版本，用户能切历史版本。默认单个包不超过 20 MB、500 个文件，交之前可用 ks proto check 自检"`
	Videos     []string         `json:"videos,omitempty" jsonschema:"本机录屏文件路径（MP4/WebM/MOV，默认单个不超过 50 MB，上限可在 Hub 设置里调）。只在用户明确要求录屏或视频时才附加，平时用截图"`
}

type evidenceVoidIn struct {
	Evidence string `json:"evidence" jsonschema:"要作废的证据编号：evidence_attach 返回里的证据 id，或 req_get 证据条目里的 id"`
	Reason   string `json:"reason" jsonschema:"作废理由，一句话（必填，显示在需求上，例如：字数超标，已重交）"`
}

type decisionWithdrawIn struct {
	Decision   string `json:"decision" jsonschema:"要撤回的决策编号，如 D-135"`
	Reason     string `json:"reason,omitempty" jsonschema:"撤回原因，一句话（显示在需求时间线上）"`
	ReplacedBy string `json:"replaced_by,omitempty" jsonschema:"取代它的新决策编号，如 D-141；写了之后旧决策卡片会带跳转到新决策的链接"`
}

type decisionIn struct {
	Question          string           `json:"question" jsonschema:"需要用户决策的问题：一句话，只问一件事，脱离对话也能读懂——用户没看过你们的对话，不要写「刚才那个」「按上面说的」，把要定的事和它会带来什么写进这一句（建议 60 字以内）"`
	Context           string           `json:"context,omitempty" jsonschema:"说明，按“结论 / 依据 / 细节”三段写：结论是你的判断，依据是为什么，细节放最后。不要在这里埋别的要决策的点，那些另开决策。建议 400 字以内"`
	Options           []string         `json:"options,omitempty" jsonschema:"候选选项"`
	OptionImages      []string         `json:"option_images,omitempty" jsonschema:"每个选项的效果图（本机图片路径，与 options 一一对应，没有的留空字符串）。比较设计方案时用，用户会看图点选"`
	OptionPrototypes  []any            `json:"option_prototypes,omitempty" jsonschema:"每个选项的可交互原型（R-197），与 options 一一对应，没有的留空字符串；写法同 prototypes。比较交互方案时用，用户点开每套原型试用后再选。要决策的点里的选项也可以写 prototype"`
	Prototypes        []any            `json:"prototypes,omitempty" jsonschema:"问题附带的可交互原型（例如待批准方案的原型），写法同 evidence_attach 的 prototypes"`
	Approval          bool             `json:"approval,omitempty" jsonschema:"请用户批准一个方案 / 计划时设为 true：选项固定为“批准 / 拒绝”，用户两种都可以附言"`
	Images            []string         `json:"images,omitempty" jsonschema:"问题附带的本机图片路径（例如待批准的方案图）"`
	Recommendation    string           `json:"recommendation,omitempty" jsonschema:"你的推荐及理由"`
	RecommendedOption int              `json:"recommended_option,omitempty" jsonschema:"推荐的是第几个选项（从 1 开始，对应 options）；界面上这个选项标「推荐」，用户没选它时会问一句为什么。不填时只有 recommendation 与某个选项文字完全相同才标"`
	Requirement       string           `json:"requirement,omitempty" jsonschema:"关联需求编号（如 R-12）：问题是哪条需求推进中冒出来的就写哪条，用户会在这条需求的时间线上看着来龙去脉作答。不写时本会话只关联一条没完成的需求就自动挂上；和在做的需求无关的临时问题（如「做完这些要不要上线」）写 none"`
	Blocks            []map[string]any `json:"blocks,omitempty" jsonschema:"表达块（可选），放在说明下面帮用户看懂：方案对比用 table 或 compare，流程用 flow；类型和字段见工具说明"`
	Points            []map[string]any `json:"points,omitempty" jsonschema:"要决策的点（可选）：一个方案里有几个点要用户分别选时用它，[{question: 一句话问题, options: [{label, desc: 一句说明, recommended: true/false, image: 本机效果图路径, prototype: 可交互原型（写法同 prototypes，可选）}]}]，每点 2～4 个选项、标出推荐，用户逐点选择，答复会逐点注入。和 approval = true 一起用就是“按以下选择批准 / 拒绝”。不用再在 options 里重复"`
	Summary           []string         `json:"summary,omitempty" jsonschema:"方案要点（可选），每条一行、以需求编号开头，如“R-82 新命令 ks role，提示词压缩后由 hook 补发”；有它时完整说明（context）会折叠起来"`
	FromDecision      string           `json:"from_decision,omitempty" jsonschema:"来源决策（如 D-142）：这条是接着哪条决策往下问的，或要推翻哪条决策的结论；决策详情里会连成后果链"`
	Overturns         bool             `json:"overturns,omitempty" jsonschema:"为 true 表示这条要推翻 from_decision 那条的结论（例如用户之前选了 B，现在要改回 A）；接着往下问时不填"`
	Risk              string           `json:"risk,omitempty" jsonschema:"风险档：low / medium / high，不填按 high。产品方向、设计选型、语义 / 接口 / 权限变更、不可逆或对外操作一律 high。用户开了授权窗口时，low / medium 的决策可能由主助理代答（用户可推翻，推翻时会送来更正）"`
	Hard              string           `json:"hard,omitempty" jsonschema:"硬规则类别（可选）：属于其中一类就标上，标了主助理永不代答、风险档按 high。product 产品方向（做不做、做成什么样、给谁用）；design 设计选型（界面 / 交互取哪套）；semantics 语义 / 接口变更（字段含义、对外接口、数据格式）；permission 权限变更；gate 门禁 / 评测（测试、评测、审批规则的口径）；irreversible 不可逆或对外操作（删除数据、发布、推送到远端、花钱、对外发消息）；acceptance 验收"`
}

type decisionAnswerIn struct {
	Decision  string `json:"decision" jsonschema:"要代答的决策编号，如 D-135"`
	Answer    string `json:"answer,omitempty" jsonschema:"答复；决策有要决策的点（points）时是附言，可空"`
	Choices   []int  `json:"choices,omitempty" jsonschema:"决策有要决策的点时，逐点选第几个选项（从 1 开始）"`
	Reason    string `json:"reason" jsonschema:"为什么这样答、为什么不必等用户（必填，显示在用户的代答记录里）"`
	Risk      string `json:"risk" jsonschema:"你自己的风险判断：low / medium / high（必填）。取它和提问方标的中更高的一档，超出用户授权的范围就会被拒"`
	Verdict   string `json:"verdict,omitempty" jsonschema:"批准类决策（选项是批准 / 拒绝）必填：approved 批准 / rejected 拒绝，记成字段，放行检查只认它"`
	HardCheck string `json:"hard_check" jsonschema:"硬规则核对（必填）：none = 核对过，不属于 product 产品方向 / design 设计选型 / semantics 语义或接口变更 / permission 权限变更 / gate 门禁或评测 / irreversible 不可逆或对外操作 / acceptance 验收 中任何一类；属于其中一类就不要代答，留给用户"`
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

type planUpdateIn struct {
	Plan   string        `json:"plan" jsonschema:"被用户退回的计划编号（如 P-1）"`
	Title  string        `json:"title,omitempty" jsonschema:"新标题；不给表示不改"`
	Goal   *string       `json:"goal,omitempty" jsonschema:"新目标；不给表示不改"`
	Stages []planStageIn `json:"stages,omitempty" jsonschema:"改后的完整阶段列表；不给表示不改阶段。会整体按它重排，没出现的需求会移出计划"`
}

type planRefIn struct {
	Plan string `json:"plan" jsonschema:"计划编号（如 P-1）或 ID"`
}

type stageSubmitIn struct {
	Plan    string           `json:"plan" jsonschema:"计划编号（如 P-1）或 ID"`
	Stage   int              `json:"stage" jsonschema:"阶段序号（从 1 开始），只能是计划的当前阶段"`
	Summary string           `json:"summary" jsonschema:"阶段小结，用户阶段验收时看：只写跨需求的整体效果、遗留问题和你需要做的（机器验证不了、要用户动手或决策的事）。不要重复各条需求的证据，那些用户在需求里看"`
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
	mcp.AddTool(s, &mcp.Tool{Name: "req_get", Description: "读取一条需求：说明、验收标准、关联会话、证据要点、最新 3 条会话留言（req_comment 写的），以及用户的修改意见与决策（描述和意见里的附图以图片形式返回；用户附的文件会下载到本机，列出本机路径，直接读即可）。需要完整历史与元数据（含全部会话留言）时传 full = true。"},
		func(ctx context.Context, req *mcp.CallToolRequest, in reqGetIn) (*mcp.CallToolResult, any, error) {
			var raw json.RawMessage
			if err := t.c.Do(ctx, "GET", "/agent/requirements/"+url.PathEscape(in.Ref), nil, &raw); err != nil {
				return nil, nil, err
			}
			// 用户附的文件下载到本机，每个附件带上 path（R-221）
			var doc any
			if json.Unmarshal(raw, &doc) == nil {
				t.c.LocalizeFiles(ctx, doc)
				if b, err := json.Marshal(doc); err == nil {
					raw = b
				}
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
			// 关联的提交、回滚与 CI（R-156），以及被回滚时该怎么做的提示
			if c := t.reqCommitsText(ctx, in.Ref); c != "" {
				res.Content = append(res.Content, &mcp.TextContent{Text: c})
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
			body := map[string]any{"title": in.Title, "detail": in.Detail, "acceptance": in.Acceptance, "status": in.Status, "priority": in.Priority, "source": in.Source, "doc_link": in.DocLink, "kind": in.Kind, "from_decision": in.FromDecision, "session_key": t.sessionKey(ctx, req)}
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
	mcp.AddTool(s, &mcp.Tool{Name: "decision_withdraw", Description: "撤回自己提的、用户还没答复的决策：提错了、要修订时用，不要再提一条并在标题写“请忽略上一条”。撤回后从用户面板消失，在需求时间线留一条“已撤回”；修订时先 decision_raise 发新的，再用 replaced_by 指向它。只能撤回本会话提的；用户已答复的不能撤回。replaced_by 指向的决策必须仍待用户答复：已关闭 / 已撤回 / 已答复、不存在或会形成回环（互相取代）都会被拒绝，先确认再撤回。"},
		func(ctx context.Context, req *mcp.CallToolRequest, in decisionWithdrawIn) (*mcp.CallToolResult, any, error) {
			body := map[string]any{"reason": in.Reason, "replaced_by": in.ReplacedBy, "session_key": t.sessionKey(ctx, req)}
			dropEmpty(body)
			var out struct {
				Code       string `json:"code"`
				ReplacedBy string `json:"replaced_by"`
			}
			if err := t.c.Do(ctx, "POST", "/agent/decisions/"+url.PathEscape(in.Decision)+"/withdraw", body, &out); err != nil {
				return nil, nil, err
			}
			msg := "已撤回 " + out.Code
			if out.ReplacedBy != "" {
				msg += "，由 " + out.ReplacedBy + " 取代"
			}
			return text(msg), nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "req_update_status", Description: "更新需求状态并关联当前会话。开发完成设为 review（待验收），done 只能由用户验收后设置。等别的需求（定稿 / 验收 / 做完）或某条决策时设为 blocked 并用 waiting_on 指向它（R-243），不要一直标进行中干等。放行：主调度 / 主助理可以把草稿设为 ready（待开始），必须写 reason，时间线记下谁放行、为什么，用户可一键撤回；项目设置没打开「允许主调度 / 主助理放行草稿」时会被拒绝，开发、顾问不能放行。搁置（shelved）必须写 reason（一句话说明为什么搁置，可用 reason_kind 标 duplicate / changed / dropped / other），状态历史和时间线会记下。"},
		func(ctx context.Context, req *mcp.CallToolRequest, in reqStatusIn) (*mcp.CallToolResult, any, error) {
			body := map[string]any{"status": in.Status, "session_key": t.sessionKey(ctx, req), "reason": in.Reason, "reason_kind": in.ReasonKind, "waiting_on": in.WaitingOn}
			dropEmpty(body)
			var out reqView
			if err := t.c.Do(ctx, "POST", "/agent/requirements/"+url.PathEscape(in.Ref)+"/status", body, &out); err != nil {
				return nil, nil, err
			}
			msg := fmt.Sprintf("%s「%s」→ %s", out.Code, out.Title, zh(out.Status))
			if out.Status == "review" {
				msg += fmt.Sprintf("（证据 %d 条）", out.evidenceCount())
			}
			if out.Status == "blocked" {
				if out.BlockedOn != "" {
					msg += "（在等 " + out.BlockedOn + "）"
				} else {
					msg += "。没写在等什么（waiting_on），这条会进用户的「待处理」；等的是别的需求或决策时，用 waiting_on 指向它"
				}
			}
			return text(msg), nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "req_request_status", Description: "申请改需求状态（R-188）：要用户确认某条需求时用它——把草稿改为待开始，或重新打开已搁置的需求（已搁置 → 待开始 / 草稿）。写一句 reason。申请出现在用户的「待处理」，用户在审批抽屉里批准后由纸鸢直接改状态（不用你再改），时间线记成「用户批准了 Agent 的申请」；拒绝、或批准前状态已被改动而自动作废，结果都会送到你这里。不推送、不限时；同一条需求已有在等的申请时不重复建。不要再在对话里写「请在面板上把 R-x 设为待开始」。项目开了「允许主调度 / 主助理放行草稿」时，PM / Adv 对做法明确的草稿仍可直接用 req_update_status 放行，不用申请。"},
		func(ctx context.Context, req *mcp.CallToolRequest, in reqRequestStatusIn) (*mcp.CallToolResult, any, error) {
			key := t.sessionKey(ctx, req)
			if key == "" {
				return nil, nil, fmt.Errorf("无法识别当前会话")
			}
			var out struct {
				Message string `json:"message"`
			}
			if err := t.c.Do(ctx, "POST", "/agent/requirements/"+url.PathEscape(in.Ref)+"/status-request", map[string]any{"session_key": key, "to": in.To, "reason": in.Reason}, &out); err != nil {
				return nil, nil, err
			}
			return text(out.Message), nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "secret_request", Description: "申请第三方凭据（R-295）：要 API Token 之类的凭据时用它，不要让用户在终端里输入或粘贴。写清用途、关联需求、建议有效期、允许的命令，以及每个值的获取方式（按服务写清步骤，指导用户快速取到）。申请出现在用户的「待处理」，用户在网页上填值、可改项目 / 有效期 / 允许的命令后保存并授权，或拒绝；结果送到你这里。授权后用 shell 命令 ks secret run <凭据名> -- <命令> 运行：值只作为这条命令的环境变量出现，命令不在允许范围内、授权过期或被撤销时会被拒绝。不要打印、回显值或写进文件。已有等待中的申请或凭据已可用时不重复建。"},
		func(ctx context.Context, req *mcp.CallToolRequest, in secretRequestIn) (*mcp.CallToolResult, any, error) {
			key := t.sessionKey(ctx, req)
			if key == "" {
				return nil, nil, fmt.Errorf("无法识别当前会话")
			}
			var out struct {
				Message string `json:"message"`
			}
			body := map[string]any{"session_key": key, "name": in.Name, "purpose": in.Purpose, "fields": in.Fields, "requirement": in.Requirement, "allowed": in.Allowed}
			if in.Days != nil {
				body["days"] = *in.Days
			}
			if err := t.c.Do(ctx, "POST", "/agent/secrets/request", body, &out); err != nil {
				return nil, nil, err
			}
			return text(out.Message), nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "secret_list", Description: "本项目能用的凭据（R-295）：名字、状态（active 生效中 / pending 待填写 / expired 已失效 / revoked 已撤回）、每个值的环境变量名、允许的命令、是否允许写入磁盘、到期时间。没有值：取用用 shell 命令 ks secret run <凭据名> -- <命令>。"},
		func(ctx context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
			key := t.sessionKey(ctx, req)
			if key == "" {
				return nil, nil, fmt.Errorf("无法识别当前会话")
			}
			var out any
			if err := t.c.Do(ctx, "GET", "/agent/secrets?session_key="+url.QueryEscape(key), nil, &out); err != nil {
				return nil, nil, err
			}
			b, _ := json.MarshalIndent(out, "", "  ")
			return text(string(b)), nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "req_edit", Description: "改需求的标题、描述、验收标准、优先级或类型（只改给出的字段）。需求随讨论演进时在原需求上改，不要另建一条取代。谁能改：关联了这条需求的会话（创建者、在做的会话）和项目主调度 / 主助理 / 顾问；没关联的会话（包括 Dev）会被拒绝；已完成、已搁置的需求不能改。需求进行中 / 受阻 / 待验收时改验收标准，要先用 decision_raise（approval = true）请用户批准，再把批准的决策编号填进 approval。每次修改都在时间线留下改前改后与你写的 reason，用户能看到是谁改的；有别的会话在做时会通知它。"},
		func(ctx context.Context, req *mcp.CallToolRequest, in reqEditIn) (*mcp.CallToolResult, any, error) {
			body := map[string]any{"session_key": t.sessionKey(ctx, req), "reason": in.Reason, "approval": in.Approval, "from_decision": in.FromDecision}
			dropEmpty(body)
			for k, v := range map[string]*string{"title": in.Title, "detail": in.Detail, "acceptance": in.Acceptance, "priority": in.Priority, "kind": in.Kind} {
				if v != nil {
					body[k] = *v
				}
			}
			var out reqView
			if err := t.c.Do(ctx, "POST", "/agent/requirements/"+url.PathEscape(in.Ref)+"/edit", body, &out); err != nil {
				return nil, nil, err
			}
			msg := "已修改 " + out.line()
			for _, f := range out.Feedback {
				if f.Kind == "edit" {
					msg += "；" + f.Answer + "（" + f.Code + "，已记在时间线）"
					break
				}
			}
			return text(msg), nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "evidence_attach", Description: "为需求附加验收证据，用户验收时读它。推荐结构化地交：verdict 一句结论（40 字以内，写用户能看到的变化）；blocks 里放一个 checklist，逐条对照验收标准给 pass / fail / ask 和一句依据，用 see 指向证明它的块；界面类附 figure 或 compare，流程类附 flow；截图（images、figure、compare）每张写一句话标题（title），改动的地方用 boxes 矩形框标出来，图按顺序全部显示：重点看的放前面，补充、测试一类的放后面；todo 只写机器验证不了的事，没有就不写；实现细节放 details、顺带改动放 aside，两者默认折叠。也可以只写 summary（按同样顺序写成文字）。交之前先自查截图里的文字有没有被截断、溢出或重叠。交错的证据用 evidence_void 作废后重交，不要堆着（返回里有证据 id）。做了可交互原型（HTML 页面）时用 prototypes 附上，用户在纸鸢里直接打开试用。" + writing.UIEvidenceGuide + "超过字数阈值照常保存，返回里会提示压缩。\n" + writing.BlocksGuide},
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
			ids, err := t.uploadCached(ctx, in.Videos, cache)
			if err != nil {
				return nil, nil, err
			}
			if len(ids) > 0 {
				body["files"] = ids
			}
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
				ids = append(ids, make([]string, len(in.Images))...)
			}
			if err := t.addBlocks(ctx, body, in.Blocks, cache); err != nil {
				return nil, nil, err
			}
			var notes []string
			if pids, err := t.protos(ctx, in.Prototypes, in.Ref, body["session_key"], false, &notes); err != nil {
				return nil, nil, err
			} else if len(pids) > 0 {
				body["prototypes"] = pids
			}
			var out struct {
				reqView
				EvidenceID    string   `json:"evidence_id"`
				BlockWarnings []string `json:"block_warnings"`
				ImageHints    []string `json:"image_hints"`
			}
			if err := t.c.Do(ctx, "POST", "/agent/requirements/"+url.PathEscape(in.Ref)+"/evidence", body, &out); err != nil {
				return nil, nil, err
			}
			msg := fmt.Sprintf("%s 已附证据（共 %d 条）", out.Code, out.evidenceCount())
			if len(ids) > 0 {
				msg += fmt.Sprintf("，本次附件 %d 个", len(ids))
			}
			msg += "；状态：" + zh(out.Status)
			if out.EvidenceID != "" {
				msg += "\n证据 id：" + out.EvidenceID + "（交错了用 evidence_void 作废后重交）"
			}
			msg += protoNotes(notes)
			msg += blockWarnings(out.BlockWarnings)
			msg += imageHints(out.ImageHints)
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
	mcp.AddTool(s, &mcp.Tool{Name: "evidence_void", Description: "作废一条交错的证据（格式不对、字数超标、误交、被新证据取代）。交错的证据用它作废后重交，不要堆着。只有交这条证据的会话，或本项目的主调度 / 主助理能作废；必须写理由。作废的证据在需求上折叠成一行「已作废 · 理由」，用户可展开看原文，记录谁、什么时候、为什么作废；不再计入证据条数。"},
		func(ctx context.Context, req *mcp.CallToolRequest, in evidenceVoidIn) (*mcp.CallToolResult, any, error) {
			var out reqView
			if err := t.c.Do(ctx, "POST", "/agent/evidence/"+url.PathEscape(in.Evidence)+"/void", map[string]any{"session_key": t.sessionKey(ctx, req), "reason": in.Reason}, &out); err != nil {
				return nil, nil, err
			}
			return text(fmt.Sprintf("%s 的证据 %s 已作废（还有效的证据 %d 条）", out.Code, in.Evidence, out.evidenceCount())), nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "decision_raise", Description: "向用户提出一个需要用户回答的决策，进入用户的决策收件箱并推送通知。只用于要用户回答的问题；要告诉 PM 的事用 pm_notify，不要发成决策。写法：问题一句话，每次只问一件事，脱离对话也能读懂（用户看不到你们的对话）；在做某条需求时带上 requirement，决策会挂在这条需求的时间线上，用户看着需求的来龙去脉作答；互不相关的问题拆成几个决策，不要埋在说明里；说明按“结论 / 依据 / 细节”写，能用表达块（blocks）说清的就不写长文。一个方案里有几个点要用户分别决策时，用 points 逐点给选项（每点标出推荐），不要把它们埋在说明里；方案较长时用 summary 每条需求一行，完整说明会被折叠。" + writing.DecisionTableGuide + "要用户回答才能推进的问题，先用它发到面板再等，终端里的话用户不一定看得到。超过字数阈值照常保存，返回里会提示压缩。用户作答后，答复会在用户下一次发消息时自动注入本会话上下文；在此之前可以继续推进不受影响的工作。\n" + writing.BlocksGuide},
		func(ctx context.Context, req *mcp.CallToolRequest, in decisionIn) (*mcp.CallToolResult, any, error) {
			body := map[string]any{"question": in.Question, "context": in.Context, "options": in.Options, "recommendation": in.Recommendation, "requirement": in.Requirement, "risk": in.Risk, "hard": in.Hard, "from_decision": in.FromDecision, "session_key": t.sessionKey(ctx, req)}
			dropEmpty(body)
			if in.Overturns {
				body["overturns"] = true
			}
			if in.Approval {
				body["approval"] = true
			}
			if in.RecommendedOption > 0 {
				body["recommended_option"] = in.RecommendedOption - 1
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
			var notes []string
			if pids, err := t.protos(ctx, in.Prototypes, in.Requirement, body["session_key"], false, &notes); err != nil {
				return nil, nil, err
			} else if len(pids) > 0 {
				body["prototypes"] = pids
			}
			if pids, err := t.protos(ctx, in.OptionPrototypes, in.Requirement, body["session_key"], true, &notes); err != nil {
				return nil, nil, err
			} else if len(pids) > 0 {
				body["option_prototypes"] = pids
			}
			if pts, ok := body["points"].([]any); ok {
				if err := t.c.PointPrototypes(ctx, pts, protoReq(in.Requirement), fmt.Sprint(body["session_key"]), &notes); err != nil {
					return nil, nil, err
				}
			}
			var out struct {
				Code            string   `json:"code"`
				RequirementCode string   `json:"requirement_code"`
				AutoLinked      bool     `json:"auto_linked"`
				BlockWarnings   []string `json:"block_warnings"`
			}
			if err := t.c.Do(ctx, "POST", "/agent/decisions", body, &out); err != nil {
				return nil, nil, err
			}
			msg := fmt.Sprintf("%v 已进入用户的决策收件箱，作答后会自动注入本会话", out.Code)
			msg += protoNotes(notes)
			if out.AutoLinked && out.RequirementCode != "" {
				msg += fmt.Sprintf("\n本会话只在做 %s，已自动挂到它的时间线上；不是关于它的，撤回后重提并写明 requirement。", out.RequirementCode)
			}
			msg += blockWarnings(out.BlockWarnings)
			if h := writing.DecisionHint(in.Question, in.Context); h != "" {
				msg += "\n" + h
			}
			return text(msg), nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "decision_answer", Description: "主助理（Aide，也叫参谋）用：用户开了授权窗口时，在到期前代用户答复本项目范围内的决策。必须写 reason、你自己的风险判断 risk（取它和提问方标的中更高的一档，超出窗口范围就拒绝）和硬规则核对 hard_check（none = 不属于任何硬规则类别）。硬规则永不代答，接口会拒绝：批准方案 / 计划、设计选型（带效果图或标了 design）、产品方向、语义 / 接口 / 权限变更、门禁 / 评测、不可逆或对外操作、验收，以及你自己提的决策；问题或选项里提到产品方向、语义、接口、权限、门禁、评测、不可逆、删除、发布、推送到远端、花钱等的也不能代答（算拿不准）。拿不准的留给用户，要用户尽快看的用 away_escalate 推给用户。代答会留痕，用户可推翻，推翻时更正送回提问方。没有窗口、窗口已到期 / 撤销、或你不是被授权的 Adv 时会被拒绝。"},
		func(ctx context.Context, req *mcp.CallToolRequest, in decisionAnswerIn) (*mcp.CallToolResult, any, error) {
			body := map[string]any{"session_key": t.sessionKey(ctx, req), "answer": in.Answer, "reason": in.Reason, "risk": in.Risk, "hard_check": in.HardCheck}
			if in.Verdict != "" {
				body["verdict"] = in.Verdict
			}
			if len(in.Choices) > 0 {
				cs := make([]int, len(in.Choices))
				for i, c := range in.Choices {
					cs[i] = c - 1
				}
				body["choices"] = cs
			}
			var out struct {
				Code string `json:"code"`
			}
			if err := t.c.Do(ctx, "POST", "/agent/decisions/"+url.PathEscape(in.Decision)+"/answer", body, &out); err != nil {
				return nil, nil, err
			}
			return text(out.Code + " 已代用户答复并送给提问方；用户能在代答记录里看到，可能推翻"), nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "session_link_req", Description: "开始做某条需求时调用：把当前会话关联到这条需求，待开始的需求会自动变为进行中，让用户在看板上看到你在做什么。草稿：只有主调度 / 主助理关联、且项目开了「允许主调度 / 主助理放行草稿」时记一次放行（理由「开始做 R-x」，用户可撤回）再变为进行中；Dev 等其他身份关联草稿、或没开放行时只关联、仍是草稿，返回里会提示请 PM 或用户放行。"},
		func(ctx context.Context, req *mcp.CallToolRequest, in linkIn) (*mcp.CallToolResult, any, error) {
			key := t.sessionKey(ctx, req)
			if key == "" {
				return nil, nil, fmt.Errorf("无法识别当前会话")
			}
			var out reqView
			if err := t.c.Do(ctx, "POST", "/agent/sessions/"+url.PathEscape(key)+"/link", map[string]any{"requirement": in.Requirement}, &out); err != nil {
				return nil, nil, err
			}
			msg := "已关联 " + out.line()
			for _, n := range out.Notices {
				msg += "\n" + n
			}
			return text(msg), nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "plan_create", Description: "把较大的目标拆成计划：按顺序的阶段，阶段内可放多条需求（已有或新建；新建的与同项目未关闭需求同名时，直接关联已有需求并在返回里说明，已有的请用 ref 引用）。提交后进入用户的“待批准”，批准后第一阶段的需求会指派给本会话。"},
		func(ctx context.Context, req *mcp.CallToolRequest, in planCreateIn) (*mcp.CallToolResult, any, error) {
			body := map[string]any{"title": in.Title, "goal": in.Goal, "stages": in.Stages, "session_key": t.sessionKey(ctx, req)}
			dropEmpty(body)
			var out planView
			if err := t.c.Do(ctx, "POST", "/agent/plans", body, &out); err != nil {
				return nil, nil, err
			}
			msg := out.Code + " 已提交，等待用户批准；批准后第一阶段的需求会指派给你。\n" + out.String()
			for _, n := range out.Notices {
				msg += "\n注意：" + n
			}
			return text(msg), nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "plan_update", Description: "用户退回了你的计划（附意见）后，按意见修改并重新提交同一个计划，不要另建新计划：计划回到用户的“待批准”。只能用于被退回的计划。"},
		func(ctx context.Context, req *mcp.CallToolRequest, in planUpdateIn) (*mcp.CallToolResult, any, error) {
			body := map[string]any{"title": in.Title, "goal": in.Goal, "stages": in.Stages, "session_key": t.sessionKey(ctx, req)}
			dropEmpty(body)
			var out planView
			if err := t.c.Do(ctx, "PUT", "/agent/plans/"+url.PathEscape(in.Plan), body, &out); err != nil {
				return nil, nil, err
			}
			msg := out.Code + " 已按意见修改并重新提交，等待用户批准。\n" + out.String()
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
	mcp.AddTool(s, &mcp.Tool{Name: "stage_submit", Description: "计划当前阶段的需求都做完（待验收）后调用：交阶段小结，阶段进入“待阶段验收”。小结只写跨需求的整体效果、遗留问题和你需要做的，不重复各条需求的证据，可附表达块（blocks）。用户通过后下一阶段会自动指派给你；退回会附意见送达。\n" + writing.BlocksGuide},
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
	Code    string   `json:"code"`
	Title   string   `json:"title"`
	Goal    string   `json:"goal"`
	Status  string   `json:"status"`
	Paused  bool     `json:"paused"`
	Stages  []struct {
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
	return "\n有块或改动框没有保存（其余内容已保存）：\n- " + strings.Join(w, "\n- ")
}

// imageHints 把 hub 退回的附图提示（没写标题、框写错，R-183）拼进工具返回。
func imageHints(w []string) string {
	if len(w) == 0 {
		return ""
	}
	return "\n附图提示（证据已保存）：\n- " + strings.Join(w, "\n- ")
}

func hasChecklist(in []map[string]any) bool {
	for _, b := range in {
		if s, _ := b["type"].(string); strings.EqualFold(s, blocks.Checklist) {
			return true
		}
	}
	return false
}

// protos 交原型包（R-197），返回原型 ID。aligned：与选项一一对应，空的留空字符串。
func (t *tools) protos(ctx context.Context, specs []any, requirement string, sessionKey any, aligned bool, notes *[]string) ([]string, error) {
	var ids []string
	key, _ := sessionKey.(string)
	for _, x := range specs {
		spec, ok := client.ParseProtoSpec(x)
		if !ok {
			if aligned {
				ids = append(ids, "")
			}
			continue
		}
		up, err := t.c.UploadPrototype(ctx, spec, protoReq(requirement), key)
		if err != nil {
			return nil, err
		}
		*notes = append(*notes, up.Note())
		ids = append(ids, up.ID)
	}
	return ids, nil
}

func protoReq(r string) string {
	if strings.EqualFold(strings.TrimSpace(r), "none") {
		return ""
	}
	return r
}

func protoNotes(notes []string) string {
	if len(notes) == 0 {
		return ""
	}
	return "\n已交" + strings.Join(notes, "；")
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
				if mime, _ := m["mime"].(string); mime != "" && !protocol.IsImage(mime) {
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
