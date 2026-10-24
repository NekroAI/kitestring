package e2e

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/client"
	"github.com/NekroAI/kitestring/internal/store"
)

type tellRes struct {
	Delivered bool   `json:"delivered"`
	To        string `json:"to"`
	Role      string `json:"role"`
	Code      string `json:"code"`
	Message   string `json:"message"`
}

// 团队消息按身份寻址（R-191）：--to aide|pm|reviewer 按身份发给本项目现在的会话，换了会话照样送到；
// 没有主助理时退回主调度并注明；没有评审时报错；评审多个时发给最近活跃的；
// 回复送回提问方，子任务提问的回复送到它所属的主调度会话并注明转给哪个子任务。
func TestTellByRole(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	c := client.New(30 * time.Second)
	ctx := context.Background()
	ev := func(sid, name string, extra map[string]any) {
		p := map[string]any{"session_id": sid, "cwd": cwd, "hook_event_name": name}
		for k, v := range extra {
			p[k] = v
		}
		hook(t, "claude", p)
	}
	for _, sid := range []string{"pm", "dev", "aide1", "aide2", "rev1", "rev2"} {
		ev(sid, "UserPromptSubmit", map[string]any{"prompt": "上线", "prompt_id": "p0"})
	}
	e.waitFor("会话入库", func() bool { return e.session("claude:rev2").Key != "" })
	setRole := func(key, role string) {
		t.Helper()
		if err := c.Do(ctx, "POST", "/agent/sessions/role", map[string]any{"session_key": key, "role": role}, nil); err != nil {
			t.Fatal(err)
		}
	}
	setRole("claude:pm", "pm")
	setRole("claude:dev", "dev")
	tell := func(body map[string]any) (tellRes, error) {
		var out tellRes
		err := c.Do(ctx, "POST", "/agent/team/tell", body, &out)
		return out, err
	}
	mustTell := func(body map[string]any) (tellRes, store.Decision) {
		t.Helper()
		out, err := tell(body)
		if err != nil || !out.Delivered {
			t.Fatalf("应送出：%+v %v", out, err)
		}
		d, err := e.store.GetDecision(ctx, out.Code)
		if err != nil {
			t.Fatal(err)
		}
		return out, d
	}

	// 没有评审：报错，不送
	if _, err := tell(map[string]any{"session_key": "claude:dev", "text": "帮我看看", "to": "reviewer"}); err == nil || !strings.Contains(err.Error(), "没有在线的评审") {
		t.Fatalf("没有评审时应报清楚的错：%v", err)
	}
	// 不认识的身份：报错
	if _, err := tell(map[string]any{"session_key": "claude:dev", "text": "在吗", "to": "dev"}); err == nil || !strings.Contains(err.Error(), "收件身份只能是") {
		t.Fatalf("收件身份不对应报错：%v", err)
	}

	// 没有主助理：退回给主调度，注明原本要给主助理
	out, d := mustTell(map[string]any{"session_key": "claude:dev", "text": "列表用卡片还是表格？倾向表格", "to": "aide"})
	if out.To != "claude:pm" || out.Role != "pm" || !strings.Contains(out.Message, "退回给主调度") || !strings.Contains(d.Answer, "原本发给主助理") {
		t.Fatalf("没有主助理时应退回主调度并注明：%+v / %s", out, d.Answer)
	}

	// 有主助理：按身份送到；中文名也认；主助理挂着 ks wait 时被唤醒，看到回复方式
	setRole("claude:aide1", "aide")
	done := parkWait(t, e, c, "claude:aide1")
	out, d = mustTell(map[string]any{"session_key": "claude:dev", "text": "列表用卡片还是表格？倾向表格", "to": "主助理"})
	if out.To != "claude:aide1" || d.AssignedBy != "claude:dev" || d.FromAgent != "" {
		t.Fatalf("应送给主助理：%+v / %+v", out, d)
	}
	woke(t, done, "团队消息", "Dev", "claude:dev", "倾向表格", "ks team tell --reply "+out.Code)

	// 主助理回复：回到提问的开发会话
	rep, rd := mustTell(map[string]any{"session_key": "claude:aide1", "text": "用表格，数据列多", "reply_to": out.Code})
	if rep.To != "claude:dev" || rd.ReplyTo != d.ID || !strings.Contains(rd.Answer, "回复 "+out.Code) || !strings.Contains(rd.Answer, "用表格") {
		t.Fatalf("回复应回到提问方：%+v / %+v", rep, rd)
	}
	// 回复时再填收件身份、回复不是发给自己的消息：报错
	if _, err := tell(map[string]any{"session_key": "claude:aide1", "text": "x", "reply_to": out.Code, "to": "pm"}); err == nil {
		t.Fatal("回复时填收件身份应报错")
	}
	if _, err := tell(map[string]any{"session_key": "claude:pm", "text": "x", "reply_to": out.Code}); err == nil || !strings.Contains(err.Error(), "不是发给你的") {
		t.Fatalf("回复别人的消息应报错：%v", err)
	}

	// 换了主助理会话：照样送到新的那个
	setRole("claude:aide2", "aide")
	out, _ = mustTell(map[string]any{"session_key": "claude:dev", "text": "按钮放左还是右", "to": "aide"})
	if out.To != "claude:aide2" {
		t.Fatalf("换会话后应送到新的主助理：%+v", out)
	}

	// 评审多个：发给最近活跃的
	setRole("claude:rev1", "reviewer")
	setRole("claude:rev2", "reviewer")
	last := func(key string) time.Time { return e.session(key).LastActiveAt }
	ev("rev2", "PreToolUse", map[string]any{"tool_name": "Read", "tool_input": map[string]any{"file_path": "a.go"}})
	e.waitFor("rev2 更活跃", func() bool { return last("claude:rev2").After(last("claude:rev1")) })
	if out, _ = mustTell(map[string]any{"session_key": "claude:dev", "text": "R-1 改完了，请看一眼", "to": "reviewer"}); out.To != "claude:rev2" {
		t.Fatalf("应发给最近活跃的评审 rev2：%+v", out)
	}
	ev("rev1", "PreToolUse", map[string]any{"tool_name": "Read", "tool_input": map[string]any{"file_path": "b.go"}})
	e.waitFor("rev1 更活跃", func() bool { return last("claude:rev1").After(last("claude:rev2")) })
	if out, _ = mustTell(map[string]any{"session_key": "claude:dev", "text": "再看一眼", "to": "reviewer"}); out.To != "claude:rev1" {
		t.Fatalf("应发给最近活跃的评审 rev1：%+v", out)
	}

	// 子任务：主调度派出的子任务调 pm_notify，daemon 按 tool_use_id 认出 agent_id
	ev("pm", "PreToolUse", map[string]any{"tool_name": "Agent", "tool_use_id": "toolu_agent", "tool_input": map[string]any{"description": "做 R-191 团队消息", "subagent_type": "general-purpose"}})
	ev("pm", "SubagentStart", map[string]any{"agent_id": "sub-1", "agent_type": "general-purpose"})
	e.waitFor("子任务入册", func() bool { return e.store.AgentLabel(ctx, "claude:pm", "sub-1") != "" })
	ev("pm", "PreToolUse", map[string]any{"agent_id": "sub-1", "agent_type": "general-purpose", "tool_name": "mcp__kitestring__pm_notify", "tool_use_id": "toolu_sub"})
	var who struct {
		SessionKey string `json:"session_key"`
		AgentID    string `json:"agent_id"`
	}
	if err := c.Do(ctx, "GET", "/session?tool_use_id=toolu_sub", nil, &who); err != nil || who.SessionKey != "claude:pm" || who.AgentID != "sub-1" {
		t.Fatalf("应认出是主调度的子任务 sub-1：%+v %v", who, err)
	}
	out, d = mustTell(map[string]any{"session_key": who.SessionKey, "agent_id": who.AgentID, "text": "撤回按钮要不要二次确认？倾向不要", "to": "aide"})
	if out.To != "claude:aide2" || d.FromAgent != "sub-1" || !strings.Contains(d.Answer, "的子任务「做 R-191 团队消息」（sub-1）") || !strings.Contains(out.Message, "由它转给你") {
		t.Fatalf("子任务的提问应注明子任务：%+v / %s", out, d.Answer)
	}
	// 主助理回复：送到子任务所属的主调度会话，注明转给哪个子任务
	woke(t, parkWait(t, e, c, "claude:pm"), "原本发给主助理") // 先收掉之前退回给主调度的消息
	done = parkWait(t, e, c, "claude:pm")
	rep, rd = mustTell(map[string]any{"session_key": "claude:aide2", "text": "不要，撤回可以再撤回", "reply_to": out.Code})
	if rep.To != "claude:pm" || !strings.Contains(rd.Answer, "请转给子任务「做 R-191 团队消息」（sub-1）") {
		t.Fatalf("子任务提问的回复应回到主调度并注明子任务：%+v / %s", rep, rd.Answer)
	}
	woke(t, done, "团队消息", "请转给子任务「做 R-191 团队消息」（sub-1）", "不要，撤回可以再撤回")
	// 子任务告诉主调度进度：它所属的会话就是主调度，照样送到
	if out, _ = mustTell(map[string]any{"session_key": "claude:pm", "agent_id": "sub-1", "text": "后端做完了，在补测试"}); out.To != "claude:pm" {
		t.Fatalf("子任务应能告诉自己所属的主调度：%+v", out)
	}
	// 主调度主线程不给自己发
	if _, err := tell(map[string]any{"session_key": "claude:pm", "text": "x"}); err == nil {
		t.Fatal("主调度主线程给自己发应报错")
	}

	// 提问的主助理会话结束了：回复送给现在接任的主助理
	out, _ = mustTell(map[string]any{"session_key": "claude:aide2", "text": "R-1 的验收建议写好了"})
	ev("aide2", "SessionEnd", nil)
	e.waitFor("aide2 结束", func() bool { return e.session("claude:aide2").State == store.StateEnded })
	setRole("claude:aide1", "aide")
	rep, rd = mustTell(map[string]any{"session_key": "claude:pm", "text": "收到", "reply_to": out.Code})
	if rep.To != "claude:aide1" || !strings.Contains(rd.Answer, "转给现在的主助理") {
		t.Fatalf("提问方结束后应转给接任的主助理：%+v / %s", rep, rd.Answer)
	}
}
