package mcpserver

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/NekroAI/kitestring/internal/summary"
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

var presenceLabel = map[string]string{"standby": "在线待命", "working": "工作中", "offline": "离线"}

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
	To          string `json:"to" jsonschema:"Dev 会话键（team 列出的 key）；填 auto 由 Kitestring 挑：在线待命优先，其次手上需求少、上下文轻"`
	Comment     string `json:"comment,omitempty" jsonschema:"交代给开发的话，例如为什么交给它、和哪条需求相关"`
	Reason      string `json:"reason,omitempty" jsonschema:"派的是草稿时必填：放行理由。派草稿会顺带把它放行为待开始，记进时间线、用户可撤回；项目没打开放行时派草稿会被拒绝"`
}

type needIn struct {
	Count  int    `json:"count" jsonschema:"推荐开几个 Dev 会话（1～5）；0 表示撤回在等批准的推荐"`
	Reason string `json:"reason,omitempty" jsonschema:"为什么要开：为什么要并行、为什么一次性子代理不合适；用户在审批里看到"`
}

type recommendIn struct {
	Count    int    `json:"count" jsonschema:"推荐开几个 Dev 会话（1～5）"`
	Reason   string `json:"reason" jsonschema:"为什么要开：为什么要并行、为什么一次性子代理不合适（例如几条需求要和用户多轮看效果、互不相干可以同时推进）"`
	Work     string `json:"work,omitempty" jsonschema:"开了做什么：交给它们的需求（写编号）"`
	Duration string `json:"duration,omitempty" jsonschema:"预计用多久，例如「两小时」"`
	Minutes  int    `json:"minutes,omitempty" jsonschema:"批准时限（分钟）；不填用用户设置里的（默认 30）。到点没批按规则视为拒绝"`
}

type briefIn struct {
	Text string `json:"text" jsonschema:"简报：一段话（建议 150 字以内），先结论——你离开期间完成了什么、现在在等你做什么、有什么风险；提到需求 / 计划 / 决策写编号（如 R-12），网页会画成印记。传空字符串清除"`
}

type morningAdviceIn struct {
	Requirement string `json:"requirement" jsonschema:"待验收的需求编号（如 R-12）"`
	Verdict     string `json:"verdict" jsonschema:"pass = 建议通过；return = 建议退回；clear = 撤掉建议"`
	Reason      string `json:"reason,omitempty" jsonschema:"理由（pass / return 必填）：对照验收标准一两句，例如「3/3 达标，截图明暗两套都有」或「缺手机截图」"`
}

type morningIn struct {
	Text   *string           `json:"text,omitempty" jsonschema:"总述：一段话（建议 150 字以内），先结论——用户离开期间整体推进到哪、你替用户定了几件、有什么要用户先看的；提到需求 / 决策写编号。不传表示不改，传空字符串清除"`
	Advice []morningAdviceIn `json:"advice,omitempty" jsonschema:"逐条验收建议，每条覆盖该需求上一份；只能写给待验收的需求"`
}

type tellIn struct {
	Text        string `json:"text" jsonschema:"要告诉 PM 的话：一两句，先结论（例如：R-12 卡在接口没定，先做了 R-13）"`
	Requirement string `json:"requirement,omitempty" jsonschema:"相关需求编号（如 R-12），可省略"`
}

func (t *tools) registerTeam(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{Name: "pm_notify", Description: "给本项目的 PM 留言：进展、受阻、发现的问题、做完一批等要让 PM 知道的事。只送给 PM（它挂着 ks wait 时立刻收到），不进用户的收件箱；要用户拍板的事仍用 decision_raise。需求设为待验收时 PM 会自动收到通知，不必再发。项目没有 PM 时不会送出，也不报错。"},
		func(ctx context.Context, req *mcp.CallToolRequest, in tellIn) (*mcp.CallToolResult, any, error) {
			key := t.sessionKey(ctx, req)
			if key == "" {
				return nil, nil, fmt.Errorf("无法识别当前会话")
			}
			var out struct {
				Message string `json:"message"`
			}
			body := map[string]any{"session_key": key, "text": in.Text, "requirement": in.Requirement}
			dropEmpty(body)
			if err := t.c.Do(ctx, "POST", "/agent/team/tell", body, &out); err != nil {
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
			if err := t.c.Do(ctx, "POST", "/agent/brief", map[string]any{"session_key": key, "text": in.Text}, nil); err != nil {
				return nil, nil, err
			}
			if in.Text == "" {
				return text("已清除简报。"), nil, nil
			}
			return text("简报已更新，用户在工作台顶部能看到。"), nil, nil
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
	mcp.AddTool(s, &mcp.Tool{Name: "away_set", Description: "主助理（Aide）用：写离开期间小结——用户回来时工作台顶部先看到它（用户离开多久不固定，起点由纸鸢按授权窗口等记录算，不用你管）。你只写两样：一段总述（text）和待验收需求的逐条验收建议（advice：建议通过 / 建议退回 + 理由，显示在「要你处理」的验收卡上）。代答、放行、交了证据的需求、团队消息由纸鸢自动汇总，不用写。用户回来前更新一次总述；每条需求交验收后看过证据就写建议。"}, away)
	// morning_set 是 away_set 的旧名，保留给已在用的会话。
	mcp.AddTool(s, &mcp.Tool{Name: "morning_set", Description: "（旧名，等同 away_set）" + "主助理（Aide）用：写离开期间小结——用户回来时工作台顶部先看到它（用户离开多久不固定，起点由纸鸢按授权窗口等记录算，不用你管）。你只写两样：一段总述（text）和待验收需求的逐条验收建议（advice：建议通过 / 建议退回 + 理由，显示在「要你处理」的验收卡上）。代答、放行、交了证据的需求、团队消息由纸鸢自动汇总，不用写。用户回来前更新一次总述；每条需求交验收后看过证据就写建议。"}, away)
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
	mcp.AddTool(s, &mcp.Tool{Name: "dev_recommend", Description: "PM 用：推荐用户开独立的 Dev 会话。默认把需求交给后台一次性子代理（每条需求一个干净上下文）；只有特别适合并行、需要和用户多轮沟通时才推荐。写明为什么要开（为什么要并行、为什么子代理不合适）、开几个、做什么、预计多久。推荐以「请求」出现在用户的审批里，不推送；用户批准后才拿到接入语，Dev 上线时你会收到通知。到点（默认 30 分钟）没批按规则视为拒绝。批准 / 拒绝 / 超时的结果都会送到你这里，等待期间照常用子代理。同项目已有在等批准的推荐时不会重复提。"},
		func(ctx context.Context, req *mcp.CallToolRequest, in recommendIn) (*mcp.CallToolResult, any, error) {
			return recommend(ctx, req, "/agent/team/recommend", map[string]any{"count": in.Count, "reason": in.Reason, "work": in.Work, "duration": in.Duration, "minutes": in.Minutes})
		})
	mcp.AddTool(s, &mcp.Tool{Name: "need_devs", Description: "（旧名，请改用 dev_recommend）PM 用：推荐用户开 Dev 会话，等同 dev_recommend：以限时请求出现在用户的审批里，批准后才推送接入语，超时视为拒绝。count = 0 撤回在等批准的推荐。"},
		func(ctx context.Context, req *mcp.CallToolRequest, in needIn) (*mcp.CallToolResult, any, error) {
			return recommend(ctx, req, "/agent/team/need", map[string]any{"count": in.Count, "reason": in.Reason})
		})
	mcp.AddTool(s, &mcp.Tool{Name: "team", Description: "PM 用：查看本项目的工人池——PM 与各 Dev 会话的在线状态（在线待命 / 工作中 / 离线）、上下文占用、手上的需求。Dev 按适合接活的顺序排列。"},
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
				b.WriteString("Dev：没有。默认交给后台一次性子代理做；确实适合并行、需要多轮沟通时用 dev_recommend 推荐开 Dev 会话（用户限时批准）。")
			} else {
				fmt.Fprintf(&b, "开发（%d 个，适合接活的在前）：\n", len(out.Devs))
				for _, w := range out.Devs {
					b.WriteString(w.line() + "\n")
				}
			}
			return text(b.String()), nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "req_assign", Description: "PM 用：把需求交给本项目的某个 Dev 会话（to 填 team 里的 key，或 auto 由 Kitestring 挑人）。Dev 在待命时立刻开工，在忙则这一轮结束后收到；看板上显示由它负责。派的是草稿时等于顺带放行：必须写 reason，项目没打开「允许主调度 / 主助理放行草稿」时会被拒绝。"},
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
