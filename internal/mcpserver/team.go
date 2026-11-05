package mcpserver

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/NekroAI/kitestring/internal/summary"
	"github.com/NekroAI/kitestring/internal/writing"
)

// 工人池（R-86）：PM 用 team 查看本项目的开发会话，用 req_assign 把需求交给开发。

type worker struct {
	Key           string `json:"key"`
	Title         string `json:"title"`
	Tool          string `json:"tool"`
	Device        string `json:"device"`
	Presence      string `json:"presence"`
	ContextTokens int    `json:"context_tokens"`
	ContextMax    int    `json:"context_max"`
	Open          []struct {
		Code   string `json:"code"`
		Title  string `json:"title"`
		Status string `json:"status"`
	} `json:"open"`
}

// 常驻身份的在线状态，按 docs/copywriting.md 5.2（statusSets.presence）
var presenceLabel = map[string]string{"standby": "待命", "working": "运行中", "offline": "空闲"}

func (w worker) line() string {
	var b strings.Builder
	fmt.Fprintf(&b, "- %s「%s」%s · %s@%s", w.Key, clip(w.Title, 40), presenceLabel[w.Presence], w.Tool, w.Device)
	if w.ContextTokens > 0 {
		fmt.Fprintf(&b, " · 上下文 %dk", w.ContextTokens/1000)
	}
	if len(w.Open) == 0 {
		b.WriteString(" · 手上没有需求")
	} else {
		var rs []string
		for _, r := range w.Open {
			rs = append(rs, r.Code+"「"+clip(r.Title, 24)+"」")
		}
		b.WriteString(" · 手上：" + strings.Join(rs, "、"))
	}
	return b.String()
}

func clip(s string, n int) string { return summary.Truncate(strings.Join(strings.Fields(s), " "), n) }

type assignIn struct {
	Requirement string `json:"requirement" jsonschema:"需求编号（如 R-12）"`
	To          string `json:"to" jsonschema:"Dev 会话键（team 列出的 key）；填 auto 由 Kitestring 挑：待命优先，其次手上需求少、上下文轻"`
	Comment     string `json:"comment,omitempty" jsonschema:"交代给开发的话，例如为什么指派给它、和哪条需求相关"`
	Reason      string `json:"reason,omitempty" jsonschema:"指派的是草稿时必填：放行理由。派草稿会顺带把它放行为待开始，记进时间线、用户可撤回；项目没打开放行时派草稿会被拒绝"`
}

type needIn struct {
	Count  int    `json:"count" jsonschema:"推荐开几个 Dev 会话（1～5）；0 表示撤回在等批准的推荐"`
	Reason string `json:"reason,omitempty" jsonschema:"为什么要开：为什么要并行、为什么一次性子任务不合适；用户在审批里看到"`
}

type recommendIn struct {
	Count    int    `json:"count" jsonschema:"推荐开几个 Dev 会话（1～5）"`
	Reason   string `json:"reason" jsonschema:"为什么要开：为什么要并行、为什么一次性子任务不合适（例如几条需求要和用户多轮看效果、互不相干可以同时推进）"`
	Work     string `json:"work,omitempty" jsonschema:"开了做什么：指派给它们的需求（写编号）"`
	Duration string `json:"duration,omitempty" jsonschema:"预计用多久，例如「两小时」"`
	Minutes  int    `json:"minutes,omitempty" jsonschema:"批准时限（分钟）；不填用用户设置里的（默认 30）。到点没批按规则视为拒绝"`
}

type briefIn struct {
	Headline string   `json:"headline,omitempty" jsonschema:"一句话结论（建议 40 字以内）：工作台常驻条上只显示它，所以要自己读得通；不写时网页取简报第一句"`
	Look     []string `json:"look,omitempty" jsonschema:"要用户先看的几条（建议 3 条以内，每条一句，写编号），展开后排在最前"`
	Text     string   `json:"text" jsonschema:"简报：一段话（建议 200 字以内，超出照常保存并提示压缩），先结论——你离开期间完成了什么、现在在等你做什么、有什么风险；提到需求 / 计划 / 决策写编号（如 R-12），网页会画成印记。传空字符串清除"`
}

type handoffIn struct {
	Doing      string `json:"doing,omitempty" jsonschema:"做到一半的事：需求编号、做到哪、分支 / worktree 在哪"`
	Agreements string `json:"agreements,omitempty" jsonschema:"口头约定：用户在对话里交代、还没落成记录的（临时安排先用 arrange_add 记成当前安排）"`
	Caveats    string `json:"caveats,omitempty" jsonschema:"注意事项：坑、在跑的进程、别碰的东西"`
	Waiting    string `json:"waiting,omitempty" jsonschema:"旧栏目：等用户的。纸鸢里已有（接任概览会列出），不必写"`
	Rules      string `json:"rules,omitempty" jsonschema:"旧栏目：约定和规矩。纸鸢里已有，不必写；口头约定写 agreements"`
	Next       string `json:"next,omitempty" jsonschema:"旧栏目：下一步。不必写"`
	Notes      string `json:"notes,omitempty" jsonschema:"旧栏目：注意事项，等同 caveats"`
}

type arrangeAddIn struct {
	Text  string `json:"text" jsonschema:"安排：一两句话，写清要怎样（如「额度紧，做完 R-12、R-13 就停」「这次可以 push」）"`
	Until string `json:"until,omitempty" jsonschema:"到期：时长（30m、2h、1d）或日期（2026-11-02、2026-11-02 18:00、18:00）；不填表示撤销前一直有效"`
}

type arrangeListIn struct {
	All bool `json:"all,omitempty" jsonschema:"true = 连已到期、已撤销的也列出（最近 50 条）"`
}

type arrangeRevokeIn struct {
	Code string `json:"code" jsonschema:"要撤销的安排编号（N-x）"`
}

type morningAdviceIn struct {
	Requirement string `json:"requirement" jsonschema:"待验收的需求编号（如 R-12）"`
	Verdict     string `json:"verdict" jsonschema:"pass = 建议通过；return = 建议退回；clear = 撤掉建议"`
	Reason      string `json:"reason,omitempty" jsonschema:"理由（pass / return 必填）：对照验收标准一两句，例如「3/3 达标，截图明暗两套都有」或「缺手机截图」"`
}

type morningIn struct {
	Headline *string           `json:"headline,omitempty" jsonschema:"一句话结论（建议 40 字以内）：工作台常驻条上只显示它，所以要自己读得通；不传表示不改，传空字符串清除"`
	Look     []string          `json:"look,omitempty" jsonschema:"要用户先看的几条（建议 3 条以内，每条一句，写编号）；不传表示不改"`
	Text     *string           `json:"text,omitempty" jsonschema:"总述：一段话（建议 200 字以内，超出照常保存并提示压缩），先结论——用户离开期间整体推进到哪、你替用户定了几件、有什么要用户先看的；提到需求 / 决策写编号。不传表示不改，传空字符串清除"`
	Advice   []morningAdviceIn `json:"advice,omitempty" jsonschema:"逐条验收建议，每条覆盖该需求上一份；只能写给待验收的需求"`
}

type commentIn struct {
	Ref    string           `json:"ref" jsonschema:"需求编号（如 R-12）或 ID"`
	Text   string           `json:"text" jsonschema:"留言正文：先结论，再分条写细节（600 字以内，超出照常保存并提示压缩）；提到需求 / 决策写编号"`
	Images []string         `json:"images,omitempty" jsonschema:"附图的本机路径（PNG/JPEG/GIF/WebP），会自动上传；同 ks req comment --image"`
	Blocks []map[string]any `json:"blocks,omitempty" jsonschema:"表达块，字段同 evidence_attach（checklist / compare / figure / flow / metrics / scope / options / table）；同 ks req comment --blocks"`
}

type watchIn struct {
	Ref  string `json:"ref" jsonschema:"需求编号（如 R-12）或 ID"`
	Off  bool   `json:"off,omitempty" jsonschema:"true = 退订（同 ks req unwatch），含因关联而来的默认订阅；不填是订阅（同 ks req watch）"`
	Mode string `json:"mode,omitempty" jsonschema:"订阅档位：all（默认）两档都收；action 只收要动手的（验收标准被改、评审不通过、关联决策被作答、被退回或受阻），同 ks req watch --action-only"`
}

type watchingIn struct{}

type sessionsIn struct {
	Role        string `json:"role,omitempty" jsonschema:"只看某个身份：aide / advisor / reviewer / pm / dev（也认中文名和别称）；none 只看无身份的会话。同 ks team sessions --role"`
	AllProjects bool   `json:"all_projects,omitempty" jsonschema:"列所有项目的会话，不只本项目。同 --all-projects"`
	All         bool   `json:"all,omitempty" jsonschema:"也列已结束的会话（最近活跃的 50 个）。同 --all"`
}

type tellIn struct {
	Text        string `json:"text" jsonschema:"要说的话：编号加一句话（120 字以内，超出照常发送并提示），先结论（例如：R-12 卡在接口没定，先做了 R-13；问取舍时写清选项和你倾向哪个）；细节先用 req_comment 写进需求留言"`
	To          string `json:"to,omitempty" jsonschema:"收件身份：pm（主调度，默认）、aide（主助理）、reviewer（评审）、advisor（顾问）、dev（开发）；也可直接填会话键（team_sessions 里的 tool:id），没有身份的会话也能收。设计取舍问 aide，进度和受阻告诉 pm。回复时不填"`
	ToSession   string `json:"to_session,omitempty" jsonschema:"点名收件会话键（tool:id）：本项目有多个顾问 / 评审 / 开发时指定，没有身份的会话也行；不填发给该身份最近活跃的一个"`
	ReplyTo     string `json:"reply_to,omitempty" jsonschema:"回复收到的团队消息时填它的编号（D-x）：回复自动送回发消息的一方"`
	Requirement string `json:"requirement,omitempty" jsonschema:"相关需求编号（如 R-12），可省略"`
	FYI         bool   `json:"fyi,omitempty" jsonschema:"只告知（R-227）：对方不必动手，不唤醒它挂着的 ks wait，等它下次因别的事被叫醒或用户发消息时一并带上。部署 / 重启、进展通报填 true；提问、求裁定、指派、回复不填（立刻叫醒）"`
}

type broadcastIn struct {
	Text        string `json:"text" jsonschema:"要说的话：编号加一句话（120 字以内，超出照常发送并提示）；细节先用 req_comment 写进需求留言"`
	Roles       string `json:"roles,omitempty" jsonschema:"只发给这些身份，逗号分隔（如 dev,reviewer），也认中文名和别称；none 表示没身份的会话。不填发给全体。同 ks team broadcast --role"`
	Requirement string `json:"requirement,omitempty" jsonschema:"相关需求编号（如 R-12），可省略"`
	FYI         bool   `json:"fyi,omitempty" jsonschema:"只告知：不唤醒对方挂着的 ks wait，任何会话都能发。不填是「要动手」，只有主调度、主助理能发，别的身份会被拒绝"`
}

type sentIn struct {
	Ref   string `json:"ref,omitempty" jsonschema:"只看这一条（D-x；广播的任意一行会带出整次广播）。不填列最近 10 条"`
	Limit int    `json:"limit,omitempty" jsonschema:"最多列几条（一次广播算一条），默认 10"`
}

// tellDesc 是 team_tell（旧名 pm_notify）的说明。
const tellDesc = "点对点给本项目的团队成员发消息（命令行 ks team tell；旧名 pm_notify，参数相同）：按身份或会话键送给本项目没结束的那个会话。to = pm（主调度，默认）：进展、受阻、发现的问题、做完一批；to = aide（主助理）：设计取舍、拿不准的判断，没有主助理时退回给主调度并注明；to = reviewer（评审）：发给最近活跃的评审，没有时报错；to = advisor（顾问）：发给最近活跃的顾问（多个时可用 to_session 点名），没有在线顾问时报错退回；to = dev（开发）：发给最近活跃的开发；to 也可直接填会话键（team_sessions 里的 tool:id），没有身份的会话也能收，对方已结束或失联时报错退回。返回逐个收件人的送达状态：已送达 / 待送达（写明原因，如 Codex 要等用户下次发消息）/ 无法送达，之后用 team_sent 查。发送方永远是当前会话，不能指定。收到的团队消息要回复时填 reply_to（D-x），回复自动送回发消息的一方（子任务发的送到它所属的会话，并注明转给哪个子任务）。只送给 Agent（默认立刻叫醒挂着 ks wait 的对方；只是告知、对方不必动手的——部署 / 重启、进展通报——填 fyi = true，不唤醒，等对方下次被叫醒或用户发消息时一并带上），不进用户的收件箱；要用户决策的事仍用 decision_raise。消息只做通知：放编号加一句话（120 字以内，超出照常发送并提示），细节先用 req_comment 写进需求留言，再发消息请对方去看。需求设为待验收时主调度会自动收到通知，不必再发。项目没有主调度时发给 pm 的消息不会送出，但不算出错：会返回「本项目当前没有主调度，消息未送达」，不必重试。"

func (t *tools) registerTeam(s *mcp.Server) {
	tell := func(ctx context.Context, req *mcp.CallToolRequest, in tellIn) (*mcp.CallToolResult, any, error) {
		key, agent := t.caller(ctx, req)
		if key == "" {
			return nil, nil, fmt.Errorf("无法识别当前会话")
		}
		var out struct {
			Message string `json:"message"`
		}
		body := map[string]any{"session_key": key, "text": in.Text, "requirement": in.Requirement, "to": in.To, "to_session": in.ToSession, "reply_to": in.ReplyTo, "agent_id": agent}
		dropEmpty(body)
		if in.FYI {
			body["fyi"] = true
		}
		if err := t.c.Do(ctx, "POST", "/agent/team/tell", body, &out); err != nil {
			return nil, nil, err
		}
		return text(out.Message), nil, nil
	}
	mcp.AddTool(s, &mcp.Tool{Name: "team_tell", Description: tellDesc}, tell)
	mcp.AddTool(s, &mcp.Tool{Name: "pm_notify", Description: "（旧名，等同 team_tell，参数相同）" + tellDesc}, tell)
	mcp.AddTool(s, &mcp.Tool{Name: "team_broadcast", Description: "项目内广播（命令行 ks team broadcast，R-249）：发给本项目没结束的会话，不含你自己和后台任务会话；roles 按身份过滤。两档：fyi = true「只告知」任何会话都能发，不唤醒对方（部署 / 重启、进展通报）；不填是「要动手」，立刻叫醒对方，只有主调度、主助理能发，别的身份会被拒绝、请改用 fyi 或用 team_tell 点对点。离线的会话跳过。返回逐个收件人的送达状态（已送达 / 待送达及原因 / 无法送达）。消息只做通知：编号加一句话，细节先写进 req_comment。发送方永远是当前会话。"},
		func(ctx context.Context, req *mcp.CallToolRequest, in broadcastIn) (*mcp.CallToolResult, any, error) {
			key, agent := t.caller(ctx, req)
			if key == "" {
				return nil, nil, fmt.Errorf("无法识别当前会话")
			}
			body := map[string]any{"session_key": key, "text": in.Text, "roles": in.Roles, "requirement": in.Requirement, "agent_id": agent}
			dropEmpty(body)
			if in.FYI {
				body["fyi"] = true
			}
			var out struct {
				Message string `json:"message"`
			}
			if err := t.c.Do(ctx, "POST", "/agent/team/broadcast", body, &out); err != nil {
				return nil, nil, err
			}
			return text(out.Message), nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "team_sent", Description: "查你最近发的团队消息现在的送达状态（命令行 ks team sent [D-x]，R-249）：一次广播算一条，逐个收件人列出已送达（带时间）/ 待送达（原因，如 Codex 要等用户下次发消息）/ 无法送达。ref 填 D-x 只看那一条。只能查自己发的。"},
		func(ctx context.Context, req *mcp.CallToolRequest, in sentIn) (*mcp.CallToolResult, any, error) {
			key := t.sessionKey(ctx, req)
			if key == "" {
				return nil, nil, fmt.Errorf("无法识别当前会话")
			}
			q := url.Values{"session_key": {key}}
			if in.Ref != "" {
				q.Set("ref", in.Ref)
			}
			if in.Limit > 0 {
				q.Set("limit", fmt.Sprint(in.Limit))
			}
			var out struct {
				Text string `json:"text"`
			}
			if err := t.c.Do(ctx, "GET", "/agent/team/sent?"+q.Encode(), nil, &out); err != nil {
				return nil, nil, err
			}
			return text(out.Text), nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "req_comment", Description: "在需求下留言（命令行 ks req comment，R-249）：要讲细节时写在这里——方案、取舍、发现的问题、给接手的人的交代；消息（pm_notify）只放编号加一句话请对方来看。留言进需求时间线，和用户留言放在一起，署名是当前会话（身份 + 会话名），不能指定别的会话；req_get 默认带最新 3 条会话留言（full = true 给全部），用户在网页上看得到。订阅了这条需求的会话（含关联它的）会收到一句只告知的提醒，要别人立刻来看另用 team_tell。先结论再分条，600 字以内，超出照常保存并提示压缩；可附图（images）和表达块（blocks）。\n" + writing.BlocksGuide},
		func(ctx context.Context, req *mcp.CallToolRequest, in commentIn) (*mcp.CallToolResult, any, error) {
			key, agent := t.caller(ctx, req)
			if key == "" {
				return nil, nil, fmt.Errorf("无法识别当前会话")
			}
			body := map[string]any{"session_key": key, "text": in.Text, "agent_id": agent}
			dropEmpty(body)
			cache := map[string]string{}
			ids, err := t.uploadCached(ctx, in.Images, cache)
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
				Message       string   `json:"message"`
				Hint          string   `json:"hint"`
				BlockWarnings []string `json:"block_warnings"`
			}
			if err := t.c.Do(ctx, "POST", "/agent/requirements/"+url.PathEscape(in.Ref)+"/comments", body, &out); err != nil {
				return nil, nil, err
			}
			msg := out.Message + blockWarnings(out.BlockWarnings)
			if out.Hint != "" {
				msg += "\n" + out.Hint
			}
			return text(msg), nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "req_watch", Description: "订阅 / 退订一条需求（命令行 ks req watch R-x [--action-only] / ks req unwatch R-x，R-249）：需求有变动（状态、描述 / 验收标准修改、留言、证据、评审意见、关联决策作答）时收到一句话提醒，细节自己去 req_get 看。两档：要动手的（验收标准被改、评审不通过、关联决策被作答、被退回或受阻）立刻叫醒；只告知的（别的状态变化、描述、留言、证据、评审通过）不叫醒、60 秒内合并成一条。关联或正在做的需求默认已订阅，off = true 可以退订，退订优先。自己引起的变动不通知自己。订阅者永远是当前会话。"},
		func(ctx context.Context, req *mcp.CallToolRequest, in watchIn) (*mcp.CallToolResult, any, error) {
			key := t.sessionKey(ctx, req)
			if key == "" {
				return nil, nil, fmt.Errorf("无法识别当前会话")
			}
			body := map[string]any{"session_key": key}
			switch {
			case in.Off:
				body["off"] = true
			case in.Mode == "action":
				body["mode"] = "action"
			default:
				body["mode"] = "all"
			}
			var out struct {
				Message string `json:"message"`
			}
			if err := t.c.Do(ctx, "POST", "/agent/requirements/"+url.PathEscape(in.Ref)+"/watch", body, &out); err != nil {
				return nil, nil, err
			}
			return text(out.Message), nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "req_watching", Description: "列出当前会话订阅的需求（命令行 ks req watching，R-249）：显式订阅的、因关联而订阅的（写明「因关联而订阅」）、退订掉的；与 ks req watching 同一段输出。"},
		func(ctx context.Context, req *mcp.CallToolRequest, _ watchingIn) (*mcp.CallToolResult, any, error) {
			key := t.sessionKey(ctx, req)
			if key == "" {
				return nil, nil, fmt.Errorf("无法识别当前会话")
			}
			var out struct {
				Text string `json:"text"`
			}
			if err := t.c.Do(ctx, "GET", "/agent/watching?"+url.Values{"session_key": {key}}.Encode(), nil, &out); err != nil {
				return nil, nil, err
			}
			return text(out.Text), nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "handoff_write", Description: "交接（R-224、R-245）：用户点了「交接」、要换会话 / 换工具 / 换设备接任你的身份时，写一份交接存进纸鸢（显示在你的会话时间线上）。只写纸鸢里没有的三栏，几行即可（三栏合计超过 300 字照常保存并提示压缩）：doing 做到一半的事、agreements 口头约定、caveats 注意事项。需求状态、待验收、待答决策、放行队列、合入 / 推送情况，新会话接任时纸鸢按身份自动列出，不必写；用户口头交代的临时安排先用 arrange_add 记成当前安排。新会话运行 ks role <身份> 时自动读到最新一份，你随即卸任：身份清除、不再被唤醒，之后不要再挂 ks wait。主调度有在跑的子任务时会被拒绝：先停派新活，等子任务交完再写。可以重复调用，覆盖还没被接任的那一份。"},
		func(ctx context.Context, req *mcp.CallToolRequest, in handoffIn) (*mcp.CallToolResult, any, error) {
			key := t.sessionKey(ctx, req)
			if key == "" {
				return nil, nil, fmt.Errorf("无法识别当前会话")
			}
			var out struct {
				Message string `json:"message"`
			}
			body := map[string]any{"session_key": key, "doing": in.Doing, "agreements": in.Agreements, "caveats": in.Caveats, "waiting": in.Waiting, "rules": in.Rules, "next": in.Next, "notes": in.Notes}
			if err := t.c.Do(ctx, "POST", "/agent/handoff", body, &out); err != nil {
				return nil, nil, err
			}
			return text(out.Message), nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "arrange_add", Description: "记一条当前安排（R-245，命令行 ks arrange add）：用户在对话里口头交代的临时安排（如「额度紧，做完这几条就停」「这次可以 push」）落成项目上的记录 N-x，署你的名，可带到期时间。用户在工作台上看得到、能撤销；接任你身份的新会话、压缩上下文后都会自动读到，不靠交接转述。"},
		func(ctx context.Context, req *mcp.CallToolRequest, in arrangeAddIn) (*mcp.CallToolResult, any, error) {
			key := t.sessionKey(ctx, req)
			if key == "" {
				return nil, nil, fmt.Errorf("无法识别当前会话")
			}
			var out struct {
				Message string `json:"message"`
			}
			if err := t.c.Do(ctx, "POST", "/agent/arrangements", map[string]any{"session_key": key, "text": in.Text, "until": in.Until}, &out); err != nil {
				return nil, nil, err
			}
			return text(out.Message), nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "arrange_list", Description: "列出本项目的当前安排（R-245，命令行 ks arrange list）：默认只列生效中的，all = true 连已到期、已撤销的。"},
		func(ctx context.Context, req *mcp.CallToolRequest, in arrangeListIn) (*mcp.CallToolResult, any, error) {
			key := t.sessionKey(ctx, req)
			if key == "" {
				return nil, nil, fmt.Errorf("无法识别当前会话")
			}
			q := url.Values{"session_key": {key}}
			if in.All {
				q.Set("all", "true")
			}
			var out struct {
				Arrangements []struct {
					Code      string     `json:"code"`
					Text      string     `json:"text"`
					BySession string     `json:"by_session"`
					ByTitle   string     `json:"by_title"`
					ExpiresAt *time.Time `json:"expires_at"`
					Status    string     `json:"status"`
				} `json:"arrangements"`
			}
			if err := t.c.Do(ctx, "GET", "/agent/arrangements?"+q.Encode(), nil, &out); err != nil {
				return nil, nil, err
			}
			if len(out.Arrangements) == 0 {
				return text("本项目现在没有生效中的安排。"), nil, nil
			}
			var b strings.Builder
			for _, a := range out.Arrangements {
				who := "用户"
				if a.BySession != "" {
					who = a.ByTitle
					if who == "" {
						who = a.BySession
					}
				}
				until := "撤销前一直有效"
				if a.ExpiresAt != nil {
					until = "至 " + a.ExpiresAt.Local().Format("01-02 15:04")
				}
				state := map[string]string{"expired": "［已到期］", "revoked": "［已撤销］"}[a.Status]
				fmt.Fprintf(&b, "%s %s「%s」——%s记，%s\n", a.Code, state, a.Text, who, until)
			}
			return text(b.String()), nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "arrange_revoke", Description: "撤销一条当前安排（R-245，命令行 ks arrange revoke N-x）：用户说这条不再适用、或事情已经做完时撤掉。留痕不删。"},
		func(ctx context.Context, req *mcp.CallToolRequest, in arrangeRevokeIn) (*mcp.CallToolResult, any, error) {
			key := t.sessionKey(ctx, req)
			if key == "" {
				return nil, nil, fmt.Errorf("无法识别当前会话")
			}
			var out struct {
				Message string `json:"message"`
			}
			if err := t.c.Do(ctx, "POST", "/agent/arrangements/"+url.PathEscape(in.Code)+"/revoke", map[string]any{"session_key": key}, &out); err != nil {
				return nil, nil, err
			}
			return text(out.Message), nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "brief_set", Description: "PM 用：写一段给用户看的简报，显示在纸鸢工作台顶部（署名 PM 和时间）。每次覆盖上一份；阶段交付、派完一批活、有事等用户处理时更新。超过 12 小时没更新的简报网页不再显示。"},
		func(ctx context.Context, req *mcp.CallToolRequest, in briefIn) (*mcp.CallToolResult, any, error) {
			key := t.sessionKey(ctx, req)
			if key == "" {
				return nil, nil, fmt.Errorf("无法识别当前会话")
			}
			var out struct {
				Hint string `json:"hint"`
			}
			if err := t.c.Do(ctx, "POST", "/agent/brief", map[string]any{"session_key": key, "text": in.Text, "headline": in.Headline, "look": in.Look}, &out); err != nil {
				return nil, nil, err
			}
			if in.Text == "" && in.Headline == "" && len(in.Look) == 0 {
				return text("已清除简报。"), nil, nil
			}
			return text("简报已更新，用户在工作台顶部能看到。" + out.Hint), nil, nil
		})
	away := func(ctx context.Context, req *mcp.CallToolRequest, in morningIn) (*mcp.CallToolResult, any, error) {
		key := t.sessionKey(ctx, req)
		if key == "" {
			return nil, nil, fmt.Errorf("无法识别当前会话")
		}
		body := map[string]any{"session_key": key}
		if in.Text != nil {
			body["text"] = *in.Text
		}
		if in.Headline != nil {
			body["headline"] = *in.Headline
		}
		if in.Look != nil {
			body["look"] = in.Look
		}
		if len(in.Advice) > 0 {
			body["advice"] = in.Advice
		}
		var out struct {
			Message string `json:"message"`
		}
		if err := t.c.Do(ctx, "POST", "/agent/morning", body, &out); err != nil {
			return nil, nil, err
		}
		return text(out.Message), nil, nil
	}
	mcp.AddTool(s, &mcp.Tool{Name: "away_set", Description: "主助理（Aide）用：写离开期间小结——用户回来时工作台顶部先看到它（用户离开多久不固定，起点由纸鸢按授权窗口等记录算，不用你管）。你只写两样：一段总述（text）和待验收需求的逐条验收建议（advice：建议通过 / 建议退回 + 理由，显示在「待处理」的验收卡上）。代答、放行、交了证据的需求、团队消息由纸鸢自动汇总，不用写。用户回来前更新一次总述；每条需求交验收后看过证据就写建议。"}, away)
	mcp.AddTool(s, &mcp.Tool{Name: "away_escalate", Description: "主助理（Aide）用：用户暂时离开时，把你拿不准、要用户尽快看的待答复决策推给用户（离开期间推送是分级过滤的，你能代答、已写建议的不推；拿不准的用它推）。要写一句理由。"},
		func(ctx context.Context, req *mcp.CallToolRequest, in escalateIn) (*mcp.CallToolResult, any, error) {
			key := t.sessionKey(ctx, req)
			if key == "" {
				return nil, nil, fmt.Errorf("无法识别当前会话")
			}
			var out struct {
				Message string `json:"message"`
			}
			if err := t.c.Do(ctx, "POST", "/agent/away/escalate", map[string]any{"session_key": key, "decision": in.Decision, "reason": in.Reason}, &out); err != nil {
				return nil, nil, err
			}
			return text(out.Message), nil, nil
		})
	// morning_set 是 away_set 的旧名，保留给已在用的会话。
	mcp.AddTool(s, &mcp.Tool{Name: "morning_set", Description: "（旧名，等同 away_set）" + "主助理（Aide）用：写离开期间小结——用户回来时工作台顶部先看到它（用户离开多久不固定，起点由纸鸢按授权窗口等记录算，不用你管）。你只写两样：一段总述（text）和待验收需求的逐条验收建议（advice：建议通过 / 建议退回 + 理由，显示在「待处理」的验收卡上）。代答、放行、交了证据的需求、团队消息由纸鸢自动汇总，不用写。用户回来前更新一次总述；每条需求交验收后看过证据就写建议。"}, away)
	recommend := func(ctx context.Context, req *mcp.CallToolRequest, path string, body map[string]any) (*mcp.CallToolResult, any, error) {
		key := t.sessionKey(ctx, req)
		if key == "" {
			return nil, nil, fmt.Errorf("无法识别当前会话")
		}
		body["session_key"] = key
		dropEmpty(body)
		var out struct {
			Message string `json:"message"`
		}
		if err := t.c.Do(ctx, "POST", path, body, &out); err != nil {
			return nil, nil, err
		}
		return text(out.Message), nil, nil
	}
	mcp.AddTool(s, &mcp.Tool{Name: "dev_recommend", Description: "PM 用：推荐用户开独立的 Dev 会话。默认由后台一次性子任务处理需求（每条需求一个干净上下文）；只有特别适合并行、需要和用户多轮沟通时才推荐。写明为什么要开（为什么要并行、为什么子任务不合适）、开几个、做什么、预计多久。推荐以「请求」出现在用户的审批里，不推送；用户批准后才拿到接入语，Dev 上线时你会收到通知。到点（默认 30 分钟）没批按规则视为拒绝。批准 / 拒绝 / 超时的结果都会送到你这里，等待期间照常用子任务。同项目已有在等批准的推荐时不会重复提。"},
		func(ctx context.Context, req *mcp.CallToolRequest, in recommendIn) (*mcp.CallToolResult, any, error) {
			return recommend(ctx, req, "/agent/team/recommend", map[string]any{"count": in.Count, "reason": in.Reason, "work": in.Work, "duration": in.Duration, "minutes": in.Minutes})
		})
	mcp.AddTool(s, &mcp.Tool{Name: "need_devs", Description: "（旧名，请改用 dev_recommend）PM 用：推荐用户开 Dev 会话，等同 dev_recommend：以限时请求出现在用户的审批里，批准后才推送接入语，超时视为拒绝。count = 0 撤回在等批准的推荐。"},
		func(ctx context.Context, req *mcp.CallToolRequest, in needIn) (*mcp.CallToolResult, any, error) {
			return recommend(ctx, req, "/agent/team/need", map[string]any{"count": in.Count, "reason": in.Reason})
		})
	mcp.AddTool(s, &mcp.Tool{Name: "team", Description: "PM 用：查看本项目的工人池——PM 与各 Dev 会话的在线状态（待命 / 运行中 / 空闲）、上下文占用、手上的需求。Dev 按适合接收需求的顺序排列。"},
		func(ctx context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
			key := t.sessionKey(ctx, req)
			if key == "" {
				return nil, nil, fmt.Errorf("无法识别当前会话")
			}
			var out struct {
				ProjectName string   `json:"project_name"`
				PM          *worker  `json:"pm"`
				Devs        []worker `json:"devs"`
			}
			if err := t.c.Do(ctx, "GET", "/agent/team?"+url.Values{"session_key": {key}}.Encode(), nil, &out); err != nil {
				return nil, nil, err
			}
			var b strings.Builder
			fmt.Fprintf(&b, "项目「%s」\nPM：", out.ProjectName)
			if out.PM == nil {
				b.WriteString("没有\n")
			} else {
				b.WriteString(strings.TrimPrefix(out.PM.line(), "- ") + "\n")
			}
			if len(out.Devs) == 0 {
				b.WriteString("Dev：没有。默认由后台一次性子任务做；确实适合并行、需要多轮沟通时用 dev_recommend 推荐开 Dev 会话（用户限时批准）。")
			} else {
				fmt.Fprintf(&b, "开发（%d 个，适合接收需求的在前）：\n", len(out.Devs))
				for _, w := range out.Devs {
					b.WriteString(w.line() + "\n")
				}
			}
			return text(b.String()), nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "team_sessions", Description: "在线会话列表（命令行 ks team sessions，R-249）：本项目所有没结束的会话，每个带身份（没有写「无身份」）、工具（Claude Code / claude-x 等启动命令 / Codex 终端 / Codex App / DSH）、状态（待命 / 运行中 / 空闲 / 已结束）、手上的需求、现在发消息能否立刻叫醒及原因。叫醒规则只看会话记录：Claude 挂着后台 ks wait、Codex 前台 ks wait 未到期、DSH 插件待命能立刻叫醒；正在跑的这一轮结束后收到；Codex 没挂 wait 不能后台叫醒，等用户下次发消息；已结束或失联为离线。role 只看某个身份（none 只看无身份的），all_projects 列所有项目，all 也列已结束的。不同于 team（PM 的工人池）。"},
		func(ctx context.Context, req *mcp.CallToolRequest, in sessionsIn) (*mcp.CallToolResult, any, error) {
			key := t.sessionKey(ctx, req)
			if key == "" && !in.AllProjects {
				return nil, nil, fmt.Errorf("无法识别当前会话；要看所有项目的会话填 all_projects = true")
			}
			q := url.Values{}
			if key != "" {
				q.Set("session_key", key)
			}
			if in.Role != "" {
				q.Set("role", in.Role)
			}
			if in.AllProjects {
				q.Set("all_projects", "true")
			}
			if in.All {
				q.Set("all", "true")
			}
			var out struct {
				Text string `json:"text"`
			}
			if err := t.c.Do(ctx, "GET", "/agent/team/sessions?"+q.Encode(), nil, &out); err != nil {
				return nil, nil, err
			}
			return text(out.Text), nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "req_assign", Description: "PM 用：把需求指派给本项目的某个 Dev 会话（to 填 team 里的 key，或 auto 由 Kitestring 挑人）。Dev 在待命时立刻开始，在运行则这一轮结束后收到；看板上显示由它负责。指派的是草稿时等于顺带放行：必须写 reason，项目没打开「允许主调度 / 主助理放行草稿」时会被拒绝。"},
		func(ctx context.Context, req *mcp.CallToolRequest, in assignIn) (*mcp.CallToolResult, any, error) {
			key := t.sessionKey(ctx, req)
			if key == "" {
				return nil, nil, fmt.Errorf("无法识别当前会话")
			}
			var out struct {
				Message string `json:"message"`
			}
			body := map[string]any{"session_key": key, "to": in.To, "comment": in.Comment, "reason": in.Reason}
			dropEmpty(body)
			if err := t.c.Do(ctx, "POST", "/agent/requirements/"+url.PathEscape(in.Requirement)+"/assign", body, &out); err != nil {
				return nil, nil, err
			}
			return text(out.Message), nil, nil
		})
}

type escalateIn struct {
	Decision string `json:"decision" jsonschema:"待答复的决策编号，如 D-183"`
	Reason   string `json:"reason" jsonschema:"一句话：为什么要用户尽快看（必填）"`
}
