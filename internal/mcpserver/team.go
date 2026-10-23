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
	Count  int    `json:"count" jsonschema:"还需要几个 Dev 会话（1～5）；0 表示不再需要、撤销提醒"`
	Reason string `json:"reason,omitempty" jsonschema:"为什么需要：哪些需求在等、为什么不适合用 subagent 做；用户会在推送里看到"`
}

type briefIn struct {
	Text string `json:"text" jsonschema:"简报：一段话（建议 150 字以内），先结论——你离开期间完成了什么、现在在等你做什么、有什么风险；提到需求 / 计划 / 决策写编号（如 R-12），网页会画成印记。传空字符串清除"`
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
	mcp.AddTool(s, &mcp.Tool{Name: "need_devs", Description: "PM 用：需要 Dev 会话、但工人池里没有在线的 Dev 时，提醒用户开新的 Dev 会话。用户手机会收到推送，里面有原因和接入用的那句话；Dev 上线后提醒自动消失。req_assign 用 auto 找不到在线 Dev 时会自动提醒，不必重复调用。"},
		func(ctx context.Context, req *mcp.CallToolRequest, in needIn) (*mcp.CallToolResult, any, error) {
			key := t.sessionKey(ctx, req)
			if key == "" {
				return nil, nil, fmt.Errorf("无法识别当前会话")
			}
			body := map[string]any{"session_key": key, "count": in.Count, "reason": in.Reason}
			dropEmpty(body)
			if err := t.c.Do(ctx, "POST", "/agent/team/need", body, nil); err != nil {
				return nil, nil, err
			}
			if in.Count == 0 {
				return text("已撤销提醒。"), nil, nil
			}
			return text(fmt.Sprintf("已提醒用户开 %d 个 Dev 会话。Dev 上线后用 team 查看，再用 req_assign 派活；等待期间可以挂 ks wait。", in.Count)), nil, nil
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
				b.WriteString("Dev：没有。需要 Dev 时用 need_devs 提醒用户开 Dev 会话，或自己用后台 subagent 做。")
			} else {
				fmt.Fprintf(&b, "开发（%d 个，适合接活的在前）：\n", len(out.Devs))
				for _, w := range out.Devs {
					b.WriteString(w.line() + "\n")
				}
			}
			return text(b.String()), nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "req_assign", Description: "PM 用：把需求交给本项目的某个 Dev 会话（to 填 team 里的 key，或 auto 由 Kitestring 挑人）。Dev 在待命时立刻开工，在忙则这一轮结束后收到；看板上显示由它负责。派的是草稿时等于顺带放行：必须写 reason，项目没打开「允许 PM / Adv 放行草稿」时会被拒绝。"},
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
