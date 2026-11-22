package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/protocol"
)

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
	ev1, src1, p1, skip1 := d.foldAgyPreInvocationLocked(key, "100@t1", map[string]any{"initialNumSteps": float64(1)}, tp)
	if skip1 || ev1 != "UserPromptSubmit" || src1 != "" || p1 != "第一步：查仓库" {
		t.Fatalf("step 1 unexpected: ev=%q src=%q p=%q skip=%v", ev1, src1, p1, skip1)
	}

	// 2. 同一轮工具调用后的第二次 PreInvocation -> 折叠跳过
	ev2, _, _, skip2 := d.foldAgyPreInvocationLocked(key, "100@t1", map[string]any{"initialNumSteps": float64(3)}, tp)
	if !skip2 || ev2 != "PreInvocation" {
		t.Fatalf("step 2 expected fold skip=true, got ev=%q skip=%v", ev2, skip2)
	}

	// 3. 同一轮内用户插话产生更高 step_index 的 USER_EXPLICIT -> 新 turn
	writeAgyTranscript(t, appDir, cid, []string{
		`{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","content":"第一步：查仓库"}`,
		`{"step_index":3,"source":"USER_EXPLICIT","type":"USER_INPUT","content":"顺便加个测试"}`,
	})
	ev3, src3, p3, skip3 := d.foldAgyPreInvocationLocked(key, "100@t1", map[string]any{"initialNumSteps": float64(4)}, tp)
	if skip3 || ev3 != "UserPromptSubmit" || src3 != "" || p3 != "顺便加个测试" {
		t.Fatalf("step 3 expected queued prompt turn, got ev=%q src=%q p=%q skip=%v", ev3, src3, p3, skip3)
	}

	// 4. Stop 放行后，没有新的 USER_EXPLICIT 时再次触发 PreInvocation -> SourceAuto
	d.finishAgyStopLocked(key)
	ev4, src4, _, skip4 := d.foldAgyPreInvocationLocked(key, "100@t1", map[string]any{"initialNumSteps": float64(6)}, tp)
	if skip4 || ev4 != "UserPromptSubmit" || src4 != protocol.SourceAuto {
		t.Fatalf("step 4 expected SourceAuto wake, got ev=%q src=%q skip=%v", ev4, src4, skip4)
	}

	// 5. initialNumSteps 回落 -> compact
	ev5, src5, _, skip5 := d.foldAgyPreInvocationLocked(key, "100@t1", map[string]any{"initialNumSteps": float64(2)}, tp)
	if skip5 || ev5 != "UserPromptSubmit" || src5 != "compact" {
		t.Fatalf("step 5 expected compact, got ev=%q src=%q skip=%v", ev5, src5, skip5)
	}

	// 6. 实例变化 -> resume
	ev6, src6, _, skip6 := d.foldAgyPreInvocationLocked(key, "200@t2", map[string]any{"initialNumSteps": float64(3)}, tp)
	if skip6 || ev6 != "UserPromptSubmit" || src6 != "resume" {
		t.Fatalf("step 6 expected resume, got ev=%q src=%q skip=%v", ev6, src6, skip6)
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
	d.rememberAgyMCPLocked("agy:cid-a", "cid-a", "mcp:kitestring:team_tell", map[string]any{"to": "dev-1", "msg": "hi"})
	d.rememberAgyMCPLocked("agy:cid-b", "cid-b", "mcp__kitestring__team_tell", map[string]any{"msg": "hi", "to": "dev-1"})
	d.mu.Unlock()

	hLookup := canonicalArgsHash(json.RawMessage(`{"msg":"hi","to":"dev-1"}`))
	req := httptest.NewRequest(http.MethodGet, "/session?mcp_tool=team_tell&args_hash="+hLookup, nil)
	rec := httptest.NewRecorder()
	d.handleSessionLookup(rec, req)
	var out1 map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &out1)
	if out1["session_key"] != "agy:cid-a" {
		t.Fatalf("first FIFO lookup expected agy:cid-a, got %#v", out1)
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
