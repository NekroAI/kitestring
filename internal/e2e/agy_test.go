package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/client"
	"github.com/NekroAI/kitestring/internal/hookcli"
	"github.com/NekroAI/kitestring/internal/protocol"
	"github.com/NekroAI/kitestring/internal/store"
)

func hookAgy(t *testing.T, event string, payload map[string]any) string {
	t.Helper()
	b, _ := json.Marshal(payload)
	var out bytes.Buffer
	code := hookcli.Run([]string{protocol.ToolAgy, event}, bytes.NewReader(b), &out)
	if code != 0 {
		t.Fatalf("agy hook %s 应永远返回 0，得到 %d", event, code)
	}
	flushed()
	return out.String()
}

func writeAgyTranscriptFile(t *testing.T, appDir, cid string, lines []string) string {
	t.Helper()
	p := filepath.Join(appDir, "brain", cid, ".system_generated", "logs", "transcript.jsonl")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAgyCrossAgentCollaborationLifecycle(t *testing.T) {
	e := setup(t)
	home := t.TempDir()
	appDir := filepath.Join(home, "agy-data")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("ANTIGRAVITY_AGENT", "1")
	t.Setenv("ANTIGRAVITY_APP_DATA_DIR", appDir)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	t.Setenv("DSH_SESSION_ID", "")
	t.Setenv("DSH_KITESTRING_SESSION", "")

	cwd, _ := os.Getwd()
	cidPM := "11111111-1111-1111-1111-111111111111"
	childCID := "22222222-2222-2222-2222-222222222222"
	agyKey := protocol.SessionKey(protocol.ToolAgy, cidPM)

	writeAgyTranscriptFile(t, appDir, cidPM, []string{
		`{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","created_at":"2026-11-22T10:00:00Z","content":"协调 Claude Dev、Codex Rev 与 DSH Aide 完成登录改造"}`,
	})

	// 1. AGY PM SessionStart + PreInvocation 折叠（2 次 PreInvocation 仅算 1 轮 UserPromptSubmit）
	if out := hookAgy(t, "SessionStart", map[string]any{
		"hookEventName":  "SessionStart",
		"conversationId": cidPM,
		"workspacePath":  cwd,
		"client":         "cli",
	}); strings.TrimSpace(out) != "{}" {
		t.Fatalf("SessionStart 无待注入内容时应输出 {}，得到 %q", out)
	}

	hookAgy(t, "PreInvocation", map[string]any{
		"hookEventName":   "PreInvocation",
		"conversationId":  cidPM,
		"workspacePath":   cwd,
		"initialNumSteps": 1,
	})
	e.waitFor("AGY PM 会话入库", func() bool {
		s := e.session(agyKey)
		return s.Key == agyKey && s.Turns == 1
	})

	// 同一 turn 内的第二次 PreInvocation 应被折叠，不增加 Turns
	hookAgy(t, "PreInvocation", map[string]any{
		"hookEventName":   "PreInvocation",
		"conversationId":  cidPM,
		"workspacePath":   cwd,
		"initialNumSteps": 2,
	})
	if s := e.session(agyKey); s.Turns != 1 || s.Harness() != "Antigravity 终端" {
		t.Fatalf("PreInvocation 未折叠或 Harness 异常：turns=%d harness=%q", s.Turns, s.Harness())
	}

	// AGY PM 声明角色并建需求 R-1
	c := client.New(30 * time.Second)
	ctx := context.Background()
	if err := c.Do(ctx, "POST", "/agent/sessions/role", map[string]any{"session_key": agyKey, "role": "pm"}, nil); err != nil {
		t.Fatal(err)
	}
	e.waitFor("AGY PM 角色生效", func() bool { return e.session(agyKey).Role == "pm" })

	var req store.Requirement
	if err := c.Do(ctx, "POST", "/agent/requirements", map[string]any{
		"title":       "多 Agent 登录改造",
		"status":      "ready",
		"session_key": agyKey,
	}, &req); err != nil || req.Code == "" {
		t.Fatalf("创建需求失败: code=%q err=%v", req.Code, err)
	}

	// 2. Claude Dev + Codex Rev + DSH Aide 同项目上线并认领角色
	hook(t, "claude", map[string]any{"session_id": "s-dev", "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "Dev 就绪", "prompt_id": "d0"})
	hook(t, "codex", map[string]any{"session_id": "s-rev", "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "Rev 就绪", "turn_id": "c0"})
	hook(t, "dsh", map[string]any{"session_id": "s-aide", "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "Aide 就绪", "client": "web", "instance": "9999@1"})
	e.waitFor("四个团队成员均上线", func() bool {
		return e.session("claude:s-dev").Key != "" && e.session("codex:s-rev").Key != "" && e.session("dsh:s-aide").Key != ""
	})
	for key, role := range map[string]string{
		"claude:s-dev": "dev",
		"codex:s-rev":  "reviewer",
		"dsh:s-aide":   "aide",
	} {
		if err := c.Do(ctx, "POST", "/agent/sessions/role", map[string]any{"session_key": key, "role": role}, nil); err != nil {
			t.Fatalf("assign role %s=%s: %v", key, role, err)
		}
	}

	// 3. AGY Stop 在未挂 ks wait 时触发常驻拦阻（decision=continue）
	stopBlockedOut := hookAgy(t, "Stop", map[string]any{
		"hookEventName":  "Stop",
		"conversationId": cidPM,
		"workspacePath":  cwd,
	})
	var blockResp struct {
		Decision string `json:"decision"`
		Reason   string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(stopBlockedOut), &blockResp); err != nil {
		t.Fatalf("Stop continue 输出应为 JSON: %q: %v", stopBlockedOut, err)
	}
	if blockResp.Decision != "continue" || !strings.Contains(blockResp.Reason, "BypassSandbox=true") || !strings.Contains(blockResp.Reason, "ks wait") {
		t.Fatalf("AGY Stop guard 应返回 decision=continue 与 BypassSandbox 指引，得到 %#v", blockResp)
	}

	// 先消化掉 Claude Dev / Codex Rev / DSH Aide 上线推送给 PM 的团队通知，再真正挂起待命。
	e.waitFor("团队上线通知下行到 daemon", func() bool {
		var st map[string]any
		_ = c.Do(ctx, "GET", "/status", nil, &st)
		n, _ := st["decisions"].(float64)
		return n >= 3
	})
	firstWait := parkWait(t, e, c, agyKey)
	woke(t, firstWait, "claude:s-dev", "codex:s-rev", "dsh:s-aide")
	e.waitFor("上次 wait 退出后回到非挂起态", func() bool { return !e.session(agyKey).Parked })

	// 挂上后台 ks wait，再更新 transcript.jsonl 后 Stop 放行（decision=allow）并上报指标
	writeAgyTranscriptFile(t, appDir, cidPM, []string{
		`{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","created_at":"2026-11-22T10:00:00Z","content":"协调 Claude Dev、Codex Rev 与 DSH Aide 完成登录改造"}`,
		`{"step_index":1,"source":"MODEL","type":"PLANNER_RESPONSE","created_at":"2026-11-22T10:00:05Z","model":"gemini-4-carbon-high","input_tokens":200,"cache_read_tokens":1000,"cache_write_tokens":0,"output_tokens":45,"content":"已派工给 Claude Dev，并同步 Codex Rev 与 DSH Aide。"}`,
	})
	agyWait := parkWait(t, e, c, agyKey)
	stopAllowOut := hookAgy(t, "Stop", map[string]any{
		"hookEventName":  "Stop",
		"conversationId": cidPM,
		"workspacePath":  cwd,
	})
	var allowResp struct {
		Decision string `json:"decision"`
	}
	if err := json.Unmarshal([]byte(stopAllowOut), &allowResp); err != nil || allowResp.Decision != "allow" {
		t.Fatalf("挂好 ks wait 后 Stop 应返回 decision=allow，得到 %q", stopAllowOut)
	}

	e.waitFor("AGY 上下文、模型与最近回复同步到 Hub", func() bool {
		s := e.session(agyKey)
		if s.State != store.StateParked || s.ContextTokens != 1245 || s.ContextMax != 1_000_000 || s.Model != "gemini-4-carbon-high" {
			return false
		}
		lr, err := e.store.GetLastReply(ctx, agyKey)
		return err == nil && lr != nil && lr.Text == "已派工给 Claude Dev，并同步 Codex Rev 与 DSH Aide。"
	})

	// 4. 无 _meta 时经 PreToolUse + 参数规范化哈希识别 MCP 调用来源，完成跨 Agent 指派与团队唤醒
	preAssignOut := hookAgy(t, "PreToolUse", map[string]any{
		"hookEventName":  "PreToolUse",
		"conversationId": cidPM,
		"workspacePath":  cwd,
		"toolName":       "mcp:kitestring:req_assign",
		"toolInput":      map[string]any{"to": "claude:s-dev", "requirement": req.Code},
	})
	var preAllow struct {
		Decision string `json:"decision"`
	}
	if json.Unmarshal([]byte(preAssignOut), &preAllow) != nil || preAllow.Decision != "allow" {
		t.Fatalf("PreToolUse 应返回 decision=allow，得到 %q", preAssignOut)
	}
	argsHash := protocol.CanonicalArgsHash(json.RawMessage(`{"requirement":"` + req.Code + `","to":"claude:s-dev"}`))
	var sessLookup struct {
		SessionKey string `json:"session_key"`
	}
	if err := c.Do(ctx, "GET", "/session?"+url.Values{"mcp_tool": {"req_assign"}, "args_hash": {argsHash}}.Encode(), nil, &sessLookup); err != nil {
		t.Fatal(err)
	}
	if sessLookup.SessionKey != agyKey {
		t.Fatalf("参数哈希反查会话失败: got=%q want=%q", sessLookup.SessionKey, agyKey)
	}

	e.allowRelease(agyKey)
	if err := c.Do(ctx, "POST", "/agent/requirements/"+req.Code+"/status", map[string]any{
		"session_key": agyKey,
		"status":      "ready",
		"reason":      "PM 放行进入待开始",
	}, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.Do(ctx, "POST", "/agent/requirements/"+req.Code+"/assign", map[string]any{
		"session_key": agyKey,
		"to":          "claude:s-dev",
	}, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.Do(ctx, "POST", "/agent/team/tell", map[string]any{
		"session_key": agyKey,
		"to":          "aide",
		"text":        "请复核 " + req.Code + " 登录方案",
	}, nil); err != nil {
		t.Fatal(err)
	}

	// DSH Aide 回复 PM -> 立即唤醒正在 ks wait 的 AGY PM
	if err := c.Do(ctx, "POST", "/agent/team/tell", map[string]any{
		"session_key": "dsh:s-aide",
		"to":          "pm",
		"text":        "方案已复核通过",
	}, nil); err != nil {
		t.Fatal(err)
	}
	woke(t, agyWait, "方案已复核通过")

	// 5. invoke_subagent PostToolUse + SubagentStop 归因子代理 transcript
	writeAgyTranscriptFile(t, appDir, childCID, []string{
		`{"step_index":0,"source":"MODEL","type":"PLANNER_RESPONSE","created_at":"2026-11-22T10:02:00Z","model":"gemini-4-carbon-high","input_tokens":50,"cache_read_tokens":200,"cache_write_tokens":0,"output_tokens":20,"content":"子代理审计通过"}`,
	})
	hookAgy(t, "PreToolUse", map[string]any{
		"hookEventName":  "PreToolUse",
		"conversationId": cidPM,
		"workspacePath":  cwd,
		"toolName":       "invoke_subagent",
		"toolInput":      map[string]any{"Role": "Code Reviewer", "TypeName": "research", "Prompt": "check auth"},
	})
	hookAgy(t, "PostToolUse", map[string]any{
		"hookEventName":  "PostToolUse",
		"conversationId": cidPM,
		"workspacePath":  cwd,
		"toolName":       "invoke_subagent",
		"toolOutput":     "Launched subagent conversationId: " + childCID,
	})
	hookAgy(t, "SubagentStop", map[string]any{
		"hookEventName":  "SubagentStop",
		"conversationId": cidPM,
		"workspacePath":  cwd,
	})

	// 6. compact（initialNumSteps 回落）经 PreInvocation 触发 injectSteps 重投角色与 AGY 沙箱指引
	compactOut := hookAgy(t, "PreInvocation", map[string]any{
		"hookEventName":   "PreInvocation",
		"conversationId":  cidPM,
		"workspacePath":   cwd,
		"initialNumSteps": 1, // 从 lastNumSteps=2 回落，判定为 compact
	})
	var injectResp struct {
		InjectSteps []struct {
			SystemMessage struct {
				SystemMessage string `json:"systemMessage"`
			} `json:"systemMessage"`
		} `json:"injectSteps"`
	}
	if err := json.Unmarshal([]byte(compactOut), &injectResp); err != nil || len(injectResp.InjectSteps) == 0 {
		t.Fatalf("compact 后应返回 injectSteps.systemMessage，得到 %q (err=%v)", compactOut, err)
	}
	injected := injectResp.InjectSteps[0].SystemMessage.SystemMessage
	if !strings.Contains(injected, "主调度（PM）") || !strings.Contains(injected, "BypassSandbox=true") {
		t.Fatalf("compact 重投内容缺少 PM 身份或 AGY 沙箱说明：%s", injected)
	}
}
