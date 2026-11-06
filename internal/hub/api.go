package hub

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

	"github.com/NekroAI/kitestring/internal/blocks"
	"github.com/NekroAI/kitestring/internal/notify"
	"github.com/NekroAI/kitestring/internal/protocol"
	"github.com/NekroAI/kitestring/internal/store"
	"github.com/NekroAI/kitestring/internal/summary"
	"github.com/NekroAI/kitestring/internal/version"
)

// Handler 组装全部 HTTP 路由。web 为前端静态资源处理器，可为 nil。
func (h *Hub) Handler(web http.Handler) http.Handler {
	webMux := http.NewServeMux()
	agentMux := http.NewServeMux()
	publicMux := http.NewServeMux()
	guestMux := http.NewServeMux()

	webAPI := humago.New(webMux, apiConfig())
	agentAPI := humago.New(agentMux, apiConfig())
	publicAPI := humago.New(publicMux, apiConfig())
	guestAPI := humago.New(guestMux, apiConfig())
	h.registerPublic(publicAPI)
	h.registerShareGuest(guestAPI) // 分享访客（R-281）：免登录只读，单独一组路由，不经 requireWeb
	h.registerShareGuestComments(guestAPI)
	h.registerShareWeb(webAPI)
	h.registerShareComments(webAPI)
	h.registerWeb(webAPI)
	h.registerReview(webAPI)
	h.registerRelease(webAPI)
	h.registerGrantWeb(webAPI)
	h.registerMorningWeb(webAPI)
	h.registerAwayWeb(webAPI)
	h.registerReviewerWeb(webAPI)
	h.registerPlans(webAPI)
	h.registerNeedWeb(webAPI)
	h.registerRolePromptsWeb(webAPI)
	h.registerAgent(agentAPI)
	h.registerAgentEdit(agentAPI)
	h.registerAgentPlans(agentAPI)
	h.registerAgentRoles(agentAPI)
	h.registerAgentTeam(agentAPI)
	h.registerReviewerAgent(agentAPI)
	h.registerAgentEvidence(agentAPI)
	h.registerAgentNeed(agentAPI)
	h.registerStatusRequest(agentAPI)
	h.registerAgentBrief(agentAPI)
	h.registerAgentTell(agentAPI)
	h.registerAgentComment(agentAPI)
	h.registerAgentWatch(agentAPI)
	h.registerAgentGrant(agentAPI)
	h.registerAgentMorning(agentAPI)
	h.registerAgentAway(agentAPI)
	h.registerAgentWaitCheck(agentAPI)
	h.registerSessionReply(webAPI)
	h.registerStatusWeb(webAPI)
	h.registerPoolWeb(webAPI)
	h.registerAIUsageWeb(webAPI)
	h.registerStatusAgent(agentAPI)
	h.registerChainWeb(webAPI)
	h.registerChainAgent(agentAPI)
	h.registerCommitsWeb(webAPI)
	h.registerLanesWeb(webAPI)
	h.registerCommitsAgent(agentAPI)
	h.registerOrphansWeb(webAPI)
	h.registerHandoffWeb(webAPI)
	h.registerHandoffAgent(agentAPI)
	h.registerArrangeWeb(webAPI)
	h.registerCommsWeb(webAPI)
	h.registerAgentArrange(agentAPI)

	root := http.NewServeMux()
	root.Handle("/api/auth/", publicMux)
	// passkey（R-7）：登记要先登录；登录本身不需要
	root.Handle("POST /api/auth/passkey/begin", http.HandlerFunc(h.handlePasskeyLoginBegin))
	root.Handle("POST /api/auth/passkey/finish", http.HandlerFunc(h.handlePasskeyLoginFinish))
	root.Handle("POST /api/passkeys/register/begin", h.requireWeb(http.HandlerFunc(h.handlePasskeyRegisterBegin)))
	root.Handle("POST /api/passkeys/register/finish", h.requireWeb(http.HandlerFunc(h.handlePasskeyRegisterFinish)))
	// 分享访客（R-281）：只认路径里的访客 token，只有读、输密码和访客留言；附件和实时通道不走 huma
	root.Handle("/api/share/", guestMethodGuard(guestMux))
	root.Handle("GET /api/share/{token}/files/{id}", http.HandlerFunc(h.handleGuestFile))
	root.Handle("GET /api/share/{token}/stream", http.HandlerFunc(h.serveGuestStream))
	root.Handle("/api/agent/", h.requireDevice(agentMux))
	root.Handle("/api/device/ws", h.requireDevice(http.HandlerFunc(h.serveDeviceWS)))
	root.Handle("/api/stream", h.requireWeb(http.HandlerFunc(h.broker.serve)))
	webUploader := func(*http.Request) string { return "user" }
	devUploader := func(r *http.Request) string { d, _ := deviceFrom(r.Context()); return "device:" + d.ID }
	root.Handle("POST /api/files", h.requireWeb(h.handleUpload(webUploader)))
	root.Handle("GET /api/files/{id}", h.requireWeb(http.HandlerFunc(h.handleDownload)))
	root.Handle("POST /api/agent/files", h.requireDevice(h.handleUpload(devUploader)))
	root.Handle("GET /api/agent/files/{id}", h.requireDevice(http.HandlerFunc(h.handleDownload)))
	// 可交互原型（R-197）：Agent 交原型包；原型在隔离路径里打开，不认登录（令牌或访客签名就是凭据）
	root.Handle("POST /api/agent/prototypes", h.requireDevice(http.HandlerFunc(h.handleProtoUpload)))
	root.Handle("/p/{token}/{path...}", http.HandlerFunc(h.handleProto))
	root.Handle("/p/g/{link}/{proto}/{sig}/{path...}", http.HandlerFunc(h.handleGuestProto))
	root.Handle("/api/", h.requireWeb(webMux))
	if web != nil {
		root.Handle("/", h.shareMeta(web))
	}
	return withGzip(withClient(withToldHandler(root)))
}

func apiConfig() huma.Config {
	c := huma.DefaultConfig("Kitestring Hub API", version.Version)
	c.DocsPath = ""
	c.OpenAPIPath = ""
	c.SchemasPath = ""
	c.CreateHooks = nil // 不在响应里附加 $schema 链接
	return c
}

// OpenAPI 返回完整 API 的 OpenAPI 文档，供前端生成类型（D12）。
func (h *Hub) OpenAPI() *huma.OpenAPI {
	mux := http.NewServeMux()
	api := humago.New(mux, apiConfig())
	h.registerPublic(api)
	h.registerShareGuest(api)
	h.registerShareGuestComments(api)
	h.registerShareWeb(api)
	h.registerShareComments(api)
	h.registerWeb(api)
	h.registerReview(api)
	h.registerRelease(api)
	h.registerGrantWeb(api)
	h.registerMorningWeb(api)
	h.registerAwayWeb(api)
	h.registerReviewerWeb(api)
	h.registerPlans(api)
	h.registerNeedWeb(api)
	h.registerRolePromptsWeb(api)
	h.registerAgent(api)
	h.registerAgentEdit(api)
	h.registerAgentPlans(api)
	h.registerAgentRoles(api)
	h.registerAgentTeam(api)
	h.registerReviewerAgent(api)
	h.registerAgentEvidence(api)
	h.registerAgentNeed(api)
	h.registerStatusRequest(api)
	h.registerAgentBrief(api)
	h.registerAgentTell(api)
	h.registerAgentComment(api)
	h.registerAgentWatch(api)
	h.registerAgentGrant(api)
	h.registerAgentMorning(api)
	h.registerAgentAway(api)
	h.registerAgentWaitCheck(api)
	h.registerSessionReply(api)
	h.registerStatusWeb(api)
	h.registerPoolWeb(api)
	h.registerAIUsageWeb(api)
	h.registerStatusAgent(api)
	h.registerChainWeb(api)
	h.registerChainAgent(api)
	h.registerCommitsWeb(api)
	h.registerLanesWeb(api)
	h.registerCommitsAgent(api)
	h.registerOrphansWeb(api)
	h.registerHandoffWeb(api)
	h.registerHandoffAgent(api)
	h.registerArrangeWeb(api)
	h.registerCommsWeb(api)
	h.registerAgentArrange(api)
	return api.OpenAPI()
}

func apiErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, store.ErrNotFound) {
		return huma.Error404NotFound(err.Error())
	}
	var se huma.StatusError
	if errors.As(err, &se) {
		return err
	}
	return huma.Error400BadRequest(err.Error())
}

type empty struct{}

type okBody struct {
	Body struct {
		OK bool `json:"ok"`
	}
}

func ok() *okBody {
	o := &okBody{}
	o.Body.OK = true
	return o
}

// ---------- 公开接口：登录 ----------

type authStatus struct {
	Body struct {
		Initialized    bool     `json:"initialized"`
		LoggedIn       bool     `json:"logged_in"`
		Version        string   `json:"version"`
		PasskeyOrigins []string `json:"passkey_origins" doc:"登记过 passkey 的网页来源，登录页据此显示 passkey 登录（R-7）"`
	}
}

type passwordIn struct {
	Body struct {
		Password string `json:"password" minLength:"8"`
	}
}

type loginOut struct {
	SetCookie http.Cookie `header:"Set-Cookie"`
	Body      struct {
		OK bool `json:"ok"`
	}
}

type cookieIn struct {
	Session string `cookie:"ks_session"`
}

func (h *Hub) registerPublic(api huma.API) {
	huma.Register(api, huma.Operation{OperationID: "auth-status", Method: http.MethodGet, Path: "/api/auth/status", Tags: []string{"auth"}},
		func(ctx context.Context, in *cookieIn) (*authStatus, error) {
			out := &authStatus{}
			set, err := h.passwordSet(ctx)
			if err != nil {
				return nil, err
			}
			out.Body.Initialized = set
			out.Body.Version = version.Version
			out.Body.PasskeyOrigins = h.passkeyOrigins(ctx)
			if in.Session != "" {
				out.Body.LoggedIn, _ = h.store.ValidWebSession(ctx, HashToken(in.Session))
			}
			return out, nil
		})
	huma.Register(api, huma.Operation{OperationID: "auth-setup", Method: http.MethodPost, Path: "/api/auth/setup", Tags: []string{"auth"}, Summary: "首次设置密码"},
		func(ctx context.Context, in *passwordIn) (*loginOut, error) {
			set, err := h.passwordSet(ctx)
			if err != nil {
				return nil, err
			}
			if set {
				return nil, huma.Error409Conflict("密码已设置，请登录")
			}
			if err := h.SetPassword(ctx, in.Body.Password); err != nil {
				return nil, err
			}
			return h.loginOK(ctx)
		})
	huma.Register(api, huma.Operation{OperationID: "auth-login", Method: http.MethodPost, Path: "/api/auth/login", Tags: []string{"auth"}},
		func(ctx context.Context, in *struct {
			Body struct {
				Password string `json:"password"`
			}
		}) (*loginOut, error) {
			if err := h.verifyPassword(ctx, in.Body.Password); err != nil {
				return nil, err
			}
			return h.loginOK(ctx)
		})
	huma.Register(api, huma.Operation{OperationID: "auth-logout", Method: http.MethodPost, Path: "/api/auth/logout", Tags: []string{"auth"}},
		func(ctx context.Context, in *cookieIn) (*loginOut, error) {
			if in.Session != "" {
				_ = h.store.DeleteWebSession(ctx, HashToken(in.Session), false)
			}
			out := &loginOut{SetCookie: http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true}}
			out.Body.OK = true
			return out, nil
		})
}

func (h *Hub) loginOK(ctx context.Context) (*loginOut, error) {
	c, err := h.newWebSession(ctx)
	if err != nil {
		return nil, err
	}
	out := &loginOut{SetCookie: c}
	out.Body.OK = true
	return out, nil
}

// ---------- Web 接口 ----------

// decisionPath：决策通知点开去哪——它所在的会话（决策就在会话里答复），没有会话时去工作台。
func decisionPath(d store.Decision) string {
	if d.SessionKey != "" {
		return "/?s=" + url.QueryEscape(d.SessionKey)
	}
	return "/"
}

type sessionsOut struct {
	Body struct {
		Sessions []store.Session `json:"sessions"`
		Devices  []store.Device  `json:"devices"`
		Rev      int64           `json:"rev" doc:"查询开始时的推送序号：网页把序号更大的会话推送叠加到这份列表上（R-56）"`
	}
}

type sessionDetailOut struct {
	Body struct {
		Session store.Session    `json:"session"`
		Events  []protocol.Event `json:"events"`
		// LastReply：最近一轮结束时的最后一条回复（Markdown，最长 2 万字）；还没有时省略（R-109）
		LastReply *store.LastReply `json:"last_reply,omitempty"`
		// Handoffs：这个会话交出去的和接手的交接（R-224），新的在前
		Handoffs []store.Handoff `json:"handoffs,omitempty"`
	}
}

type keyPath struct {
	Key string `path:"key"`
}

type idPath struct {
	ID string `path:"id"`
}

type refPath struct {
	Ref string `path:"ref"`
}

type devicesOut struct {
	Body struct {
		Devices []store.Device `json:"devices"`
	}
}

type deviceCreated struct {
	Body struct {
		Device store.Device `json:"device"`
		Token  string       `json:"token"`
	}
}

type projectsOut struct {
	Body struct {
		Projects   []store.Project   `json:"projects"`
		Workspaces []store.Workspace `json:"workspaces"`
	}
}

type workspaceIn struct {
	Body struct {
		Name   string             `json:"name" minLength:"1"`
		Notify store.NotifyConfig `json:"notify"`
	}
}

type workspaceOut struct {
	Body store.Workspace
}

type projectIn struct {
	ID   string `path:"id"`
	Body struct {
		Name        string `json:"name" minLength:"1"`
		WorkspaceID string `json:"workspace_id,omitempty"`
		DocRoot     string `json:"doc_root"`
		// AutoDispatch：会话空闲时自动送达排给它的待开始需求；不传表示不修改
		AutoDispatch *bool `json:"auto_dispatch,omitempty"`
		// Hidden：在项目列表里隐藏；不传表示不修改
		Hidden *bool `json:"hidden,omitempty"`
		// AgentRelease：允许 主调度 / 主助理 把草稿放行为待开始（R-166）；不传表示不修改
		AgentRelease *bool `json:"agent_release,omitempty"`
		// CommitOnAccept：验收通过后提示 主调度 提交属于该需求的改动（不推送）（R-192）；不传表示不修改
		CommitOnAccept *bool `json:"commit_on_accept,omitempty"`
		// ModelPolicy：子任务模型策略（R-228）：economy（经济）/ balanced（均衡，默认）/ quality（质量优先）；不传表示不修改
		ModelPolicy *string `json:"model_policy,omitempty" enum:"economy,balanced,quality" doc:"子任务模型策略：economy 经济 / balanced 均衡（默认）/ quality 质量优先"`
		// DevIdleMinutes：开发会话待命超时（R-229）：待命超过这么多分钟、手上没有排给它的需求，就提示收尾、不再送订阅事件；不传表示不修改
		DevIdleMinutes *int `json:"dev_idle_minutes,omitempty" minimum:"5" maximum:"720" doc:"开发会话待命超时的分钟数（5～720，默认 30）"`
		// StallMinutes：进行中停滞（R-243）：进行中的需求这么久没动静、又没有会话在跑，就进「待处理」；不传表示不修改
		StallMinutes *int `json:"stall_minutes,omitempty" minimum:"30" maximum:"2880" doc:"进行中需求停滞的分钟数（30～2880，默认 120）"`
		// StartNudgeMinutes：指派后未开始处理（R-278）：送达后这么久没开始处理就再提醒接收方一次，再过同样时长进「待处理」；不传表示不修改
		StartNudgeMinutes *int `json:"start_nudge_minutes,omitempty" minimum:"2" maximum:"240" doc:"指派送达后未开始处理的提醒分钟数（2～240，默认 10）"`
		// IdleOfflineMinutes：空等下线（R-272）：常驻会话连续空等累计超过这么多分钟，就写交接下线；不传表示不修改
		IdleOfflineMinutes *int `json:"idle_offline_minutes,omitempty" minimum:"60" maximum:"1440" doc:"空等下线时长的分钟数（60～1440，默认 240）"`
		// WaitMaxMinutes：Claude Code 会话 ks wait 单次最长等待（R-272），不超过 55 分钟；不传表示不修改
		WaitMaxMinutes *int `json:"wait_max_minutes,omitempty" minimum:"5" maximum:"55" doc:"ks wait 单次最长等待的分钟数（5～55，默认 55）"`
	}
}

type decisionsOut struct {
	Body struct {
		Decisions []store.Decision `json:"decisions"`
	}
}

type decisionOut struct {
	Body store.Decision
}

type answersIn struct {
	Body struct {
		Answers []struct {
			Ref     string   `json:"ref"`
			Answer  string   `json:"answer" doc:"答复；有要决策的点（points）时是附言，可空"`
			Files   []string `json:"files,omitempty" doc:"附件 ID（图片、录屏或任意文件）"`
			Choices []int    `json:"choices,omitempty" doc:"逐点的选择（R-112）：与决策的 points 一一对应，填选项序号"`
			Reject  bool     `json:"reject,omitempty" doc:"批准类的多点决策：拒绝（此时不要求逐点选择）"`
			Reason  string   `json:"reason,omitempty" doc:"可选的一句话理由（R-157）：没选推荐项时说为什么；会随答复送给提问的 Agent，并记在决策上"`
			Verdict string   `json:"verdict,omitempty" enum:"approved,rejected" doc:"批准类决策：用户点的是批准还是拒绝（R-239），存成字段，放行检查和界面只读它"`
		} `json:"answers" minItems:"1"`
	}
}

type reqsOut struct {
	Body struct {
		Requirements []store.Requirement `json:"requirements"`
		Statuses     []string            `json:"statuses"`
	}
}

type reqOut struct {
	Body store.Requirement
}

// reqCreatedOut：Agent 新建需求后的需求，外加同名提示（R-128）。
type reqCreatedOut struct {
	Body struct {
		store.Requirement
		Notices []string `json:"notices,omitempty" doc:"同项目已有同名未关闭需求时的提示；仍照常新建"`
	}
}

// evidenceOut：附证据后的需求，外加表达块的解析提示（R-97）。
type evidenceOut struct {
	Body struct {
		store.Requirement
		EvidenceID    string   `json:"evidence_id,omitempty" doc:"这次交的证据编号（R-189），要作废它时用（evidence_void / ks req evidence void）"`
		BlockWarnings []string `json:"block_warnings,omitempty" doc:"没有保存的块及原因；证据本身已保存"`
		ImageHints    []string `json:"image_hints,omitempty" doc:"附图的提示（R-183）：没写标题、改动框写错被丢掉等；证据本身已保存"`
	}
}

// decisionRaisedOut：新决策，外加表达块的解析提示（R-97）。
type decisionRaisedOut struct {
	Body struct {
		store.Decision
		BlockWarnings []string `json:"block_warnings,omitempty" doc:"没有保存的块及原因；决策本身已提出"`
		AutoLinked    bool     `json:"auto_linked,omitempty" doc:"没带需求、会话只关联一条没完成的需求，纸鸢自动挂上了（R-241）"`
	}
}

// ReqFields 是 Web 与 agent 共用的需求可写字段（导出以便 Huma 展开嵌入字段）。
type ReqFields struct {
	ProjectID  string `json:"project_id,omitempty"`
	Title      string `json:"title" minLength:"1"`
	Detail     string `json:"detail,omitempty"`
	Status     string `json:"status,omitempty" enum:"draft,ready,in_progress,blocked,review,done,shelved"`
	Priority   string `json:"priority,omitempty" enum:"P0,P1,P2,P3"`
	Source     string `json:"source,omitempty"`
	Acceptance string `json:"acceptance,omitempty"`
	DocLink    string `json:"doc_link,omitempty"`
	Kind       string `json:"kind,omitempty" doc:"design = 设计类需求（标注，Agent 按需要交方案图）；feature = 普通；不传表示不修改"`
	// 搁置原因（R-157）：把状态改成 shelved 时可选填；类别与一句话都可以不填
	ShelveKind   string `json:"shelve_kind,omitempty" enum:"duplicate,changed,dropped,other" doc:"搁置原因的类别：重复 / 方向变了 / 不做了 / 其他；只在状态改为 shelved 时记录，可不填"`
	ShelveReason string `json:"shelve_reason,omitempty" doc:"搁置原因的一句话；只在状态改为 shelved 时记录，可不填"`
	// Files 是描述里的附图 ID；不传表示不修改，传空数组表示清空。
	Files []string `json:"files,omitempty"`
}

func (h *Hub) toReq(ctx context.Context, f ReqFields) (store.Requirement, error) {
	r := store.Requirement{ProjectID: f.ProjectID, Title: f.Title, Detail: f.Detail, Status: f.Status, Priority: f.Priority, Source: f.Source, Acceptance: f.Acceptance, DocLink: f.DocLink, Kind: f.Kind}
	if f.Files != nil {
		files, err := h.store.ResolveFiles(ctx, f.Files)
		if err != nil {
			return r, err
		}
		r.Files = files
	}
	return r, nil
}

// SettingsView 是返回给 Web 的配置，隐藏 API key。
type SettingsView struct {
	PublicURL     string             `json:"public_url"`
	AI            AIFeatures         `json:"ai"`
	SummaryBase   string             `json:"summary_base_url"`
	SummaryModel  string             `json:"summary_model"`
	SummaryKeySet bool               `json:"summary_api_key_set"`
	NotifyDefault store.NotifyConfig `json:"notify_default"`
	NotifyEvents  []string           `json:"notify_events"`
	ImageMB       int                `json:"upload_image_mb" doc:"图片大小上限（MB）"`
	VideoMB       int                `json:"upload_video_mb" doc:"录屏大小上限（MB）"`
	FileMB        int                `json:"upload_file_mb" doc:"其它文件（图片、录屏以外）大小上限（MB）"`
	PrototypeMB   int                `json:"upload_prototype_mb" doc:"可交互原型包（zip）大小上限（MB，R-197）"`
	DevRequestMin int                `json:"dev_request_minutes" doc:"PM 推荐开 Dev 会话的批准时限（分钟），到点没批视为拒绝"`
	CompactTokens int                `json:"compact_tokens" doc:"建议压缩阈值（token）：会话上下文超过它时标「建议压缩」、圆环变红，到一半变黄（R-224、R-225）"`
}

type settingsIn struct {
	Body struct {
		PublicURL     string             `json:"public_url"`
		AI            AIFeatures         `json:"ai"`
		SummaryBase   string             `json:"summary_base_url"`
		SummaryModel  string             `json:"summary_model"`
		SummaryKey    string             `json:"summary_api_key,omitempty" doc:"留空表示不修改"`
		NotifyDefault store.NotifyConfig `json:"notify_default"`
		ImageMB       int                `json:"upload_image_mb,omitempty" minimum:"0" maximum:"4096" doc:"图片大小上限（MB），0 或留空用默认 10"`
		VideoMB       int                `json:"upload_video_mb,omitempty" minimum:"0" maximum:"4096" doc:"录屏大小上限（MB），0 或留空用默认 50"`
		FileMB        int                `json:"upload_file_mb,omitempty" minimum:"0" maximum:"4096" doc:"其它文件大小上限（MB），0 或留空用默认 20"`
		PrototypeMB   int                `json:"upload_prototype_mb,omitempty" minimum:"0" maximum:"4096" doc:"可交互原型包（zip）大小上限（MB），0 或留空用默认 20"`
		DevRequestMin int                `json:"dev_request_minutes,omitempty" minimum:"0" maximum:"1440" doc:"PM 推荐开 Dev 会话的批准时限（分钟），0 或留空用默认 30"`
		CompactTokens int                `json:"compact_tokens,omitempty" minimum:"0" maximum:"2000000" doc:"建议压缩阈值（token），0 或留空用默认 30 万；最低 5 万"`
	}
}

type settingsOut struct {
	Body SettingsView
}

func (h *Hub) settingsView(ctx context.Context) (*settingsOut, error) {
	s, err := h.Settings(ctx)
	if err != nil {
		return nil, err
	}
	img, vid, file := s.Upload.bytes()
	return &settingsOut{Body: SettingsView{
		PublicURL: s.PublicURL, AI: s.AI, SummaryBase: s.Summary.BaseURL, SummaryModel: s.Summary.Model,
		SummaryKeySet: s.Summary.APIKey != "", NotifyDefault: s.NotifyDefault, NotifyEvents: notify.AllEvents,
		ImageMB: int(img >> 20), VideoMB: int(vid >> 20), FileMB: int(file >> 20), PrototypeMB: int(s.Upload.protoBytes() >> 20), DevRequestMin: h.devRequestMinutes(ctx, 0), CompactTokens: s.compactTokens(),
	}}, nil
}

func (h *Hub) registerWeb(api huma.API) {
	tag := func(t string) []string { return []string{t} }

	huma.Register(api, huma.Operation{OperationID: "change-password", Method: http.MethodPost, Path: "/api/password", Tags: tag("auth")},
		func(ctx context.Context, in *struct {
			Body struct {
				Old string `json:"old"`
				New string `json:"new" minLength:"8"`
			}
		}) (*loginOut, error) {
			if err := h.verifyPassword(ctx, in.Body.Old); err != nil {
				var se huma.StatusError
				if errors.As(err, &se) && se.GetStatus() == http.StatusTooManyRequests {
					return nil, err
				}
				return nil, huma.Error400BadRequest("原密码错误")
			}
			if err := h.SetPassword(ctx, in.Body.New); err != nil {
				return nil, err
			}
			return h.loginOK(ctx)
		})

	// 实例墙
	huma.Register(api, huma.Operation{OperationID: "list-sessions", Method: http.MethodGet, Path: "/api/sessions", Tags: tag("sessions")},
		func(ctx context.Context, in *struct {
			Background bool `query:"background" doc:"为 true 时只列后台任务（桌面应用自己发起的，如 Codex 项目建议）；默认不含它们（R-179）"`
		}) (*sessionsOut, error) {
			out := &sessionsOut{}
			out.Body.Rev = h.broker.Rev()
			var err error
			if out.Body.Sessions, err = h.store.ListSessions(ctx, recentWindow, in.Background); err != nil {
				return nil, err
			}
			out.Body.Devices, err = h.store.ListDevices(ctx)
			return out, err
		})
	huma.Register(api, huma.Operation{OperationID: "get-session", Method: http.MethodGet, Path: "/api/sessions/{key}", Tags: tag("sessions")},
		func(ctx context.Context, in *keyPath) (*sessionDetailOut, error) {
			out := &sessionDetailOut{}
			var err error
			if out.Body.Session, err = h.store.GetSession(ctx, in.Key); err != nil {
				return nil, apiErr(err)
			}
			if out.Body.Events, err = h.store.SessionEvents(ctx, in.Key, 50); err != nil {
				return nil, err
			}
			if out.Body.LastReply, err = h.store.GetLastReply(ctx, in.Key); err != nil {
				return nil, err
			}
			out.Body.Handoffs, err = h.store.HandoffsOf(ctx, in.Key)
			return out, err
		})

	huma.Register(api, huma.Operation{OperationID: "mark-session-done", Method: http.MethodPost, Path: "/api/sessions/{key}/done", Tags: tag("sessions"), Summary: "手动标记 / 取消“已完成”（你再给它发消息时自动取消）"},
		func(ctx context.Context, in *struct {
			Key  string `path:"key"`
			Body struct {
				Done bool `json:"done"`
			}
		}) (*sessionDetailOut, error) {
			if err := h.store.SetSessionDone(ctx, in.Key, in.Body.Done); err != nil {
				return nil, apiErr(err)
			}
			h.broker.publish("sessions")
			out := &sessionDetailOut{}
			var err error
			out.Body.Session, err = h.store.GetSession(ctx, in.Key)
			out.Body.Events = []protocol.Event{}
			return out, apiErr(err)
		})

	// 设备
	huma.Register(api, huma.Operation{OperationID: "list-devices", Method: http.MethodGet, Path: "/api/devices", Tags: tag("devices")},
		func(ctx context.Context, _ *empty) (*devicesOut, error) {
			out := &devicesOut{}
			var err error
			out.Body.Devices, err = h.store.ListDevices(ctx)
			return out, err
		})
	huma.Register(api, huma.Operation{OperationID: "create-device", Method: http.MethodPost, Path: "/api/devices", Tags: tag("devices"), Summary: "新增设备并返回一次性显示的 token"},
		func(ctx context.Context, in *struct {
			Body struct {
				Name string `json:"name" minLength:"1" maxLength:"64"`
			}
		}) (*deviceCreated, error) {
			tok := NewDeviceToken()
			d, err := h.store.CreateDevice(ctx, strings.TrimSpace(in.Body.Name), HashToken(tok))
			if err != nil {
				return nil, apiErr(err)
			}
			h.broker.publish("devices")
			out := &deviceCreated{}
			out.Body.Device, out.Body.Token = d, tok
			return out, nil
		})
	huma.Register(api, huma.Operation{OperationID: "revoke-device", Method: http.MethodPost, Path: "/api/devices/{id}/revoke", Tags: tag("devices")},
		func(ctx context.Context, in *idPath) (*okBody, error) {
			if err := h.store.RevokeDevice(ctx, in.ID); err != nil {
				return nil, apiErr(err)
			}
			h.mu.Lock()
			if c := h.conns[in.ID]; c != nil {
				c.cancel()
			}
			h.mu.Unlock()
			h.broker.publish("devices", "sessions")
			return ok(), nil
		})

	huma.Register(api, huma.Operation{OperationID: "delete-device", Method: http.MethodDelete, Path: "/api/devices/{id}", Tags: tag("devices"), Summary: "删除已吊销的设备（保留历史会话），名称可重新使用"},
		func(ctx context.Context, in *idPath) (*okBody, error) {
			if err := h.store.DeleteDevice(ctx, in.ID); err != nil {
				return nil, apiErr(err)
			}
			h.broker.publish("devices", "sessions")
			return ok(), nil
		})

	// 项目与工作空间
	huma.Register(api, huma.Operation{OperationID: "list-projects", Method: http.MethodGet, Path: "/api/projects", Tags: tag("projects")},
		func(ctx context.Context, _ *empty) (*projectsOut, error) {
			out := &projectsOut{}
			var err error
			if out.Body.Projects, err = h.store.ListProjects(ctx); err != nil {
				return nil, err
			}
			out.Body.Workspaces, err = h.store.ListWorkspaces(ctx)
			return out, err
		})
	huma.Register(api, huma.Operation{OperationID: "update-project", Method: http.MethodPut, Path: "/api/projects/{id}", Tags: tag("projects")},
		func(ctx context.Context, in *projectIn) (*okBody, error) {
			cur, err := h.store.GetProject(ctx, in.ID)
			if err != nil {
				return nil, apiErr(err)
			}
			auto, hidden, release, commit := cur.AutoDispatch, cur.Hidden, cur.AgentRelease, cur.CommitOnAccept
			if in.Body.CommitOnAccept != nil {
				commit = *in.Body.CommitOnAccept
			}
			policy := cur.ModelPolicy
			if in.Body.ModelPolicy != nil {
				policy = *in.Body.ModelPolicy
			}
			idle := cur.DevIdleMinutes
			if in.Body.DevIdleMinutes != nil {
				idle = *in.Body.DevIdleMinutes
			}
			stall := cur.StallMinutes
			if in.Body.StallMinutes != nil {
				stall = *in.Body.StallMinutes
			}
			nudge := cur.StartNudgeMinutes
			if in.Body.StartNudgeMinutes != nil {
				nudge = *in.Body.StartNudgeMinutes
			}
			offline, waitMax := cur.IdleOfflineMinutes, cur.WaitMaxMinutes
			if in.Body.IdleOfflineMinutes != nil {
				offline = *in.Body.IdleOfflineMinutes
			}
			if in.Body.WaitMaxMinutes != nil {
				waitMax = *in.Body.WaitMaxMinutes
			}
			if in.Body.AgentRelease != nil {
				release = *in.Body.AgentRelease
			}
			if in.Body.AutoDispatch != nil {
				auto = *in.Body.AutoDispatch
			}
			if in.Body.Hidden != nil {
				hidden = *in.Body.Hidden
			}
			err = h.store.UpdateProject(ctx, store.Project{ID: in.ID, Name: in.Body.Name, WorkspaceID: in.Body.WorkspaceID, DocRoot: in.Body.DocRoot, AutoDispatch: auto, Hidden: hidden, AgentRelease: release, CommitOnAccept: commit, ModelPolicy: policy, DevIdleMinutes: idle, StallMinutes: stall, StartNudgeMinutes: nudge, IdleOfflineMinutes: offline, WaitMaxMinutes: waitMax})
			if err != nil {
				return nil, apiErr(err)
			}
			if now, err := h.store.GetProject(ctx, in.ID); err == nil {
				for _, text := range projectSettingsChanges(cur, now) {
					h.onSettingsChanged(ctx, in.ID, text) // 用户改项目设置，订阅了 settings 的 主调度 / 主助理 被唤醒（R-171）
				}
			}
			h.broker.publish("projects", "sessions", "requirements")
			return ok(), nil
		})
	huma.Register(api, huma.Operation{OperationID: "merge-project", Method: http.MethodPost, Path: "/api/projects/{id}/merge", Tags: tag("projects"), Summary: "把项目并入另一个项目"},
		func(ctx context.Context, in *struct {
			ID   string `path:"id"`
			Body struct {
				Into string `json:"into" minLength:"1" doc:"并入的目标项目 ID"`
			}
		}) (*okBody, error) {
			if err := h.store.MergeProject(ctx, in.ID, in.Body.Into); err != nil {
				if errors.Is(err, store.ErrNotFound) {
					return nil, apiErr(err)
				}
				return nil, huma.Error400BadRequest(err.Error())
			}
			h.broker.publish("projects", "sessions", "requirements", "decisions", "plans")
			return ok(), nil
		})
	huma.Register(api, huma.Operation{OperationID: "create-workspace", Method: http.MethodPost, Path: "/api/workspaces", Tags: tag("projects")},
		func(ctx context.Context, in *workspaceIn) (*workspaceOut, error) {
			w, err := h.store.SaveWorkspace(ctx, store.Workspace{Name: in.Body.Name, Notify: in.Body.Notify})
			if err != nil {
				return nil, apiErr(err)
			}
			h.broker.publish("projects")
			return &workspaceOut{Body: w}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "update-workspace", Method: http.MethodPut, Path: "/api/workspaces/{id}", Tags: tag("projects")},
		func(ctx context.Context, in *struct {
			ID   string `path:"id"`
			Body struct {
				Name   string             `json:"name" minLength:"1"`
				Notify store.NotifyConfig `json:"notify"`
			}
		}) (*workspaceOut, error) {
			w, err := h.store.SaveWorkspace(ctx, store.Workspace{ID: in.ID, Name: in.Body.Name, Notify: in.Body.Notify})
			if err != nil {
				return nil, apiErr(err)
			}
			h.broker.publish("projects")
			return &workspaceOut{Body: w}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "delete-workspace", Method: http.MethodDelete, Path: "/api/workspaces/{id}", Tags: tag("projects")},
		func(ctx context.Context, in *idPath) (*okBody, error) {
			if err := h.store.DeleteWorkspace(ctx, in.ID); err != nil {
				return nil, apiErr(err)
			}
			h.broker.publish("projects")
			return ok(), nil
		})
	// passkey 管理（R-7）
	huma.Register(api, huma.Operation{OperationID: "list-passkeys", Method: http.MethodGet, Path: "/api/passkeys", Tags: tag("auth"), Summary: "已登记的 passkey"},
		func(ctx context.Context, _ *empty) (*struct {
			Body struct {
				Passkeys []store.Passkey `json:"passkeys"`
			}
		}, error) {
			out := &struct {
				Body struct {
					Passkeys []store.Passkey `json:"passkeys"`
				}
			}{}
			var err error
			out.Body.Passkeys, err = h.store.ListPasskeys(ctx)
			return out, err
		})
	huma.Register(api, huma.Operation{OperationID: "update-passkey", Method: http.MethodPut, Path: "/api/passkeys/{id}", Tags: tag("auth"), Summary: "重命名 passkey"},
		func(ctx context.Context, in *struct {
			ID   string `path:"id"`
			Body struct {
				Name string `json:"name" minLength:"1"`
			}
		}) (*okBody, error) {
			if err := h.store.RenamePasskey(ctx, in.ID, in.Body.Name); err != nil {
				return nil, err
			}
			h.broker.publish("settings")
			return ok(), nil
		})
	huma.Register(api, huma.Operation{OperationID: "delete-passkey", Method: http.MethodDelete, Path: "/api/passkeys/{id}", Tags: tag("auth"), Summary: "删除 passkey"},
		func(ctx context.Context, in *struct {
			ID string `path:"id"`
		}) (*okBody, error) {
			if err := h.store.DeletePasskey(ctx, in.ID); err != nil {
				return nil, err
			}
			h.broker.publish("settings")
			return ok(), nil
		})
	// 系统推送（R-49）
	huma.Register(api, huma.Operation{OperationID: "get-push", Method: http.MethodGet, Path: "/api/push", Tags: tag("projects"), Summary: "系统推送的公钥与已订阅的设备"},
		func(ctx context.Context, _ *empty) (*struct {
			Body struct {
				PublicKey string          `json:"public_key"`
				Subs      []store.PushSub `json:"subs"`
			}
		}, error) {
			out := &struct {
				Body struct {
					PublicKey string          `json:"public_key"`
					Subs      []store.PushSub `json:"subs"`
				}
			}{}
			var err error
			if out.Body.PublicKey, _, err = h.vapidKeys(ctx); err != nil {
				return nil, err
			}
			out.Body.Subs, err = h.store.ListPushSubs(ctx)
			return out, err
		})
	huma.Register(api, huma.Operation{OperationID: "subscribe-push", Method: http.MethodPost, Path: "/api/push/subscribe", Tags: tag("projects"), Summary: "在这台设备上开启系统推送"},
		func(ctx context.Context, in *struct {
			Origin string `header:"Origin"`
			Body   struct {
				Endpoint string `json:"endpoint" minLength:"1"`
				P256dh   string `json:"p256dh" minLength:"1"`
				Auth     string `json:"auth" minLength:"1"`
				Name     string `json:"name,omitempty"`
			}
		}) (*struct{ Body store.PushSub }, error) {
			if !strings.HasPrefix(in.Body.Endpoint, "https://") {
				return nil, huma.Error400BadRequest("推送地址无效")
			}
			p, err := h.store.SavePushSub(ctx, store.PushSub{Endpoint: in.Body.Endpoint, P256dh: in.Body.P256dh, Auth: in.Body.Auth, Name: in.Body.Name, Origin: in.Origin})
			if err != nil {
				return nil, err
			}
			h.broker.publish("settings")
			return &struct{ Body store.PushSub }{p}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "delete-push", Method: http.MethodPost, Path: "/api/push/unsubscribe", Tags: tag("projects"), Summary: "关闭某台设备的系统推送（按 ID 或推送地址）"},
		func(ctx context.Context, in *struct {
			Body struct {
				Ref string `json:"ref" minLength:"1"`
			}
		}) (*okBody, error) {
			if err := h.store.DeletePushSub(ctx, in.Body.Ref); err != nil {
				return nil, err
			}
			h.broker.publish("settings")
			return ok(), nil
		})
	huma.Register(api, huma.Operation{OperationID: "test-push", Method: http.MethodPost, Path: "/api/push/test", Tags: tag("projects"), Summary: "给某台设备发一条测试推送"},
		func(ctx context.Context, in *struct {
			Body struct {
				Ref string `json:"ref" minLength:"1"`
			}
		}) (*struct {
			Body struct {
				Sent int `json:"sent"`
			}
		}, error) {
			n, err := h.sendPush(ctx, notify.Message{Event: "test", Title: pushTestTitle, Body: pushTestBody}, "/settings?tab=notify", in.Body.Ref)
			if err != nil {
				return nil, huma.Error502BadGateway("推送服务拒绝了这条推送：" + err.Error())
			}
			if n == 0 {
				return nil, huma.Error400BadRequest("没有送出：这台设备的订阅可能已失效，请关闭后重新开启")
			}
			out := &struct {
				Body struct {
					Sent int `json:"sent"`
				}
			}{}
			out.Body.Sent = n
			return out, nil
		})
	huma.Register(api, huma.Operation{OperationID: "test-notify", Method: http.MethodPost, Path: "/api/notify/test", Tags: tag("projects"), Summary: "向指定渠道配置发送测试通知"},
		func(ctx context.Context, in *struct {
			Body store.NotifyConfig
		}) (*okBody, error) {
			if !notify.HasChannel(in.Body) {
				return nil, huma.Error400BadRequest("未配置任何渠道")
			}
			if err := notify.Send(ctx, in.Body, notify.Message{Title: notifyTestTitle, Body: notifyTestBody, Group: "kitestring"}); err != nil {
				return nil, huma.Error502BadGateway(err.Error())
			}
			return ok(), nil
		})

	// 决策收件箱
	huma.Register(api, huma.Operation{OperationID: "list-decisions", Method: http.MethodGet, Path: "/api/decisions", Tags: tag("decisions")},
		func(ctx context.Context, in *struct {
			Status []string `query:"status" enum:"pending,answered,delivered,closed"`
		}) (*decisionsOut, error) {
			out := &decisionsOut{}
			var err error
			out.Body.Decisions, err = h.store.ListDecisions(ctx, in.Status, 500)
			return out, err
		})
	huma.Register(api, huma.Operation{OperationID: "answer-decisions", Method: http.MethodPost, Path: "/api/decisions/answers", Tags: tag("decisions"), Summary: "批量作答"},
		func(ctx context.Context, in *answersIn) (*decisionsOut, error) {
			out := &decisionsOut{Body: struct {
				Decisions []store.Decision `json:"decisions"`
			}{Decisions: []store.Decision{}}}
			devices := map[string]bool{}
			for _, a := range in.Body.Answers {
				files, err := h.store.ResolveFiles(ctx, a.Files)
				if err != nil {
					return nil, err
				}
				answer, choices := a.Answer, []int(nil)
				d0, err := h.store.GetDecision(ctx, a.Ref)
				if err != nil {
					return nil, apiErr(err)
				}
				// 改状态申请（R-188）：需求状态在批准前已被改动 → 这条申请作废，不再记答复
				if vd, voided, err := h.checkStatusRequest(ctx, d0); err != nil {
					return nil, err
				} else if voided {
					out.Body.Decisions = append(out.Body.Decisions, vd)
					continue
				}
				if len(a.Choices) > 0 || a.Reject {
					// 多点决策：hub 把逐点的选择拼成答复，注入给 Agent 的内容不依赖网页怎么写
					if len(d0.Points) > 0 {
						if answer, err = pointsAnswer(d0, a.Choices, a.Answer, a.Reject); err != nil {
							return nil, huma.Error400BadRequest(err.Error())
						}
						choices = a.Choices
					}
				}
				d, err := h.store.AnswerDecisionPoints(ctx, a.Ref, answer, files, choices, a.Reason, answerVerdict(a.Verdict, a.Reject))
				if err != nil {
					return nil, apiErr(err)
				}
				// 离开期间你在哪台设备上答的（R-210）
				h.awayUser(ctx, "answer", d.ProjectID, d.Code, d.Question, brief(approvalText(d), 80))
				h.onRequestAnswered(ctx, d)         // 批准开 Dev 会话 → 推送接入语（R-169）
				h.applyStatusRequest(ctx, d)        // 批准改状态申请 → 纸鸢改状态（R-188）
				h.watchDecisionAnswered(ctx, d, "") // 关联决策被作答，告诉订阅了这条需求的会话（R-249 P4）
				out.Body.Decisions = append(out.Body.Decisions, d)
				if x, err := h.store.GetSession(ctx, d.SessionKey); err == nil {
					devices[x.DeviceID] = true
				}
			}
			for id := range devices {
				h.pushDecisions(ctx, id)
			}
			h.broker.publish("decisions", "sessions", "requirements")
			return out, nil
		})
	decisionAction := func(id, path string, fn func(ctx context.Context, ref string, body map[string]string) (store.Decision, error)) {
		huma.Register(api, huma.Operation{OperationID: id, Method: http.MethodPost, Path: "/api/decisions/{ref}/" + path, Tags: tag("decisions")},
			func(ctx context.Context, in *struct {
				Ref  string `path:"ref"`
				Body map[string]string
			}) (*decisionOut, error) {
				d, err := fn(ctx, in.Ref, in.Body)
				if err != nil {
					return nil, apiErr(err)
				}
				h.decisionsChanged(ctx, d)
				return &decisionOut{Body: d}, nil
			})
	}
	decisionAction("reassign-decision", "reassign", func(ctx context.Context, ref string, b map[string]string) (store.Decision, error) {
		if _, err := h.store.GetSession(ctx, b["session_key"]); err != nil {
			return store.Decision{}, errors.New("目标会话不存在")
		}
		prev, err := h.store.GetDecision(ctx, ref)
		if err != nil {
			return prev, err
		}
		d, err := h.store.ReassignDecision(ctx, ref, b["session_key"])
		if err == nil && prev.SessionKey != "" {
			if x, err := h.store.GetSession(ctx, prev.SessionKey); err == nil {
				h.pushDecisions(ctx, x.DeviceID) // 从原设备的缓存中撤下
			}
		}
		return d, err
	})
	decisionAction("resend-decision", "resend", func(ctx context.Context, ref string, _ map[string]string) (store.Decision, error) {
		return h.store.ResendDecision(ctx, ref)
	})
	decisionAction("close-decision", "close", func(ctx context.Context, ref string, b map[string]string) (store.Decision, error) {
		return h.store.CloseDecision(ctx, ref, b["reason"], b["replaced_by"])
	})

	// 需求看板
	huma.Register(api, huma.Operation{OperationID: "list-requirements", Method: http.MethodGet, Path: "/api/requirements", Tags: tag("requirements")},
		func(ctx context.Context, in *struct {
			ProjectID string   `query:"project_id"`
			Status    []string `query:"status"`
		}) (*reqsOut, error) {
			out := &reqsOut{}
			var err error
			out.Body.Requirements, err = h.store.ListRequirements(ctx, in.ProjectID, in.Status)
			out.Body.Statuses = store.ReqStatuses
			return out, err
		})
	huma.Register(api, huma.Operation{OperationID: "create-requirement", Method: http.MethodPost, Path: "/api/requirements", Tags: tag("requirements")},
		func(ctx context.Context, in *struct{ Body ReqFields }) (*reqOut, error) {
			r, err := h.toReq(ctx, in.Body)
			if err != nil {
				return nil, err
			}
			r.CreatedBy = "user"
			r, err = h.store.CreateRequirement(ctx, r)
			if err != nil {
				return nil, apiErr(err)
			}
			h.broker.publish("requirements")
			h.kick(ctx, r.ID) // 项目有 PM 时，新建的待开始需求排给它（R-85）
			return &reqOut{Body: r}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "get-requirement", Method: http.MethodGet, Path: "/api/requirements/{ref}", Tags: tag("requirements")},
		func(ctx context.Context, in *refPath) (*reqOut, error) {
			r, err := h.store.GetRequirement(ctx, in.Ref)
			return &reqOut{Body: r}, apiErr(err)
		})
	huma.Register(api, huma.Operation{OperationID: "update-requirement", Method: http.MethodPut, Path: "/api/requirements/{ref}", Tags: tag("requirements")},
		func(ctx context.Context, in *struct {
			Ref  string `path:"ref"`
			Body ReqFields
		}) (*reqOut, error) {
			cur, err := h.store.GetRequirement(ctx, in.Ref)
			if err != nil {
				return nil, apiErr(err)
			}
			r, err := h.toReq(ctx, in.Body)
			if err != nil {
				return nil, err
			}
			r.ID = cur.ID
			uctx := ctx
			if r.Status == store.ReqShelved {
				uctx = store.WithActor(ctx, store.Actor{ReasonKind: in.Body.ShelveKind, Reason: in.Body.ShelveReason})
			}
			if r.Status == store.ReqDraft && cur.Status != store.ReqDraft && cur.Release != nil {
				uctx = store.WithActor(ctx, store.Actor{Via: "withdraw", Ref: cur.Release.Code})
			}
			r, err = h.store.UpdateRequirement(uctx, r)
			if err != nil {
				return nil, apiErr(err)
			}
			h.reopenStage(ctx, cur, r.Status)
			if r.Status == store.ReqDraft && cur.Status != store.ReqDraft {
				h.withdrawRelease(ctx, cur, "", "") // 你把放行过的需求改回草稿 = 撤回放行（R-166）
			}
			h.broker.publish("requirements", "sessions", "plans")
			h.kick(ctx, r.ID)
			if r.Status != cur.Status {
				h.refreshDelivered(ctx, r)
				var told []string
				if r.Status == store.ReqDraft && cur.Release != nil {
					told = withdrawTold(cur) // 撤回放行时已经告诉过在做的会话，不重复送
				}
				h.onUserStatusChanged(ctx, cur, r, told...) // 指派的 PM、关联会话和订阅者知道用户改了状态（R-171）
			}
			// 用户在网页上改了内容（R-249 P4）：改验收标准要动手，改标题 / 描述只告知
			var changed []string
			for _, f := range [][3]string{{"标题", cur.Title, r.Title}, {"描述", cur.Detail, r.Detail}, {"验收标准", cur.Acceptance, r.Acceptance}} {
				if f[1] != f[2] {
					changed = append(changed, f[0])
				}
			}
			if len(changed) > 0 {
				ev := evDetail
				if cur.Acceptance != r.Acceptance {
					ev = evAcceptance
				}
				h.reqEvent(ctx, r, ev, "", "改了"+strings.Join(changed, "、"))
			}
			return &reqOut{Body: r}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "delete-requirement", Method: http.MethodDelete, Path: "/api/requirements/{ref}", Tags: tag("requirements")},
		func(ctx context.Context, in *refPath) (*okBody, error) {
			cur, err := h.store.GetRequirement(ctx, in.Ref)
			if err != nil {
				return nil, apiErr(err)
			}
			if err := h.store.DeleteRequirement(ctx, cur.ID); err != nil {
				return nil, apiErr(err)
			}
			h.broker.publish("requirements", "sessions")
			return ok(), nil
		})
	huma.Register(api, huma.Operation{OperationID: "link-requirement", Method: http.MethodPost, Path: "/api/requirements/{ref}/links", Tags: tag("requirements"), Summary: "手动关联会话（D5）"},
		func(ctx context.Context, in *struct {
			Ref  string `path:"ref"`
			Body struct {
				SessionKey string `json:"session_key"`
				Unlink     bool   `json:"unlink,omitempty"`
			}
		}) (*reqOut, error) {
			r, err := h.store.GetRequirement(ctx, in.Ref)
			if err != nil {
				return nil, apiErr(err)
			}
			if in.Body.Unlink {
				err = h.store.UnlinkSession(ctx, r.ID, in.Body.SessionKey)
			} else {
				err = h.store.LinkSession(ctx, r.ID, in.Body.SessionKey)
			}
			if err != nil {
				return nil, apiErr(err)
			}
			h.broker.publish("requirements", "sessions")
			if !in.Body.Unlink {
				h.kick(ctx, r.ID) // 待开始的需求关联到空闲会话：马上送过去
			}
			r, err = h.store.GetRequirement(ctx, r.ID)
			return &reqOut{Body: r}, apiErr(err)
		})

	// 设置
	huma.Register(api, huma.Operation{OperationID: "get-settings", Method: http.MethodGet, Path: "/api/settings", Tags: tag("settings")},
		func(ctx context.Context, _ *empty) (*settingsOut, error) { return h.settingsView(ctx) })
	huma.Register(api, huma.Operation{OperationID: "update-settings", Method: http.MethodPut, Path: "/api/settings", Tags: tag("settings")},
		func(ctx context.Context, in *settingsIn) (*settingsOut, error) {
			cur, err := h.Settings(ctx)
			if err != nil {
				return nil, err
			}
			b := in.Body
			cur.PublicURL = strings.TrimSuffix(strings.TrimSpace(b.PublicURL), "/")
			cur.AI = b.AI
			cur.Summary.BaseURL = strings.TrimSpace(b.SummaryBase)
			cur.Summary.Model = strings.TrimSpace(b.SummaryModel)
			if b.SummaryKey != "" {
				cur.Summary.APIKey = b.SummaryKey
			}
			cur.NotifyDefault = b.NotifyDefault
			cur.Upload = UploadLimits{ImageMB: b.ImageMB, VideoMB: b.VideoMB, FileMB: b.FileMB, PrototypeMB: b.PrototypeMB}
			cur.DevRequestMinutes = b.DevRequestMin
			cur.CompactTokens = b.CompactTokens
			if err := h.SaveSettings(ctx, cur); err != nil {
				return nil, err
			}
			return h.settingsView(ctx)
		})
	huma.Register(api, huma.Operation{OperationID: "recap", Method: http.MethodPost, Path: "/api/recap", Tags: tag("ai"), Summary: "生成一段时间内的进展回顾"},
		func(ctx context.Context, in *recapIn) (*recapOut, error) {
			if !in.Body.To.After(in.Body.From) {
				return nil, huma.Error400BadRequest("时间范围无效")
			}
			text, err := h.Recap(ctx, in.Body.From, in.Body.To)
			if err != nil {
				return nil, huma.Error422UnprocessableEntity(err.Error())
			}
			out := &recapOut{}
			out.Body.Markdown, out.Body.From, out.Body.To = text, in.Body.From, in.Body.To
			return out, nil
		})
	huma.Register(api, huma.Operation{OperationID: "test-summary", Method: http.MethodPost, Path: "/api/settings/test-summary", Tags: tag("settings"),
		Summary: "用一段固定的示例对话调用摘要模型，验证配置是否可用"},
		func(ctx context.Context, _ *empty) (*summaryTestOut, error) {
			s, err := h.Settings(ctx)
			if err != nil {
				return nil, err
			}
			out := &summaryTestOut{}
			out.Body.SamplePrompt = summarySamplePrompt
			out.Body.SampleReply = summarySampleReply
			start := time.Now()
			text, err := summary.Generate(h.aiCtx(ctx, aiTest), s.Summary, summarySamplePrompt, summarySampleReply)
			out.Body.LatencyMS = time.Since(start).Milliseconds()
			if err != nil {
				return nil, huma.Error502BadGateway(err.Error())
			}
			out.Body.Summary = text
			return out, nil
		})
}

type recapIn struct {
	Body struct {
		From time.Time `json:"from"`
		To   time.Time `json:"to"`
	}
}

type recapOut struct {
	Body struct {
		Markdown string    `json:"markdown"`
		From     time.Time `json:"from"`
		To       time.Time `json:"to"`
	}
}

const (
	summarySamplePrompt = "给登录页加上“记住我”功能"
	summarySampleReply  = "已在登录表单增加“记住我”复选框，勾选后会话有效期延长到 30 天，并补充了单元测试，全部通过。"
)

type summaryTestOut struct {
	Body struct {
		SamplePrompt string `json:"sample_prompt" doc:"示例：用户最后一条指令"`
		SampleReply  string `json:"sample_reply" doc:"示例：助手最后一条回复"`
		Summary      string `json:"summary" doc:"模型生成的一句话摘要"`
		LatencyMS    int64  `json:"latency_ms"`
	}
}

// ---------- agent 接口（设备 token，经 daemon 转发 MCP / CLI 调用） ----------

type agentDecisionIn struct {
	Body struct {
		SessionKey        string   `json:"session_key,omitempty" doc:"提出决策的会话，由 daemon 填写"`
		Question          string   `json:"question" minLength:"1"`
		Context           string   `json:"context,omitempty"`
		Options           []string `json:"options,omitempty"`
		OptionImages      []string `json:"option_images,omitempty" doc:"每个选项的效果图附件 ID（与 options 一一对应，可留空），先上传到 /api/agent/files"`
		Approval          bool     `json:"approval,omitempty" doc:"请用户批准：选项固定为“批准 / 拒绝”，两种都可以附言"`
		Images            []string `json:"images,omitempty" doc:"问题附带的图（例如待批准的方案图）的附件 ID"`
		Prototypes        []string `json:"prototypes,omitempty" doc:"问题附带的可交互原型 ID（R-197），先传到 /api/agent/prototypes"`
		OptionPrototypes  []string `json:"option_prototypes,omitempty" doc:"每个选项的可交互原型 ID（与 options 一一对应，可留空，R-197）"`
		Blocks            []any    `json:"blocks,omitempty" doc:"表达块（R-97），同证据；决策常用 options / compare / table / flow。不合法的块丢掉并在 block_warnings 里说明"`
		Points            []any    `json:"points,omitempty" doc:"要决策的点（R-112）：[{question, options:[{label, desc, recommended, image}]}]，用户逐点选择；不合法的点丢掉并在 block_warnings 里说明"`
		Summary           []string `json:"summary,omitempty" doc:"方案要点，每条一行（以需求编号开头）；有它时网页把完整说明折叠起来"`
		Recommendation    string   `json:"recommendation,omitempty"`
		RecommendedOption *int     `json:"recommended_option,omitempty" doc:"推荐的是第几个选项（从 0 开始，R-239）；不填时只有 recommendation 与某个选项文字完全相同才算推荐，不做模糊匹配"`
		Requirement       string   `json:"requirement,omitempty" doc:"关联需求 ID 或 R-编号；不写时会话只关联一条没完成的需求就自动挂上（R-241），写 none 表示与在做的需求无关、不挂"`
		FromDecision      string   `json:"from_decision,omitempty" doc:"来源决策（R-154）：这条决策接着哪条决策往下问，或推翻了哪条（D-x）"`
		Overturns         bool     `json:"overturns,omitempty" doc:"为 true 表示这条决策要推翻 from_decision 的结论（需同时给 from_decision）"`
		Risk              string   `json:"risk,omitempty" enum:"low,medium,high" doc:"风险档（R-163）：不填按 high。产品方向、设计选型、语义 / 接口 / 权限变更、不可逆或对外操作一律 high；只有 low / medium 的决策可能由参谋在用户的授权窗口内代答"`
		Hard              string   `json:"hard,omitempty" enum:"product,design,semantics,permission,gate,irreversible,acceptance" doc:"硬规则类别（R-210）：属于其中一类就标上，标了主助理永不代答、风险档按 high。product 产品方向；design 设计选型；semantics 语义 / 接口变更；permission 权限变更；gate 门禁 / 评测；irreversible 不可逆或对外操作（删除数据、发布、推送到远端、花钱、对外发消息）；acceptance 验收"`
	}
}

type linkOut struct {
	Body struct {
		store.Requirement
		Notices []string `json:"notices,omitempty" doc:"提示，例如草稿没放行、只关联未开始处理（R-166）"`
	}
}

type agentReqIn struct {
	Body struct {
		ReqFields
		SessionKey   string `json:"session_key,omitempty"`
		FromDecision string `json:"from_decision,omitempty" doc:"产生这条需求的决策（R-154），如 D-142；用户在某条决策里作答后由此新建的需求要填"`
	}
}

func (h *Hub) registerAgent(api huma.API) {
	tag := []string{"agent"}
	huma.Register(api, huma.Operation{OperationID: "agent-list-requirements", Method: http.MethodGet, Path: "/api/agent/requirements", Tags: tag},
		func(ctx context.Context, in *struct {
			SessionKey string   `query:"session_key"`
			All        bool     `query:"all" doc:"为真时列出所有项目，否则只列会话所在项目"`
			Status     []string `query:"status"`
		}) (*reqsOut, error) {
			project := ""
			if !in.All && in.SessionKey != "" {
				if x, err := h.store.GetSession(ctx, in.SessionKey); err == nil {
					project = x.ProjectID
				}
			}
			out := &reqsOut{}
			var err error
			out.Body.Requirements, err = h.store.ListRequirements(ctx, project, in.Status)
			out.Body.Statuses = store.ReqStatuses
			return out, err
		})
	huma.Register(api, huma.Operation{OperationID: "agent-get-requirement", Method: http.MethodGet, Path: "/api/agent/requirements/{ref}", Tags: tag},
		func(ctx context.Context, in *refPath) (*reqOut, error) {
			r, err := h.store.GetRequirement(ctx, in.Ref)
			return &reqOut{Body: r}, apiErr(err)
		})
	huma.Register(api, huma.Operation{OperationID: "agent-create-requirement", Method: http.MethodPost, Path: "/api/agent/requirements", Tags: tag},
		func(ctx context.Context, in *agentReqIn) (*reqCreatedOut, error) {
			r, err := h.toReq(ctx, in.Body.ReqFields)
			if err != nil {
				return nil, err
			}
			r.CreatedBy = "agent"
			if r.FromDecision, err = h.fromDecision(ctx, in.Body.FromDecision); err != nil {
				return nil, err
			}
			if r.Status == store.ReqReady {
				r.Status = store.ReqDraft // Agent 记下的需求先停在草稿，你确认后才排进会话的待办（R-66）
			}
			if r.ProjectID == "" && in.Body.SessionKey != "" {
				if x, err := h.store.GetSession(ctx, in.Body.SessionKey); err == nil {
					r.ProjectID = x.ProjectID
				}
			}
			var notices []string
			if ex, err := h.store.OpenRequirementByTitle(ctx, r.ProjectID, r.Title); err == nil {
				notices = append(notices, fmt.Sprintf("同项目已有同名未关闭需求 %s，这次仍新建了；如果是重复，请搁置新建的这条，改在 %s 上继续", ex.Code, ex.Code))
			}
			r, err = h.store.CreateRequirement(store.WithActor(ctx, store.Actor{Session: in.Body.SessionKey}), r)
			if err != nil {
				return nil, apiErr(err)
			}
			if in.Body.SessionKey != "" {
				_ = h.store.LinkSession(ctx, r.ID, in.Body.SessionKey)
			}
			h.broker.publish("requirements", "sessions")
			r, err = h.store.GetRequirement(ctx, r.ID)
			out := &reqCreatedOut{}
			out.Body.Requirement, out.Body.Notices = r, notices
			return out, apiErr(err)
		})
	huma.Register(api, huma.Operation{OperationID: "agent-update-status", Method: http.MethodPost, Path: "/api/agent/requirements/{ref}/status", Tags: tag},
		func(ctx context.Context, in *struct {
			Ref  string `path:"ref"`
			Body struct {
				Status     string `json:"status" enum:"draft,ready,in_progress,blocked,review,done,shelved"`
				SessionKey string `json:"session_key,omitempty"`
				Reason     string `json:"reason,omitempty" doc:"放行（设为 ready）时必填的理由；改回草稿时可写撤回原因（R-166）；搁置（shelved）时必填的搁置原因（R-157）"`
				ReasonKind string `json:"reason_kind,omitempty" enum:"duplicate,changed,dropped,other" doc:"搁置原因的类别：重复 / 方向变了 / 不做了 / 其他；不填按其他算"`
				WaitingOn  string `json:"waiting_on,omitempty" doc:"设为 blocked 时在等的需求或决策编号（R-x / D-x，R-243）：顺着等待关系落到用户身上的那一条会在「待处理」写明「R-x 在等这条」；不写时这条受阻需求本身进「待处理」"`
			}
		}) (*reqOut, error) {
			if in.Body.WaitingOn != "" && in.Body.Status != store.ReqBlocked {
				return nil, huma.Error400BadRequest("waiting_on 只在设为 blocked（受阻）时用：写在等的需求或决策编号（R-x / D-x）")
			}
			if in.Body.Status == store.ReqDone {
				return nil, huma.Error400BadRequest("“已完成”只能由用户在验收后设置；完成开发请设为 review（待验收）")
			}
			if in.Body.Status == store.ReqShelved && strings.TrimSpace(in.Body.Reason) == "" {
				return nil, huma.Error400BadRequest("Agent 搁置需求必须写 reason：一句话说明为什么搁置（重复 / 方向变了 / 不做了 / 其他，可用 reason_kind 标类别）")
			}
			before, err := h.store.GetRequirement(ctx, in.Ref)
			if err != nil {
				return nil, apiErr(err)
			}
			if h.isReviewer(ctx, in.Body.SessionKey) {
				return nil, huma.Error403Forbidden("评审只审查和验证，不改需求状态；验证结果用 review_submit（命令行 ks req review）交到需求上，状态由开发 / 主调度 / 用户改")
			}
			ctx = store.WithActor(ctx, store.Actor{Session: in.Body.SessionKey})
			if in.Body.Status == store.ReqShelved {
				ctx = store.WithActor(ctx, store.Actor{ReasonKind: in.Body.ReasonKind, Reason: in.Body.Reason})
			}
			if in.Body.Status == store.ReqReady && before.Status != store.ReqBlocked { // 受阻改回待开始是解除受阻，不是放行（R-269）
				// 放行（R-166）：只有 主调度 / 主助理、项目开关打开、从草稿、写了理由才行；不关联放行的会话，需求照常归原会话或 PM
				x, err := h.releaseCheck(ctx, before, in.Body.SessionKey, in.Body.Reason)
				if err != nil {
					return nil, err
				}
				r, err := h.release(ctx, before, x, in.Body.Reason)
				if err != nil {
					return nil, err
				}
				h.reqStatusEvent(ctx, r, before.Status, in.Body.SessionKey) // 放行也是改状态（R-249 P4）
				h.broker.publish("requirements", "sessions", "plans")
				h.kick(ctx, r.ID)
				return &reqOut{Body: r}, nil
			}
			waitOn := ""
			if in.Body.Status == store.ReqBlocked {
				if waitOn, err = h.checkWaitOn(ctx, before, in.Body.WaitingOn); err != nil {
					return nil, err
				}
				ctx = store.WithActor(ctx, store.Actor{Ref: waitOn, Reason: in.Body.Reason})
			}
			sctx := ctx
			if in.Body.Status == store.ReqDraft && before.Release != nil {
				sctx = store.WithActor(ctx, store.Actor{Via: "withdraw", Ref: before.Release.Code}) // 改回草稿 = 撤回放行
			}
			r, err := h.store.SetRequirementStatus(sctx, before.ID, in.Body.Status)
			if err != nil {
				return nil, apiErr(err)
			}
			if r.Status == store.ReqBlocked {
				if err := h.store.SetBlockedOn(ctx, r.ID, waitOn, in.Body.Reason); err != nil {
					return nil, apiErr(err)
				}
				r, _ = h.store.GetRequirement(ctx, r.ID)
			}
			h.reopenStage(ctx, before, r.Status)
			if r.Status == store.ReqDraft && before.Status != store.ReqDraft {
				h.withdrawRelease(ctx, before, in.Body.SessionKey, in.Body.Reason)
				r, _ = h.store.GetRequirement(ctx, r.ID)
			}
			if in.Body.SessionKey != "" {
				_ = h.store.LinkSession(ctx, r.ID, in.Body.SessionKey)
			}
			h.refreshDelivered(ctx, r)
			h.broker.publish("requirements", "sessions", "plans")
			if r.Status != before.Status && (r.Status == store.ReqReview || r.Status == store.ReqBlocked) {
				told := ""
				if r.Status == store.ReqReview {
					h.autoReview(ctx, r, in.Body.SessionKey)         // 本项目有在线评审就自动派验证任务（R-232）
					told = h.onReview(ctx, r.ID, in.Body.SessionKey) // 告诉指派的 PM（R-135）
				}
				if full, err := h.store.GetRequirement(ctx, r.ID); err == nil {
					h.onStatusEvent(ctx, full, in.Body.SessionKey, told) // 订阅了交验收 / 受阻的会话（R-164）
				}
			}
			if full, err := h.store.GetRequirement(ctx, r.ID); err == nil {
				h.reqStatusEvent(ctx, full, before.Status, in.Body.SessionKey) // 订阅了这条需求的会话（R-249 P4），上面通知过的不重复送
			}
			if r.Status == store.ReqReview {
				if full, err := h.store.GetRequirement(ctx, r.ID); err == nil {
					r = full
				}
				// 交给了评审的，等评审交意见（或 30 分钟到点）再推给用户（R-237）
				if !r.Review.Holding() {
					h.sendNotify(r.ProjectID, notify.Message{Event: notify.EventRequirementReview, Title: pushReviewTitle(r.Code), Body: r.Title, Group: "kitestring"}, "/requirements?r="+r.Code)
				}
			}
			return &reqOut{Body: r}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "agent-attach-evidence", Method: http.MethodPost, Path: "/api/agent/requirements/{ref}/evidence", Tags: tag},
		func(ctx context.Context, in *struct {
			Ref  string `path:"ref"`
			Body struct {
				Kind       string   `json:"kind" doc:"例：test、screenshot、commit、doc、manual"`
				Summary    string   `json:"summary,omitempty" doc:"证据正文；给了 verdict / blocks 时可省略，由 hub 拼出纯文本"`
				Link       string   `json:"link,omitempty"`
				Files      []string `json:"files,omitempty" doc:"截图等附图 ID（先上传到 /api/agent/files）"`
				Images     []any    `json:"images,omitempty" doc:"带标题与改动框的附图（R-183），排在 files 后面，按给的顺序显示：[{id: 附件 ID, title: 一句话标题, boxes: [{x, y, w, h, label}]}]，框用图片原始尺寸的比例坐标（0～1）。写错的框丢掉并在 image_hints 里说明，证据照常保存"`
				SessionKey string   `json:"session_key,omitempty"`
				Verdict    string   `json:"verdict,omitempty" doc:"一句结论（40 字以内）"`
				Todo       []string `json:"todo,omitempty" doc:"要你做的：只写机器验证不了的事"`
				Details    string   `json:"details,omitempty" doc:"实现细节（默认折叠）"`
				Aside      []string `json:"aside,omitempty" doc:"顺带改动（默认折叠）"`
				Blocks     []any    `json:"blocks,omitempty" doc:"表达块（R-97）：checklist / compare / figure / flow / metrics / scope / options / table；图片字段填附件 ID。不合法的块丢掉并在 block_warnings 里说明，证据照常保存"`
				Prototypes []string `json:"prototypes,omitempty" doc:"可交互原型 ID（R-197），先传到 /api/agent/prototypes"`
			}
		}) (*evidenceOut, error) {
			r, err := h.store.GetRequirement(ctx, in.Ref)
			if err != nil {
				return nil, apiErr(err)
			}
			b := in.Body
			files, err := h.store.ResolveFiles(ctx, b.Files)
			if err != nil {
				return nil, err
			}
			shots, hints := blocks.ParseImages(b.Images, h.blockFile(ctx))
			files = mergeFiles(files, shots)
			payload, warns := blocks.ParsePayload(b.Verdict, b.Todo, b.Details, b.Aside, b.Blocks, h.blockFile(ctx))
			if payload != nil {
				files = mergeFiles(files, blocks.Files(payload.Blocks))
			}
			all := make([]blocks.File, len(files))
			for i, f := range files {
				all[i] = blocks.File(f)
			}
			if hint := blocks.TitleHints(all, payload.BlockList()); hint != "" {
				hints = append(hints, hint)
			}
			summary := strings.TrimSpace(b.Summary)
			if summary == "" {
				summary = payload.Text()
			}
			if summary == "" {
				return nil, huma.Error400BadRequest("证据需要正文：summary，或 verdict（一句结论）加 blocks")
			}
			// 标成截图 / 录屏却没附文件，用户验收时会看不到任何东西：直接拒绝，让 agent 补上附件。
			if h.isReviewer(ctx, b.SessionKey) {
				return nil, huma.Error400BadRequest("评审的审查意见和验证结果用 review_submit（命令行 ks req review）交，不算验收证据；它同样支持 verdict、checklist 和截图")
			}
			switch strings.ToLower(b.Kind) {
			case store.EvidenceReview:
				// 审查意见走 review_submit（R-88）：只有审查员能交，且不算验收证据
				return nil, huma.Error400BadRequest("审查意见请用 review_submit（命令行 ks req review）提交，只有 Rev 会话能交；验收证据换一个 kind，例如 test、manual")
			case "screenshot", "video", "recording":
				if len(files) == 0 {
					return nil, huma.Error400BadRequest("证据类型为 " + b.Kind + " 时必须附上图片或录屏（MCP 用 images / videos，命令行用 --image / --video）")
				}
			}
			protos, err := h.store.ResolvePrototypes(ctx, b.Prototypes)
			if err != nil {
				return nil, err
			}
			if len(protos) < len(b.Prototypes) {
				warns = append(warns, "有原型 ID 找不到，没有挂上")
			}
			if err := h.store.AdoptPrototypes(ctx, b.Prototypes, r.ID); err != nil {
				return nil, err
			}
			added, err := h.store.AddEvidence(ctx, store.Evidence{RequirementID: r.ID, SessionKey: b.SessionKey, Kind: b.Kind, Summary: summary, Link: b.Link, Files: files, Payload: payload, Prototypes: protos})
			if err != nil {
				return nil, apiErr(err)
			}
			ev := "附了证据"
			if payload != nil && payload.Verdict != "" {
				ev += "：" + brief(payload.Verdict, 30)
			}
			h.reqEvent(ctx, r, evEvidence, b.SessionKey, ev) // 订阅了这条需求的会话（R-249 P4）
			h.broker.publish("requirements")
			r, err = h.store.GetRequirement(ctx, r.ID)
			out := &evidenceOut{}
			out.Body.Requirement = r
			out.Body.EvidenceID = added.ID
			out.Body.BlockWarnings = warns
			out.Body.ImageHints = hints
			return out, apiErr(err)
		})
	huma.Register(api, huma.Operation{OperationID: "agent-raise-decision", Method: http.MethodPost, Path: "/api/agent/decisions", Tags: tag},
		func(ctx context.Context, in *agentDecisionIn) (*decisionRaisedOut, error) {
			b := in.Body
			d := store.Decision{SessionKey: b.SessionKey, Question: b.Question, Context: b.Context, Options: b.Options, Recommendation: b.Recommendation, Risk: b.Risk, Overturns: b.Overturns, Hard: b.Hard}
			if d.Hard != "" {
				d.Risk = store.RiskHigh // 标了硬规则类别：风险档按 high（R-210）
			}
			from, err := h.fromDecision(ctx, b.FromDecision)
			if err != nil {
				return nil, err
			}
			if b.Overturns && from == "" {
				return nil, huma.Error400BadRequest("overturns = true 时要用 from_decision 写明推翻的是哪条决策（D-x）")
			}
			d.FromDecision = from
			var warns, pw []string
			d.Blocks, warns = blocks.Parse(b.Blocks, h.blockFile(ctx))
			d.Points, pw = blocks.ParsePointsProto(b.Points, h.blockFile(ctx), h.protoResolver(ctx))
			warns = append(warns, pw...)
			for _, x := range b.Summary {
				if x = strings.TrimSpace(x); x != "" && len(d.Summary) < 8 {
					d.Summary = append(d.Summary, x)
				}
			}
			if d.Options == nil {
				d.Options = []string{}
			}
			if b.Approval {
				d.Approval = true
				d.Options = []string{"批准", "拒绝"}
				b.OptionImages = nil
				b.OptionPrototypes = nil
			}
			if d.Prototypes, err = h.store.ResolvePrototypes(ctx, b.Prototypes); err != nil {
				return nil, err
			}
			if len(b.OptionPrototypes) > 0 {
				// 每个选项最多一个原型，固定在提出时那一版
				d.OptionPrototypes = make([][]store.Prototype, len(d.Options))
				for i := range d.Options {
					d.OptionPrototypes[i] = []store.Prototype{}
					if i < len(b.OptionPrototypes) && b.OptionPrototypes[i] != "" {
						ps, err := h.store.ResolvePrototypes(ctx, []string{b.OptionPrototypes[i]})
						if err != nil {
							return nil, err
						}
						d.OptionPrototypes[i] = ps
					}
				}
			}
			d.RecPick = b.RecommendedOption // 推荐选项存成字段（R-239），CreateDecision 定 rec_option
			if len(b.Images) > 0 {
				fs, err := h.store.ResolveFiles(ctx, b.Images)
				if err != nil {
					return nil, err
				}
				d.ContextFiles = fs
			}
			if len(b.OptionImages) > 0 {
				// 方案对比：每个选项最多一张效果图
				d.OptionFiles = make([][]store.FileRef, len(d.Options))
				for i := range d.Options {
					d.OptionFiles[i] = []store.FileRef{}
					if i < len(b.OptionImages) && b.OptionImages[i] != "" {
						fs, err := h.store.ResolveFiles(ctx, []string{b.OptionImages[i]})
						if err != nil {
							return nil, err
						}
						d.OptionFiles[i] = fs
					}
				}
			}
			if b.SessionKey != "" {
				if x, err := h.store.GetSession(ctx, b.SessionKey); err == nil {
					d.ProjectID, d.FromRole = x.ProjectID, x.Role // 提问时的身份：接任概览据此找上一任主助理提的决策（R-245）
				}
			}
			auto := false
			if strings.EqualFold(strings.TrimSpace(b.Requirement), "none") {
				b.Requirement = "" // 明说和在做的需求无关（如「做完这些要不要上线」），不自动挂
			} else if b.Requirement == "" && b.SessionKey != "" {
				// 没带需求、会话只在做一条需求（没完成也没搁置的关联恰好一条）时自动挂上（R-241）
				if ids, err := h.store.OpenReqsOfSession(ctx, b.SessionKey); err == nil && len(ids) == 1 {
					b.Requirement, auto = ids[0], true
				}
			}
			if b.Requirement != "" {
				r, err := h.store.GetRequirement(ctx, b.Requirement)
				if err != nil {
					return nil, huma.Error400BadRequest("关联需求不存在：" + b.Requirement)
				}
				d.RequirementID = r.ID
				if d.ProjectID == "" {
					d.ProjectID = r.ProjectID
				}
				if b.SessionKey != "" {
					_ = h.store.LinkSession(ctx, r.ID, b.SessionKey)
				}
			}
			if d.RequirementID != "" {
				// 原型交上来时可能还不知道挂哪条需求（自动挂上的），补记，访客页据此判断能不能看（R-197）
				ids := append([]string{}, b.Prototypes...)
				ids = append(ids, b.OptionPrototypes...)
				blocks.WalkPrototypes(d.Points, func(p *blocks.Prototype) { ids = append(ids, p.ID) })
				if err := h.store.AdoptPrototypes(ctx, ids, d.RequirementID); err != nil {
					return nil, err
				}
			}
			d, err = h.store.CreateDecision(ctx, d)
			if err != nil {
				return nil, apiErr(err)
			}
			h.broker.publish("decisions", "sessions", "requirements")
			go h.aiOnDecision(d)
			h.onDecisionRaised(ctx, d) // 订阅了 decision 的会话（R-164）
			// 授权窗口开着、参谋可以代答的决策先不推送（不吵醒你，R-163）；仍留在收件箱里。离开期间按硬规则 / 拿不准分级（R-210）
			h.sendNotify(d.ProjectID, notify.Message{Event: notify.EventDecisionRaised, Title: pushDecisionTitle(d.Code), Body: d.Question, Group: "kitestring"}, decisionPath(d), h.decisionPush(ctx, d))
			out := &decisionRaisedOut{}
			out.Body.Decision = d
			out.Body.BlockWarnings = warns
			out.Body.AutoLinked = auto
			return out, nil
		})
	huma.Register(api, huma.Operation{OperationID: "agent-get-decision", Method: http.MethodGet, Path: "/api/agent/decisions/{ref}", Tags: tag},
		func(ctx context.Context, in *refPath) (*decisionOut, error) {
			d, err := h.store.GetDecision(ctx, in.Ref)
			return &decisionOut{Body: d}, apiErr(err)
		})
	huma.Register(api, huma.Operation{OperationID: "agent-withdraw-decision", Method: http.MethodPost, Path: "/api/agent/decisions/{ref}/withdraw", Tags: tag, Summary: "Agent 撤回自己提的、还没答复的决策（R-136）"},
		func(ctx context.Context, in *struct {
			Ref  string `path:"ref"`
			Body struct {
				SessionKey string `json:"session_key,omitempty"`
				Reason     string `json:"reason,omitempty" doc:"撤回原因"`
				ReplacedBy string `json:"replaced_by,omitempty" doc:"取代它的决策编号，如 D-142；必须仍待用户答复，已关闭 / 已撤回 / 已答复或会形成回环时拒绝"`
			}
		}) (*decisionOut, error) {
			d, err := h.store.WithdrawDecision(ctx, in.Ref, in.Body.SessionKey, in.Body.Reason, in.Body.ReplacedBy)
			if err != nil {
				return nil, apiErr(err)
			}
			h.decisionsChanged(ctx, d)
			return &decisionOut{Body: d}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "agent-link-session", Method: http.MethodPost, Path: "/api/agent/sessions/{key}/link", Tags: tag},
		func(ctx context.Context, in *struct {
			Key  string `path:"key"`
			Body struct {
				Requirement string `json:"requirement"`
			}
		}) (*linkOut, error) {
			r, err := h.store.GetRequirement(ctx, in.Body.Requirement)
			if err != nil {
				return nil, apiErr(err)
			}
			x, err := h.store.GetSession(ctx, in.Key)
			if err != nil {
				return nil, huma.Error404NotFound("会话尚未上报，稍后重试")
			}
			if err := h.store.LinkSession(ctx, r.ID, in.Key); err != nil {
				return nil, apiErr(err)
			}
			out := &linkOut{}
			ctx = store.WithActor(ctx, store.Actor{Session: x.Key, Via: "link"})
			// Agent 关联会话就是开始做了：待开始的需求自动进入进行中（R-35）。
			// 草稿要先放行（R-166）：项目开了放行时记一次放行（理由「开始做 R-x」）再进入进行中；没开时只关联、不改状态
			switch r.Status {
			case store.ReqReady:
				if _, err := h.store.SetRequirementStatus(ctx, r.ID, store.ReqInProgress); err != nil {
					return nil, apiErr(err)
				}
			case store.ReqBlocked:
				// 受阻的需求有会话关联开工：转为进行中，时间线写明原来在等什么（R-269）
				lctx := store.WithActor(ctx, store.Actor{Ref: r.BlockedOn, Reason: waitedText(r)})
				if _, err := h.store.SetRequirementStatus(lctx, r.ID, store.ReqInProgress); err != nil {
					return nil, apiErr(err)
				}
				out.Body.Notices = append(out.Body.Notices, "这条原来是受阻（"+waitedText(r)+"），已转为进行中；等的事其实没完成的话，再设一次受阻即可")
			case store.ReqDraft:
				if !canRelease(x.Role) {
					out.Body.Notices = append(out.Body.Notices, "已关联，但这条还是草稿，请主调度放行，或用 req_request_status 申请用户批准")
					break
				}
				p, perr := h.store.GetProject(ctx, r.ProjectID)
				if r.ProjectID == "" || perr != nil || !p.AgentRelease {
					out.Body.Notices = append(out.Body.Notices, "已关联，但"+releaseOff+"；放行前它仍是草稿")
					break
				}
				if _, err := h.release(ctx, r, x, "开始做 "+r.Code); err != nil {
					return nil, err
				}
				if _, err := h.store.SetRequirementStatus(ctx, r.ID, store.ReqInProgress); err != nil {
					return nil, apiErr(err)
				}
			}
			h.broker.publish("requirements", "sessions")
			out.Body.Requirement, err = h.store.GetRequirement(ctx, r.ID)
			if err == nil {
				h.reqStatusEvent(ctx, out.Body.Requirement, r.Status, x.Key) // 开始做（待开始 → 进行中）也告诉订阅者（R-249 P4）
			}
			return out, apiErr(err)
		})
}
