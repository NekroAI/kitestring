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
	// ToolDSH 是 DeepSeek Harness（DSH）。它由 Host 插件直接上报事件，
	// 不经过 hook 子进程；会话标识、待命和唤醒都由插件负责。
	ToolDSH = "dsh"
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
	// Claude 配置来源（R-240）：非默认 CLAUDE_CONFIG_DIR 才有。Profile 是显示名，ProfileDir 是目录路径，
	// Launcher 是用户配的启动命令。只含目录与命令名，不含任何凭证或网关地址（D13）
	Profile    string `json:"profile,omitempty"`
	ProfileDir string `json:"profile_dir,omitempty"`
	Launcher   string `json:"launcher,omitempty"`
	Model      string `json:"model,omitempty"`
	TurnID     string `json:"turn_id,omitempty"` // Codex turn_id / Claude prompt_id
	Source     string `json:"source,omitempty"`  // SessionStart.source
	Reason     string `json:"reason,omitempty"`  // SessionEnd.reason
	Prompt     string `json:"prompt,omitempty"`  // UserPromptSubmit，截断
	// Wake / WakeNote：自动注入的 UserPromptSubmit 的来源（WakeBg / WakeAgent / WakeOutput）和一句说明（R-287）
	Wake      string `json:"wake,omitempty"`
	WakeNote  string `json:"wake_note,omitempty"`
	ToolName  string `json:"tool_name,omitempty"`
	ToolUseID string `json:"tool_use_id,omitempty"`
	ToolBrief string `json:"tool_brief,omitempty"` // 工具动作简述（命令截断 / 文件名），不含代码正文
	NotifType string `json:"notification_type,omitempty"`
	PermMode  string `json:"permission_mode,omitempty"`
	AgentType string `json:"agent_type,omitempty"` // SubagentStart / SubagentStop
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

// GitCheck 是 Hub 请 daemon 在本机仓库里查的事（R-245）：Dir 是仓库里的一个目录（会话的工作目录），
// SHAs 是要查的提交，Deploy 是正在运行的 ks 的构建提交（version.Commit，没有时为空）。只读，不改仓库。
type GitCheck struct {
	ReqID  string   `json:"req_id"`
	Dir    string   `json:"dir"`
	SHAs   []string `json:"shas,omitempty"`
	Deploy string   `json:"deploy,omitempty"`
}

// 提交在某条线上的情况（R-245）。
const (
	PlaceAncestor = "ancestor" // 直接在这条线上（是它的祖先）
	PlacePatch    = "patch"    // 不是祖先，但这条线上有补丁相同的提交（cherry-pick / rebase 过去的）
	PlacePicked   = "picked"   // 补丁对不上（摘的时候解决过冲突），但这条线上有用 cherry-pick -x 摘它的提交（说明里写着来源）
	PlaceNo       = "no"       // 不在
	PlaceUnknown  = "unknown"  // 查不了：本机仓库里没有这个提交，或部署版本不明
)

// CommitPlace 是一个提交是否已在 main、是否已在部署的版本里。
type CommitPlace struct {
	SHA     string `json:"sha"`
	Subject string `json:"subject,omitempty"` // 本机仓库里这个提交的标题（找不到提交时为空）
	Main    string `json:"main" enum:"ancestor,patch,picked,no,unknown"`
	Deploy  string `json:"deploy" enum:"ancestor,patch,picked,no,unknown"`
}

// GitResult 是 daemon 的查询结果。Main 是主线分支名（main / master，取不到时为空）；
// Upstream 是它的上游（如 origin/main），没有上游时为空、NoRemote 为真；Ahead 是 main 比上游多几个提交。
type GitResult struct {
	ReqID    string        `json:"req_id"`
	Main     string        `json:"main,omitempty"`
	Upstream string        `json:"upstream,omitempty"`
	NoRemote bool          `json:"no_remote,omitempty"`
	Ahead    int           `json:"ahead"`
	Commits  []CommitPlace `json:"commits,omitempty"`
	Err      string        `json:"err,omitempty"`
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
	Advice     string    `json:"advice,omitempty"`    // kind=feedback：退回时主助理的验收建议（一句话，已标明出处；不是用户原话，R-242）
	Replyable  bool      `json:"replyable,omitempty"` // 会话发来的团队消息（R-191）：送达时写上怎么回复，回复自动送回发消息的一方
}

// StopQuestion 是停下提醒（R-291）的标题：kind = role。会话运行中时，daemon 在它这一轮结束（Stop）时拦下结束并送达这条提醒。
const StopQuestion = "用户请你停下"

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
	MsgUsage       = "usage"        // daemon → hub：按分钟汇总的 token 用量与冷启动（只有数字，R-206）
	MsgUsageAck    = "usage_ack"    // hub → daemon：已存下的用量批次（Ack 里是批次号）
	MsgShells      = "shells"       // daemon → hub：后台命令的起止、结果与最后一行输出摘要（R-208，有变化的才发）
	MsgShellsAck   = "shells_ack"   // hub → daemon：已存下的后台命令批次（Ack 里是批次号）
	MsgGitCheck    = "git_check"    // hub → daemon：在本机仓库查提交是否已合入 main / 已部署，以及 main 有几个没推送的提交（R-245）
	MsgGitResult   = "git_result"   // daemon → hub：查询结果
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
	GitCheck    *GitCheck         `json:"git_check,omitempty"`
	GitResult   *GitResult        `json:"git_result,omitempty"`
	// token 用量（R-206）：Batch 是批次号，Hub 存好后用 MsgUsageAck 回这个号
	Batch string        `json:"batch,omitempty"`
	Usage []UsageMinute `json:"usage,omitempty"`
	Colds []ColdStart   `json:"colds,omitempty"`
	// 后台命令（R-208）：Batch 同上，Hub 存好后用 MsgShellsAck 回批次号
	Shells []ShellCmd `json:"shells,omitempty"`
}

// UsageMinute 是本机从对话记录里读出的、某会话（或子任务）某模型某一分钟的 token 用量累计值（绝对值，Hub 直接覆盖，重发不重复计）。
// 只有会话键、子任务编号、模型名和数字，不带对话内容（R-206）。
type UsageMinute struct {
	Key      string `json:"k"`
	Agent    string `json:"a,omitempty"`
	Model    string `json:"model,omitempty"`
	Minute   int64  `json:"m"`    // Unix 分钟
	Input    int64  `json:"in"`   // 没命中缓存的输入
	CacheW   int64  `json:"cw"`   // 写缓存（含两个档位）
	CacheW1h int64  `json:"cw1h"` // 其中写进 1 小时档的（其余是 5 分钟档）
	CacheR   int64  `json:"cr"`   // 读缓存（只算命中率）
	Output   int64  `json:"out"`  // 输出
	Requests int    `json:"n"`    // 回复次数（去重后）
}

// 冷启动起因（R-206）。
const (
	ColdIdle    = "idle"    // 闲置超过缓存档位
	ColdCompact = "compact" // 上下文压缩
	ColdModel   = "model"   // 换了模型
	ColdFirst   = "start"   // 这份记录的第一次回复
	ColdOther   = "other"   // 看不出起因
)

// ColdStart 是一次缓存重建：单次回复写缓存（Codex 是未命中缓存的输入）超过阈值。
type ColdStart struct {
	Key       string `json:"k"`
	Agent     string `json:"a,omitempty"`
	Model     string `json:"model,omitempty"`
	At        int64  `json:"at"`     // Unix 毫秒
	Tokens    int64  `json:"tokens"` // 这次重算的 token 数
	Cause     string `json:"cause" enum:"idle,compact,model,start,other"`
	IdleMin   int    `json:"idle_min"` // 距上一次回复多少分钟（第一次回复为 0）
	PrevModel string `json:"prev_model,omitempty"`
}

// 后台命令的结果（R-208）。
const (
	ShellRunning = "running" // 还在跑
	ShellOK      = "ok"      // 成功（退出码 0）
	ShellFailed  = "failed"  // 失败（退出码非 0）
	ShellStopped = "stopped" // 被停止（Agent 停掉、到了后台时限、被信号终止、纸鸢停的）
	ShellEnded   = "ended"   // 已结束，退出码未知（进程不在了，没收到结束通知）
	ShellLost    = "lost"    // 结束时间和退出码都不知道（本功能上线前开始、进程早已不在的）
)

// 后台命令的类型（R-208，按命令自动识别）。
const (
	ShellBuild   = "build"   // 构建 / 测试
	ShellDeploy  = "deploy"  // 部署
	ShellService = "service" // 服务（开发服务器等常驻进程）
	ShellPoll    = "poll"    // 轮询（until / while 循环里 sleep、tail -f、gh run watch）
	ShellOther   = "other"
)

// ShellCmd 是一条后台命令（R-208）：Claude Code run_in_background 的命令，或 Codex 会话下活过宽限期的后台 shell。
// 只带类型、描述、命令、起止、退出码和最后一行输出的摘要（截断），不带输出内容本身。
type ShellCmd struct {
	Key    string `json:"k"`
	Agent  string `json:"a,omitempty"`   // 子任务里开的：子任务的 agent_id
	Ref    string `json:"ref"`           // tool_use_id；Codex 的后台 shell 是“sh-进程号@启动时间”
	Task   string `json:"tid,omitempty"` // 对应 R-162 后台任务的 ID（sh-进程号），网页据此不重复列
	Kind   string `json:"kind" enum:"build,deploy,service,poll,other"`
	Label  string `json:"label"` // run_in_background 的描述，没有就是命令摘要（截断）
	Cmd    string `json:"cmd"`   // 完整命令（截断到 1000 字），悬停时看
	Start  int64  `json:"start"` // Unix 毫秒
	End    int64  `json:"end,omitempty"`
	Status string `json:"status" enum:"running,ok,failed,stopped,ended,lost"`
	Exit   *int   `json:"exit,omitempty"`
	Last   string `json:"last,omitempty"`               // 最后一行输出（截到 160 字），只在本机读输出文件得到
	Hot    string `json:"hot,omitempty" enum:"cpu,mem"` // 跑飞：CPU 长时间占满 / 内存持续上涨
	HotAt  int64  `json:"hot_at,omitempty"`             // 从什么时候开始（Unix 毫秒）
	RSSKB  int64  `json:"rss_kb,omitempty"`             // 跑飞时整棵进程树的常驻内存
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
