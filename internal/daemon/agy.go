package daemon

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/NekroAI/kitestring/internal/protocol"
)

const agyRoleNote = `【Antigravity（agy）环境提示】
- 你正在 Google Antigravity 环境中运行；如终端开启了沙箱导致 ks CLI 报错 operation not permitted，请带 BypassSandbox=true 执行 ks role / ks wait / ks secret run 等 CLI 命令。
- MCP 工具（mcp__kitestring__*）运行在宿主机，可直接调用；结束前按要求用 ks wait 待命，待命期间会通过唤醒机制收到新消息。`

type agyTurnState struct {
	lastUserStep  int
	lastNumSteps  int
	lastInstance  string
	inTurn        bool
	firstUserSeen bool
}

type agySubInfo struct {
	parentKey  string
	parentSID  string
	childCID   string
	role       string
	typeName   string
	prompt     string
	transcript string
	startedAt  time.Time
	stopped    bool
}

type agyPendingMCP struct {
	sessionKey string
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

var agyUUIDRe = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)

func (d *Daemon) ensureAgyMapsLocked() {
	if d.agyTurns == nil {
		d.agyTurns = make(map[string]*agyTurnState)
	}
	if d.agySubs == nil {
		d.agySubs = make(map[string]*agySubInfo)
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

// normalizeAgyPayload 将 Antigravity hook 的 camelCase 字段补齐为 daemon 通用的 snake_case 键。
func normalizeAgyPayload(event string, payload map[string]any, env map[string]string) string {
	if payload == nil {
		return event
	}
	if event == "" {
		event = firstNonEmptyString(payload, "hook_event_name", "hookEventName")
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
	if _, ok := payload["tool_response"]; !ok {
		for _, k := range []string{"toolResponse", "toolOutput", "tool_output", "output", "result"} {
			if v, exists := payload[k]; exists && v != nil {
				payload["tool_response"] = v
				break
			}
		}
	}
	if tid := firstNonEmptyString(payload, "tool_use_id", "toolUseId", "toolCallId", "callId"); tid != "" && firstNonEmptyString(payload, "tool_use_id") == "" {
		payload["tool_use_id"] = tid
	}
	if prompt := firstNonEmptyString(payload, "prompt", "userPrompt", "user_prompt", "message"); prompt != "" && firstNonEmptyString(payload, "prompt") == "" {
		payload["prompt"] = prompt
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
		return strings.TrimPrefix(cwd, "file://")
	}
	for _, k := range []string{"workspacePaths", "workspaceUris", "workspace_paths", "workspace_uris"} {
		if arr, ok := payload[k].([]any); ok && len(arr) > 0 {
			if s, ok := arr[0].(string); ok && strings.TrimSpace(s) != "" {
				return strings.TrimPrefix(strings.TrimSpace(s), "file://")
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

func agyTranscriptMeta(path string) (latestUserStep int, latestUserPrompt, latestModel string) {
	latestUserStep = -1
	if path == "" {
		return
	}
	data := readTailBytes(path, 256<<10)
	if len(data) == 0 {
		return
	}
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
		if latestModel == "" && s.Source == "MODEL" {
			m := strings.TrimSpace(s.Model)
			if m != "" && m != "<synthetic>" && !strings.EqualFold(m, "auto") {
				latestModel = m
			}
		}
		if latestUserStep < 0 && (s.Source == "USER_EXPLICIT" || s.Type == "USER_INPUT") {
			latestUserStep = s.StepIndex
			latestUserPrompt = strings.TrimSpace(s.Content)
		}
		if latestModel != "" && latestUserStep >= 0 {
			break
		}
	}
	return
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
// 同时识别 compact / resume / 无新用户消息的后台唤醒。
func (d *Daemon) foldAgyPreInvocationLocked(key, instance string, payload map[string]any, transcriptPath string) (foldedEvent, source, prompt string, skip bool) {
	d.ensureAgyMapsLocked()
	st := d.agyTurns[key]
	if st == nil {
		st = &agyTurnState{lastUserStep: -1}
		d.agyTurns[key] = st
	}
	if instance != "" && st.lastInstance != "" && st.lastInstance != instance {
		// 宿主终端或进程实例变化，视作恢复会话（resume）以便重投角色与未读团队消息。
		source = "resume"
	}
	if instance != "" {
		st.lastInstance = instance
	}

	userStep, userPrompt, _ := agyTranscriptMeta(transcriptPath)
	rawPrompt := firstNonEmptyString(payload, "prompt")
	if rawPrompt != "" {
		prompt = rawPrompt
	} else {
		prompt = userPrompt
	}

	numSteps, hasNumSteps := firstInt(payload, "initialNumSteps", "initial_num_steps")
	srcHint := firstNonEmptyString(payload, "source")
	isCompact := srcHint == "compact" ||
		strings.HasPrefix(strings.TrimSpace(prompt), "/compact") ||
		(hasNumSteps && st.lastNumSteps > 0 && numSteps > 0 && numSteps < st.lastNumSteps)
	if hasNumSteps && numSteps > 0 {
		st.lastNumSteps = numSteps
	}

	if isCompact {
		st.inTurn = true
		st.firstUserSeen = true
		if userStep >= 0 {
			st.lastUserStep = userStep
		}
		return "UserPromptSubmit", "compact", prompt, false
	}

	if source == "resume" || srcHint == "resume" {
		st.inTurn = true
		st.firstUserSeen = true
		if userStep >= 0 {
			st.lastUserStep = userStep
		}
		return "UserPromptSubmit", "resume", prompt, false
	}

	newExplicitStep := userStep >= 0 && userStep > st.lastUserStep
	if st.inTurn && !newExplicitStep {
		return "PreInvocation", "", "", true
	}

	// 开启一个新的用户 / 自动唤醒 turn
	if st.firstUserSeen && userStep >= 0 && userStep == st.lastUserStep && rawPrompt == "" {
		source = protocol.SourceAuto
	}
	st.inTurn = true
	st.firstUserSeen = true
	if userStep >= 0 {
		st.lastUserStep = userStep
	}
	return "UserPromptSubmit", source, prompt, false
}

func (d *Daemon) finishAgyStopLocked(key string) {
	if st := d.agyTurns[key]; st != nil {
		st.inTurn = false
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

func (d *Daemon) rememberAgyMCPLocked(key, sid, rawTool string, toolInput any) {
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

func (d *Daemon) claimAgyMCPLocked(rawTool, argsHash string) string {
	bare, ok := normalizeAgyMCPTool(rawTool)
	if !ok || d.agyMCP == nil {
		return ""
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
			return item.sessionKey
		}
	}
	if len(list) == 1 {
		// 单次调用刚被同工具记录过时容错复用（防同一次调用内重复查会话）。
		if now.Sub(list[0].createdAt) <= 5*time.Second {
			return list[0].sessionKey
		}
	}
	return ""
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

func (d *Daemon) noteAgySubagentToolLocked(parentKey, parentSID, transcriptPath string, payload map[string]any, env map[string]string) *agySubInfo {
	d.ensureAgyMapsLocked()
	in, _ := payload["tool_input"].(map[string]any)
	role, _ := in["Role"].(string)
	if role == "" {
		role, _ = in["role"].(string)
	}
	typeName, _ := in["TypeName"].(string)
	if typeName == "" {
		typeName, _ = in["typeName"].(string)
	}
	prompt, _ := in["Prompt"].(string)

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

	childTranscript := ""
	if transcriptPath != "" && parentSID != "" && strings.Contains(transcriptPath, parentSID) {
		childTranscript = strings.ReplaceAll(transcriptPath, parentSID, childCID)
	}
	if childTranscript == "" {
		childTranscript = resolveAgyTranscript(childCID, nil, env)
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
	if prev := d.agySubs[childCID]; prev != nil {
		if info.role == "" {
			info.role = prev.role
		}
		if info.typeName == "" {
			info.typeName = prev.typeName
		}
		if info.prompt == "" {
			info.prompt = prev.prompt
		}
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
		if s.Type == "GENERIC" && s.Content != "" {
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
