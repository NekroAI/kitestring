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
	// AgentID：子 Agent 的标识。Claude Code 在子 Agent 里触发的 hook 带 agent_id（session_id 仍是主会话的），
	// 主线程没有；SubagentStart / SubagentStop 也带。Hub 据此把子 Agent 的工具事件只算作后台任务，不改主会话状态（R-115）
	AgentID   string `json:"agent_id,omitempty"`
	BgTasks   int    `json:"bg_tasks,omitempty"`   // Stop 时仍在运行的后台任务数（后台 shell / 子 Agent）
	BgBrief   string `json:"bg_brief,omitempty"`   // 后台任务简述，截断
	LastReply string `json:"last_reply,omitempty"` // 仅在 Hub 侧生成摘要时上送，截断
	// Reply：Stop 时这一轮的最后一条回复（截到 ReplyMax 字），Hub 存为会话的“最后回复”，不写进事件记录（R-109）
	Reply    string `json:"reply,omitempty"`
	Git      *Git   `json:"git,omitempty"`
	Instance string `json:"instance,omitempty"` // 所属终端（agent 进程号 + 启动时间），区分同一对话在多个终端打开
	// Tools：这个会话的 Kitestring 工具（MCP 进程）版本状态，daemon 发现变化时随下一条事件捎上（R-59）。
	// "ok" 表示已是最新；其它非空值是过期工具的版本（"旧版" 表示太旧、没有上报版本）；空表示没有变化。
	Tools string `json:"tools,omitempty"`
	// Context / ContextMax：一轮结束时会话的上下文 token 数与上下文窗口（不知道时为 0），来自会话记录里的用量字段（R-60）
	Context    int `json:"context,omitempty"`
	ContextMax int `json:"context_max,omitempty"`
	// Until：ks wait 开始时（KsWait）最长等到什么时候（R-74）
	Until *time.Time `json:"until,omitempty"`
	// WaitOn：ks wait --on 给的订阅（R-164），规整后的逗号列表；空表示沿用上次，"default" 恢复按身份的默认，"none" 不订阅
	WaitOn string `json:"wait_on,omitempty"`
	// Tasks：KsTasks 事件带的这个会话当前在跑的后台 shell 全集（覆盖式，R-162）；只有标签与开始时间，不含命令输出
	Tasks []Task `json:"tasks,omitempty"`
	// Commits：daemon 读本地 git log 发现的新提交（R-156），只含标题写了需求编号（R-x）或是 revert 的提交；
	// Hub 按项目 + 哈希去重存下，关联到需求，不写进事件记录。纸鸢只读 git，不改仓库
	Commits []Commit `json:"commits,omitempty"`
}

// Commit 是 daemon 从本地 git log 读到的一条提交（R-156）：哈希、标题、提交时间；
// Reverts 是它回滚的提交（提交说明里 "This reverts commit <哈希>"），不是 revert 时为空。
type Commit struct {
	SHA     string    `json:"sha"`
	Subject string    `json:"subject"`
	At      time.Time `json:"at"`
	Reverts string    `json:"reverts,omitempty"`
}

// Task 是 daemon 从进程表认出的一个后台 shell（R-162）。
type Task struct {
	ID        string    `json:"id"`
	Label     string    `json:"label"` // run_in_background 时的描述，没有就是命令（截断）
	StartedAt time.Time `json:"started_at"`
}

// EventTasks：daemon 发现某会话的后台 shell 有变化时上报的合成事件，Tasks 是当前全集（R-162）。
const EventTasks = "KsTasks"

// ReplyMax 是随 Stop 上送的最后回复的字数上限（R-109）。
const ReplyMax = 20000

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
	ReqCode    string    `json:"req_code,omitempty"`  // 关联需求编号；kind=plan 时是计划编号（P-n）
	ReqTitle   string    `json:"req_title,omitempty"`
	Files      []File    `json:"files,omitempty"`     // 答复 / 意见的附件（图片、录屏或任意文件）
	Quiet      bool      `json:"quiet,omitempty"`     // 不唤醒 ks wait（R-198）：照旧注入，下一轮开始或因别的消息唤醒时一并带上
	Replyable  bool      `json:"replyable,omitempty"` // 会话发来的团队消息（R-191）：送达时写上怎么回复，回复自动送回发消息的一方
}

// File 是随决策下行的附件（图片或录屏）引用；daemon 下载到本地后在送达文字里给出路径。
type File struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Mime string `json:"mime"`
}

// WS 消息类型。
const (
	MsgHello       = "hello"        // daemon → hub：设备信息
	MsgEvents      = "events"       // daemon → hub：批量事件
	MsgAck         = "ack"          // hub → daemon：已持久化的事件 ID
	MsgDecisions   = "decisions"    // hub → daemon：待注入决策全集（覆盖式）
	MsgDelivered   = "delivered"    // daemon → hub：决策已注入
	MsgConfig      = "config"       // hub → daemon：下发给设备的运行配置
	MsgSessions    = "sessions"     // hub → daemon：本设备上未结束会话的终端实例（daemon 重启后据此继续盯进程，R-204）
	MsgRoles       = "roles"        // hub → daemon：本设备上有身份的会话全集（覆盖式，R-82）
	MsgRolePrompts = "role_prompts" // hub → daemon：用户改写过的身份提示词全集（覆盖式，R-98）
	MsgOrphans     = "orphans"      // daemon → hub：本机会话结束后还在跑（或已脱离 Agent）的后台进程全集（覆盖式，R-209）
	MsgStopProc    = "stop_proc"    // hub → daemon：停止其中一个（daemon 核对 pid + 启动时间后才发信号）
	MsgStopResult  = "stop_result"  // daemon → hub：停止结果
)

// DeviceConfig 是 Hub 下发给 daemon 的运行配置。
type DeviceConfig struct {
	// SendLastReply 为真时 daemon 在 Stop 事件中附带截断的最后回复，供 Hub 生成摘要（D3 / D7）。
	SendLastReply bool `json:"send_last_reply"`
}

// Envelope 是 WebSocket 上的统一消息外壳。
type Envelope struct {
	Type      string            `json:"type"`
	Hello     *Hello            `json:"hello,omitempty"`
	Events    []Event           `json:"events,omitempty"`
	Ack       []string          `json:"ack,omitempty"`
	Decisions []Decision        `json:"decisions,omitempty"`
	Delivered []Delivery        `json:"delivered,omitempty"`
	Config    *DeviceConfig     `json:"config,omitempty"`
	Roles     map[string]string `json:"roles,omitempty"` // 会话键 → 身份
	Sessions  []SessionInstance `json:"sessions,omitempty"`
	// RolePrompts：身份 → 用户改写后的提示词正文（只含改写过的；没有的身份用内置默认模板，R-98）。
	RolePrompts map[string]string `json:"role_prompts,omitempty"`
	Orphans     []OrphanProc      `json:"orphans,omitempty"`
	StopProc    *StopProc         `json:"stop_proc,omitempty"`
	StopResult  *StopResult       `json:"stop_result,omitempty"`
}

// 后台进程状态（R-209）。
const (
	ProcOrphan   = "orphan"   // 开它的 Agent 进程（会话）已经结束，它还在跑
	ProcDetached = "detached" // 会话还在，但它已脱离 Agent（父进程变成 1，Agent 管不到它），可能仍在用
)

// OrphanProc 是 daemon 认出的一棵后台进程树（以根进程为准）：会话开出来、会话结束后仍在跑，或已脱离 Agent。
type OrphanProc struct {
	ID         string    `json:"id"`    // 根进程“进程号@启动时间”
	Pid        int       `json:"pid"`   // 根进程号
	Start      string    `json:"start"` // 根进程启动时间（ps lstart 原文，核对用）
	StartedAt  time.Time `json:"started_at"`
	State      string    `json:"state" enum:"orphan,detached"`
	Cmd        string    `json:"cmd"`
	Cwd        string    `json:"cwd,omitempty"`
	SessionKey string    `json:"session_key,omitempty"`
	Ports      []int     `json:"ports,omitempty"` // 在监听的 TCP 端口（命令行里写了 --port 的排第一）
	CPU        float64   `json:"cpu"`             // 整棵树的 %CPU 之和
	RSSKB      int64     `json:"rss_kb"`          // 整棵树的常驻内存（KB）
	Procs      int       `json:"procs"`           // 树里的进程数
}

// StopProc 是 Hub 转来的停止请求。Force 为假时发 SIGTERM，为真时发 SIGKILL。
type StopProc struct {
	ReqID string `json:"req_id"`
	Pid   int    `json:"pid"`
	Start string `json:"start"`
	Force bool   `json:"force,omitempty"`
}

// 停止结果。
const (
	StopExited  = "exited"  // SIGTERM 后全部退出
	StopRunning = "running" // SIGTERM 后几秒仍在运行（可再选强制）
	StopKilled  = "killed"  // SIGKILL 后已退出
	StopFailed  = "failed"  // 发了信号仍在运行，或发信号出错
	StopRefused = "refused" // 核对不通过，没有发信号
	StopGone    = "gone"    // 进程已经不在了，没有发信号
)

// StopResult 是 daemon 的停止结果。
type StopResult struct {
	ReqID   string `json:"req_id"`
	Result  string `json:"result"`
	Signal  string `json:"signal,omitempty"` // SIGTERM / SIGKILL；没发信号时为空
	Detail  string `json:"detail,omitempty"`
	Waited  int    `json:"waited_ms,omitempty"` // 发信号后等了多久
	Remain  int    `json:"remain,omitempty"`    // 还在运行的进程数
	Signals int    `json:"signals,omitempty"`   // 发了信号的进程数
}

// SessionInstance 是 Hub 记着的一个未结束会话的终端实例（进程号@启动时间）。
type SessionInstance struct {
	Key          string `json:"key"`
	Instance     string `json:"instance,omitempty"`
	PrevInstance string `json:"prev_instance,omitempty"`
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
	// Block 非空时 hook 拦下这次结束（Stop），并把它作为理由交给 Agent 继续处理（R-83）
	Block string `json:"block,omitempty"`
}

// 附件类型（R-221）：只有这几种图片、录屏在页面里直接显示；其它文件（包括 SVG、HTML）一律按下载返回，不在页面里打开。
var inlineMime = map[string]string{
	"image/png": ".png", "image/jpeg": ".jpg", "image/gif": ".gif", "image/webp": ".webp",
	"video/mp4": ".mp4", "video/webm": ".webm", "video/quicktime": ".mov",
}

// InlineMime：可以在页面里直接显示的图片或录屏。
func InlineMime(mime string) bool { _, ok := inlineMime[mime]; return ok }

// IsImage：能直接显示的图片（不含 SVG）。
func IsImage(mime string) bool { return InlineMime(mime) && len(mime) > 6 && mime[:6] == "image/" }

// IsVideo：能直接播放的录屏。
func IsVideo(mime string) bool { return InlineMime(mime) && len(mime) > 6 && mime[:6] == "video/" }

// MediaExt 是图片 / 录屏的扩展名；其它类型返回空。
func MediaExt(mime string) string { return inlineMime[mime] }
