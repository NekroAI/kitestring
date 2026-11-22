package protocol

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
)

// KitestringMCPTools 列出 ks mcp 注册的全部 42 个工具名（按字典序）。
var KitestringMCPTools = []string{
	"arrange_add",
	"arrange_list",
	"arrange_revoke",
	"asset_push",
	"away_escalate",
	"away_set",
	"brief_set",
	"commit_link",
	"decision_answer",
	"decision_raise",
	"decision_withdraw",
	"dev_recommend",
	"evidence_attach",
	"evidence_void",
	"handoff_write",
	"morning_set",
	"need_devs",
	"plan_create",
	"plan_get",
	"plan_update",
	"pm_notify",
	"req_assign",
	"req_comment",
	"req_create",
	"req_edit",
	"req_get",
	"req_list",
	"req_request_status",
	"req_update_status",
	"req_watch",
	"req_watching",
	"review_submit",
	"secret_amend",
	"secret_list",
	"secret_request",
	"session_link_req",
	"stage_submit",
	"team",
	"team_broadcast",
	"team_sent",
	"team_sessions",
	"team_tell",
}

var kitestringMCPToolSet = func() map[string]bool {
	m := make(map[string]bool, len(KitestringMCPTools))
	for _, n := range KitestringMCPTools {
		m[n] = true
	}
	return m
}()

// NormalizeMCPToolName 把不同宿主上报的前缀工具名归一化为 kitestring 的裸工具名。
func NormalizeMCPToolName(name string) (string, bool) {
	name = strings.TrimSpace(name)
	for _, prefix := range []string{
		"mcp:kitestring:",
		"mcp__kitestring__",
		"mcp_kitestring_",
		"kitestring/",
		"kitestring:",
		"mcp/kitestring/",
	} {
		if strings.HasPrefix(name, prefix) {
			bare := strings.TrimPrefix(name, prefix)
			if bare != "" {
				return bare, true
			}
		}
	}
	if kitestringMCPToolSet[name] {
		return name, true
	}
	return "", false
}

// CanonicalArgsHash 对 MCP 工具参数做键序无关的规范化 SHA-256 哈希，
// 供 PreToolUse hook 与 ks mcp 在没有 _meta 会话标识时精确配对同一次调用。
func CanonicalArgsHash(v any) string {
	norm := normalizeJSONValue(v)
	b, err := json.Marshal(norm)
	if err != nil {
		b = []byte("{}")
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:16])
}

func normalizeJSONValue(v any) any {
	switch x := v.(type) {
	case nil:
		return map[string]any{}
	case json.RawMessage:
		trimmed := bytes.TrimSpace(x)
		if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
			return map[string]any{}
		}
		dec := json.NewDecoder(bytes.NewReader(trimmed))
		dec.UseNumber()
		var parsed any
		if dec.Decode(&parsed) != nil {
			return map[string]any{}
		}
		return normalizeJSONValue(parsed)
	case string:
		trimmed := strings.TrimSpace(x)
		if trimmed == "" || trimmed == "null" {
			return map[string]any{}
		}
		if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
			dec := json.NewDecoder(strings.NewReader(trimmed))
			dec.UseNumber()
			var parsed any
			if dec.Decode(&parsed) == nil {
				return normalizeJSONValue(parsed)
			}
		}
		return x
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			switch val.(type) {
			case map[string]any, []any, json.RawMessage:
				out[k] = normalizeJSONValue(val)
			default:
				out[k] = val
			}
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			switch item.(type) {
			case map[string]any, []any, json.RawMessage:
				out[i] = normalizeJSONValue(item)
			default:
				out[i] = item
			}
		}
		return out
	default:
		return x
	}
}
