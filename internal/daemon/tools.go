package daemon

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/NekroAI/kitestring/internal/version"
)

// 工具版本过期提醒（R-59）：升级 ks 后，已开着的会话仍跑着旧的 ks mcp 进程，看不到新参数和新说明。
// ks mcp / CLI 每次请求都带上自己的版本（X-KS-Version），旧到没有这个头的就是更早的版本。
// daemon 按会话记下工具状态：变化时随这个会话的下一条事件捎给 Hub（网页标“工具待更新”），
// 并在下一次给它注入消息时提醒一次。

const versionHeader = "X-KS-Version" // 与 client.VersionHeader 一致（client 依赖本包，不能反向引用）

const staleOld = "旧版"

type toolsState struct {
	status map[string]string // 会话 → "ok" 或过期工具的版本
	sent   map[string]string // 已经上报给 Hub 的状态
	noted  map[string]bool   // 已经提醒过 Agent
}

func newToolsState() toolsState {
	return toolsState{status: map[string]string{}, sent: map[string]string{}, noted: map[string]bool{}}
}

// noteTools 记下会话 key 的工具版本（来自请求头）。
func (d *Daemon) noteTools(key, header string) {
	if key == "" {
		return
	}
	st := "ok"
	if header == "" {
		st = staleOld
	} else if header != version.Version {
		st = header
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.tools.status[key] != st {
		d.tools.status[key] = st
		d.tools.noted[key] = false
	}
}

// toolsForEvent 返回需要随事件上报的工具状态（没有变化时为空）。
func (d *Daemon) toolsForEvent(key string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	st := d.tools.status[key]
	if st == "" || st == d.tools.sent[key] {
		return ""
	}
	d.tools.sent[key] = st
	return st
}

// toolsNote 返回给 Agent 的一次性提醒（工具已是最新或已提醒过时为空）。
func (d *Daemon) toolsNote(key string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	st := d.tools.status[key]
	if st == "" || st == "ok" || d.tools.noted[key] {
		return ""
	}
	d.tools.noted[key] = true
	return "[Kitestring] 提醒：本会话的 Kitestring 工具（MCP）还是升级前的版本（" + st + "，当前 " + version.Version + "），新参数和新说明它都没有。" +
		"需要用到新功能时改用 shell 命令 ks（总是最新版，如 ks req get R-12、ks req status R-12 review、ks decision raise …，ks --help 查看），" +
		"或请用户重启会话 / 在终端里用 /mcp 重连 kitestring。\n"
}

// watchTools 包住发往 Hub 的 Agent 请求：从查询参数或 JSON 请求体里找出会话，记下它的工具版本。
func (d *Daemon) watchTools(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Query().Get("session_key")
		if key == "" && strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") && r.ContentLength > 0 && r.ContentLength < 1<<20 {
			b, err := io.ReadAll(r.Body)
			if err == nil {
				r.Body = io.NopCloser(bytes.NewReader(b))
				var body struct {
					SessionKey string `json:"session_key"`
				}
				if json.Unmarshal(b, &body) == nil {
					key = body.SessionKey
				}
			}
		}
		if key == "" {
			// /agent/sessions/{key}/link 这类路径里带着会话
			if rest, ok := strings.CutPrefix(r.URL.Path, "/agent/sessions/"); ok {
				key, _, _ = strings.Cut(rest, "/")
			}
		}
		d.noteTools(key, r.Header.Get(versionHeader))
		next.ServeHTTP(w, r)
	})
}
