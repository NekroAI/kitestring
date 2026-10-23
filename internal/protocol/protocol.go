// Package protocol 定义 daemon 与 Hub 之间、hook 与 daemon 之间的线上消息格式。
package protocol

import (
	"crypto/rand"
	"time"

	"github.com/oklog/ulid/v2"
)

// 工具标识。
const (
	ToolClaude = "claude"
	ToolCodex  = "codex"
)

// NewID 生成按时间排序的 ULID，用于事件幂等与补传。
func NewID() string {
	return ulid.MustNew(ulid.Timestamp(time.Now()), rand.Reader).String()
}

// SessionKey 把工具与会话 ID 拼成全局唯一的会话键。
func SessionKey(tool, sessionID string) string { return tool + ":" + sessionID }

// Git 是 daemon 采集的工作目录 git 状态。
type Git struct {
	Branch    string `json:"branch,omitempty"`
	Remote    string `json:"remote,omitempty"`
	Root      string `json:"root,omitempty"`
	Dirty     int    `json:"dirty"`
	Worktree  bool   `json:"worktree,omitempty"`
	Available bool   `json:"available"`
}

// Event 是一条从设备上报的 hook 事件（已按 D3 裁剪，不含对话与代码正文）。
type Event struct {
	ID        string    `json:"id"`
	Time      time.Time `json:"time"`
	Tool      string    `json:"tool"`
	Name      string    `json:"name"` // hook_event_name
	SessionID string    `json:"session_id"`
	Cwd       string    `json:"cwd,omitempty"`
	Client    string    `json:"client,omitempty"`
	Model     string    `json:"model,omitempty"`
	TurnID    string    `json:"turn_id,omitempty"` // Codex turn_id / Claude prompt_id
	Source    string    `json:"source,omitempty"`  // SessionStart.source
	Reason    string    `json:"reason,omitempty"`  // SessionEnd.reason
	Prompt    string    `json:"prompt,omitempty"`  // UserPromptSubmit，截断
	ToolName  string    `json:"tool_name,omitempty"`
	ToolUseID string    `json:"tool_use_id,omitempty"`
	ToolBrief string    `json:"tool_brief,omitempty"` // 工具动作简述（命令截断 / 文件名），不含代码正文
	NotifType string    `json:"notification_type,omitempty"`
	PermMode  string    `json:"permission_mode,omitempty"`
	AgentType string    `json:"agent_type,omitempty"` // SubagentStart / SubagentStop
	BgTasks   int       `json:"bg_tasks,omitempty"`   // Stop 时仍在运行的后台任务数（后台 shell / 子 Agent）
	BgBrief   string    `json:"bg_brief,omitempty"`   // 后台任务简述，截断
	LastReply string    `json:"last_reply,omitempty"` // 仅在 Hub 侧生成摘要时上送，截断
	Git       *Git      `json:"git,omitempty"`
	Instance  string    `json:"instance,omitempty"` // 所属终端（agent 进程号 + 启动时间），区分同一对话在多个终端打开
	// Tools：这个会话的 Kitestring 工具（MCP 进程）版本状态，daemon 发现变化时随下一条事件捎上（R-59）。
	// "ok" 表示已是最新；其它非空值是过期工具的版本（"旧版" 表示太旧、没有上报版本）；空表示没有变化。
	Tools string `json:"tools,omitempty"`
}

// ks wait 挂起等待：daemon 在等待开始 / 结束时上报的合成事件。
const (
	EventWait    = "KsWait"
	EventWaitEnd = "KsWaitEnd"
)

// Decision 是下行到 daemon、等待注入的已决策项。
type Decision struct {
	ID         string    `json:"id"`
	Code       string    `json:"code"`
	SessionKey string    `json:"session_key"`
	Question   string    `json:"question"`
	Answer     string    `json:"answer"`
	AnsweredAt time.Time `json:"answered_at"`
	Delivered  bool      `json:"delivered,omitempty"` // 已接收，仅用于恢复 / 压缩时补注入
	Kind       string    `json:"kind,omitempty"`      // decision | feedback | note | assign | plan
	ReqCode    string    `json:"req_code,omitempty"` // 关联需求编号；kind=plan 时是计划编号（P-n）
	ReqTitle   string    `json:"req_title,omitempty"`
	Files      []File    `json:"files,omitempty"` // 答复 / 意见附图
}

// File 是随决策下行的附件（图片或录屏）引用；daemon 下载到本地后在送达文字里给出路径。
type File struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Mime string `json:"mime"`
}

// WS 消息类型。
const (
	MsgHello     = "hello"     // daemon → hub：设备信息
	MsgEvents    = "events"    // daemon → hub：批量事件
	MsgAck       = "ack"       // hub → daemon：已持久化的事件 ID
	MsgDecisions = "decisions" // hub → daemon：待注入决策全集（覆盖式）
	MsgDelivered = "delivered" // daemon → hub：决策已注入
	MsgConfig    = "config"    // hub → daemon：下发给设备的运行配置
)

// DeviceConfig 是 Hub 下发给 daemon 的运行配置。
type DeviceConfig struct {
	// SendLastReply 为真时 daemon 在 Stop 事件中附带截断的最后回复，供 Hub 生成摘要（D3 / D7）。
	SendLastReply bool `json:"send_last_reply"`
}

// Envelope 是 WebSocket 上的统一消息外壳。
type Envelope struct {
	Type      string        `json:"type"`
	Hello     *Hello        `json:"hello,omitempty"`
	Events    []Event       `json:"events,omitempty"`
	Ack       []string      `json:"ack,omitempty"`
	Decisions []Decision    `json:"decisions,omitempty"`
	Delivered []Delivery    `json:"delivered,omitempty"`
	Config    *DeviceConfig `json:"config,omitempty"`
}

// Hello 是 daemon 连接后上报的设备信息。
type Hello struct {
	Version  string `json:"version"`
	Hostname string `json:"hostname"`
	Platform string `json:"platform"`
}

// Delivery 记录一次注入。
type Delivery struct {
	DecisionID string    `json:"decision_id"`
	SessionKey string    `json:"session_key"`
	TurnID     string    `json:"turn_id,omitempty"`
	Time       time.Time `json:"time"`
}

// HookRequest 是 ks hook 发给本机 daemon 的请求。
type HookRequest struct {
	Tool    string            `json:"tool"`
	Event   string            `json:"event"`
	Payload map[string]any    `json:"payload"`
	Env     map[string]string `json:"env,omitempty"`
	PPID    int               `json:"ppid"`
}

// HookResponse 是 daemon 给 hook 的回复；Context 非空时由 hook 作为 additionalContext 输出。
type HookResponse struct {
	Context string `json:"context,omitempty"`
}
