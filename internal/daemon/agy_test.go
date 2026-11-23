package daemon

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/protocol"
)

func newAgyDaemon(t *testing.T, dir string) *Daemon {
	t.Helper()
	d := newQDaemon(t, dir)
	d.git = newGitCache()
	d.commits = newCommitScan()
	d.sessions = map[string]*sessionInfo{}
	d.toolUses = map[string]toolUse{}
	d.toolCond = sync.NewCond(&d.mu)
	d.tools = newToolsState()
	return d
}

func writeAgyTranscript(t *testing.T, dir, cid string, lines []string) string {
	t.Helper()
	p := filepath.Join(dir, "brain", cid, ".system_generated", "logs", "transcript.jsonl")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestNormalizeAgyPayloadAndResolveTranscript(t *testing.T) {
	appDir := t.TempDir()
	cid := "11111111-1111-1111-1111-111111111111"
	tp := writeAgyTranscript(t, appDir, cid, []string{
		`{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","content":"实现登录页"}`,
	})

	payload := map[string]any{
		"hookEventName": "PreToolUse",
		"conversationId": cid,
		"workspaceUris":  []any{"file:///tmp/demo-repo"},
		"toolName":       "run_command",
		"toolInput":      map[string]any{"CommandLine": "go test ./..."},
		"toolCallId":     "call-42",
	}
	env := map[string]string{
		"ANTIGRAVITY_AGENT":        "1",
		"ANTIGRAVITY_APP_DATA_DIR": appDir,
	}
	evName := normalizeAgyPayload("", payload, env)
	if evName != "PreToolUse" {
		t.Fatalf("expected PreToolUse, got %q", evName)
	}
	if payload["session_id"] != cid {
		t.Fatalf("expected session_id=%s, got %#v", cid, payload["session_id"])
	}
	if payload["transcript_path"] != tp {
		t.Fatalf("expected resolved transcript %s, got %#v", tp, payload["transcript_path"])
	}
	if payload["cwd"] != "/tmp/demo-repo" {
		t.Fatalf("expected stripped workspaceUri cwd, got %#v", payload["cwd"])
	}
	if payload["tool_name"] != "run_command" || payload["tool_use_id"] != "call-42" {
		t.Fatalf("unexpected tool mapping: %#v", payload)
	}
}

func TestFoldAgyPreInvocationLifecycle(t *testing.T) {
	d := &Daemon{}
	appDir := t.TempDir()
	cid := "22222222-2222-2222-2222-222222222222"
	key := protocol.SessionKey(protocol.ToolAgy, cid)

	tp := writeAgyTranscript(t, appDir, cid, []string{
		`{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","content":"第一步：查仓库"}`,
	})

	// 1. 首轮 PreInvocation -> 转为 UserPromptSubmit
	f1 := d.foldAgyPreInvocationLocked(key, "100@t1", map[string]any{"initialNumSteps": float64(1)}, tp)
	if f1.skip || f1.event != "UserPromptSubmit" || f1.source != "" || f1.prompt != "第一步：查仓库" {
		t.Fatalf("step 1 unexpected: %+v", f1)
	}

	// 2. 同一轮工具调用后的第二次 PreInvocation -> 折叠跳过
	f2 := d.foldAgyPreInvocationLocked(key, "100@t1", map[string]any{"initialNumSteps": float64(3)}, tp)
	if !f2.skip || f2.event != "PreInvocation" {
		t.Fatalf("step 2 expected fold skip=true, got %+v", f2)
	}

	// 3. 同一轮内用户插话产生更高 step_index 的 USER_EXPLICIT -> 新 turn
	writeAgyTranscript(t, appDir, cid, []string{
		`{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","content":"第一步：查仓库"}`,
		`{"step_index":3,"source":"USER_EXPLICIT","type":"USER_INPUT","content":"顺便加个测试"}`,
	})
	f3 := d.foldAgyPreInvocationLocked(key, "100@t1", map[string]any{"initialNumSteps": float64(4)}, tp)
	if f3.skip || f3.event != "UserPromptSubmit" || f3.source != "" || f3.prompt != "顺便加个测试" {
		t.Fatalf("step 3 expected queued prompt turn, got %+v", f3)
	}

	// 4. Stop 放行后，没有新的 USER_EXPLICIT 时再次触发 PreInvocation -> SourceAuto
	d.finishAgyStopLocked(key, tp)
	f4 := d.foldAgyPreInvocationLocked(key, "100@t1", map[string]any{"initialNumSteps": float64(6)}, tp)
	if f4.skip || f4.event != "UserPromptSubmit" || f4.source != protocol.SourceAuto {
		t.Fatalf("step 4 expected SourceAuto wake, got %+v", f4)
	}

	// 5. CHECKPOINT 步骤（真实压缩标志）或 initialNumSteps 回落 -> compact + emitCompact
	writeAgyTranscript(t, appDir, cid, []string{
		`{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","content":"第一步：查仓库"}`,
		`{"step_index":3,"source":"USER_EXPLICIT","type":"USER_INPUT","content":"顺便加个测试"}`,
		`{"step_index":7,"source":"SYSTEM","type":"CHECKPOINT","content":"# Resuming from a compaction"}`,
	})
	f5 := d.foldAgyPreInvocationLocked(key, "100@t1", map[string]any{"initialNumSteps": float64(8)}, tp)
	if f5.skip || f5.event != "UserPromptSubmit" || f5.source != "compact" || !f5.emitCompact {
		t.Fatalf("step 5 expected compact+emitCompact from CHECKPOINT, got %+v", f5)
	}

	// 6. 实例变化 -> resume
	f6 := d.foldAgyPreInvocationLocked(key, "200@t2", map[string]any{"initialNumSteps": float64(9)}, tp)
	if f6.skip || f6.event != "UserPromptSubmit" || f6.source != "resume" {
		t.Fatalf("step 6 expected resume, got %+v", f6)
	}
}

func TestAgyMCPArgsHashAssociationAndLookup(t *testing.T) {
	d := &Daemon{
		sessions: map[string]*sessionInfo{
			"agy:cid-a": {client: "cli"},
			"agy:cid-b": {client: "ide"},
		},
		tools: newToolsState(),
	}

	// 不同键序与 json.RawMessage 对同一参数生成完全一致的哈希
	hMap := canonicalArgsHash(map[string]any{"to": "dev-1", "req_id": "R-9", "meta": map[string]any{"b": 2, "a": 1}})
	hRaw := canonicalArgsHash(json.RawMessage(`{"meta":{"a":1,"b":2},"req_id":"R-9","to":"dev-1"}`))
	if hMap != hRaw {
		t.Fatalf("canonicalArgsHash mismatch: map=%s raw=%s", hMap, hRaw)
	}
	if hNil, hEmpty := canonicalArgsHash(nil), canonicalArgsHash(json.RawMessage(`{}`)); hNil != hEmpty {
		t.Fatalf("empty args hash mismatch: nil=%s empty=%s", hNil, hEmpty)
	}

	d.mu.Lock()
	d.rememberAgyMCPLocked("agy:cid-a", "agent-1", "cid-a", "mcp:kitestring:team_tell", map[string]any{"to": "dev-1", "msg": "hi"})
	d.rememberAgyMCPLocked("agy:cid-b", "", "cid-b", "mcp__kitestring__team_tell", map[string]any{"msg": "hi", "to": "dev-1"})
	d.mu.Unlock()

	hLookup := canonicalArgsHash(json.RawMessage(`{"msg":"hi","to":"dev-1"}`))
	req := httptest.NewRequest(http.MethodGet, "/session?mcp_tool=team_tell&args_hash="+hLookup, nil)
	rec := httptest.NewRecorder()
	d.handleSessionLookup(rec, req)
	var out1 map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &out1)
	if out1["session_key"] != "agy:cid-a" || out1["agent_id"] != "agent-1" {
		t.Fatalf("first FIFO lookup expected agy:cid-a / agent-1, got %#v", out1)
	}

	req2 := httptest.NewRequest(http.MethodGet, "/session?mcp_tool=team_tell&args_hash="+hLookup, nil)
	rec2 := httptest.NewRecorder()
	d.handleSessionLookup(rec2, req2)
	var out2 map[string]string
	_ = json.Unmarshal(rec2.Body.Bytes(), &out2)
	if out2["session_key"] != "agy:cid-b" {
		t.Fatalf("second FIFO lookup expected agy:cid-b, got %#v", out2)
	}

	// agy_cid 直接查询
	req3 := httptest.NewRequest(http.MethodGet, "/session?agy_cid=cid-b", nil)
	rec3 := httptest.NewRecorder()
	d.handleSessionLookup(rec3, req3)
	var out3 map[string]string
	_ = json.Unmarshal(rec3.Body.Bytes(), &out3)
	if out3["session_key"] != "agy:cid-b" {
		t.Fatalf("agy_cid lookup expected agy:cid-b, got %#v", out3)
	}
}

func TestAgyContextUsageReplyAndSubagent(t *testing.T) {
	appDir := t.TempDir()
	parentCID := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	childCID := "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"

	parentTP := writeAgyTranscript(t, appDir, parentCID, []string{
		`{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","created_at":"2026-11-22T10:00:00Z","content":"调研一下"}`,
		`{"step_index":1,"source":"MODEL","type":"PLANNER_RESPONSE","created_at":"2026-11-22T10:00:02Z","model":"gemini-4-carbon-high","input_tokens":100,"cache_read_tokens":500,"cache_write_tokens":50,"output_tokens":20,"content":"先派子代理看代码"}`,
		`{"step_index":2,"source":"MODEL","type":"GENERIC","created_at":"2026-11-22T10:00:05Z","model":null,"input_tokens":null,"content":"Launched subagent conversationId: ` + childCID + `"}`,
		`{"step_index":3,"source":"MODEL","type":"PLANNER_RESPONSE","created_at":"2026-11-22T10:00:08Z","model":"gemini-4-carbon-high","input_tokens":200,"cache_read_tokens":1000,"cache_write_tokens":0,"output_tokens":45,"content":"已派发 Codebase Researcher，结论稍后汇报。"}`,
	})
	_ = writeAgyTranscript(t, appDir, childCID, []string{
		`{"step_index":0,"source":"MODEL","type":"PLANNER_RESPONSE","created_at":"2026-11-22T10:00:06Z","model":"gemini-4-carbon-high","input_tokens":80,"cache_read_tokens":400,"cache_write_tokens":0,"output_tokens":30,"content":"已完成代码梳理：入口在 cmd/ks/main.go"}`,
	})

	used, win := contextUsage(parentTP)
	if used != 1245 || win != 1_000_000 {
		t.Fatalf("contextUsage expected (1245, 1000000), got (%d, %d)", used, win)
	}
	if reply := latestReply(parentTP); reply != "已派发 Codebase Researcher，结论稍后汇报。" {
		t.Fatalf("latestReply unexpected: %q", reply)
	}

	d := &Daemon{}
	parentKey := protocol.SessionKey(protocol.ToolAgy, parentCID)
	d.mu.Lock()
	sub := d.noteAgySubagentToolLocked(parentKey, parentCID, parentTP, map[string]any{
		"tool_input": map[string]any{"Role": "Codebase Researcher", "TypeName": "research"},
	}, map[string]string{"ANTIGRAVITY_APP_DATA_DIR": appDir})
	d.mu.Unlock()
	if sub == nil || sub.childCID != childCID || sub.role != "Codebase Researcher" {
		t.Fatalf("unexpected sub info: %#v", sub)
	}

	d.mu.Lock()
	stopped := d.findAgySubagentStopLocked(parentKey, map[string]any{})
	d.mu.Unlock()
	if stopped == nil || stopped.childCID != childCID {
		t.Fatalf("findAgySubagentStopLocked unexpected: %#v", stopped)
	}
	if sum := agySubagentSummary(stopped, nil); sum != "已完成代码梳理：入口在 cmd/ks/main.go" {
		t.Fatalf("agySubagentSummary unexpected: %q", sum)
	}
}

func TestUsageScanner_AgyLineAndSubagentAttribution(t *testing.T) {
	dir := t.TempDir()
	q, err := openQueue(filepath.Join(dir, "daemon.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer q.close()

	now := time.Date(2026, 11, 22, 10, 5, 0, 0, time.UTC)
	u, err := openUsage(q.db)
	if err != nil {
		t.Fatal(err)
	}
	u.now = func() time.Time { return now }

	parentCID := "cccccccc-cccc-cccc-cccc-cccccccccccc"
	childCID := "dddddddd-dddd-dddd-dddd-dddddddddddd"
	parentKey := protocol.SessionKey(protocol.ToolAgy, parentCID)

	parentTP := writeAgyTranscript(t, dir, parentCID, []string{
		`{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","created_at":"2026-11-22T10:04:10Z","content":"hello"}`,
		`{"step_index":1,"source":"MODEL","type":"GENERIC","created_at":"2026-11-22T10:04:12Z","model":null,"input_tokens":null,"content":"tool out"}`,
		`{"step_index":2,"source":"MODEL","type":"PLANNER_RESPONSE","created_at":"2026-11-22T10:04:15Z","model":"gemini-4-carbon-high","input_tokens":120,"cache_read_tokens":800,"cache_write_tokens":40,"output_tokens":35,"content":"done"}`,
	})
	childTP := writeAgyTranscript(t, dir, childCID, []string{
		`{"step_index":0,"source":"MODEL","type":"PLANNER_RESPONSE","created_at":"2026-11-22T10:04:20Z","model":"gemini-4-carbon-high","input_tokens":60,"cache_read_tokens":300,"cache_write_tokens":0,"output_tokens":15,"content":"child done"}`,
	})

	u.track(parentKey, parentTP)
	u.trackSubagent(parentKey, childCID, childTP)
	u.scan()
	// 再扫一次确认不会重复累计
	u.scan()

	var totalIn, totalOut, rows int
	if err := q.db.QueryRow(`SELECT COALESCE(SUM(inp),0), COALESCE(SUM(out),0), COUNT(*) FROM usage_minutes WHERE session_key = ?`, parentKey).Scan(&totalIn, &totalOut, &rows); err != nil {
		t.Fatal(err)
	}
	if totalIn != 180 || totalOut != 50 || rows != 2 {
		t.Fatalf("unexpected usage_minutes totals: in=%d out=%d rows=%d", totalIn, totalOut, rows)
	}
}

func pathToFileURI(p string) string {
	s := filepath.ToSlash(p)
	if strings.HasPrefix(s, "/") {
		return "file://" + s
	}
	return "file:///" + s
}

func TestNormalizeAgyToolCallAndStopStates(t *testing.T) {
	p := map[string]any{
		"hookEventName": "PreToolUse",
		"conversationId": "cid-tc",
		"transcriptPath": "/Users/test/.gemini/antigravity-ide/brain/cid-tc/.system_generated/logs/transcript.jsonl",
		"lastUserInput":  "check R-7",
		"stepIdx":        float64(19),
		"toolCall": map[string]any{
			"id":   "toolu_mcp_1",
			"name": "call_mcp_tool",
			"args": map[string]any{
				"ServerName": "kitestring_kitestring",
				"ToolName":   "req_update_status",
				"Arguments":  map[string]any{"id": "R-7", "status": "done"},
			},
		},
	}
	ev := normalizeAgyPayload("", p, nil)
	if ev != "PreToolUse" {
		t.Fatalf("expected PreToolUse, got %q", ev)
	}
	if p["tool_name"] != "mcp__kitestring__req_update_status" || p["tool_use_id"] != "toolu_mcp_1" {
		t.Fatalf("call_mcp_tool unwrap failed: %#v", p)
	}
	if _, hasPrompt := p["prompt"]; hasPrompt {
		t.Fatalf("lastUserInput should not overwrite prompt before folding: %#v", p)
	}
	if p["last_user_input"] != "check R-7" {
		t.Fatalf("last_user_input missing: %#v", p)
	}
	if c := detectAgyClient(p, nil, nil); c != "ide" {
		t.Fatalf("expected ide from transcriptPath, got %q", c)
	}
	if brief := toolBrief(str(p, "tool_name"), p["tool_input"].(map[string]any)); brief != "R-7 · done" {
		t.Fatalf("unexpected MCP brief: %q", brief)
	}

	stopP := map[string]any{
		"hookEventName":     "Stop",
		"conversationId":    "cid-tc",
		"terminationReason": "USER_CANCELED",
	}
	if got := normalizeAgyPayload("", stopP, nil); got != "Interrupt" || stopP["hook_event_name"] != "Interrupt" {
		t.Fatalf("expected Interrupt on USER_CANCELED, got %q %#v", got, stopP)
	}
}

func TestAgyTurn1DelayedFlushAndCompactionColdStart(t *testing.T) {
	dir := t.TempDir()
	d := newAgyDaemon(t, dir)
	defer d.q.close()
	var err error
	if d.usage, err = openUsage(d.q.db); err != nil {
		t.Fatal(err)
	}

	appDir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	t.Setenv("ANTIGRAVITY_APP_DATA_DIR", appDir)
	cid := "33333333-3333-3333-3333-333333333333"
	key := protocol.SessionKey(protocol.ToolAgy, cid)

	tp := writeAgyTranscript(t, appDir, cid, []string{
		`{"step_index":0,"source":"SYSTEM","type":"TASK_BOUNDARY","created_at":"2026-11-23T10:00:00Z","content":""}`,
	})

	// Turn 1 PreInvocation #1 fires before USER_EXPLICIT flushes; stdin provides lastUserInput
	ev1, ok1 := d.buildEvent(protocol.HookRequest{
		Tool: protocol.ToolAgy, Event: "PreInvocation",
		Payload: map[string]any{"conversationId": cid, "transcriptPath": tp, "lastUserInput": "检查代码质量", "initialNumSteps": float64(1)},
		Env:     map[string]string{"ANTIGRAVITY_APP_DATA_DIR": appDir},
	})
	if !ok1 || ev1.Name != "UserPromptSubmit" || ev1.Prompt != "检查代码质量" || ev1.Source != "" {
		t.Fatalf("Turn 1 PreInvocation #1 unexpected: %+v ok=%v", ev1, ok1)
	}

	// USER_EXPLICIT flushes before PreInvocation #2 in the same turn -> must be folded (skipped), NOT counted as turn 2
	writeAgyTranscript(t, appDir, cid, []string{
		`{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","created_at":"2026-11-23T10:00:01Z","content":"<USER_REQUEST>\n检查代码质量\n</USER_REQUEST>\n<ADDITIONAL_METADATA>\nnow\n</ADDITIONAL_METADATA>"}`,
		`{"step_index":1,"source":"MODEL","type":"PLANNER_RESPONSE","created_at":"2026-11-23T10:00:05Z","model":"gemini-4-carbon-high","input_tokens":500,"cache_read_tokens":20000,"cache_write_tokens":0,"output_tokens":30,"content":"working"}`,
	})
	_, ok2 := d.buildEvent(protocol.HookRequest{
		Tool: protocol.ToolAgy, Event: "PreInvocation",
		Payload: map[string]any{"conversationId": cid, "transcriptPath": tp, "lastUserInput": "检查代码质量", "initialNumSteps": float64(2)},
		Env:     map[string]string{"ANTIGRAVITY_APP_DATA_DIR": appDir},
	})
	if ok2 {
		t.Fatal("delayed USER_EXPLICIT flush within Turn 1 should be folded/skipped")
	}

	// Compaction: CHECKPOINT + post-checkpoint large cache write
	writeAgyTranscript(t, appDir, cid, []string{
		`{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","created_at":"2026-11-23T10:00:01Z","content":"<USER_REQUEST>\n检查代码质量\n</USER_REQUEST>"}`,
		`{"step_index":1,"source":"MODEL","type":"PLANNER_RESPONSE","created_at":"2026-11-23T10:00:05Z","model":"gemini-4-carbon-high","input_tokens":500,"cache_read_tokens":40000,"cache_write_tokens":0,"output_tokens":30,"content":"working"}`,
		`{"step_index":2,"source":"SYSTEM","type":"CHECKPOINT","created_at":"2026-11-23T10:01:00Z","content":"# Resuming from a compaction"}`,
		`{"step_index":3,"source":"MODEL","type":"PLANNER_RESPONSE","created_at":"2026-11-23T10:01:05Z","model":"gemini-4-carbon-high","input_tokens":200,"cache_read_tokens":1000,"cache_write_tokens":22000,"output_tokens":40,"content":"resumed"}`,
	})
	ev3, ok3 := d.buildEvent(protocol.HookRequest{
		Tool: protocol.ToolAgy, Event: "PreInvocation",
		Payload: map[string]any{"conversationId": cid, "transcriptPath": tp, "lastUserInput": "检查代码质量", "initialNumSteps": float64(4)},
		Env:     map[string]string{"ANTIGRAVITY_APP_DATA_DIR": appDir},
	})
	if !ok3 || ev3.Name != "UserPromptSubmit" || ev3.Source != "compact" {
		t.Fatalf("expected compact UserPromptSubmit, got %+v ok=%v", ev3, ok3)
	}
	evs, _, _ := d.q.pending(10)
	if len(evs) != 1 || evs[0].Name != "PreCompact" {
		t.Fatalf("expected synthetic PreCompact queued before UserPromptSubmit, got %+v", evs)
	}

	d.usage.now = func() time.Time { return time.Date(2026, 11, 23, 10, 2, 0, 0, time.UTC) }
	d.usage.track(key, tp)
	d.usage.scan()
	var cause string
	if err := d.q.db.QueryRow(`SELECT cause FROM usage_colds WHERE session_key = ? ORDER BY at_ms DESC LIMIT 1`, key).Scan(&cause); err != nil {
		t.Fatal(err)
	}
	if cause != protocol.ColdCompact {
		t.Fatalf("expected ColdCompact cold start cause, got %q", cause)
	}

	// Stop with fullyIdle=false sets BgTasks >= 1
	stopEv, okStop := d.buildEvent(protocol.HookRequest{
		Tool: protocol.ToolAgy, Event: "Stop",
		Payload: map[string]any{"conversationId": cid, "transcriptPath": tp, "fullyIdle": false},
		Env:     map[string]string{"ANTIGRAVITY_APP_DATA_DIR": appDir},
	})
	if !okStop || stopEv.Name != "Stop" || stopEv.BgTasks < 1 {
		t.Fatalf("expected Stop with BgTasks>=1 for fullyIdle=false, got %+v", stopEv)
	}
}

func TestAgyChildSubagentHookRoutingAndShells(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	d := newAgyDaemon(t, dir)
	defer d.q.close()
	var err error
	if d.shells, err = openShells(d.q.db); err != nil {
		t.Fatal(err)
	}
	d.usage, err = openUsage(d.q.db)
	if err != nil {
		t.Fatal(err)
	}
	d.usage.onLine = d.shells.transcriptLine

	appDir := t.TempDir()
	parentCID := "44444444-4444-4444-4444-444444444444"
	childCID := "55555555-5555-5555-5555-555555555555"
	parentKey := protocol.SessionKey(protocol.ToolAgy, parentCID)
	parentTP := writeAgyTranscript(t, appDir, parentCID, []string{
		`{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","created_at":"2026-11-23T10:00:00Z","content":"<USER_REQUEST>\n跑部署并查代码\n</USER_REQUEST>"}`,
	})
	childTP := writeAgyTranscript(t, appDir, childCID, []string{
		`{"step_index":0,"source":"SYSTEM","type":"SYSTEM_MESSAGE","created_at":"2026-11-23T10:00:02Z","content":"<SYSTEM_MESSAGE>\n[Message] timestamp=2026-11-23T10:00:02Z sender=` + parentCID + ` priority=MESSAGE_PRIORITY_HIGH content=Inspect daemon files\n</SYSTEM_MESSAGE>"}`,
	})

	// Parent session start + invoke_subagent PreToolUse
	if _, ok := d.buildEvent(protocol.HookRequest{Tool: protocol.ToolAgy, Event: "SessionStart", Payload: map[string]any{"conversationId": parentCID, "transcriptPath": parentTP}}); !ok {
		t.Fatal("parent SessionStart failed")
	}
	preSub, okPreSub := d.buildEvent(protocol.HookRequest{
		Tool: protocol.ToolAgy, Event: "PreToolUse",
		Payload: map[string]any{
			"conversationId": parentCID,
			"transcriptPath": parentTP,
			"toolCall": map[string]any{
				"id":   "call_sub_1",
				"name": "invoke_subagent",
				"args": map[string]any{"Role": "Code Auditor", "TypeName": "research", "Prompt": "Inspect daemon files"},
			},
		},
	})
	if !okPreSub || preSub.ToolBrief != "Code Auditor" {
		t.Fatalf("parent invoke_subagent PreToolUse unexpected: %+v", preSub)
	}

	// Child sends SessionStart, PreInvocation (dropped after SubagentStart), PreToolUse(call_mcp_tool), and Stop
	cStart, okCS := d.buildEvent(protocol.HookRequest{
		Tool: protocol.ToolAgy, Event: "SessionStart", PPID: 99999,
		Payload: map[string]any{"conversationId": childCID, "transcriptPath": childTP},
	})
	if !okCS || cStart.Name != "SubagentStart" || cStart.SessionID != parentCID || cStart.AgentID != childCID || cStart.AgentType != "research" || cStart.ToolBrief != "Code Auditor" {
		t.Fatalf("child SessionStart must route to SubagentStart on parent: %+v ok=%v", cStart, okCS)
	}
	if _, okCI := d.buildEvent(protocol.HookRequest{
		Tool: protocol.ToolAgy, Event: "PreInvocation",
		Payload: map[string]any{"conversationId": childCID, "transcriptPath": childTP, "initialNumSteps": float64(1)},
	}); okCI {
		t.Fatal("child PreInvocation after SubagentStart must be suppressed")
	}

	cTool, okCT := d.buildEvent(protocol.HookRequest{
		Tool: protocol.ToolAgy, Event: "PreToolUse",
		Payload: map[string]any{
			"conversationId": childCID,
			"transcriptPath": childTP,
			"toolCall": map[string]any{
				"id":   "call_child_mcp",
				"name": "call_mcp_tool",
				"args": map[string]any{"ServerName": "kitestring_kitestring", "ToolName": "req_get", "Arguments": map[string]any{"id": "R-9"}},
			},
		},
	})
	if !okCT || cTool.SessionID != parentCID || cTool.AgentID != childCID || cTool.ToolName != "mcp__kitestring__req_get" || cTool.ToolBrief != "R-9" {
		t.Fatalf("child MCP PreToolUse routing unexpected: %+v ok=%v", cTool, okCT)
	}
	// Verify /session lookup attributes MCP call to parent session AND child agent_id
	d.tools = newToolsState()
	hReq := canonicalArgsHash(map[string]any{"id": "R-9"})
	reqLookup := httptest.NewRequest(http.MethodGet, "/session?mcp_tool=req_get&args_hash="+hReq, nil)
	recLookup := httptest.NewRecorder()
	d.handleSessionLookup(recLookup, reqLookup)
	var lookupOut map[string]string
	_ = json.Unmarshal(recLookup.Body.Bytes(), &lookupOut)
	if lookupOut["session_key"] != parentKey || lookupOut["agent_id"] != childCID {
		t.Fatalf("child MCP session lookup failed: %#v", lookupOut)
	}

	// Child writes final reply and stops -> routed to SubagentStop
	writeAgyTranscript(t, appDir, childCID, []string{
		`{"step_index":0,"source":"SYSTEM","type":"SYSTEM_MESSAGE","created_at":"2026-11-23T10:00:02Z","content":"<SYSTEM_MESSAGE>\n[Message] sender=` + parentCID + ` content=Inspect\n</SYSTEM_MESSAGE>"}`,
		`{"step_index":1,"source":"MODEL","type":"PLANNER_RESPONSE","created_at":"2026-11-23T10:00:10Z","model":"gemini-4-carbon-high","input_tokens":50,"cache_read_tokens":200,"cache_write_tokens":0,"output_tokens":15,"content":"All daemon checks clean."}`,
	})
	cStop, okCStop := d.buildEvent(protocol.HookRequest{
		Tool: protocol.ToolAgy, Event: "Stop",
		Payload: map[string]any{"conversationId": childCID, "transcriptPath": childTP},
	})
	if !okCStop || cStop.Name != "SubagentStop" || cStop.SessionID != parentCID || cStop.AgentID != childCID || cStop.LastReply != "All daemon checks clean." {
		t.Fatalf("child Stop routing unexpected: %+v ok=%v", cStop, okCStop)
	}
	d.mu.Lock()
	_, standaloneExists := d.sessions[protocol.SessionKey(protocol.ToolAgy, childCID)]
	d.mu.Unlock()
	if standaloneExists {
		t.Fatal("child conversationId must never create a standalone session entry")
	}

	// Background shell (run_command -> task-543 -> task-543.log -> exit code 0)
	now := time.Now().UTC()
	d.shells.now = func() time.Time { return now }
	logFile := filepath.Join(dir, "task-543.log")
	_ = os.WriteFile(logFile, []byte("building...\ndeployed ok\n"), 0o600)
	_, okRunPre := d.buildEvent(protocol.HookRequest{
		Tool: protocol.ToolAgy, Event: "PreToolUse",
		Payload: map[string]any{
			"conversationId": parentCID,
			"transcriptPath": parentTP,
			"toolCall": map[string]any{
				"id":   "call_bg_1",
				"name": "run_command",
				"args": map[string]any{"CommandLine": "scripts/deploy.sh", "WaitMsBeforeAsync": float64(500), "toolSummary": "Deploy service"},
			},
		},
	})
	if !okRunPre {
		t.Fatal("run_command PreToolUse failed")
	}
	taskLine := fmt.Sprintf(`{"step_index":2,"source":"SYSTEM","type":"GENERIC","created_at":%q,"content":"Tool is running as a background task with task id: %s/task-543\nTask Description: scripts/deploy.sh\nTask logs are available at: %s"}`, now.Format(time.RFC3339), parentCID, pathToFileURI(logFile))
	writeAgyTranscript(t, appDir, parentCID, []string{
		`{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","created_at":"2026-11-23T10:00:00Z","content":"<USER_REQUEST>\n跑部署\n</USER_REQUEST>"}`,
		taskLine,
	})
	_, okRunPost := d.buildEvent(protocol.HookRequest{
		Tool: protocol.ToolAgy, Event: "PostToolUse",
		Payload: map[string]any{
			"conversationId": parentCID,
			"transcriptPath": parentTP,
			"toolCall":       map[string]any{"id": "call_bg_1", "name": "run_command", "args": map[string]any{"CommandLine": "scripts/deploy.sh"}},
		},
	})
	if !okRunPost {
		t.Fatal("run_command PostToolUse failed")
	}
	d.shells.mu.Lock()
	recShell := d.shells.recs[shellID(parentKey, "call_bg_1")]
	d.shells.mu.Unlock()
	if recShell == nil || !recShell.launched || recShell.c.Task != "task-543" || recShell.c.Last != "deployed ok" || recShell.c.Status != protocol.ShellRunning {
		t.Fatalf("expected running agy shell with log tail, got %#v", recShell)
	}
	doneMsg := fmt.Sprintf("<SYSTEM_MESSAGE>\n[Message] timestamp=%s sender=%s/task-543 priority=MESSAGE_PRIORITY_HIGH content=Task id \"%s/task-543\" finished with result:\nCommand exited with code 0.\n</SYSTEM_MESSAGE>", now.Format(time.RFC3339), parentCID, parentCID)
	d.shells.notifyText(parentKey, doneMsg, now.Add(5*time.Second))
	if recShell.c.Status != protocol.ShellOK || recShell.c.Exit == nil || *recShell.c.Exit != 0 {
		t.Fatalf("expected finished agy shell OK exit=0, got %+v", recShell.c)
	}
}

func writeAgyTranscriptFull(t *testing.T, dir, cid string, lines []string) string {
	t.Helper()
	p := filepath.Join(dir, "brain", cid, ".system_generated", "logs", "transcript_full.jsonl")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAgyMultiSubagentDelayedFlushNoGhostSessions(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	d := newAgyDaemon(t, dir)
	defer d.q.close()
	var err error
	if d.usage, err = openUsage(d.q.db); err != nil {
		t.Fatal(err)
	}

	appDir := t.TempDir()
	parentCID := "10000000-0000-0000-0000-000000000000"
	child1 := "10000000-0000-0000-0000-000000000001"
	child2 := "10000000-0000-0000-0000-000000000002"
	child3 := "10000000-0000-0000-0000-000000000003"
	grandchild := "10000000-0000-0000-0000-000000000004"
	parentTP := writeAgyTranscriptFull(t, appDir, parentCID, []string{
		`{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","created_at":"2026-11-23T17:00:00Z","content":"批量画角色立绘"}`,
	})
	child1TP := filepath.Join(appDir, "brain", child1, ".system_generated", "logs", "transcript_full.jsonl")
	child2TP := filepath.Join(appDir, "brain", child2, ".system_generated", "logs", "transcript_full.jsonl")
	child3TP := filepath.Join(appDir, "brain", child3, ".system_generated", "logs", "transcript_full.jsonl")
	grandTP := filepath.Join(appDir, "brain", grandchild, ".system_generated", "logs", "transcript_full.jsonl")

	// 父会话启动
	if _, ok := d.buildEvent(protocol.HookRequest{
		Tool: protocol.ToolAgy, Event: "SessionStart",
		Payload: map[string]any{"conversationId": parentCID, "transcriptPath": parentTP},
	}); !ok {
		t.Fatal("parent SessionStart failed")
	}

	// 1. 父会话派发 Artist 1：PostToolUse 无 tool_response，且父/子 transcript 均未刷盘
	if _, ok := d.buildEvent(protocol.HookRequest{
		Tool: protocol.ToolAgy, Event: "PreToolUse",
		Payload: map[string]any{
			"conversationId": parentCID, "transcriptPath": parentTP,
			"toolCall": map[string]any{"id": "call_1", "name": "invoke_subagent", "args": map[string]any{"Role": "Artist 1", "TypeName": "self", "Prompt": "Draw portrait 1"}},
		},
	}); !ok {
		t.Fatal("invoke_subagent #1 PreToolUse failed")
	}
	if _, ok := d.buildEvent(protocol.HookRequest{
		Tool: protocol.ToolAgy, Event: "PostToolUse",
		Payload: map[string]any{
			"conversationId": parentCID, "transcriptPath": parentTP,
			"toolCall": map[string]any{"id": "call_1", "name": "invoke_subagent", "args": map[string]any{"Role": "Artist 1", "TypeName": "self", "Prompt": "Draw portrait 1"}},
		},
	}); !ok {
		t.Fatal("invoke_subagent #1 PostToolUse failed")
	}

	// 2. 在 child1 启动前，父会话已开始派发 Artist 2（PreToolUse）
	if _, ok := d.buildEvent(protocol.HookRequest{
		Tool: protocol.ToolAgy, Event: "PreToolUse",
		Payload: map[string]any{
			"conversationId": parentCID, "transcriptPath": parentTP,
			"toolCall": map[string]any{"id": "call_2", "name": "invoke_subagent", "args": map[string]any{"Role": "Artist 2", "TypeName": "self", "Prompt": "Draw portrait 2"}},
		},
	}); !ok {
		t.Fatal("invoke_subagent #2 PreToolUse failed")
	}

	// child1 的 SessionStart 在 transcript 未落盘时到达 -> 暂缓（不泄漏成独立会话）
	if ev, ok := d.buildEvent(protocol.HookRequest{
		Tool: protocol.ToolAgy, Event: "SessionStart",
		Payload: map[string]any{"conversationId": child1, "transcriptPath": child1TP},
	}); ok {
		t.Fatalf("child1 delayed SessionStart should be deferred, got %+v", ev)
	}
	// child1 的 PreInvocation 无 lastUserInput -> 优先认领已 PostToolUse 的 Artist 1
	c1Start, ok1 := d.buildEvent(protocol.HookRequest{
		Tool: protocol.ToolAgy, Event: "PreInvocation",
		Payload: map[string]any{"conversationId": child1, "transcriptPath": child1TP, "initialNumSteps": float64(1)},
	})
	if !ok1 || c1Start.Name != "SubagentStart" || c1Start.SessionID != parentCID || c1Start.AgentID != child1 || c1Start.ToolBrief != "Artist 1" {
		t.Fatalf("child1 PreInvocation should claim Artist 1, got %+v ok=%v", c1Start, ok1)
	}

	// 此时父 transcript 才补刷了 child1 的 Created the following subagent，紧接着 Artist 2 的 PostToolUse 到达：
	// 绝不能把已认领的 child1 错配给 Artist 2（杜绝 off-by-one）！
	writeAgyTranscriptFull(t, appDir, parentCID, []string{
		`{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","created_at":"2026-11-23T17:00:00Z","content":"批量画角色立绘"}`,
		`{"step_index":1,"source":"SYSTEM","type":"GENERIC","created_at":"2026-11-23T17:00:01Z","content":"Created the following subagent:\nconversationId: ` + child1 + `"}`,
	})
	if _, ok := d.buildEvent(protocol.HookRequest{
		Tool: protocol.ToolAgy, Event: "PostToolUse",
		Payload: map[string]any{
			"conversationId": parentCID, "transcriptPath": parentTP,
			"toolCall": map[string]any{"id": "call_2", "name": "invoke_subagent", "args": map[string]any{"Role": "Artist 2", "TypeName": "self", "Prompt": "Draw portrait 2"}},
		},
	}); !ok {
		t.Fatal("invoke_subagent #2 PostToolUse failed")
	}

	// 3. 派发 Artist 3（Pre + Post），随后 child2、child3 在未刷盘时依次启动
	if _, ok := d.buildEvent(protocol.HookRequest{
		Tool: protocol.ToolAgy, Event: "PreToolUse",
		Payload: map[string]any{
			"conversationId": parentCID, "transcriptPath": parentTP,
			"toolCall": map[string]any{"id": "call_3", "name": "invoke_subagent", "args": map[string]any{"Role": "Artist 3", "TypeName": "research", "Prompt": "Audit portraits"}},
		},
	}); !ok {
		t.Fatal("invoke_subagent #3 PreToolUse failed")
	}
	if _, ok := d.buildEvent(protocol.HookRequest{
		Tool: protocol.ToolAgy, Event: "PostToolUse",
		Payload: map[string]any{
			"conversationId": parentCID, "transcriptPath": parentTP,
			"toolCall": map[string]any{"id": "call_3", "name": "invoke_subagent", "args": map[string]any{"Role": "Artist 3", "TypeName": "research", "Prompt": "Audit portraits"}},
		},
	}); !ok {
		t.Fatal("invoke_subagent #3 PostToolUse failed")
	}

	for _, tc := range []struct {
		cid      string
		tp       string
		wantRole string
		wantType string
	}{
		{child2, child2TP, "Artist 2", "self"},
		{child3, child3TP, "Artist 3", "research"},
	} {
		if ev, ok := d.buildEvent(protocol.HookRequest{
			Tool: protocol.ToolAgy, Event: "SessionStart",
			Payload: map[string]any{"conversationId": tc.cid, "transcriptPath": tc.tp},
		}); ok {
			t.Fatalf("%s delayed SessionStart should be deferred, got %+v", tc.cid, ev)
		}
		ev, ok := d.buildEvent(protocol.HookRequest{
			Tool: protocol.ToolAgy, Event: "PreInvocation",
			Payload: map[string]any{"conversationId": tc.cid, "transcriptPath": tc.tp, "initialNumSteps": float64(1)},
		})
		if !ok || ev.Name != "SubagentStart" || ev.SessionID != parentCID || ev.AgentID != tc.cid || ev.ToolBrief != tc.wantRole || ev.AgentType != tc.wantType {
			t.Fatalf("%s expected SubagentStart role=%q type=%q, got %+v ok=%v", tc.cid, tc.wantRole, tc.wantType, ev, ok)
		}
	}

	// 4. 孙子代理：child2 再次 invoke_subagent，grandchild 应归属到根父会话 parentCID
	if _, ok := d.buildEvent(protocol.HookRequest{
		Tool: protocol.ToolAgy, Event: "PreToolUse",
		Payload: map[string]any{
			"conversationId": child2, "transcriptPath": child2TP,
			"toolCall": map[string]any{"id": "call_grand", "name": "invoke_subagent", "args": map[string]any{"Role": "Grandchild Helper", "TypeName": "self", "Prompt": "Crop image"}},
		},
	}); !ok {
		t.Fatal("grandchild PreToolUse failed")
	}
	if _, ok := d.buildEvent(protocol.HookRequest{
		Tool: protocol.ToolAgy, Event: "SessionStart",
		Payload: map[string]any{"conversationId": grandchild, "transcriptPath": grandTP},
	}); ok {
		t.Fatal("grandchild SessionStart should be deferred")
	}
	gcStart, okGC := d.buildEvent(protocol.HookRequest{
		Tool: protocol.ToolAgy, Event: "PreInvocation",
		Payload: map[string]any{"conversationId": grandchild, "transcriptPath": grandTP, "initialNumSteps": float64(1)},
	})
	if !okGC || gcStart.Name != "SubagentStart" || gcStart.SessionID != parentCID || gcStart.AgentID != grandchild || gcStart.ToolBrief != "Grandchild Helper" {
		t.Fatalf("grandchild should route to root parentCID, got %+v ok=%v", gcStart, okGC)
	}

	// 5. 四个子代理并发结束 -> 每一个都必须准确产出自己的 SubagentStop（不能互相误杀兄弟）
	for _, cid := range []string{child1, child2, child3, grandchild} {
		tp := filepath.Join(appDir, "brain", cid, ".system_generated", "logs", "transcript_full.jsonl")
		writeAgyTranscriptFull(t, appDir, cid, []string{
			`{"step_index":0,"source":"SYSTEM","type":"SYSTEM_MESSAGE","created_at":"2026-11-23T17:00:02Z","content":"<SYSTEM_MESSAGE>\n[Message] sender=` + parentCID + ` content=work\n</SYSTEM_MESSAGE>"}`,
			`{"step_index":1,"source":"MODEL","type":"PLANNER_RESPONSE","created_at":"2026-11-23T17:00:08Z","model":"gemini-4-carbon-high","input_tokens":40,"cache_read_tokens":100,"cache_write_tokens":0,"output_tokens":10,"content":"done ` + cid[len(cid)-1:] + `"}`,
		})
		stEv, okSt := d.buildEvent(protocol.HookRequest{
			Tool: protocol.ToolAgy, Event: "Stop",
			Payload: map[string]any{"conversationId": cid, "transcriptPath": tp},
		})
		if !okSt || stEv.Name != "SubagentStop" || stEv.SessionID != parentCID || stEv.AgentID != cid || stEv.LastReply != "done "+cid[len(cid)-1:] {
			t.Fatalf("%s Stop should emit own SubagentStop, got %+v ok=%v", cid, stEv, okSt)
		}
	}

	// 6. 已结束的子代理收到父会话 send_message 再次唤醒 -> 重新发 SubagentStart 并可再次正常结束
	reWake, okReWake := d.buildEvent(protocol.HookRequest{
		Tool: protocol.ToolAgy, Event: "PreInvocation",
		Payload: map[string]any{"conversationId": child1, "transcriptPath": child1TP, "initialNumSteps": float64(3)},
	})
	if !okReWake || reWake.Name != "SubagentStart" || reWake.SessionID != parentCID || reWake.AgentID != child1 {
		t.Fatalf("re-awakened child1 should emit SubagentStart, got %+v ok=%v", reWake, okReWake)
	}
	reStop, okReStop := d.buildEvent(protocol.HookRequest{
		Tool: protocol.ToolAgy, Event: "Stop",
		Payload: map[string]any{"conversationId": child1, "transcriptPath": child1TP},
	})
	if !okReStop || reStop.Name != "SubagentStop" || reStop.AgentID != child1 {
		t.Fatalf("re-awakened child1 Stop should emit SubagentStop, got %+v ok=%v", reStop, okReStop)
	}

	// 7. 确认没有任何子代理泄漏成 d.sessions 中的顶层独立会话
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, cid := range []string{child1, child2, child3, grandchild} {
		if d.sessions[protocol.SessionKey(protocol.ToolAgy, cid)] != nil {
			t.Fatalf("subagent %s leaked into top-level d.sessions", cid)
		}
	}
}

func TestAgyDeferredSessionVsNormalUserSessionAndInstances(t *testing.T) {
	d := &Daemon{sessions: map[string]*sessionInfo{}, instances: map[string]string{}, dshInst: map[string]map[string]bool{}}
	parentCID := "20000000-0000-0000-0000-000000000000"
	parentKey := protocol.SessionKey(protocol.ToolAgy, parentCID)
	d.sessions[parentKey] = &sessionInfo{client: "cli"}
	d.noteDSHInstanceLocked("43509@1", parentKey, false)

	d.noteAgySubagentPreLocked(parentKey, parentCID, "43509@1", map[string]any{
		"tool_use_id": "call_sub",
		"tool_input":  map[string]any{"Role": "Worker", "TypeName": "self", "Prompt": "Do work"},
	})

	// 1. 另一个终端（不同 instance）新开用户会话：SessionStart 不应被暂缓
	otherTerminalCID := "20000000-0000-0000-0000-000000000001"
	sub, _, _, drop := d.routeAgyChildLocked(otherTerminalCID, "SessionStart", "90001@1", map[string]any{}, nil)
	if sub != nil || drop {
		t.Fatalf("new session on different instance must not be deferred/claimed: sub=%#v drop=%v", sub, drop)
	}

	// 2. 同进程（如 IDE 共享进程）新开用户标签页：SessionStart 暂缓，但带 last_user_input 的 PreInvocation 正常建独立会话，不抢占子代理槽位
	ideUserCID := "20000000-0000-0000-0000-000000000002"
	sub, _, _, drop = d.routeAgyChildLocked(ideUserCID, "SessionStart", "43509@1", map[string]any{}, nil)
	if sub != nil || !drop {
		t.Fatalf("same-instance fresh SessionStart during pending subagent should defer: sub=%#v drop=%v", sub, drop)
	}
	sub, _, _, drop = d.routeAgyChildLocked(ideUserCID, "PreInvocation", "43509@1", map[string]any{"last_user_input": "帮我重构登录模块"}, nil)
	if sub != nil || drop {
		t.Fatalf("user PreInvocation with last_user_input must NOT be hijacked as subagent: sub=%#v drop=%v", sub, drop)
	}
	if len(d.agyPendingSubs[parentCID]) != 1 {
		t.Fatalf("pending subagent slot must remain intact for real child, got %d", len(d.agyPendingSubs[parentCID]))
	}
}

func TestAgyMultiTerminalSwitchNoFalseCompact(t *testing.T) {
	d := &Daemon{}
	appDir := t.TempDir()
	cid := "30000000-0000-0000-0000-000000000000"
	key := protocol.SessionKey(protocol.ToolAgy, cid)
	tp := writeAgyTranscriptFull(t, appDir, cid, []string{
		`{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","content":"长会话"}`,
	})

	f1 := d.foldAgyPreInvocationLocked(key, "43509@1", map[string]any{"initialNumSteps": float64(4009)}, tp)
	if f1.skip || f1.emitCompact {
		t.Fatalf("step 1 unexpected: %+v", f1)
	}
	// 切到另一终端且 initialNumSteps 略微回落（4009 -> 4002）-> 仅 resume，不误报 PreCompact
	f2 := d.foldAgyPreInvocationLocked(key, "12350@1", map[string]any{"initialNumSteps": float64(4002)}, tp)
	if f2.skip || f2.source != "resume" || f2.emitCompact {
		t.Fatalf("multi-terminal switch should resume without PreCompact, got %+v", f2)
	}
	// 同一终端内步数大幅回落 -> 仍正常识别压缩
	f3 := d.foldAgyPreInvocationLocked(key, "12350@1", map[string]any{"initialNumSteps": float64(120)}, tp)
	if f3.skip || f3.source != "compact" || !f3.emitCompact {
		t.Fatalf("same-instance step drop should still emit compact, got %+v", f3)
	}
}

func TestAgyLongStep0TranscriptFullAndUsageReattribution(t *testing.T) {
	appDir := t.TempDir()
	parentCID := "40000000-0000-0000-0000-000000000000"
	childCID := "40000000-0000-0000-0000-000000000001"

	// 同时存在 transcript.jsonl（截断版/空）与 transcript_full.jsonl（>24KB 超长 Step 0）
	shortTP := writeAgyTranscript(t, appDir, childCID, []string{`{"step_index":0,"source":"SYSTEM","type":"TASK_BOUNDARY","content":""}`})
	longPrompt := strings.Repeat("详细任务上下文A", 3200) // ~28KB UTF-8，远超旧版 16KB 读取限制
	fullTP := writeAgyTranscriptFull(t, appDir, childCID, []string{
		fmt.Sprintf(`{"step_index":0,"source":"SYSTEM","type":"SYSTEM_MESSAGE","created_at":"2026-11-23T17:00:00Z","content":"<SYSTEM_MESSAGE>\n[Message] timestamp=2026-11-23T17:00:00Z sender=%s priority=MESSAGE_PRIORITY_HIGH content=%s\n</SYSTEM_MESSAGE>"}`, parentCID, longPrompt),
	})

	if got := resolveAgyTranscript(childCID, nil, map[string]string{"ANTIGRAVITY_APP_DATA_DIR": appDir}); got != fullTP {
		t.Fatalf("resolveAgyTranscript should prefer transcript_full.jsonl, got %q want %q", got, fullTP)
	}
	gotParent, gotPrompt := detectAgyParentFromTranscript(childCID, shortTP)
	if gotParent != parentCID || !strings.HasPrefix(gotPrompt, "详细任务上下文A") {
		t.Fatalf("detectAgyParentFromTranscript failed on >16KB Step 0 / sibling fallback: parent=%q promptLen=%d", gotParent, len(gotPrompt))
	}

	// 验证 agyHookAcceptsContext 仅对 SessionStart/PreInvocation/PostInvocation 返回 true
	for _, ev := range []string{"SessionStart", "PreInvocation", "PostInvocation"} {
		if !agyHookAcceptsContext(ev) {
			t.Fatalf("expected agyHookAcceptsContext(%q)=true", ev)
		}
	}
	for _, ev := range []string{"PreToolUse", "PostToolUse", "Stop", "Interrupt"} {
		if agyHookAcceptsContext(ev) {
			t.Fatalf("expected agyHookAcceptsContext(%q)=false", ev)
		}
	}

	// 验证 usageScanner.trackSubagent 支持把原先按独立会话登记的文件改归属到父会话
	q, err := openQueue(filepath.Join(t.TempDir(), "daemon.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer q.close()
	u, err := openUsage(q.db)
	if err != nil {
		t.Fatal(err)
	}
	u.now = func() time.Time { return time.Date(2026, 11, 23, 17, 5, 0, 0, time.UTC) }
	standaloneKey := protocol.SessionKey(protocol.ToolAgy, childCID)
	parentKey := protocol.SessionKey(protocol.ToolAgy, parentCID)
	u.track(standaloneKey, fullTP)
	u.trackSubagent(parentKey, childCID, fullTP)
	writeAgyTranscriptFull(t, appDir, childCID, []string{
		fmt.Sprintf(`{"step_index":0,"source":"SYSTEM","type":"SYSTEM_MESSAGE","created_at":"2026-11-23T17:00:00Z","content":"<SYSTEM_MESSAGE>\n[Message] sender=%s content=ok\n</SYSTEM_MESSAGE>"}`, parentCID),
		`{"step_index":1,"source":"MODEL","type":"PLANNER_RESPONSE","created_at":"2026-11-23T17:04:00Z","model":"gemini-4-carbon-high","input_tokens":75,"cache_read_tokens":200,"cache_write_tokens":0,"output_tokens":25,"content":"ok"}`,
	})
	u.scan()

	var parentIn, standaloneIn int
	_ = q.db.QueryRow(`SELECT COALESCE(SUM(inp),0) FROM usage_minutes WHERE session_key = ? AND agent_id = ?`, parentKey, childCID).Scan(&parentIn)
	_ = q.db.QueryRow(`SELECT COALESCE(SUM(inp),0) FROM usage_minutes WHERE session_key = ?`, standaloneKey).Scan(&standaloneIn)
	if parentIn != 75 || standaloneIn != 0 {
		t.Fatalf("trackSubagent re-attribution failed: parentIn=%d standaloneIn=%d", parentIn, standaloneIn)
	}
}
