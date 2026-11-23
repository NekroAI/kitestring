package daemon

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/NekroAI/kitestring/internal/protocol"
	"github.com/NekroAI/kitestring/internal/summary"
)

const agyRoleNote = `【Antigravity（agy）环境提示】
- 你正在 Google Antigravity 环境中运行；如终端开启了沙箱导致 ks CLI 报错 operation not permitted，请带 BypassSandbox=true 执行 ks role / ks wait / ks secret run 等 CLI 命令。
- MCP 工具（mcp__kitestring__*）运行在宿主机，可直接调用；结束前按要求用 ks wait 待命，待命期间会通过唤醒机制收到新消息。`

type agyTurnState struct {
	lastUserStep       int
	lastCheckpointStep int
	lastWakeStep       int
	lastNumSteps       int
	lastInstance       string
	lastUserInput      string
	inTurn             bool
	firstUserSeen      bool
	seeded             bool
}

type agyFoldResult struct {
	event        string
	source       string
	prompt       string
	wakeNoteHint string
	emitCompact  bool
	skip         bool
}

type agySubInfo struct {
	parentKey   string
	parentSID   string
	childCID    string
	role        string
	typeName    string
	prompt      string
	transcript  string
	startedAt   time.Time
	startedSent bool
	stopped     bool
}

func (s *agySubInfo) agentType() string {
	if s == nil {
		return ""
	}
	if s.typeName != "" {
		return s.typeName
	}
	return "subagent"
}

func (s *agySubInfo) label() string {
	if s == nil {
		return ""
	}
	if s.role != "" {
		return summary.Truncate(strings.Join(strings.Fields(s.role), " "), 80)
	}
	if s.prompt != "" {
		p := strings.Join(strings.Fields(s.prompt), " ")
		if i := strings.IndexAny(p, "。！？"); i > 8 {
			p = p[:i]
		}
		return summary.Truncate(p, 60)
	}
	return s.typeName
}

type agyPendingMCP struct {
	sessionKey string
	agentID    string
	cid        string
	toolName   string
	argsHash   string
	createdAt  time.Time
	claimed    bool
}

type agyStep struct {
	StepIndex        int           `json:"step_index"`
	Source           string        `json:"source"`
	Type             string        `json:"type"`
	CreatedAt        string        `json:"created_at"`
	Model            string        `json:"model"`
	InputTokens      int           `json:"input_tokens"`
	CacheReadTokens  int           `json:"cache_read_tokens"`
	CacheWriteTokens int           `json:"cache_write_tokens"`
	OutputTokens     int           `json:"output_tokens"`
	Content          string        `json:"content"`
	ToolCalls        []agyToolCall `json:"tool_calls"`
}

type agyToolCall struct {
	Name string `json:"name"`
	Args any    `json:"args"`
}

type agyUsageSample struct {
	DedupID          string
	Model            string
	Timestamp        time.Time
	InputTokens      int
	CacheReadTokens  int
	CacheWriteTokens int
	OutputTokens     int
}

var (
	agyUUIDRe        = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)
	agyUUIDStrictRe  = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	agySysMsgLineRe  = regexp.MustCompile(`(?s)\[Message\][^\n]*?sender=([^\s]+)[^\n]*?content=(.*)$`)
	agyWinDriveURIRe = regexp.MustCompile(`^/[A-Za-z]:/`)
)

func fileURIToPath(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if strings.HasPrefix(raw, "file://") {
		if u, err := url.Parse(raw); err == nil && u.Scheme == "file" {
			p := u.Path
			if agyWinDriveURIRe.MatchString(p) {
				return filepath.FromSlash(p[1:])
			}
			return p
		}
		raw = strings.TrimPrefix(raw, "file://")
	}
	return raw
}

func (d *Daemon) ensureAgyMapsLocked() {
	if d.agyTurns == nil {
		d.agyTurns = make(map[string]*agyTurnState)
	}
	if d.agySubs == nil {
		d.agySubs = make(map[string]*agySubInfo)
	}
	if d.agyPendingSubs == nil {
		d.agyPendingSubs = make(map[string][]*agySubInfo)
	}
	if d.agyMCP == nil {
		d.agyMCP = make(map[string][]*agyPendingMCP)
	}
}

func firstNonEmptyString(payload map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := payload[k].(string); ok {
			if s := strings.TrimSpace(v); s != "" {
				return s
			}
		}
	}
	return ""
}

func firstInt(payload map[string]any, keys ...string) (int, bool) {
	for _, k := range keys {
		switch v := payload[k].(type) {
		case float64:
			return int(v), true
		case int:
			return v, true
		case int64:
			return int(v), true
		case json.Number:
			if n, err := v.Int64(); err == nil {
				return int(n), true
			}
		}
	}
	return 0, false
}

func agySID(payload map[string]any) string {
	return firstNonEmptyString(payload, "conversationId", "conversation_id", "sessionId", "session_id")
}

func normalizeAgyMCPServer(srv string) string {
	srv = strings.TrimSpace(srv)
	if srv == "kitestring_kitestring" || srv == "kitestring" || strings.HasSuffix(srv, "_kitestring") {
		return "kitestring"
	}
	return srv
}

// normalizeAgyPayload 将 Antigravity hook 的 camelCase / toolCall 字段补齐为 daemon 通用的 snake_case 键。
func normalizeAgyPayload(event string, payload map[string]any, env map[string]string) string {
	if payload == nil {
		return event
	}
	if event == "" {
		event = firstNonEmptyString(payload, "hook_event_name", "hookEventName")
	}
	if event == "Stop" && strings.EqualFold(firstNonEmptyString(payload, "terminationReason", "termination_reason"), "USER_CANCELED") {
		event = "Interrupt"
		payload["hook_event_name"] = "Interrupt"
	}
	sid := agySID(payload)
	if sid != "" && firstNonEmptyString(payload, "session_id") == "" {
		payload["session_id"] = sid
	}
	if tp := resolveAgyTranscript(sid, payload, env); tp != "" && firstNonEmptyString(payload, "transcript_path") == "" {
		payload["transcript_path"] = tp
	}
	if cwd := extractAgyCwd(payload); cwd != "" && firstNonEmptyString(payload, "cwd") == "" {
		payload["cwd"] = cwd
	}
	if tc, ok := payload["toolCall"].(map[string]any); ok {
		if firstNonEmptyString(payload, "tool_use_id", "toolUseId", "toolCallId", "callId") == "" {
			if tid := firstNonEmptyString(tc, "id", "toolUseId", "toolCallId", "callId"); tid != "" {
				payload["tool_use_id"] = tid
			}
		}
		if firstNonEmptyString(payload, "tool_name", "toolName") == "" {
			if tn := firstNonEmptyString(tc, "name", "toolName"); tn != "" {
				payload["tool_name"] = tn
			}
		}
		if _, has := payload["tool_input"]; !has {
			for _, k := range []string{"args", "arguments", "input", "toolInput"} {
				if v, exists := tc[k]; exists && v != nil {
					payload["tool_input"] = v
					break
				}
			}
		}
	}
	if tn := firstNonEmptyString(payload, "tool_name", "toolName"); tn != "" && firstNonEmptyString(payload, "tool_name") == "" {
		payload["tool_name"] = tn
	}
	if _, ok := payload["tool_input"]; !ok {
		for _, k := range []string{"toolInput", "arguments", "args", "input"} {
			if v, exists := payload[k]; exists && v != nil {
				payload["tool_input"] = v
				break
			}
		}
	}
	if rawIn, ok := payload["tool_input"].(string); ok {
		var m map[string]any
		if json.Unmarshal([]byte(rawIn), &m) == nil && m != nil {
			payload["tool_input"] = m
		}
	}
	if tid := firstNonEmptyString(payload, "tool_use_id", "toolUseId", "toolCallId", "callId"); tid != "" && firstNonEmptyString(payload, "tool_use_id") == "" {
		payload["tool_use_id"] = tid
	}
	if firstNonEmptyString(payload, "tool_use_id") == "" && firstNonEmptyString(payload, "tool_name") != "" {
		if stepIdx, ok := firstInt(payload, "stepIdx", "step_idx"); ok {
			payload["tool_use_id"] = fmt.Sprintf("agy-step-%d", stepIdx)
		}
	}
	if tn := firstNonEmptyString(payload, "tool_name"); tn == "call_mcp_tool" {
		if in, ok := payload["tool_input"].(map[string]any); ok {
			srv := normalizeAgyMCPServer(firstNonEmptyString(in, "ServerName", "serverName", "server_name"))
			mcpName := firstNonEmptyString(in, "ToolName", "toolName", "tool_name")
			var mcpArgs any = map[string]any{}
			for _, ak := range []string{"Arguments", "arguments", "args"} {
				if v, exists := in[ak]; exists && v != nil {
					mcpArgs = v
					break
				}
			}
			if rawArgs, ok := mcpArgs.(string); ok {
				var m map[string]any
				if json.Unmarshal([]byte(rawArgs), &m) == nil && m != nil {
					mcpArgs = m
				}
			}
			if mcpName != "" {
				if srv != "" {
					payload["tool_name"] = "mcp__" + srv + "__" + mcpName
				} else {
					payload["tool_name"] = mcpName
				}
				payload["tool_input"] = mcpArgs
			}
		}
	} else if tn != "" && !strings.HasPrefix(tn, "mcp__") {
		if bare, ok := protocol.NormalizeMCPToolName(tn); ok {
			payload["tool_name"] = "mcp__kitestring__" + bare
		}
	}
	if _, ok := payload["tool_response"]; !ok {
		for _, k := range []string{"toolResponse", "toolOutput", "tool_output", "output", "result"} {
			if v, exists := payload[k]; exists && v != nil {
				payload["tool_response"] = v
				break
			}
		}
	}
	if prompt := firstNonEmptyString(payload, "prompt", "userPrompt", "user_prompt", "message"); prompt != "" && firstNonEmptyString(payload, "prompt") == "" {
		payload["prompt"] = prompt
	}
	if lastUI := firstNonEmptyString(payload, "lastUserInput", "last_user_input"); lastUI != "" && firstNonEmptyString(payload, "last_user_input") == "" {
		payload["last_user_input"] = lastUI
	}
	if model := firstNonEmptyString(payload, "model", "modelName", "model_name"); model != "" && firstNonEmptyString(payload, "model") == "" {
		payload["model"] = model
	}
	if src := firstNonEmptyString(payload, "source", "trigger"); src != "" && firstNonEmptyString(payload, "source") == "" {
		payload["source"] = src
	}
	if reason := firstNonEmptyString(payload, "reason", "stopReason"); reason != "" && firstNonEmptyString(payload, "reason") == "" {
		payload["reason"] = reason
	}
	if aid := firstNonEmptyString(payload, "agent_id", "agentId", "subagentConversationId", "childConversationId", "subagentId"); aid != "" && firstNonEmptyString(payload, "agent_id") == "" {
		payload["agent_id"] = aid
	}
	if atype := firstNonEmptyString(payload, "agent_type", "agentType", "subagentType", "typeName"); atype != "" && firstNonEmptyString(payload, "agent_type") == "" {
		payload["agent_type"] = atype
	}
	return event
}

func extractAgyCwd(payload map[string]any) string {
	if cwd := firstNonEmptyString(payload, "cwd", "workspacePath", "workspace_path", "workingDirectory", "working_directory"); cwd != "" {
		return fileURIToPath(cwd)
	}
	for _, k := range []string{"workspacePaths", "workspaceUris", "workspace_paths", "workspace_uris"} {
		if arr, ok := payload[k].([]any); ok && len(arr) > 0 {
			if s, ok := arr[0].(string); ok && strings.TrimSpace(s) != "" {
				return fileURIToPath(s)
			}
		}
	}
	return ""
}

func resolveAgyTranscript(sid string, payload map[string]any, env map[string]string) string {
	if tp := firstNonEmptyString(payload, "transcript_path", "transcriptPath"); tp != "" {
		return tp
	}
	if sid == "" {
		return ""
	}
	var dirs []string
	if env != nil {
		if d := strings.TrimSpace(env["ANTIGRAVITY_APP_DATA_DIR"]); d != "" {
			dirs = append(dirs, d)
		}
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		dirs = append(dirs,
			filepath.Join(home, ".gemini", "antigravity-cli"),
			filepath.Join(home, ".gemini", "antigravity-ide"),
			filepath.Join(home, ".gemini", "antigravity"),
		)
	}
	for _, dir := range dirs {
		cand := filepath.Join(dir, "brain", sid, ".system_generated", "logs", "transcript.jsonl")
		if info, err := os.Stat(cand); err == nil && !info.IsDir() {
			return cand
		}
	}
	return ""
}

func detectAgyClient(payload map[string]any, env map[string]string, cmdLines []string) string {
	if c := strings.ToLower(firstNonEmptyString(payload, "client", "surface", "entrypoint")); c != "" {
		switch {
		case strings.Contains(c, "ide") || strings.Contains(c, "vscode"):
			return "ide"
		case strings.Contains(c, "app") || strings.Contains(c, "desktop"):
			return "app"
		case strings.Contains(c, "cli"):
			return "cli"
		}
	}
	if p := firstNonEmptyString(payload, "transcriptPath", "transcript_path", "artifactDirectoryPath", "artifact_directory_path"); p != "" {
		low := strings.ToLower(filepath.ToSlash(p))
		switch {
		case strings.Contains(low, "antigravity-ide"):
			return "ide"
		case strings.Contains(low, "antigravity-cli"):
			return "cli"
		case strings.Contains(low, "/.gemini/antigravity/"):
			return "app"
		}
	}
	for _, line := range cmdLines {
		low := strings.ToLower(line)
		switch {
		case strings.Contains(low, "antigravity.app") || strings.Contains(low, "antigravity ide") || strings.Contains(low, "code helper") || strings.Contains(low, "electron"):
			return "ide"
		case strings.Contains(low, "antigravity 2") || strings.Contains(low, "antigravity app"):
			return "app"
		case strings.Contains(low, "/agy") || strings.HasPrefix(low, "agy ") || low == "agy":
			return "cli"
		}
	}
	if env != nil {
		tp := strings.ToLower(env["TERM_PROGRAM"])
		switch {
		case strings.Contains(tp, "vscode") || strings.Contains(tp, "antigravity"):
			return "ide"
		case tp != "":
			return "cli"
		}
		if appDir := strings.ToLower(env["ANTIGRAVITY_APP_DATA_DIR"]); strings.Contains(appDir, "antigravity-cli") {
			return "cli"
		} else if appDir != "" {
			return "app"
		}
	}
	return "cli"
}

func agyContextWindow(model string) int {
	low := strings.ToLower(strings.TrimSpace(model))
	switch {
	case low == "":
		return 0
	case strings.Contains(low, "[1m]"):
		return 1_000_000
	case strings.Contains(low, "gemini"):
		return 1_000_000
	case strings.Contains(low, "claude"):
		return 200_000
	case strings.Contains(low, "gpt"):
		return 200_000
	default:
		return 1_000_000
	}
}

func parseAgyUsageLine(line []byte) (agyUsageSample, bool) {
	var s agyStep
	if json.Unmarshal(line, &s) != nil {
		return agyUsageSample{}, false
	}
	if s.Source != "MODEL" || s.Type == "GENERIC" {
		return agyUsageSample{}, false
	}
	if s.InputTokens == 0 && s.CacheReadTokens == 0 && s.CacheWriteTokens == 0 && s.OutputTokens == 0 {
		return agyUsageSample{}, false
	}
	ts, err := time.Parse(time.RFC3339Nano, s.CreatedAt)
	if err != nil {
		ts = time.Now().UTC()
	}
	model := strings.TrimSpace(s.Model)
	if model == "<synthetic>" || strings.EqualFold(model, "auto") {
		model = ""
	}
	return agyUsageSample{
		DedupID:          fmt.Sprintf("agy:%d:%s", s.StepIndex, s.CreatedAt),
		Model:            model,
		Timestamp:        ts,
		InputTokens:      s.InputTokens,
		CacheReadTokens:  s.CacheReadTokens,
		CacheWriteTokens: s.CacheWriteTokens,
		OutputTokens:     s.OutputTokens,
	}, true
}

func agyContextUsageFromTail(data []byte) (used, window int, ok bool) {
	lines := bytes.Split(data, []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		line := bytes.TrimSpace(lines[i])
		if len(line) == 0 {
			continue
		}
		var s agyStep
		if json.Unmarshal(line, &s) != nil {
			continue
		}
		if s.Source != "MODEL" || s.Type == "GENERIC" {
			continue
		}
		total := s.InputTokens + s.CacheReadTokens + s.CacheWriteTokens + s.OutputTokens
		if total <= 0 {
			continue
		}
		return total, agyContextWindow(s.Model), true
	}
	return 0, 0, false
}

func agyReplyFromTail(data []byte) string {
	lines := bytes.Split(data, []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		line := bytes.TrimSpace(lines[i])
		if len(line) == 0 {
			continue
		}
		var s agyStep
		if json.Unmarshal(line, &s) != nil {
			continue
		}
		if s.Source != "MODEL" || s.Type == "GENERIC" {
			continue
		}
		if txt := strings.TrimSpace(s.Content); txt != "" {
			return txt
		}
	}
	return ""
}

type agyMeta struct {
	userStep       int
	userPrompt     string
	model          string
	modelStep      int
	checkpointStep int
	wakeStep       int
	wakeRaw        string
	wakeTaskDesc   string
}

func readAgyTranscriptMeta(path string) agyMeta {
	meta := agyMeta{userStep: -1, modelStep: -1, checkpointStep: -1, wakeStep: -1}
	if path == "" {
		return meta
	}
	data := readTailBytes(path, 256<<10)
	if len(data) == 0 {
		return meta
	}
	taskDescs := map[string]string{}
	lines := bytes.Split(data, []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		line := bytes.TrimSpace(lines[i])
		if len(line) == 0 {
			continue
		}
		var s agyStep
		if json.Unmarshal(line, &s) != nil {
			continue
		}
		if s.Type == "GENERIC" && s.Content != "" && strings.Contains(s.Content, "background task with task id:") {
			for _, m := range agyBgStartRe.FindAllStringSubmatch(s.Content, -1) {
				fullID := strings.TrimSpace(m[1])
				desc := strings.TrimSpace(m[2])
				if fullID != "" && desc != "" {
					taskDescs[fullID] = desc
				}
			}
		}
		if meta.model == "" && s.Source == "MODEL" {
			if meta.modelStep < 0 {
				meta.modelStep = s.StepIndex
			}
			m := strings.TrimSpace(s.Model)
			if m != "" && m != "<synthetic>" && !strings.EqualFold(m, "auto") {
				meta.model = m
			}
		} else if meta.modelStep < 0 && s.Source == "MODEL" {
			meta.modelStep = s.StepIndex
		}
		if meta.userStep < 0 && (s.Source == "USER_EXPLICIT" || s.Type == "USER_INPUT") {
			meta.userStep = s.StepIndex
			meta.userPrompt = strings.TrimSpace(s.Content)
		}
		if meta.checkpointStep < 0 && s.Source == "SYSTEM" && s.Type == "CHECKPOINT" {
			meta.checkpointStep = s.StepIndex
		}
		if meta.wakeStep < 0 && s.Source == "SYSTEM" && s.Type == "SYSTEM_MESSAGE" && strings.Contains(s.Content, "[Message]") {
			meta.wakeStep = s.StepIndex
			meta.wakeRaw = strings.TrimSpace(s.Content)
		}
	}
	if meta.wakeRaw != "" {
		if m := agySysMsgLineRe.FindStringSubmatch(meta.wakeRaw); m != nil {
			sender := strings.TrimSpace(m[1])
			if desc := taskDescs[sender]; desc != "" {
				meta.wakeTaskDesc = desc
			}
		}
	}
	return meta
}

func agyTranscriptMeta(path string) (latestUserStep int, latestUserPrompt, latestModel string) {
	m := readAgyTranscriptMeta(path)
	return m.userStep, m.userPrompt, m.model
}

func readTailBytes(path string, maxBytes int64) []byte {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil
	}
	size := info.Size()
	if size <= 0 {
		return nil
	}
	offset := int64(0)
	if size > maxBytes {
		offset = size - maxBytes
	}
	if _, err := f.Seek(offset, 0); err != nil {
		return nil
	}
	buf := make([]byte, size-offset)
	n, _ := f.Read(buf)
	buf = buf[:n]
	if offset > 0 {
		if idx := bytes.IndexByte(buf, '\n'); idx >= 0 && idx+1 < len(buf) {
			buf = buf[idx+1:]
		}
	}
	return buf
}

// foldAgyPreInvocationLocked 把同一轮内重复触发的 PreInvocation 折叠成单次 UserPromptSubmit，
// 同时识别 CHECKPOINT 上下文压缩、resume、以及后台任务 / 子代理结束后的自动唤醒。
func (d *Daemon) foldAgyPreInvocationLocked(key, instance string, payload map[string]any, transcriptPath string) agyFoldResult {
	d.ensureAgyMapsLocked()
	st := d.agyTurns[key]
	if st == nil {
		st = &agyTurnState{lastUserStep: -1, lastCheckpointStep: -1, lastWakeStep: -1}
		d.agyTurns[key] = st
	}
	var src string
	if instance != "" && st.lastInstance != "" && st.lastInstance != instance {
		// 宿主终端或进程实例变化，视作恢复会话（resume）以便重投角色与未读团队消息。
		src = "resume"
	}
	if instance != "" {
		st.lastInstance = instance
	}

	meta := readAgyTranscriptMeta(transcriptPath)
	explicitPrompt := firstNonEmptyString(payload, "prompt")
	lastUI := firstNonEmptyString(payload, "last_user_input")

	if !st.seeded {
		st.seeded = true
		if meta.checkpointStep >= 0 && (meta.userStep > meta.checkpointStep || meta.modelStep > meta.checkpointStep) {
			st.lastCheckpointStep = meta.checkpointStep
		}
		if meta.wakeStep >= 0 && (meta.userStep > meta.wakeStep || meta.modelStep > meta.wakeStep) {
			st.lastWakeStep = meta.wakeStep
		}
	}

	numSteps, hasNumSteps := firstInt(payload, "initialNumSteps", "initial_num_steps")
	srcHint := firstNonEmptyString(payload, "source")
	newCheckpoint := meta.checkpointStep >= 0 && meta.checkpointStep > st.lastCheckpointStep
	if newCheckpoint {
		st.lastCheckpointStep = meta.checkpointStep
	}
	checkSlash := explicitPrompt
	if checkSlash == "" {
		checkSlash = meta.userPrompt
	}
	isCompact := srcHint == "compact" ||
		strings.HasPrefix(strings.TrimSpace(checkSlash), "/compact") ||
		newCheckpoint ||
		(hasNumSteps && st.lastNumSteps > 0 && numSteps > 0 && numSteps < st.lastNumSteps)
	if hasNumSteps && numSteps > 0 {
		st.lastNumSteps = numSteps
	}

	newExplicitStep := meta.userStep >= 0 && meta.userStep > st.lastUserStep
	newLastUI := lastUI != "" && lastUI != st.lastUserInput

	chooseUserPrompt := func() string {
		if explicitPrompt != "" {
			return explicitPrompt
		}
		if newExplicitStep && meta.userPrompt != "" {
			return meta.userPrompt
		}
		if lastUI != "" {
			return lastUI
		}
		return meta.userPrompt
	}

	if isCompact {
		st.inTurn = true
		st.firstUserSeen = true
		if meta.userStep >= 0 {
			st.lastUserStep = meta.userStep
		}
		if lastUI != "" {
			st.lastUserInput = lastUI
		}
		prompt := ""
		if explicitPrompt != "" || newExplicitStep || newLastUI {
			prompt = chooseUserPrompt()
		}
		return agyFoldResult{event: "UserPromptSubmit", source: "compact", prompt: prompt, emitCompact: true}
	}

	if src == "resume" || srcHint == "resume" {
		st.inTurn = true
		st.firstUserSeen = true
		if meta.userStep >= 0 {
			st.lastUserStep = meta.userStep
		}
		if lastUI != "" {
			st.lastUserInput = lastUI
		}
		return agyFoldResult{event: "UserPromptSubmit", source: "resume", prompt: chooseUserPrompt()}
	}

	// Turn 1 启动时 transcript 尚未落盘 USER_EXPLICIT（先用 lastUserInput 启动），
	// 同轮后续 PreInvocation 时 USER_EXPLICIT 才落盘：不要误判成新插话或新 turn。
	if st.inTurn && st.lastUserStep < 0 && newExplicitStep && !newLastUI {
		st.lastUserStep = meta.userStep
		return agyFoldResult{event: "PreInvocation", skip: true}
	}

	newWakeStep := explicitPrompt == "" && meta.wakeStep > meta.userStep && meta.wakeStep > st.lastWakeStep && !newLastUI
	if st.inTurn && !newExplicitStep && !newLastUI && !newWakeStep {
		return agyFoldResult{event: "PreInvocation", skip: true}
	}

	if newWakeStep {
		st.lastWakeStep = meta.wakeStep
		st.inTurn = true
		st.firstUserSeen = true
		if meta.userStep >= 0 {
			st.lastUserStep = meta.userStep
		}
		return agyFoldResult{
			event:        "UserPromptSubmit",
			source:       protocol.SourceAuto,
			prompt:       meta.wakeRaw,
			wakeNoteHint: meta.wakeTaskDesc,
		}
	}

	// 开启一个新的用户 / 自动唤醒 turn
	if st.firstUserSeen && !newExplicitStep && !newLastUI && explicitPrompt == "" {
		st.inTurn = true
		return agyFoldResult{event: "UserPromptSubmit", source: protocol.SourceAuto, prompt: meta.wakeRaw, wakeNoteHint: meta.wakeTaskDesc}
	}

	st.inTurn = true
	st.firstUserSeen = true
	prompt := chooseUserPrompt()
	if meta.userStep >= 0 {
		st.lastUserStep = meta.userStep
	}
	if lastUI != "" {
		st.lastUserInput = lastUI
	}
	return agyFoldResult{event: "UserPromptSubmit", source: src, prompt: prompt}
}

func (d *Daemon) finishAgyStopLocked(key, transcriptPath string) {
	if d.agyTurns == nil {
		return
	}
	st := d.agyTurns[key]
	if st == nil {
		return
	}
	st.inTurn = false
	if st.lastUserStep < 0 && transcriptPath != "" {
		meta := readAgyTranscriptMeta(transcriptPath)
		if meta.userStep >= 0 {
			st.lastUserStep = meta.userStep
		}
		if meta.wakeStep >= 0 && meta.wakeStep > st.lastWakeStep {
			st.lastWakeStep = meta.wakeStep
		}
	}
}

func normalizeAgyMCPTool(name string) (string, bool) {
	return protocol.NormalizeMCPToolName(name)
}

func canonicalArgsHash(v any) string {
	return protocol.CanonicalArgsHash(v)
}

func (d *Daemon) pruneAgyMCPLocked(now time.Time) {
	for k, list := range d.agyMCP {
		kept := list[:0]
		for _, item := range list {
			if now.Sub(item.createdAt) <= 30*time.Second {
				kept = append(kept, item)
			}
		}
		if len(kept) == 0 {
			delete(d.agyMCP, k)
		} else {
			d.agyMCP[k] = kept
		}
	}
}

func (d *Daemon) rememberAgyMCPLocked(key, agentID, sid, rawTool string, toolInput any) {
	bare, ok := normalizeAgyMCPTool(rawTool)
	if !ok {
		return
	}
	d.ensureAgyMapsLocked()
	now := time.Now()
	d.pruneAgyMCPLocked(now)
	h := canonicalArgsHash(toolInput)
	k := bare + "|" + h
	d.agyMCP[k] = append(d.agyMCP[k], &agyPendingMCP{
		sessionKey: key,
		agentID:    agentID,
		cid:        sid,
		toolName:   bare,
		argsHash:   h,
		createdAt:  now,
	})
}

func (d *Daemon) releaseAgyMCPLocked(key, rawTool string, toolInput any) {
	bare, ok := normalizeAgyMCPTool(rawTool)
	if !ok || d.agyMCP == nil {
		return
	}
	h := canonicalArgsHash(toolInput)
	k := bare + "|" + h
	list := d.agyMCP[k]
	if len(list) == 0 {
		return
	}
	for i, item := range list {
		if item.sessionKey == key {
			list = append(list[:i], list[i+1:]...)
			break
		}
	}
	if len(list) == 0 {
		delete(d.agyMCP, k)
	} else {
		d.agyMCP[k] = list
	}
}

func (d *Daemon) claimAgyMCPLocked(rawTool, argsHash string) (sessionKey, agentID string) {
	bare, ok := normalizeAgyMCPTool(rawTool)
	if !ok || d.agyMCP == nil {
		return "", ""
	}
	now := time.Now()
	d.pruneAgyMCPLocked(now)
	if argsHash == "" {
		argsHash = canonicalArgsHash(nil)
	}
	list := d.agyMCP[bare+"|"+argsHash]
	for _, item := range list {
		if !item.claimed {
			item.claimed = true
			return item.sessionKey, item.agentID
		}
	}
	if len(list) == 1 {
		// 单次调用刚被同工具记录过时容错复用（防同一次调用内重复查会话）。
		if now.Sub(list[0].createdAt) <= 5*time.Second {
			return list[0].sessionKey, list[0].agentID
		}
	}
	return "", ""
}

func (d *Daemon) agySessionByCIDLocked(cid string) string {
	if cid == "" {
		return ""
	}
	cand := protocol.ToolAgy + ":" + cid
	if d.sessions[cand] != nil {
		return cand
	}
	return ""
}

func extractAgySubInput(payload map[string]any) (role, typeName, prompt string) {
	in, _ := payload["tool_input"].(map[string]any)
	if in == nil {
		return "", "", ""
	}
	role = firstNonEmptyString(in, "Role", "role")
	typeName = firstNonEmptyString(in, "TypeName", "typeName", "subagent_type")
	prompt = firstNonEmptyString(in, "Prompt", "prompt")
	return
}

func sameNormText(a, b string) bool {
	na := strings.Join(strings.Fields(a), " ")
	nb := strings.Join(strings.Fields(b), " ")
	return na != "" && nb != "" && (na == nb || (len(na) >= 24 && strings.HasPrefix(nb, na)) || (len(nb) >= 24 && strings.HasPrefix(na, nb)))
}

func (d *Daemon) pruneAgyPendingSubsLocked(parentSID string, now time.Time) []*agySubInfo {
	q := d.agyPendingSubs[parentSID]
	kept := q[:0]
	for _, item := range q {
		if now.Sub(item.startedAt) <= 15*time.Minute {
			kept = append(kept, item)
		}
	}
	if len(kept) == 0 {
		delete(d.agyPendingSubs, parentSID)
		return nil
	}
	d.agyPendingSubs[parentSID] = kept
	return kept
}

func (d *Daemon) noteAgySubagentPreLocked(parentKey, parentSID string, payload map[string]any) {
	if parentSID == "" {
		return
	}
	d.ensureAgyMapsLocked()
	now := time.Now()
	q := d.pruneAgyPendingSubsLocked(parentSID, now)
	role, typeName, prompt := extractAgySubInput(payload)
	d.agyPendingSubs[parentSID] = append(q, &agySubInfo{
		parentKey: parentKey,
		parentSID: parentSID,
		role:      role,
		typeName:  typeName,
		prompt:    prompt,
		startedAt: now,
	})
}

func (d *Daemon) popPendingAgySubLocked(parentSID, roleHint, typeHint, promptHint string) *agySubInfo {
	if d.agyPendingSubs == nil {
		return nil
	}
	q := d.pruneAgyPendingSubsLocked(parentSID, time.Now())
	if len(q) == 0 {
		return nil
	}
	idx := -1
	for i, item := range q {
		if promptHint != "" && sameNormText(item.prompt, promptHint) {
			idx = i
			break
		}
	}
	if idx < 0 && (typeHint != "" || roleHint != "") {
		for i, item := range q {
			if (typeHint == "" || item.typeName == typeHint) && (roleHint == "" || item.role == roleHint) {
				idx = i
				break
			}
		}
	}
	if idx < 0 && promptHint == "" && roleHint == "" && typeHint == "" {
		idx = 0
	}
	if idx < 0 && len(q) == 1 && typeHint == "" && roleHint == "" {
		idx = 0
	}
	if idx < 0 {
		return nil
	}
	picked := q[idx]
	rest := append(q[:idx:idx], q[idx+1:]...)
	if len(rest) == 0 {
		delete(d.agyPendingSubs, parentSID)
	} else {
		d.agyPendingSubs[parentSID] = rest
	}
	return picked
}

func (d *Daemon) discardAgySubagentPreLocked(parentSID string, payload map[string]any) {
	role, typeName, prompt := extractAgySubInput(payload)
	_ = d.popPendingAgySubLocked(parentSID, role, typeName, prompt)
}

func detectAgyParentFromTranscript(childSID, path string) (parentSID, prompt string) {
	if path == "" {
		return "", ""
	}
	f, err := os.Open(path)
	if err != nil {
		return "", ""
	}
	defer f.Close()
	var buf [16 << 10]byte
	n, _ := f.Read(buf[:])
	if n == 0 {
		return "", ""
	}
	firstLine, _, _ := strings.Cut(string(buf[:n]), "\n")
	firstLine = strings.TrimSpace(firstLine)
	if firstLine == "" {
		return "", ""
	}
	var s agyStep
	if json.Unmarshal([]byte(firstLine), &s) != nil {
		return "", ""
	}
	if s.Source != "SYSTEM" || s.Type != "SYSTEM_MESSAGE" || s.StepIndex > 1 {
		return "", ""
	}
	m := agySysMsgLineRe.FindStringSubmatch(s.Content)
	if m == nil {
		return "", ""
	}
	sender := strings.TrimSpace(m[1])
	if !agyUUIDStrictRe.MatchString(sender) || sender == childSID {
		return "", ""
	}
	return sender, strings.TrimSpace(strings.TrimSuffix(m[2], "</SYSTEM_MESSAGE>"))
}

func (d *Daemon) resolveAgySubagentLocked(sid string, payload map[string]any, env map[string]string) *agySubInfo {
	if sid == "" {
		return nil
	}
	d.ensureAgyMapsLocked()
	tp := firstNonEmptyString(payload, "transcript_path")
	if sub := d.agySubs[sid]; sub != nil {
		if sub.transcript == "" && tp != "" {
			sub.transcript = tp
		}
		return sub
	}
	if st := d.agyTurns[protocol.SessionKey(protocol.ToolAgy, sid)]; st != nil && st.firstUserSeen {
		return nil
	}
	parentSID, initPrompt := detectAgyParentFromTranscript(sid, tp)
	if parentSID == "" {
		if lastUI := firstNonEmptyString(payload, "last_user_input"); lastUI != "" {
			now := time.Now()
			for pSID := range d.agyPendingSubs {
				if pSID == sid {
					continue
				}
				for _, item := range d.pruneAgyPendingSubsLocked(pSID, now) {
					if len(strings.TrimSpace(item.prompt)) >= 10 && sameNormText(item.prompt, lastUI) {
						parentSID, initPrompt = pSID, item.prompt
						break
					}
				}
				if parentSID != "" {
					break
				}
			}
		}
	}
	if parentSID == "" {
		return nil
	}
	pending := d.popPendingAgySubLocked(parentSID, "", "", initPrompt)
	if tp == "" {
		tp = resolveAgyTranscript(sid, nil, env)
	}
	info := &agySubInfo{
		parentKey:  protocol.SessionKey(protocol.ToolAgy, parentSID),
		parentSID:  parentSID,
		childCID:   sid,
		prompt:     initPrompt,
		transcript: tp,
		startedAt:  time.Now(),
	}
	if pending != nil {
		info.role = pending.role
		info.typeName = pending.typeName
		if info.prompt == "" {
			info.prompt = pending.prompt
		}
	}
	d.agySubs[sid] = info
	return info
}

func (d *Daemon) routeAgyChildLocked(sid, name string, payload map[string]any, env map[string]string) (sub *agySubInfo, newName string, extra []protocol.Event, drop bool) {
	sub = d.resolveAgySubagentLocked(sid, payload, env)
	if sub == nil {
		return nil, name, nil, false
	}
	newName = name
	startEv := func() protocol.Event {
		return protocol.Event{
			ID:        eventID(),
			Time:      time.Now().UTC(),
			Tool:      protocol.ToolAgy,
			Name:      "SubagentStart",
			SessionID: sub.parentSID,
			AgentID:   sub.childCID,
			AgentType: sub.agentType(),
			ToolBrief: sub.label(),
		}
	}
	switch name {
	case "SessionStart", "PreInvocation":
		if !sub.startedSent {
			sub.startedSent = true
			return sub, "SubagentStart", nil, false
		}
		return sub, name, nil, true
	case "PreToolUse", "PostToolUse", "PostToolUseFailure":
		if !sub.startedSent {
			sub.startedSent = true
			extra = append(extra, startEv())
		}
		return sub, name, extra, false
	case "Stop", "Interrupt":
		if sub.stopped {
			return sub, name, nil, true
		}
		if !sub.startedSent {
			sub.startedSent = true
			extra = append(extra, startEv())
		}
		sub.stopped = true
		return sub, "SubagentStop", extra, false
	default:
		return sub, name, nil, false
	}
}

func (d *Daemon) activeAgySubsLocked(parentKey string) (int, string) {
	if d.agySubs == nil {
		return 0, ""
	}
	n, brief := 0, ""
	for _, info := range d.agySubs {
		if info.parentKey == parentKey && !info.stopped {
			n++
			if brief == "" {
				brief = info.label()
			}
		}
	}
	return n, brief
}

func (d *Daemon) noteAgySubagentToolLocked(parentKey, parentSID, transcriptPath string, payload map[string]any, env map[string]string) *agySubInfo {
	d.ensureAgyMapsLocked()
	role, typeName, prompt := extractAgySubInput(payload)

	childCID := extractChildCID(parentSID, payload["tool_response"])
	if childCID == "" && transcriptPath != "" {
		childCID = extractChildCIDFromTranscript(parentSID, transcriptPath)
	}
	if childCID == "" {
		childCID = firstNonEmptyString(payload, "agent_id")
		if childCID == parentSID {
			childCID = ""
		}
	}
	if childCID == "" {
		return nil
	}

	pending := d.popPendingAgySubLocked(parentSID, role, typeName, prompt)
	if pending != nil {
		if role == "" {
			role = pending.role
		}
		if typeName == "" {
			typeName = pending.typeName
		}
		if prompt == "" {
			prompt = pending.prompt
		}
	}

	childTranscript := ""
	if transcriptPath != "" && parentSID != "" && strings.Contains(transcriptPath, parentSID) {
		childTranscript = strings.ReplaceAll(transcriptPath, parentSID, childCID)
	}
	if childTranscript == "" {
		childTranscript = resolveAgyTranscript(childCID, nil, env)
	}
	if prev := d.agySubs[childCID]; prev != nil {
		if prev.role == "" {
			prev.role = role
		}
		if prev.typeName == "" {
			prev.typeName = typeName
		}
		if prev.prompt == "" {
			prev.prompt = prompt
		}
		if prev.transcript == "" {
			prev.transcript = childTranscript
		}
		return prev
	}
	info := &agySubInfo{
		parentKey:  parentKey,
		parentSID:  parentSID,
		childCID:   childCID,
		role:       role,
		typeName:   typeName,
		prompt:     prompt,
		transcript: childTranscript,
		startedAt:  time.Now(),
	}
	d.agySubs[childCID] = info
	return info
}

func (d *Daemon) findAgySubagentStopLocked(parentKey string, payload map[string]any) *agySubInfo {
	if d.agySubs == nil {
		return nil
	}
	parentSID := strings.TrimPrefix(parentKey, protocol.ToolAgy+":")
	if aid := firstNonEmptyString(payload, "agent_id"); aid != "" && aid != parentSID {
		if info := d.agySubs[aid]; info != nil {
			if info.stopped {
				return nil
			}
			info.stopped = true
			return info
		}
	}
	var latest *agySubInfo
	for _, info := range d.agySubs {
		if info.parentKey != parentKey || info.stopped {
			continue
		}
		if latest == nil || info.startedAt.After(latest.startedAt) {
			latest = info
		}
	}
	if latest != nil {
		latest.stopped = true
	}
	return latest
}

func agySubagentSummary(sub *agySubInfo, payload map[string]any) string {
	if reply := firstNonEmptyString(payload, "last_assistant_message", "lastMessage", "summary", "output"); reply != "" {
		return reply
	}
	if sub != nil && sub.transcript != "" {
		if data := readTailBytes(sub.transcript, 256<<10); len(data) > 0 {
			if reply := agyReplyFromTail(data); reply != "" {
				return reply
			}
		}
	}
	return ""
}

func extractChildCID(parentSID string, resp any) string {
	switch x := resp.(type) {
	case map[string]any:
		for _, k := range []string{"conversationId", "conversation_id", "agentId", "subagentConversationId", "id"} {
			if s, ok := x[k].(string); ok && s != "" && s != parentSID {
				return s
			}
		}
		raw, _ := json.Marshal(x)
		return findUUIDExcept(string(raw), parentSID)
	case string:
		return findUUIDExcept(x, parentSID)
	default:
		if resp == nil {
			return ""
		}
		raw, _ := json.Marshal(resp)
		return findUUIDExcept(string(raw), parentSID)
	}
}

func extractChildCIDFromTranscript(parentSID, path string) string {
	data := readTailBytes(path, 128<<10)
	if len(data) == 0 {
		return ""
	}
	lines := bytes.Split(data, []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(string(lines[i]))
		if line == "" {
			continue
		}
		var s agyStep
		if json.Unmarshal([]byte(line), &s) != nil {
			continue
		}
		if s.Type == "GENERIC" && s.Content != "" && (strings.Contains(s.Content, "subagent") || strings.Contains(s.Content, "conversationId")) {
			if cid := findUUIDExcept(s.Content, parentSID); cid != "" {
				return cid
			}
		}
	}
	return ""
}

func findUUIDExcept(text, exclude string) string {
	for _, m := range agyUUIDRe.FindAllString(text, -1) {
		if m != exclude {
			return m
		}
	}
	return ""
}
