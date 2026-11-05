package hub

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NekroAI/kitestring/internal/store"
)

// 访客留言（R-281 第 4 段）。
//
// 访客侧：POST /api/share/{token}/comments 是访客唯一能写的接口，只能新建一条待收的访客留言（share_comments），
// 对象必须是分享范围里的需求、需求上访客看得到的决策、需求的证据，或分享里的阶段；写不了需求、决策、证据本身，也改不了状态。
// GET /api/share/{token}/comments 列出这条链接写过的留言和处理结果。
//
// 所有者侧：待收的访客留言进「待处理」，在审批抽屉里原样放入、改写放入（都可以加附言，改写保留原话）或忽略；
// 放入后写成那条需求的留言（kind = note，送达关联会话），guest 列记下「来自谁、经哪个分享、针对什么」，req_get 和送达文字里都带这个来源。

const (
	guestRateWindow = 10 * time.Minute
	guestRateMax    = 20 // 一条链接 10 分钟内最多留言条数
)

// ---------- 访客侧 ----------

type guestCommentIn struct {
	Token  string `path:"token"`
	Cookie string `cookie:"ks_share"`
	Body   struct {
		TargetKind string `json:"target_kind" enum:"req,dec,evidence,stage" doc:"留言针对的对象：req 需求 / dec 决策 / evidence 证据 / stage 阶段"`
		TargetRef  string `json:"target_ref" doc:"需求编号 R-x、决策编号 D-x、证据 ID、阶段 ID"`
		Text       string `json:"text" doc:"留言正文，最多 2000 字"`
	}
}

// GuestComment 是访客看到的自己的一条留言。
type GuestComment struct {
	ID          string    `json:"id"`
	TargetKind  string    `json:"target_kind" enum:"req,dec,evidence,stage"`
	TargetRef   string    `json:"target_ref"`
	TargetCode  string    `json:"target_code,omitempty" doc:"决策编号；阶段是所属计划编号"`
	TargetSeq   int       `json:"target_seq,omitempty"`
	TargetTitle string    `json:"target_title,omitempty"`
	Req         string    `json:"req,omitempty" doc:"所属需求编号"`
	Text        string    `json:"text"`
	Status      string    `json:"status" enum:"pending,placed,ignored" doc:"pending 待分享者处理 / placed 已放入需求 / ignored 分享者已看过、没有放入"`
	CreatedAt   time.Time `json:"created_at"`
}

func guestComment(c store.ShareComment) GuestComment {
	return GuestComment{ID: c.ID, TargetKind: c.TargetKind, TargetRef: c.TargetRef, TargetCode: c.TargetCode, TargetSeq: c.TargetSeq, TargetTitle: c.TargetTitle,
		Req: c.RequirementCode, Text: c.Text, Status: c.Status, CreatedAt: c.CreatedAt}
}

type guestCommentOut struct{ Body GuestComment }

type guestCommentsOut struct {
	Body struct {
		Comments []GuestComment `json:"comments" doc:"这条链接写过的留言，新的在前，最多 50 条"`
	}
}

// guestTarget 核对访客留言的对象在分享范围里，返回要记下的 需求 ID / 计划 ID / 阶段序号 和规范化后的对象 ID。
func (h *Hub) guestTarget(ctx context.Context, g *guestCtx, kind, ref string) (store.GuestCommentIn, error) {
	in := store.GuestCommentIn{TargetKind: kind}
	ref = strings.TrimSpace(ref)
	switch kind {
	case store.GuestOnReq:
		r, err := h.store.GetRequirement(ctx, ref)
		if err != nil || !g.scope.Reqs[r.ID] {
			return in, notIncluded()
		}
		in.TargetRef, in.RequirementID = r.ID, r.ID
	case store.GuestOnDec:
		d, err := h.store.GetDecision(ctx, ref)
		if err != nil || !g.scope.Reqs[d.RequirementID] || !slices.Contains(store.GuestDecisionKinds, d.Kind) {
			return in, notIncluded()
		}
		in.TargetRef, in.RequirementID = d.ID, d.RequirementID
	case store.GuestOnEvidence:
		owners, err := h.store.EvidenceOwner(ctx, ref)
		if err != nil || !g.scope.Reqs[owners] {
			return in, notIncluded()
		}
		in.TargetRef, in.RequirementID = ref, owners
	case store.GuestOnStage:
		if !g.scope.Stages[ref] {
			return in, notIncluded()
		}
		planID, seq, err := h.store.StagePlace(ctx, ref)
		if err != nil {
			return in, notIncluded()
		}
		in.TargetRef, in.PlanID, in.StageSeq = ref, planID, seq
	default:
		return in, huma.Error400BadRequest(shareCommentTarget)
	}
	return in, nil
}

func (h *Hub) registerShareGuestComments(api huma.API) {
	tag := []string{"share-guest"}
	huma.Register(api, huma.Operation{OperationID: "guest-comment", Method: http.MethodPost, Path: "/api/share/{token}/comments", Tags: tag,
		Summary:     "访客：留言（先进分享者的待收队列）",
		Description: "访客唯一能写的接口：在分享里的需求、决策、证据或阶段上留一条言。留言只进待收队列，分享者放入后才成为需求留言；不能借它改任何别的内容。"},
		func(ctx context.Context, in *guestCommentIn) (*guestCommentOut, error) {
			g, err := h.guestAuth(ctx, in.Token, in.Cookie)
			if err != nil {
				return nil, err
			}
			text := strings.TrimSpace(in.Body.Text)
			if text == "" {
				return nil, huma.Error400BadRequest(shareCommentEmpty)
			}
			if utf8.RuneCountInString(text) > store.GuestTextMax {
				return nil, huma.Error400BadRequest(shareCommentTooLong(store.GuestTextMax))
			}
			ci, err := h.guestTarget(ctx, g, in.Body.TargetKind, in.Body.TargetRef)
			if err != nil {
				return nil, err
			}
			if n, err := h.store.RecentLinkComments(ctx, g.link.ID, time.Now().Add(-guestRateWindow)); err != nil {
				return nil, err
			} else if n >= guestRateMax {
				return nil, huma.Error429TooManyRequests(shareCommentTooMany)
			}
			ci.ShareID, ci.LinkID, ci.GuestName, ci.Text = g.share.ID, g.link.ID, g.link.Name, text
			c, err := h.store.CreateShareComment(ctx, ci)
			if err != nil {
				return nil, err
			}
			h.broker.publish("shares")
			return &guestCommentOut{Body: guestComment(c)}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "guest-comments", Method: http.MethodGet, Path: "/api/share/{token}/comments", Tags: tag, Summary: "访客：这条链接写过的留言和处理结果"},
		func(ctx context.Context, in *guestIn) (*guestCommentsOut, error) {
			g, err := h.guestAuth(ctx, in.Token, in.Cookie)
			if err != nil {
				return nil, err
			}
			cs, err := h.store.LinkComments(ctx, g.link.ID, 50)
			if err != nil {
				return nil, err
			}
			out := &guestCommentsOut{}
			out.Body.Comments = []GuestComment{}
			for _, c := range cs {
				out.Body.Comments = append(out.Body.Comments, guestComment(c))
			}
			return out, nil
		})
}

// ---------- 所有者侧 ----------

// ShareCommentView 是所有者看到的一条访客留言。
type ShareCommentView struct {
	store.ShareComment
	// Choices：挂在阶段上的留言放入时可选的需求（这个阶段里的需求）；其它对象为空
	Choices []store.RequirementRef `json:"choices,omitempty" doc:"挂在阶段上的留言：放入时可选的需求（这个阶段的需求）"`
}

type shareCommentsOut struct {
	Body struct {
		Comments []ShareCommentView `json:"comments" doc:"待收的访客留言（全部分享），早的在前"`
	}
}

type shareCommentIDIn struct {
	ID string `path:"id"`
}

type placeIn struct {
	ID   string `path:"id"`
	Body struct {
		Text        string `json:"text,omitempty" doc:"改写放入时的正文；不填为原样放入"`
		Addend      string `json:"addend,omitempty" doc:"附言（可选）"`
		Requirement string `json:"requirement,omitempty" doc:"挂在阶段上的留言放进哪条需求（R-x）；其它对象不填，放进留言所属的需求"`
	}
}

type placeOut struct {
	Body struct {
		Comment  store.ShareComment `json:"comment"`
		Decision store.Decision     `json:"decision" doc:"放入后成为的需求留言"`
	}
}

type shareCommentOut struct{ Body store.ShareComment }

func (h *Hub) commentView(ctx context.Context, c store.ShareComment) ShareCommentView {
	v := ShareCommentView{ShareComment: c}
	if c.TargetKind == store.GuestOnStage && c.PlanID != "" {
		if p, err := h.store.GetPlan(ctx, c.PlanID); err == nil {
			for _, st := range p.Stages {
				if st.Seq != c.TargetSeq {
					continue
				}
				for _, r := range st.Requirements {
					v.Choices = append(v.Choices, store.RequirementRef{Code: r.Code, Title: r.Title, Status: r.Status})
				}
			}
		}
	}
	return v
}

func (h *Hub) registerShareComments(api huma.API) {
	tag := []string{"shares"}
	huma.Register(api, huma.Operation{OperationID: "list-share-comments", Method: http.MethodGet, Path: "/api/share-comments", Tags: tag, Summary: "待收的访客留言（全部分享）"},
		func(ctx context.Context, _ *empty) (*shareCommentsOut, error) {
			cs, err := h.store.PendingShareComments(ctx)
			if err != nil {
				return nil, err
			}
			out := &shareCommentsOut{}
			out.Body.Comments = []ShareCommentView{}
			for _, c := range cs {
				out.Body.Comments = append(out.Body.Comments, h.commentView(ctx, c))
			}
			return out, nil
		})
	huma.Register(api, huma.Operation{OperationID: "place-share-comment", Method: http.MethodPost, Path: "/api/share-comments/{id}/place", Tags: tag,
		Summary: "放入访客留言：原样或改写放进需求留言（可加附言，改写保留原话）"},
		func(ctx context.Context, in *placeIn) (*placeOut, error) {
			c, err := h.store.GetShareComment(ctx, in.ID)
			if err != nil {
				return nil, apiErr(err)
			}
			if c.Status != store.GuestPending {
				return nil, huma.Error409Conflict(store.ErrGuestHandled.Error())
			}
			ref := c.RequirementID
			if c.TargetKind == store.GuestOnStage {
				ref = strings.TrimSpace(in.Body.Requirement)
				ok := false
				for _, x := range h.commentView(ctx, c).Choices {
					ok = ok || x.Code == ref
				}
				if !ok {
					return nil, huma.Error400BadRequest(shareCommentPickReq)
				}
			}
			r, err := h.store.GetRequirement(ctx, ref)
			if err != nil {
				return nil, apiErr(err)
			}
			text := strings.TrimSpace(in.Body.Text)
			origin := store.GuestOrigin{Comment: c.ID, Name: c.GuestName, Share: c.ShareName, At: c.CreatedAt, TargetKind: c.TargetKind, Addend: strings.TrimSpace(in.Body.Addend)}
			if c.TargetKind != store.GuestOnReq {
				origin.TargetCode, origin.TargetSeq, origin.TargetTitle = c.TargetCode, c.TargetSeq, c.TargetTitle
			}
			if text == "" || text == c.Text {
				text = c.Text
			} else {
				origin.Original = c.Text
			}
			d, err := h.store.PlaceShareComment(ctx, c, r, h.feedbackTarget(ctx, r), text, origin)
			if errors.Is(err, store.ErrGuestHandled) {
				return nil, huma.Error409Conflict(err.Error())
			}
			if err != nil {
				return nil, apiErr(err)
			}
			if x, err := h.store.GetSession(ctx, d.SessionKey); err == nil {
				h.pushDecisions(ctx, x.DeviceID)
			}
			ctx = withTold(ctx)
			markTold(ctx, r.ID, d.SessionKey)
			h.reqEvent(ctx, r, evComment, "", "访客留言 "+d.Code)
			h.broker.publish("requirements", "decisions", "sessions", "shares")
			out := &placeOut{}
			out.Body.Decision = d
			out.Body.Comment, _ = h.store.GetShareComment(ctx, c.ID)
			return out, nil
		})
	huma.Register(api, huma.Operation{OperationID: "ignore-share-comment", Method: http.MethodPost, Path: "/api/share-comments/{id}/ignore", Tags: tag, Summary: "忽略访客留言（访客不会收到通知）"},
		func(ctx context.Context, in *shareCommentIDIn) (*shareCommentOut, error) {
			c, err := h.store.IgnoreShareComment(ctx, in.ID)
			if errors.Is(err, store.ErrGuestHandled) {
				return nil, huma.Error409Conflict(err.Error())
			}
			if err != nil {
				return nil, apiErr(err)
			}
			h.broker.publish("shares")
			return &shareCommentOut{Body: c}, nil
		})
}

// guestNoteSource 是放入的访客留言写给会话看的来源：「来自「小王」（经分享链接「P-8 进展」），由用户放入，针对 D-12」。
// 送达文字和 req_get 都用它（mcpserver 里另有一份同样的写法）。
func guestNoteSource(g *store.GuestOrigin) string {
	s := "来自「" + g.Name + "」（经分享链接「" + g.Share + "」），由用户放入"
	if t := guestTargetText(g); t != "" {
		s += "，针对" + t
	}
	return s
}

func guestTargetText(g *store.GuestOrigin) string {
	switch g.TargetKind {
	case store.GuestOnDec:
		return " " + g.TargetCode
	case store.GuestOnEvidence:
		return "证据「" + g.TargetTitle + "」"
	case store.GuestOnStage:
		return " " + g.TargetCode + " 阶段 " + strconv.Itoa(g.TargetSeq)
	}
	return ""
}

// guestNoteText 是放入的访客留言送达会话时的全文：来源、正文、改写前的原话、附言。
func guestNoteText(d store.Decision) string {
	g := d.Guest
	s := guestNoteSource(g) + "：" + d.Answer
	if g.Original != "" {
		s += "\n  访客原话：" + g.Original
	}
	if g.Addend != "" {
		s += "\n  用户附言：" + g.Addend
	}
	return s
}
