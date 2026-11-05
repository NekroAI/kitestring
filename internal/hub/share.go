package hub

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"golang.org/x/crypto/bcrypt"

	"github.com/NekroAI/kitestring/internal/store"
)

// 分享（R-281）：所有者侧的增删改查。访客侧（免登录只读）见 shareguest.go。
//
// 一个分享 = 名称 + 可选访问密码（所有链接共用）+ 有效期 + 挂载项（计划 / 阶段 / 需求 / 会话）+ 每个访客一条带名字的链接。
// 挂载项可开「包含以后新增的」（auto），单独取消的记成 excluded，上级的 auto 不会把它带回来；实际范围由 store.ResolveShare 算。

// sharePrefix 是访客 token 的前缀，和设备 token（kst_）区分开：访客 token 不能当成设备或网页登录态用。
const sharePrefix = "kss_"

// shareGuestPath 是访客页的网页路径；对外地址（设置里的 public_url）非空时拼成完整链接。
func shareGuestPath(token string) string { return "/s/" + token }

// ShareNode 是分享范围里的一项，所有者的关系图和访客的目录共用。
type ShareNode struct {
	Kind   string `json:"kind" enum:"plan,stage,req,session"`
	Ref    string `json:"ref" doc:"计划 / 阶段 / 需求的 ID，会话键"`
	Code   string `json:"code,omitempty" doc:"计划、需求的编号；阶段是所属计划的编号"`
	Seq    int    `json:"seq,omitempty" doc:"阶段：第几阶段"`
	Title  string `json:"title"`
	Status string `json:"status,omitempty" doc:"计划 / 阶段 / 需求的状态；会话是显示状态的原始值（state）"`
	Role   string `json:"role,omitempty" doc:"会话的身份"`
	// Parents：上级节点的 ref（都在分享里）：阶段 → 计划，需求 → 阶段，会话 → 关联的需求（可能几条）
	Parents []string `json:"parents"`
}

// ShareScopeView 是分享实际包含的内容，按 计划 → 阶段 → 需求 → 会话 排好。
type ShareScopeView struct {
	Nodes  []ShareNode `json:"nodes"`
	Plans  int         `json:"plans"`
	Stages int         `json:"stages"`
	Reqs   int         `json:"reqs"`
	Sess   int         `json:"sessions"`
}

// ShareLinkView 是所有者看到的访客链接。
type ShareLinkView struct {
	store.ShareLink
	Path string `json:"path" doc:"访客页路径 /s/<token>"`
	URL  string `json:"url,omitempty" doc:"按设置里的对外地址拼好的完整链接；没填对外地址时为空，网页用当前地址拼"`
}

// ShareView 是所有者看到的分享。
type ShareView struct {
	store.Share
	Links       []ShareLinkView `json:"links"`
	Scope       ShareScopeView  `json:"scope"`
	HasSessions bool            `json:"has_sessions" doc:"范围里有会话：新建 / 编辑时提醒「会话对话全文对访客可见」"`
	LastVisitAt *time.Time      `json:"last_visit_at,omitempty" doc:"所有链接里最近一次访问"`
	Visits      int             `json:"visits" doc:"所有链接的访问次数合计"`
}

type shareOut struct{ Body ShareView }

type sharesOut struct {
	Body struct {
		Shares []ShareView `json:"shares" doc:"生效中的在前，其余按新建时间倒序"`
	}
}

type shareIDPath struct {
	ID string `path:"id"`
}

type shareLinkPath struct {
	ID   string `path:"id"`
	Link string `path:"link"`
}

type shareItemIn struct {
	Kind     string `json:"kind" enum:"plan,stage,req,session"`
	Ref      string `json:"ref" doc:"计划 / 需求的 ID 或编号（P-x / R-x），阶段 ID，会话键"`
	Auto     bool   `json:"auto,omitempty" doc:"包含以后新增的"`
	Excluded bool   `json:"excluded,omitempty" doc:"单独取消：上级开着「包含以后新增的」也不纳入"`
}

func toItems(in []shareItemIn) []store.ShareItem {
	out := make([]store.ShareItem, len(in))
	for i, x := range in {
		out[i] = store.ShareItem{Kind: x.Kind, Ref: x.Ref, Auto: x.Auto, Excluded: x.Excluded}
	}
	return out
}

type shareCreateIn struct {
	Body struct {
		Name      string        `json:"name" minLength:"1" maxLength:"80"`
		ProjectID string        `json:"project_id,omitempty"`
		Password  string        `json:"password,omitempty" maxLength:"128" doc:"访问密码，可选；所有链接共用"`
		ExpiresAt *time.Time    `json:"expires_at,omitempty" doc:"到期时间；省略表示永久。7 天 / 30 天 / 自定义由网页换算成时间"`
		Items     []shareItemIn `json:"items"`
		Links     []string      `json:"links,omitempty" doc:"同时建好的访客链接名字（每个访客一条）"`
	}
}

type shareUpdateIn struct {
	ID   string `path:"id"`
	Body struct {
		Name     *string `json:"name,omitempty" minLength:"1" maxLength:"80"`
		Password *string `json:"password,omitempty" maxLength:"128" doc:"不传 = 不改；空串 = 去掉密码；改密码后已输过密码的访客要重新输"`
		// ExpiresAt / Forever：都不传 = 不改；forever = true 改成永久
		ExpiresAt *time.Time    `json:"expires_at,omitempty"`
		Forever   bool          `json:"forever,omitempty" doc:"改成永久"`
		Items     []shareItemIn `json:"items,omitempty" doc:"不传 = 不改；传了就整组替换"`
	}
}

type shareEnableIn struct {
	ID   string `path:"id"`
	Body *struct {
		ExpiresAt *time.Time `json:"expires_at,omitempty" doc:"已到期的分享重新启用时要给新的到期时间（或 forever）"`
		Forever   bool       `json:"forever,omitempty"`
	} `required:"false"`
}

type sharePreviewIn struct {
	Body struct {
		Items []shareItemIn `json:"items"`
	}
}

type sharePreviewOut struct {
	Body ShareScopeView
}

type shareCandidatesIn struct {
	ProjectID string `query:"project_id" doc:"只列这个项目的；空 = 全部项目"`
}

// shareCandidates 把项目里的全部计划、阶段、需求和需求关联的会话当成一个范围，勾选列表按它画。
func (h *Hub) shareCandidates(ctx context.Context, projectID string) (store.ShareScope, error) {
	sc := store.ShareScope{Plans: map[string]bool{}, Stages: map[string]bool{}, Reqs: map[string]bool{}, Sessions: map[string]bool{}}
	plans, err := h.store.ListPlans(ctx, projectID)
	if err != nil {
		return sc, err
	}
	for _, p := range plans {
		sc.Plans[p.ID] = true
		for _, st := range p.Stages {
			sc.Stages[st.ID] = true
		}
	}
	reqs, err := h.store.ListRequirements(ctx, projectID, nil)
	if err != nil {
		return sc, err
	}
	ids := make([]string, 0, len(reqs))
	for _, r := range reqs {
		sc.Reqs[r.ID] = true
		ids = append(ids, r.ID)
	}
	links, err := h.store.ReqSessionLinks(ctx, ids)
	if err != nil {
		return sc, err
	}
	for k := range links {
		sc.Sessions[k] = true
	}
	return sc, nil
}

type shareLinkIn struct {
	ID   string `path:"id"`
	Body struct {
		Name string `json:"name" minLength:"1" maxLength:"40" doc:"访客的名字，如「小王」"`
	}
}

type shareLinkUpdateIn struct {
	ID   string `path:"id"`
	Link string `path:"link"`
	Body struct {
		Name    *string `json:"name,omitempty" minLength:"1" maxLength:"40"`
		Revoked *bool   `json:"revoked,omitempty" doc:"true 撤回（下一次访问即拒绝）/ false 恢复"`
	}
}

type shareLinkOut struct{ Body ShareLinkView }

func (h *Hub) registerShareWeb(api huma.API) {
	tag := []string{"shares"}
	huma.Register(api, huma.Operation{OperationID: "list-shares", Method: http.MethodGet, Path: "/api/shares", Tags: tag, Summary: "分享列表"},
		func(ctx context.Context, _ *struct{}) (*sharesOut, error) {
			xs, err := h.store.ListShares(ctx)
			if err != nil {
				return nil, err
			}
			out := &sharesOut{}
			out.Body.Shares = []ShareView{}
			for _, x := range xs {
				v, err := h.shareView(ctx, x)
				if err != nil {
					return nil, err
				}
				out.Body.Shares = append(out.Body.Shares, v)
			}
			sort.SliceStable(out.Body.Shares, func(i, j int) bool {
				return out.Body.Shares[i].Status == store.ShareActive && out.Body.Shares[j].Status != store.ShareActive
			})
			return out, nil
		})
	huma.Register(api, huma.Operation{OperationID: "preview-share", Method: http.MethodPost, Path: "/api/shares/preview", Tags: tag, Summary: "按挂载项算出分享实际包含的内容（新建 / 编辑时画关系图）"},
		func(ctx context.Context, in *sharePreviewIn) (*sharePreviewOut, error) {
			items, err := h.store.NormShareItems(ctx, toItems(in.Body.Items))
			if err != nil {
				return nil, apiErr(err)
			}
			sc, err := h.store.ResolveShare(ctx, items)
			if err != nil {
				return nil, err
			}
			v, err := h.scopeView(ctx, sc)
			return &sharePreviewOut{Body: v}, err
		})
	huma.Register(api, huma.Operation{OperationID: "share-candidates", Method: http.MethodGet, Path: "/api/shares/candidates", Tags: tag, Summary: "可以分享的全部内容（新建 / 编辑时的勾选列表）：项目里的计划、阶段、需求和它们关联的会话"},
		func(ctx context.Context, in *shareCandidatesIn) (*sharePreviewOut, error) {
			sc, err := h.shareCandidates(ctx, in.ProjectID)
			if err != nil {
				return nil, err
			}
			v, err := h.scopeView(ctx, sc)
			return &sharePreviewOut{Body: v}, err
		})
	huma.Register(api, huma.Operation{OperationID: "create-share", Method: http.MethodPost, Path: "/api/shares", Tags: tag, Summary: "新建分享"},
		func(ctx context.Context, in *shareCreateIn) (*shareOut, error) {
			b := in.Body
			name := strings.TrimSpace(b.Name)
			if name == "" {
				return nil, huma.Error400BadRequest("分享名称不能为空")
			}
			if b.ExpiresAt != nil && !b.ExpiresAt.After(time.Now()) {
				return nil, huma.Error400BadRequest("到期时间已过，请选一个以后的时间")
			}
			items, err := h.store.NormShareItems(ctx, toItems(b.Items))
			if err != nil {
				return nil, apiErr(err)
			}
			x := store.Share{Name: name, ProjectID: b.ProjectID, ExpiresAt: b.ExpiresAt}
			if x.PasswordHash, err = sharePasswordHash(b.Password); err != nil {
				return nil, err
			}
			x, err = h.store.CreateShare(ctx, x, items)
			if err != nil {
				return nil, apiErr(err)
			}
			for _, ln := range b.Links {
				if ln = strings.TrimSpace(ln); ln != "" {
					if _, err := h.store.CreateShareLink(ctx, x.ID, ln, randomToken(sharePrefix)); err != nil {
						return nil, err
					}
				}
			}
			return h.shareResp(ctx, x.ID, true)
		})
	huma.Register(api, huma.Operation{OperationID: "get-share", Method: http.MethodGet, Path: "/api/shares/{id}", Tags: tag},
		func(ctx context.Context, in *shareIDPath) (*shareOut, error) {
			return h.shareResp(ctx, in.ID)
		})
	huma.Register(api, huma.Operation{OperationID: "update-share", Method: http.MethodPut, Path: "/api/shares/{id}", Tags: tag, Summary: "修改分享：名称、密码、有效期、挂载项"},
		func(ctx context.Context, in *shareUpdateIn) (*shareOut, error) {
			b := in.Body
			var u store.ShareUpdate
			if b.Name != nil {
				n := strings.TrimSpace(*b.Name)
				if n == "" {
					return nil, huma.Error400BadRequest("分享名称不能为空")
				}
				u.Name = &n
			}
			if b.Password != nil {
				hash, err := sharePasswordHash(*b.Password)
				if err != nil {
					return nil, err
				}
				u.PasswordHash = &hash
			}
			switch {
			case b.Forever:
				var none *time.Time
				u.ExpiresAt = &none
			case b.ExpiresAt != nil:
				if !b.ExpiresAt.After(time.Now()) {
					return nil, huma.Error400BadRequest("到期时间已过，请选一个以后的时间")
				}
				u.ExpiresAt = &b.ExpiresAt
			}
			if b.Items != nil {
				items, err := h.store.NormShareItems(ctx, toItems(b.Items))
				if err != nil {
					return nil, apiErr(err)
				}
				u.Items = items
			}
			if _, err := h.store.UpdateShare(ctx, in.ID, u); err != nil {
				return nil, apiErr(err)
			}
			if u.PasswordHash != nil {
				h.broker.dropGuests(in.ID, "") // 改了密码：已输过旧密码的访客要重新输
			}
			return h.shareResp(ctx, in.ID, true)
		})
	huma.Register(api, huma.Operation{OperationID: "revoke-share", Method: http.MethodPost, Path: "/api/shares/{id}/revoke", Tags: tag, Summary: "撤回分享：所有链接下一次访问即拒绝"},
		func(ctx context.Context, in *shareIDPath) (*shareOut, error) {
			if err := h.store.SetShareRevoked(ctx, in.ID, true); err != nil {
				return nil, apiErr(err)
			}
			h.broker.dropGuests(in.ID, "")
			return h.shareResp(ctx, in.ID, true)
		})
	huma.Register(api, huma.Operation{OperationID: "enable-share", Method: http.MethodPost, Path: "/api/shares/{id}/enable", Tags: tag, Summary: "重新启用已撤回或已失效的分享"},
		func(ctx context.Context, in *shareEnableIn) (*shareOut, error) {
			x, err := h.store.GetShare(ctx, in.ID)
			if err != nil {
				return nil, apiErr(err)
			}
			var u store.ShareUpdate
			if in.Body == nil {
				in.Body = &struct {
					ExpiresAt *time.Time `json:"expires_at,omitempty" doc:"已到期的分享重新启用时要给新的到期时间（或 forever）"`
					Forever   bool       `json:"forever,omitempty"`
				}{}
			}
			switch {
			case in.Body.Forever:
				var none *time.Time
				u.ExpiresAt = &none
			case in.Body.ExpiresAt != nil:
				if !in.Body.ExpiresAt.After(time.Now()) {
					return nil, huma.Error400BadRequest("到期时间已过，请选一个以后的时间")
				}
				u.ExpiresAt = &in.Body.ExpiresAt
			case x.ExpiresAt != nil && !x.ExpiresAt.After(time.Now()):
				return nil, huma.Error400BadRequest("分享已到期，重新启用时请设新的有效期")
			}
			if u.ExpiresAt != nil {
				if _, err := h.store.UpdateShare(ctx, in.ID, u); err != nil {
					return nil, apiErr(err)
				}
			}
			if err := h.store.SetShareRevoked(ctx, in.ID, false); err != nil {
				return nil, apiErr(err)
			}
			return h.shareResp(ctx, in.ID, true)
		})
	huma.Register(api, huma.Operation{OperationID: "delete-share", Method: http.MethodDelete, Path: "/api/shares/{id}", Tags: tag, Summary: "删除分享和它的全部链接"},
		func(ctx context.Context, in *shareIDPath) (*okBody, error) {
			if err := h.store.DeleteShare(ctx, in.ID); err != nil {
				return nil, apiErr(err)
			}
			h.broker.dropGuests(in.ID, "")
			h.broker.publish("shares")
			return ok(), nil
		})
	huma.Register(api, huma.Operation{OperationID: "create-share-link", Method: http.MethodPost, Path: "/api/shares/{id}/links", Tags: tag, Summary: "给一个访客加一条链接"},
		func(ctx context.Context, in *shareLinkIn) (*shareLinkOut, error) {
			name := strings.TrimSpace(in.Body.Name)
			if name == "" {
				return nil, huma.Error400BadRequest("访客名字不能为空")
			}
			l, err := h.store.CreateShareLink(ctx, in.ID, name, randomToken(sharePrefix))
			if err != nil {
				return nil, apiErr(err)
			}
			h.broker.publish("shares")
			return &shareLinkOut{Body: h.linkView(ctx, l)}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "update-share-link", Method: http.MethodPut, Path: "/api/shares/{id}/links/{link}", Tags: tag, Summary: "改访客名字，或撤回 / 恢复这条链接"},
		func(ctx context.Context, in *shareLinkUpdateIn) (*shareLinkOut, error) {
			var name *string
			if in.Body.Name != nil {
				n := strings.TrimSpace(*in.Body.Name)
				if n == "" {
					return nil, huma.Error400BadRequest("访客名字不能为空")
				}
				name = &n
			}
			l, err := h.store.UpdateShareLink(ctx, in.ID, in.Link, name, in.Body.Revoked)
			if err != nil {
				return nil, apiErr(err)
			}
			if in.Body.Revoked != nil && *in.Body.Revoked {
				h.broker.dropGuests(in.ID, in.Link)
			}
			h.broker.publish("shares")
			return &shareLinkOut{Body: h.linkView(ctx, l)}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "delete-share-link", Method: http.MethodDelete, Path: "/api/shares/{id}/links/{link}", Tags: tag, Summary: "删除一条访客链接"},
		func(ctx context.Context, in *shareLinkPath) (*okBody, error) {
			if err := h.store.DeleteShareLink(ctx, in.ID, in.Link); err != nil {
				return nil, apiErr(err)
			}
			h.broker.dropGuests(in.ID, in.Link)
			h.broker.publish("shares")
			return ok(), nil
		})
}

// sharePasswordHash：空密码表示不设。
func sharePasswordHash(pw string) (string, error) {
	if pw == "" {
		return "", nil
	}
	if strings.TrimSpace(pw) == "" {
		return "", huma.Error400BadRequest("访问密码不能全是空格")
	}
	b, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	return string(b), err
}

// shareResp 返回分享的最新内容；changed 为真时通知网页（所有者的列表）和访客页重新拉取。
func (h *Hub) shareResp(ctx context.Context, id string, changed ...bool) (*shareOut, error) {
	x, err := h.store.GetShare(ctx, id)
	if err != nil {
		return nil, apiErr(err)
	}
	v, err := h.shareView(ctx, x)
	if err != nil {
		return nil, err
	}
	if len(changed) > 0 && changed[0] {
		h.broker.publish("shares")
	}
	return &shareOut{Body: v}, nil
}

func (h *Hub) linkView(ctx context.Context, l store.ShareLink) ShareLinkView {
	v := ShareLinkView{ShareLink: l, Path: shareGuestPath(l.Token)}
	if s, err := h.Settings(ctx); err == nil && s.PublicURL != "" {
		v.URL = s.PublicURL + v.Path
	}
	return v
}

func (h *Hub) shareView(ctx context.Context, x store.Share) (ShareView, error) {
	v := ShareView{Share: x, Links: []ShareLinkView{}}
	for _, l := range x.Links {
		v.Links = append(v.Links, h.linkView(ctx, l))
		v.Visits += l.Visits
		if l.LastVisitAt != nil && (v.LastVisitAt == nil || l.LastVisitAt.After(*v.LastVisitAt)) {
			v.LastVisitAt = l.LastVisitAt
		}
	}
	sc, err := h.store.ResolveShare(ctx, x.Items)
	if err != nil {
		return v, err
	}
	if v.Scope, err = h.scopeView(ctx, sc); err != nil {
		return v, err
	}
	v.HasSessions = v.Scope.Sess > 0
	return v, nil
}

// scopeView 把分享范围排成 计划 → 阶段 → 需求 → 会话 的节点列表。
func (h *Hub) scopeView(ctx context.Context, sc store.ShareScope) (ShareScopeView, error) {
	v := ShareScopeView{Nodes: []ShareNode{}, Plans: len(sc.Plans), Stages: len(sc.Stages), Reqs: len(sc.Reqs), Sess: len(sc.Sessions)}
	// 计划与阶段
	plans, err := h.store.ListPlans(ctx, "")
	if err != nil {
		return v, err
	}
	stagePlan := map[string]string{} // 阶段 ID → 计划 ID
	reqStage := map[string]string{}  // 需求 ID → 阶段 ID
	sort.SliceStable(plans, func(i, j int) bool { return plans[i].CreatedAt.Before(plans[j].CreatedAt) })
	var stageNodes []ShareNode
	for _, p := range plans {
		if sc.Plans[p.ID] {
			v.Nodes = append(v.Nodes, ShareNode{Kind: store.ShareKindPlan, Ref: p.ID, Code: p.Code, Title: p.Title, Status: p.Status, Parents: []string{}})
		}
		for _, st := range p.Stages {
			stagePlan[st.ID] = p.ID
			for _, r := range st.Requirements {
				reqStage[r.ID] = st.ID
			}
			if sc.Stages[st.ID] {
				n := ShareNode{Kind: store.ShareKindStage, Ref: st.ID, Code: p.Code, Seq: st.Seq, Title: st.Title, Status: st.Status, Parents: []string{}}
				if sc.Plans[p.ID] {
					n.Parents = []string{p.ID}
				}
				stageNodes = append(stageNodes, n)
			}
		}
	}
	v.Nodes = append(v.Nodes, stageNodes...)
	// 需求：在计划里的按计划顺序，其余按编号
	reqs, err := h.store.ListRequirements(ctx, "", nil)
	if err != nil {
		return v, err
	}
	sort.SliceStable(reqs, func(i, j int) bool { return reqSeq(reqs[i].Code) < reqSeq(reqs[j].Code) })
	var reqIDs []string
	for _, r := range reqs {
		if !sc.Reqs[r.ID] {
			continue
		}
		reqIDs = append(reqIDs, r.ID)
		n := ShareNode{Kind: store.ShareKindReq, Ref: r.ID, Code: r.Code, Title: r.Title, Status: r.Status, Parents: []string{}}
		if sid := reqStage[r.ID]; sc.Stages[sid] {
			n.Parents = []string{sid}
		}
		v.Nodes = append(v.Nodes, n)
	}
	// 会话：上级是它关联的、在分享里的需求
	links, err := h.store.ReqSessionLinks(ctx, reqIDs)
	if err != nil {
		return v, err
	}
	keys := make([]string, 0, len(sc.Sessions))
	for k := range sc.Sessions {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		x, err := h.store.GetSession(ctx, k)
		if err != nil {
			continue
		}
		n := ShareNode{Kind: store.ShareKindSession, Ref: k, Title: x.Title, Status: x.State, Role: x.Role, Parents: []string{}}
		for _, rid := range links[k] {
			if sc.Reqs[rid] {
				n.Parents = append(n.Parents, rid)
			}
		}
		v.Nodes = append(v.Nodes, n)
	}
	return v, nil
}

func reqSeq(code string) int {
	n := 0
	for _, c := range strings.TrimPrefix(code, "R-") {
		if c < '0' || c > '9' {
			return n
		}
		n = n*10 + int(c-'0')
	}
	return n
}
