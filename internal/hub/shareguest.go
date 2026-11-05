package hub

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"golang.org/x/crypto/bcrypt"

	"github.com/NekroAI/kitestring/internal/store"
)

// 分享的访客侧（R-281）：免登录、只读。
//
// 鉴权：路径里的 token 找到访客链接；链接撤回、分享撤回或到期一律拒绝（每次请求都查库，撤回后下一次访问即拒绝）。
// 设了访问密码时，输对后发一个只在 /api/share/<token> 下有效的 HttpOnly cookie，值是对「链接 ID + 当前密码哈希」的签名：
// 改密码后旧 cookie 自然失效。访客 token 只在 /api/share/ 下认，网页登录（ks_session）和设备 token（kst_）都不认它。
//
// 范围：只返回分享范围里的内容（store.ResolveShare）。不在范围里的需求、决策、会话、附件一律 404「不在这个分享里」，
// 不存在的也是同一句，猜编号试不出哪些存在。内容里提到的范围外编号只留编号（R-x / D-x 是文字，不展开）。
// 只有 GET 和输密码的 POST；其它方法 405。

const guestCookie = "ks_share"

const shareKeySetting = "share_cookie_key"

// guestCtx 是一次访客请求：链接、分享，以及（输过密码后）分享范围。
type guestCtx struct {
	share store.Share
	link  store.ShareLink
	scope store.ShareScope
}

func (g *guestCtx) inScopeSession(key string) bool { return key != "" && g.scope.Sessions[key] }

// keep 只保留分享范围里的会话键。
func (g *guestCtx) keep(key string) string {
	if g.inScopeSession(key) {
		return key
	}
	return ""
}

// guestKey 返回签 cookie 的密钥，首次用到时生成并存进设置。
func (h *Hub) guestKey(ctx context.Context) ([]byte, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.shareKey != nil {
		return h.shareKey, nil
	}
	v, err := h.store.GetSetting(ctx, shareKeySetting)
	if err != nil {
		return nil, err
	}
	if v == "" {
		b := make([]byte, 32)
		_, _ = rand.Read(b)
		v = hex.EncodeToString(b)
		if err := h.store.SetSetting(ctx, shareKeySetting, v); err != nil {
			return nil, err
		}
	}
	h.shareKey, err = hex.DecodeString(v)
	return h.shareKey, err
}

func (h *Hub) guestSig(ctx context.Context, x store.Share, l store.ShareLink) (string, error) {
	key, err := h.guestKey(ctx)
	if err != nil {
		return "", err
	}
	m := hmac.New(sha256.New, key)
	m.Write([]byte(l.ID + "|" + x.ID + "|" + x.PasswordHash))
	return hex.EncodeToString(m.Sum(nil)), nil
}

// guestLink 按 token 找到可用的链接和分享；不可用时返回给访客看的错误。
func (h *Hub) guestLink(ctx context.Context, token string) (store.Share, store.ShareLink, error) {
	if !strings.HasPrefix(token, sharePrefix) {
		return store.Share{}, store.ShareLink{}, huma.Error404NotFound(shareLinkInvalid)
	}
	l, err := h.store.ShareLinkByToken(ctx, token)
	if errors.Is(err, store.ErrNotFound) {
		return store.Share{}, l, huma.Error404NotFound(shareLinkInvalid)
	}
	if err != nil {
		return store.Share{}, l, err
	}
	x, err := h.store.GetShare(ctx, l.ShareID)
	if err != nil {
		return x, l, huma.Error404NotFound(shareLinkInvalid)
	}
	switch {
	case l.RevokedAt != nil:
		return x, l, huma.Error410Gone(shareLinkRevoked)
	case x.Status == store.ShareRevoked:
		return x, l, huma.Error410Gone(shareRevokedText)
	case x.Status == store.ShareExpired:
		return x, l, huma.Error410Gone(shareExpiredText)
	}
	return x, l, nil
}

// unlocked 判断访客是否已输过密码（没设密码时总是真）。
func (h *Hub) unlocked(ctx context.Context, x store.Share, l store.ShareLink, cookie string) bool {
	if x.PasswordHash == "" {
		return true
	}
	if cookie == "" {
		return false
	}
	want, err := h.guestSig(ctx, x, l)
	return err == nil && hmac.Equal([]byte(cookie), []byte(want))
}

// guestAuth 校验访客请求并算出分享范围：链接可用、输过密码（如果设了）。
func (h *Hub) guestAuth(ctx context.Context, token, cookie string) (*guestCtx, error) {
	x, l, err := h.guestLink(ctx, token)
	if err != nil {
		return nil, err
	}
	if !h.unlocked(ctx, x, l, cookie) {
		return nil, huma.Error401Unauthorized(sharePasswordNeeded)
	}
	sc, err := h.store.ResolveShare(ctx, x.Items)
	if err != nil {
		return nil, err
	}
	return &guestCtx{share: x, link: l, scope: sc}, nil
}

func notIncluded() error { return huma.Error404NotFound(shareNotIncluded) }

// ---------- 返回给访客的数据 ----------

// GuestSession 是访客看到的会话：状态、在推进的需求、当前这一轮的一句话；不含设备、目录、分支、命令这类本机信息。
type GuestSession struct {
	Key          string     `json:"key"`
	Tool         string     `json:"tool"`
	Role         string     `json:"role" enum:",aide,advisor,reviewer,pm,dev"`
	Title        string     `json:"title"`
	Model        string     `json:"model,omitempty"`
	State        string     `json:"state" doc:"原始状态，网页按 displayState 换成显示状态"`
	WaitKind     string     `json:"wait_kind" enum:",approval,question"`
	DoneAt       *time.Time `json:"done_at,omitempty"`
	StateSince   time.Time  `json:"state_since" doc:"当前状态开始的时间：「运行中 12 分钟」"`
	StartedAt    time.Time  `json:"started_at"`
	LastActiveAt time.Time  `json:"last_active_at" doc:"最近一次有动静的时间：「3 分钟前」"`
	EndedAt      *time.Time `json:"ended_at,omitempty"`
	Turns        int        `json:"turns"`
	Background   int        `json:"background" doc:"在跑的后台命令和子任务个数"`
	Line         string     `json:"line" doc:"当前这一轮的一句话（这一轮的提问第一行，没有时用会话摘要）"`
	LastTool     string     `json:"last_tool,omitempty" doc:"最近一次工具动作（主调度不给）"`
	LastToolAt   *time.Time `json:"last_tool_at,omitempty"`
	// Requirements：它关联的、在分享里的需求（在推进哪条需求）
	Requirements []store.RequirementRef `json:"requirements"`
	Conversation bool                   `json:"conversation" doc:"访客能不能点进对话：主调度只给状态和当前一句话，为 false"`
}

func (g *guestCtx) session(x store.Session) GuestSession {
	v := GuestSession{
		Key: x.Key, Tool: x.Tool, Role: x.Role, Title: x.Title, Model: x.Model, State: x.State, WaitKind: x.WaitKind, DoneAt: x.DoneAt,
		StateSince: x.StateSince, StartedAt: x.StartedAt, LastActiveAt: x.LastActiveAt, EndedAt: x.EndedAt, Turns: x.Turns, Background: len(x.Tasks),
		LastTool: x.LastTool, LastToolAt: x.LastToolAt, Requirements: []store.RequirementRef{}, Conversation: x.Role != "pm",
	}
	v.Line = guestLine(x.LastPrompt)
	if v.Line == "" {
		v.Line = guestLine(x.Summary)
	}
	if !v.Conversation {
		v.LastTool, v.LastToolAt = "", nil
	}
	for _, r := range x.Requirements {
		if g.scope.Reqs[r.ID] {
			v.Requirements = append(v.Requirements, r)
		}
	}
	return v
}

func guestLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	if r := []rune(s); len(r) > 100 {
		s = string(r[:100]) + "…"
	}
	return s
}

// sessionRank：运行中的排前面，其次在等的、待命的，已结束的最后。
func sessionRank(state string) int {
	switch state {
	case store.StateRunning, store.StateBackground:
		return 0
	case store.StateWaitApproval, store.StateWaitReply:
		return 1
	case store.StateParked, store.StateIdle:
		return 2
	}
	return 3
}

func (h *Hub) guestSessions(ctx context.Context, g *guestCtx, keys []string) []GuestSession {
	out := []GuestSession{}
	for _, k := range keys {
		x, err := h.store.GetSession(ctx, k)
		if err != nil {
			continue
		}
		out = append(out, g.session(x))
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := sessionRank(out[i].State), sessionRank(out[j].State)
		if a != b {
			return a < b
		}
		return out[i].LastActiveAt.After(out[j].LastActiveAt)
	})
	return out
}

// guestDecision 去掉访客不该看到的会话键、设备名等，范围外的会话只留身份和标题快照。
func (g *guestCtx) decision(d store.Decision) store.Decision {
	if d.Guest != nil {
		// 放入的访客留言：访客页只给放进去的正文和附言，改写前的原话只有所有者看得到
		x := *d.Guest
		x.Original, x.Comment = "", ""
		d.Guest = &x
	}
	d.SessionKey, d.OriginSession, d.AssignedBy = g.keep(d.SessionKey), g.keep(d.OriginSession), g.keep(d.AssignedBy)
	d.ProxyBy = g.keep(d.ProxyBy)
	d.SessionState, d.DeviceName, d.DeliveredTurn = "", "", ""
	d.ProjectID = ""
	return d
}

// requirement 去掉访客不该看到的：范围外的会话键、指派对象、计划和阶段的内容（只留编号）。
func (g *guestCtx) requirement(r store.Requirement) store.Requirement {
	r.ProjectID = ""
	r.OfferedTo, r.OfferedAt, r.OfferDeliveredAt, r.OfferNudgedAt, r.OfferOverdueAt = "", nil, nil, nil, nil
	var ss []string
	for _, k := range r.Sessions {
		if g.inScopeSession(k) {
			ss = append(ss, k)
		}
	}
	r.Sessions = ss
	if r.PlanID != "" && !g.scope.Plans[r.PlanID] {
		r.PlanID = ""
	}
	if !g.stageInScope(r) {
		r.StageTitle = ""
	}
	for i := range r.Evidence {
		r.Evidence[i].SessionKey = g.keep(r.Evidence[i].SessionKey)
	}
	for i := range r.Reviews {
		r.Reviews[i].SessionKey = g.keep(r.Reviews[i].SessionKey)
	}
	for i := range r.Feedback {
		r.Feedback[i] = g.decision(r.Feedback[i])
	}
	for i := range r.StatusChanges {
		r.StatusChanges[i].Session = g.keep(r.StatusChanges[i].Session)
	}
	if r.Release != nil {
		rel := *r.Release
		rel.By = g.keep(rel.By)
		r.Release = &rel
	}
	if r.Advice != nil {
		a := *r.Advice
		a.By, a.ProjectID = g.keep(a.By), ""
		r.Advice = &a
	}
	if r.Review != nil {
		rv := *r.Review
		rv.SessionKey = g.keep(rv.SessionKey)
		r.Review = &rv
	}
	return r
}

// stageInScope：需求所在的阶段在不在分享里（需求本身只记计划 ID 和阶段序号，按计划查）。
func (g *guestCtx) stageInScope(r store.Requirement) bool {
	return r.StageSeq > 0 && g.scope.Plans[r.PlanID] // 计划在范围里时阶段标题照常显示
}

// GuestActivityView 把最近动态里范围外的会话键去掉。
func (g *guestCtx) activity(as []store.ShareActivity) []store.ShareActivity {
	for i := range as {
		as[i].Session = g.keep(as[i].Session)
	}
	return as
}

// GuestTurn 是访客看到的一轮：提问和回复全文，工具调用只给名字和一句说明，改了哪些文件；不给终端输出和文件内容。
type GuestTurn struct {
	ID        string      `json:"id"`
	Start     time.Time   `json:"start"`
	End       *time.Time  `json:"end,omitempty" doc:"这一轮结束的时间；正在运行的一轮省略"`
	Running   bool        `json:"running" doc:"正在运行的一轮（最新一轮且还没结束）"`
	Source    string      `json:"source" enum:",terminal,web,wake" doc:"terminal 在终端提的问 / web 网页上发的话 / wake 纸鸢送达的消息"`
	Prompt    string      `json:"prompt,omitempty" doc:"提问全文"`
	Inbox     []GuestItem `json:"inbox" doc:"这一轮收到的消息（网页上发的话、答复、指派等）"`
	Items     []GuestItem `json:"items" doc:"这一轮里的明确记录：改状态、证据、决策、留言……"`
	Tools     []GuestTool `json:"tools" doc:"工具调用，时间正序（最多 200 条）"`
	ToolCount int         `json:"tool_count"`
	Files     []string    `json:"files" doc:"这一轮改过的文件（文件名）"`
	Reply     string      `json:"reply,omitempty" doc:"这一轮最后的回复全文（Markdown）"`
	ReplyKind string      `json:"reply_kind" enum:"full,brief,none"`
}

// GuestTool 是一次工具调用的摘要。
type GuestTool struct {
	Name  string    `json:"name"`
	Brief string    `json:"brief,omitempty" doc:"一句说明：命令（截断）、文件名、搜索词等"`
	At    time.Time `json:"at"`
	Sub   bool      `json:"sub,omitempty" doc:"子任务调用的"`
}

// GuestItem 是轮里的一条记录。
type GuestItem struct {
	ID       string    `json:"id"`
	Kind     string    `json:"kind"`
	At       time.Time `json:"at"`
	Label    string    `json:"label,omitempty"`
	Text     string    `json:"text,omitempty"`
	Req      string    `json:"req,omitempty"`
	Dec      string    `json:"dec,omitempty"`
	From     string    `json:"from,omitempty"`
	To       string    `json:"to,omitempty"`
	PeerRole string    `json:"peer_role,omitempty"`
	Via      string    `json:"via,omitempty" doc:"改状态：link 关联时开始处理；收到的消息：tell / decision / assign 等类别"`
	Note     string    `json:"note,omitempty" doc:"证据：voided 已作废；决策：已撤回"`
	Web      bool      `json:"web,omitempty" doc:"网页上发给会话的话（这一轮的提问）"`
	Refs     []string  `json:"refs"`
}

var editTools = []string{"Edit", "Write", "MultiEdit", "NotebookEdit", "apply_patch"}

func guestItems(in []TurnItem) []GuestItem {
	out := []GuestItem{}
	for _, it := range in {
		refs := it.Refs
		if refs == nil {
			refs = []string{}
		}
		out = append(out, GuestItem{ID: it.ID, Via: it.Via, Note: it.Note, Web: it.Web, Kind: it.Kind, At: it.At, Label: it.Label, Text: it.Text, Req: it.Req, Dec: it.Dec, From: it.From, To: it.To, PeerRole: it.PeerRole, Refs: refs})
	}
	return out
}

func guestTurn(t Turn, last bool) GuestTurn {
	g := GuestTurn{ID: t.ID, Start: t.Start, End: t.End, Running: last && t.End == nil, Source: t.Source, Prompt: t.Prompt,
		Inbox: guestItems(t.Inbox), Items: guestItems(append(append([]TurnItem{}, t.Items...), t.After...)),
		Tools: []GuestTool{}, ToolCount: t.Tools.N + t.AfterTool.N, Files: []string{}, Reply: t.Reply, ReplyKind: t.ReplyKind}
	for _, c := range append(append([]ToolCall{}, t.Tools.List...), t.AfterTool.List...) {
		g.Tools = append(g.Tools, GuestTool{Name: c.Name, Brief: c.Brief, At: c.At, Sub: c.Sub})
		if slices.Contains(editTools, c.Name) && c.Brief != "" && !slices.Contains(g.Files, c.Brief) {
			g.Files = append(g.Files, c.Brief)
		}
	}
	return g
}

// ---------- 接口 ----------

type guestIn struct {
	Token  string `path:"token"`
	Cookie string `cookie:"ks_share"`
}

type guestMetaOut struct {
	Body struct {
		Name          string     `json:"name" doc:"分享名称"`
		Guest         string     `json:"guest" doc:"这条链接的访客名字"`
		NeedsPassword bool       `json:"needs_password"`
		Unlocked      bool       `json:"unlocked" doc:"已输过密码（或没设密码），可以看内容"`
		ExpiresAt     *time.Time `json:"expires_at,omitempty"`
	}
}

type guestUnlockIn struct {
	Token string `path:"token"`
	Body  struct {
		Password string `json:"password"`
	}
}

type guestUnlockOut struct {
	SetCookie http.Cookie `header:"Set-Cookie"`
	Body      struct {
		OK bool `json:"ok"`
	}
}

type guestOverviewOut struct {
	Body struct {
		Name     string                `json:"name"`
		Guest    string                `json:"guest"`
		Nodes    []ShareNode           `json:"nodes" doc:"分享里的内容，计划 → 阶段 → 需求 → 会话"`
		Codes    []string              `json:"codes" doc:"分享里的计划、需求编号：文字里提到的其它编号只显示编号、不能点开"`
		Counts   map[string]int        `json:"counts" doc:"需求状态分布：状态 → 条数"`
		Sessions []GuestSession        `json:"sessions" doc:"分享里的会话，运行中的在前"`
		Activity []store.ShareActivity `json:"activity" doc:"最近动态（只收有明确记录的事件），新的在前，最多 50 条"`
		Decs     []GuestDecRef         `json:"decs" doc:"分享里需求上访客看得到的决策和留言等记录：文字里提到这些 D-x 时能点开（落到所属需求）"`
		Now      time.Time             `json:"now" doc:"服务端时间，网页据此算「多久前」"`
	}
}

// GuestDecRef 是分享里需求上的一个决策（或留言、修改意见等记录）：编号、所属需求、标题和状态，网页据此把正文里的 D-x 画成能点的印记。
type GuestDecRef struct {
	Code   string `json:"code"`
	Req    string `json:"req" doc:"所属需求编号"`
	Title  string `json:"title"`
	Status string `json:"status"`
}

type guestRefIn struct {
	Token  string `path:"token"`
	Ref    string `path:"ref"`
	Cookie string `cookie:"ks_share"`
}

type guestReqOut struct {
	Body struct {
		Requirement store.Requirement     `json:"requirement" doc:"需求，含证据、评审意见、修改意见与留言（feedback）、状态变化；范围外的会话键已去掉"`
		Decisions   []store.Decision      `json:"decisions" doc:"需求上的决策，新的在前"`
		Sessions    []GuestSession        `json:"sessions" doc:"关联的、在分享里的会话"`
		Activity    []store.ShareActivity `json:"activity" doc:"这条需求的进展，新的在前"`
	}
}

type guestPlanOut struct {
	Body store.Plan
}

type guestDecOut struct {
	Body store.Decision
}

type guestSessionIn struct {
	Token  string `path:"token"`
	Key    string `path:"key"`
	Cookie string `cookie:"ks_share"`
	Before string `query:"before" doc:"往上加载更早一页：上一页返回的 before"`
}

type guestSessionOut struct {
	Body struct {
		Session GuestSession `json:"session"`
		Turns   []GuestTurn  `json:"turns" doc:"时间正序；主调度不给对话，为空"`
		More    bool         `json:"more"`
		Before  string       `json:"before,omitempty"`
	}
}

func (h *Hub) registerShareGuest(api huma.API) {
	tag := []string{"share-guest"}
	huma.Register(api, huma.Operation{OperationID: "guest-share", Method: http.MethodGet, Path: "/api/share/{token}", Tags: tag, Summary: "访客：分享名称、要不要输密码"},
		func(ctx context.Context, in *guestIn) (*guestMetaOut, error) {
			x, l, err := h.guestLink(ctx, in.Token)
			if err != nil {
				return nil, err
			}
			_ = h.store.TouchShareLink(ctx, l.ID)
			out := &guestMetaOut{}
			out.Body.Name, out.Body.Guest, out.Body.NeedsPassword, out.Body.ExpiresAt = x.Name, l.Name, x.HasPassword, x.ExpiresAt
			out.Body.Unlocked = h.unlocked(ctx, x, l, in.Cookie)
			return out, nil
		})
	huma.Register(api, huma.Operation{OperationID: "guest-unlock", Method: http.MethodPost, Path: "/api/share/{token}/unlock", Tags: tag, Summary: "访客：输访问密码"},
		func(ctx context.Context, in *guestUnlockIn) (*guestUnlockOut, error) {
			x, l, err := h.guestLink(ctx, in.Token)
			if err != nil {
				return nil, err
			}
			out := &guestUnlockOut{}
			out.Body.OK = true
			if x.PasswordHash != "" {
				ip := "share|" + clientFrom(ctx).IP
				if d := h.guests.attempt(ip); d > 0 {
					return nil, huma.Error429TooManyRequests(sharePasswordLocked(int(d.Minutes()) + 1))
				}
				if bcrypt.CompareHashAndPassword([]byte(x.PasswordHash), []byte(in.Body.Password)) != nil {
					time.Sleep(600 * time.Millisecond)
					if h.guests.failed(ip) {
						return nil, huma.Error429TooManyRequests(sharePasswordLocked(int(loginLockFor.Minutes())))
					}
					return nil, huma.Error401Unauthorized(sharePasswordWrong)
				}
				h.guests.success(ip)
			}
			sig, err := h.guestSig(ctx, x, l)
			if err != nil {
				return nil, err
			}
			out.SetCookie = http.Cookie{Name: guestCookie, Value: sig, Path: "/api/share/" + l.Token, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: int(webSessionTTL.Seconds())}
			return out, nil
		})
	huma.Register(api, huma.Operation{OperationID: "guest-overview", Method: http.MethodGet, Path: "/api/share/{token}/overview", Tags: tag, Summary: "访客：概览"},
		func(ctx context.Context, in *guestIn) (*guestOverviewOut, error) {
			g, err := h.guestAuth(ctx, in.Token, in.Cookie)
			if err != nil {
				return nil, err
			}
			sv, err := h.scopeView(ctx, g.scope)
			if err != nil {
				return nil, err
			}
			out := &guestOverviewOut{}
			b := &out.Body
			b.Name, b.Guest, b.Nodes, b.Now = g.share.Name, g.link.Name, sv.Nodes, time.Now().UTC()
			b.Codes, b.Counts = []string{}, map[string]int{}
			var reqIDs, keys []string
			for _, n := range sv.Nodes {
				switch n.Kind {
				case store.ShareKindPlan:
					b.Codes = append(b.Codes, n.Code)
				case store.ShareKindReq:
					b.Codes = append(b.Codes, n.Code)
					b.Counts[n.Status]++
					reqIDs = append(reqIDs, n.Ref)
				case store.ShareKindSession:
					keys = append(keys, n.Ref)
				}
			}
			b.Sessions = h.guestSessions(ctx, g, keys)
			b.Decs = []GuestDecRef{}
			for _, n := range sv.Nodes {
				if n.Kind != store.ShareKindReq {
					continue
				}
				ds, err := h.store.GuestReqDecisions(ctx, n.Ref)
				if err != nil {
					return nil, err
				}
				for _, d := range ds {
					title := d.ShortTitle
					if title == "" {
						title = guestLine(d.Question)
					}
					b.Decs = append(b.Decs, GuestDecRef{Code: d.Code, Req: n.Code, Title: title, Status: d.Status})
				}
			}
			as, err := h.store.ShareActivities(ctx, reqIDs, 50)
			if err != nil {
				return nil, err
			}
			b.Activity = g.activity(as)
			return out, nil
		})
	huma.Register(api, huma.Operation{OperationID: "guest-requirement", Method: http.MethodGet, Path: "/api/share/{token}/requirements/{ref}", Tags: tag, Summary: "访客：需求详情（决策、证据、留言、进展）"},
		func(ctx context.Context, in *guestRefIn) (*guestReqOut, error) {
			g, err := h.guestAuth(ctx, in.Token, in.Cookie)
			if err != nil {
				return nil, err
			}
			r, err := h.store.GetRequirement(ctx, in.Ref)
			if err != nil || !g.scope.Reqs[r.ID] {
				return nil, notIncluded()
			}
			out := &guestReqOut{}
			b := &out.Body
			b.Requirement = g.requirement(r)
			ds, err := h.store.ReqDecisions(ctx, r.ID)
			if err != nil {
				return nil, err
			}
			b.Decisions = []store.Decision{}
			for _, d := range ds {
				b.Decisions = append(b.Decisions, g.decision(d))
			}
			b.Sessions = h.guestSessions(ctx, g, b.Requirement.Sessions)
			as, err := h.store.ShareActivities(ctx, []string{r.ID}, 100)
			if err != nil {
				return nil, err
			}
			b.Activity = g.activity(as)
			return out, nil
		})
	huma.Register(api, huma.Operation{OperationID: "guest-plan", Method: http.MethodGet, Path: "/api/share/{token}/plans/{ref}", Tags: tag, Summary: "访客：计划与阶段（只含分享里的阶段，范围外的需求只给编号）"},
		func(ctx context.Context, in *guestRefIn) (*guestPlanOut, error) {
			g, err := h.guestAuth(ctx, in.Token, in.Cookie)
			if err != nil {
				return nil, err
			}
			p, err := h.store.GetPlan(ctx, in.Ref)
			if err != nil {
				return nil, notIncluded()
			}
			v := g.plan(p)
			if v == nil {
				return nil, notIncluded()
			}
			return &guestPlanOut{Body: *v}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "guest-decision", Method: http.MethodGet, Path: "/api/share/{token}/decisions/{ref}", Tags: tag, Summary: "访客：决策（只限分享里需求上的）"},
		func(ctx context.Context, in *guestRefIn) (*guestDecOut, error) {
			g, err := h.guestAuth(ctx, in.Token, in.Cookie)
			if err != nil {
				return nil, err
			}
			d, err := h.store.GetDecision(ctx, in.Ref)
			if err != nil || !g.scope.Reqs[d.RequirementID] || !slices.Contains(store.GuestDecisionKinds, d.Kind) {
				return nil, notIncluded()
			}
			return &guestDecOut{Body: g.decision(d)}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "guest-session", Method: http.MethodGet, Path: "/api/share/{token}/sessions/{key}", Tags: tag, Summary: "访客：会话详情（每轮提问和回复全文、工具调用摘要；主调度只给状态）"},
		func(ctx context.Context, in *guestSessionIn) (*guestSessionOut, error) {
			g, err := h.guestAuth(ctx, in.Token, in.Cookie)
			if err != nil {
				return nil, err
			}
			if !g.scope.Sessions[in.Key] {
				return nil, notIncluded()
			}
			x, err := h.store.GetSession(ctx, in.Key)
			if err != nil {
				return nil, notIncluded()
			}
			out := &guestSessionOut{}
			out.Body.Session, out.Body.Turns = g.session(x), []GuestTurn{}
			if !out.Body.Session.Conversation {
				return out, nil
			}
			var before time.Time
			if in.Before != "" {
				if before, err = time.Parse(time.RFC3339Nano, in.Before); err != nil {
					return nil, huma.Error400BadRequest("before 格式不对")
				}
			}
			ts, err := h.sessionTurns(ctx, in.Key, before, turnsDefault)
			if err != nil {
				return nil, err
			}
			for i, t := range ts.Body.Turns {
				out.Body.Turns = append(out.Body.Turns, guestTurn(t, before.IsZero() && i == len(ts.Body.Turns)-1))
			}
			out.Body.More, out.Body.Before = ts.Body.More, ts.Body.Before
			return out, nil
		})
}

// plan 只留分享里的阶段；计划本身不在分享里（只挂了某个阶段）时只给编号。范围外的需求只给编号。
func (g *guestCtx) plan(p store.Plan) *store.Plan {
	inPlan := g.scope.Plans[p.ID]
	var stages []store.Stage
	for _, st := range p.Stages {
		if !g.scope.Stages[st.ID] {
			continue
		}
		var rs []store.PlanReq
		for _, r := range st.Requirements {
			if !g.scope.Reqs[r.ID] {
				r = store.PlanReq{Code: r.Code}
			}
			rs = append(rs, r)
		}
		if rs == nil {
			rs = []store.PlanReq{}
		}
		st.Requirements = rs
		stages = append(stages, st)
	}
	if !inPlan && len(stages) == 0 {
		return nil
	}
	p.OwnerSession = g.keep(p.OwnerSession)
	p.ProjectID = ""
	if !inPlan {
		p.Title, p.Goal, p.CreatedBy = "", "", ""
	}
	if stages == nil {
		stages = []store.Stage{}
	}
	p.Stages = stages
	return &p
}

// ---------- 附件、实时通道（不走 huma） ----------

// guestAuthHTTP 是非 huma 接口的访客校验。
func (h *Hub) guestAuthHTTP(w http.ResponseWriter, r *http.Request) *guestCtx {
	var cookie string
	if c, err := r.Cookie(guestCookie); err == nil {
		cookie = c.Value
	}
	g, err := h.guestAuth(r.Context(), r.PathValue("token"), cookie)
	if err != nil {
		var se huma.StatusError
		if errors.As(err, &se) {
			writeErr(w, se.GetStatus(), se.Error())
		} else {
			writeErr(w, http.StatusInternalServerError, err.Error())
		}
		return nil
	}
	return g
}

// handleGuestFile 只放行分享范围里的内容引用到的附件：需求描述和证据（含表达块）、访客看得到的决策、阶段小结。
func (h *Hub) handleGuestFile(w http.ResponseWriter, r *http.Request) {
	g := h.guestAuthHTTP(w, r)
	if g == nil {
		return
	}
	owners, err := h.store.FileOwners(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	allowed := false
	for _, o := range owners {
		switch {
		case o.ReqID != "" && g.scope.Reqs[o.ReqID]:
			allowed = true
		case o.DecReq != "" && g.scope.Reqs[o.DecReq] && slices.Contains(store.GuestDecisionKinds, o.DecKind):
			allowed = true
		case o.StageID != "" && g.scope.Stages[o.StageID]:
			allowed = true
		}
	}
	if !allowed {
		writeErr(w, http.StatusNotFound, shareNotIncluded)
		return
	}
	h.handleDownload(w, r)
}

// guestRecheck 是实时通道复查链接是否仍可用的间隔（撤回、改密码时另外立即断开）。
const guestRecheck = 20 * time.Second

// guestDebounce：访客页的「有变化」信号最多这么久一次（Agent 事件很密，访客只需要跟上）。
const guestDebounce = 2 * time.Second

// serveGuestStream 是访客的实时通道（SSE）：内容有变化时发 changed（不带数据，访客页按自己的范围重新拉取），
// 链接撤回、分享撤回或到期、密码改了时发 gone 并断开。
func (h *Hub) serveGuestStream(w http.ResponseWriter, r *http.Request) {
	g := h.guestAuthHTTP(w, r)
	if g == nil {
		return
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "不支持流式响应", http.StatusInternalServerError)
		return
	}
	var cookie string
	if c, err := r.Cookie(guestCookie); err == nil {
		cookie = c.Value
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	sub := &guestSub{shareID: g.share.ID, linkID: g.link.ID, ch: make(chan struct{}, 1), gone: make(chan struct{})}
	h.broker.addGuest(sub)
	defer h.broker.removeGuest(sub)
	fmt.Fprintf(w, "retry: 3000\nevent: hello\ndata: %s\n\n", clock())
	fl.Flush()
	still := func() bool {
		_, err := h.guestAuth(r.Context(), r.PathValue("token"), cookie)
		return err == nil
	}
	gone := func() {
		fmt.Fprint(w, "event: gone\ndata: {}\n\n")
		fl.Flush()
	}
	ping := time.NewTicker(guestRecheck)
	defer ping.Stop()
	var last time.Time
	for {
		select {
		case <-r.Context().Done():
			return
		case <-sub.gone:
			gone()
			return
		case <-sub.ch:
			if wait := guestDebounce - time.Since(last); wait > 0 {
				select {
				case <-r.Context().Done():
					return
				case <-sub.gone:
					gone()
					return
				case <-time.After(wait):
				}
			}
			if !still() {
				gone()
				return
			}
			last = time.Now()
			fmt.Fprint(w, "event: changed\ndata: \"share\"\n\n")
			fl.Flush()
		case <-ping.C:
			if !still() {
				gone()
				return
			}
			fmt.Fprintf(w, "event: ping\ndata: %s\n\n", clock())
			fl.Flush()
		}
	}
}

// guestPostPath：访客能 POST 的只有两处——输密码 /api/share/<token>/unlock、留言 /api/share/<token>/comments（R-281 第 4 段）。
func guestPostPath(p string) bool {
	rest, ok := strings.CutPrefix(p, "/api/share/")
	if !ok {
		return false
	}
	parts := strings.Split(rest, "/")
	return len(parts) == 2 && parts[0] != "" && (parts[1] == "unlock" || parts[1] == "comments")
}

// guestMethodGuard：访客接口只有读、输密码和留言，其它方法一律 405。
func guestMethodGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead && !(r.Method == http.MethodPost && guestPostPath(r.URL.Path)) {
			writeErr(w, http.StatusMethodNotAllowed, shareReadOnly)
			return
		}
		next.ServeHTTP(w, r)
	})
}
