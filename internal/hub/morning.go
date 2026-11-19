package hub

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NekroAI/kitestring/internal/roles"
	"github.com/NekroAI/kitestring/internal/store"
)

// 离开期间小结（R-165）：你离开期间参谋做了什么、替你定了什么，工作台顶部集中呈现；你回来先看这里。
//   - 「离开期间」：用户点了「暂时离开」（R-210）时就从那一刻算起（离开中，或结束不到 12 小时）；从没点过的老数据才按下面推算：
//     项目最近一个参谋授权窗口的开启时间到现在（窗口生效中，或结束不到 12 小时）；
//     没有这样的窗口时，以参谋写总述的时间往前 12 小时；两者都没有就不出小结。
//   - 参谋只写两样（away_set / ks away，旧名 morning_set / ks morning 仍可用；只有主助理能写）：一段总述，和逐条验收建议（写进对应需求）。
//   - 其余由 hub 汇总：① 完成了什么（期间交了证据、现在待验收 / 已完成的需求，附提交）、团队消息；
//     ② 替你定了什么（期间的 主助理代答、主调度 / 主助理 放行）；③④ 只给计数——未答决策与待验收本来就在「待处理」里，不重复列。
//   - 「待处理」里的卡片挂两种标注：参谋的验收建议（advice）、参谋没代答的原因（holds）。

const (
	morningTTL     = 12 * time.Hour // 窗口结束 / 总述写下超过这么久，小结不再出现
	morningTextMax = 2000           // 防滥用的硬上限；建议长度 200 字，超出照常保存并提示压缩（R-212）
	morningTells   = 8
)

type morningDelivery struct {
	Kind string `json:"kind"`
	Text string `json:"text" doc:"证据的结论（没有时取摘要开头）"`
	Link string `json:"link,omitempty"`
}

type morningDone struct {
	RequirementCode  string            `json:"requirement_code"`
	RequirementTitle string            `json:"requirement_title"`
	Status           string            `json:"status"`
	Deliveries       []morningDelivery `json:"deliveries"`
	At               time.Time         `json:"at" doc:"最近一次交证据的时间"`
}

type morningRelease struct {
	Code             string    `json:"code" doc:"放行记录编号（D-x）"`
	RequirementCode  string    `json:"requirement_code"`
	RequirementTitle string    `json:"requirement_title"`
	By               string    `json:"by"`
	Role             string    `json:"role" doc:"放行时的身份：pm / aide（R-190 前的旧记录可能是 advisor）"`
	Reason           string    `json:"reason"`
	Withdrawn        bool      `json:"withdrawn" doc:"已被你撤回"`
	At               time.Time `json:"at"`
}

type morningTell struct {
	Code            string    `json:"code"`
	To              string    `json:"to" doc:"收消息的会话"`
	Text            string    `json:"text"`
	RequirementCode string    `json:"requirement_code,omitempty"`
	At              time.Time `json:"at"`
}

// MorningReport 是一个项目的小结。
type MorningReport struct {
	ProjectID   string             `json:"project_id"`
	ProjectName string             `json:"project_name"`
	Since       time.Time          `json:"since" doc:"离开期间从何时算起"`
	Grant       *store.Grant       `json:"grant,omitempty" doc:"据以算离开期间的授权窗口"`
	Note        *store.MorningNote `json:"note,omitempty" doc:"参谋的总述（写在离开期间之前的不算）"`
	Done        []morningDone      `json:"done" doc:"① 完成了什么：期间交了证据、现在待验收 / 已完成的需求"`
	Tells       []morningTell      `json:"tells" doc:"期间的团队消息（新的在前，最多 8 条）"`
	TellCount   int                `json:"tell_count"`
	Proxied     []store.Decision   `json:"proxied" doc:"② 参谋代答的决策（新的在前）"`
	Releases    []morningRelease   `json:"releases" doc:"② 主调度 / 主助理 放行的草稿（新的在前）"`
	Pending     int                `json:"pending" doc:"③ 留给你的：还没答的决策条数（在「待处理」里）"`
	Blocked     []string           `json:"blocked" doc:"③ 受阻的需求编号"`
	Review      int                `json:"review" doc:"④ 待验收需求条数（在「待处理」里）"`
}

// morningHold 是参谋没代答一条决策的原因，挂在「待处理」的决策卡上。
type morningHold struct {
	DecisionCode string `json:"decision_code"`
	Kind         string `json:"kind" enum:"hard,scope,ended" doc:"hard = 硬规则；scope = 超出授权的风险档；ended = 授权窗口已结束"`
	Reason       string `json:"reason"`
}

type morningOut struct {
	Body struct {
		Reports []MorningReport `json:"reports"`
		Advice  []store.Advice  `json:"advice" doc:"待验收需求上的参谋建议"`
		Holds   []morningHold   `json:"holds" doc:"参谋没代答的决策及原因"`
	}
}

// grantEnd 是窗口实际结束的时间：撤销时间或到期时间。
func grantEnd(g store.Grant) time.Time {
	if g.RevokedAt != nil && g.RevokedAt.Before(g.ExpiresAt) {
		return *g.RevokedAt
	}
	return g.ExpiresAt
}

// morningWindow 算项目的离开期间：返回起点、据以计算的窗口；ok = false 表示不出小结。
func (h *Hub) morningWindow(ctx context.Context, mc *morningCache, projectID string, note *store.MorningNote, at time.Time) (time.Time, *store.Grant, bool) {
	// 暂时离开（R-210）：起点就是点「离开」的时刻（离开中，或结束不到 12 小时）；从没点过离开的老数据沿用下面的推算
	if a, err := mc.latestAway(ctx); err == nil && (a.Active() || at.Sub(*a.EndedAt) < morningTTL) {
		mc.shared = &a.StartedAt // 各项目同一个起点：期间的记录一次查回来
		return a.StartedAt, mc.awayGrant(ctx, a, projectID), true
	}
	if g, ok := mc.latestGrant(ctx, projectID); ok {
		if g.Status == store.GrantActive || at.Sub(grantEnd(g)) < morningTTL {
			return g.CreatedAt, &g, true
		}
	}
	if note != nil && at.Sub(note.At) < morningTTL {
		return note.At.Add(-morningTTL), nil, true
	}
	return time.Time{}, nil, false
}

// morningCache 是一次小结请求里各项目共用的数据：第一次用到时一次查回来，各项目从这里取
// （R-382：原来每个项目各查一遍总述、最近的离开、最近的窗口和需求列表，二十几个项目一次请求查库近百次）。
// 只在一次请求里用，不跨请求缓存。查询出错时与原来逐个查出错一样处理：当作没有。
type morningCache struct {
	h          *Hub
	notes      map[string]store.MorningNote
	away       *store.Away
	awayErr    error
	awayGrants map[string][]store.Grant // 离开编号 → 这次离开开的窗口
	latest     map[string]store.Grant   // 项目 → 最近一个窗口
	active     map[string]store.Grant   // 项目 → 生效中的窗口
	reqs       map[string][]store.Requirement
	plans      map[string][]store.Plan
	pending    map[string][]store.Decision // 项目 → 待答复的决策
	// shared：各项目共用的起点（离开期间小结都从点「离开」那一刻算起）；这个起点之后的记录一次查回来放进 period
	shared *time.Time
	period *periodBatch
}

// periodBatch 是 shared 起点之后全部项目的记录（项目 → 列表），periodReport 从这里取。
type periodBatch struct {
	evidence map[string][]store.Delivery
	tells    map[string][]store.Decision
	releases map[string][]store.Decision
	proxied  map[string][]store.Decision
}

func (h *Hub) newMorningCache() *morningCache {
	return &morningCache{h: h, awayGrants: map[string][]store.Grant{}}
}

// note：项目的小结总述，没有时为 nil（同 GetMorning）。
func (mc *morningCache) note(ctx context.Context, projectID string) *store.MorningNote {
	if mc.notes == nil {
		ns, err := mc.h.store.MorningNotes(ctx)
		if err != nil {
			ns = map[string]store.MorningNote{}
		}
		mc.notes = ns
	}
	if n, ok := mc.notes[projectID]; ok {
		return &n
	}
	return nil
}

// latestAway：最近一次离开（同 LatestAway）。
func (mc *morningCache) latestAway(ctx context.Context) (store.Away, error) {
	if mc.away == nil {
		a, err := mc.h.store.LatestAway(ctx)
		mc.away, mc.awayErr = &a, err
	}
	return *mc.away, mc.awayErr
}

// latestGrant：项目最近一个窗口（同 ListGrants(projectID, 1) 的第一条）。
func (mc *morningCache) latestGrant(ctx context.Context, projectID string) (store.Grant, bool) {
	if mc.latest == nil {
		gs, err := mc.h.store.LatestGrants(ctx)
		if err != nil {
			gs = map[string]store.Grant{}
		}
		mc.latest = gs
	}
	g, ok := mc.latest[projectID]
	return g, ok
}

// awayGrant 返回这次离开给项目开的授权窗口（没有时取项目当前生效的窗口，按这次离开的范围收窄；scope = none 时没有）。
func (mc *morningCache) awayGrant(ctx context.Context, a store.Away, projectID string) *store.Grant {
	gs, ok := mc.awayGrants[a.ID]
	if !ok {
		var err error
		if gs, err = mc.h.store.AwayGrants(ctx, a.ID); err != nil {
			gs = nil
		}
		mc.awayGrants[a.ID] = gs
	}
	for i := len(gs) - 1; i >= 0; i-- {
		if gs[i].ProjectID == projectID {
			g := gs[i]
			return &g
		}
	}
	if mc.active == nil {
		as, err := mc.h.store.ActiveGrants(ctx)
		if err != nil {
			as = map[string]store.Grant{}
		}
		mc.active = as
	}
	if g, ok := mc.active[projectID]; ok {
		if g, ok := clampGrantByAway(g, a.Scope); ok {
			return &g
		}
	}
	return nil
}

// pendingOf：项目里待答复的决策（同 PendingDecisions，全部项目一次取回后按项目分组）。
func (mc *morningCache) pendingOf(ctx context.Context, projectID string) ([]store.Decision, error) {
	if mc.pending == nil {
		m, err := mc.h.store.PendingDecisionsAll(ctx)
		if err != nil {
			return nil, err
		}
		mc.pending = m
	}
	return orEmpty(mc.pending[projectID]), nil
}

// plansOf：项目的计划（同 ListPlans(projectID)，全部计划一次取回后按项目分组）。
func (mc *morningCache) plansOf(ctx context.Context, projectID string) ([]store.Plan, error) {
	if mc.plans == nil {
		all, err := mc.h.store.ListPlans(ctx, "")
		if err != nil {
			return nil, err
		}
		mc.plans = map[string][]store.Plan{}
		for _, x := range all {
			mc.plans[x.ProjectID] = append(mc.plans[x.ProjectID], x)
		}
	}
	return orEmpty(mc.plans[projectID]), nil
}

// periodOf：since 是各项目共用的起点时，返回这个起点之后全部项目的记录（第一次用到时查）；否则为 nil，由调用方逐个项目查。
func (mc *morningCache) periodOf(ctx context.Context, since time.Time) (*periodBatch, error) {
	if mc.shared == nil || !since.Equal(*mc.shared) {
		return nil, nil
	}
	if mc.period != nil {
		return mc.period, nil
	}
	st := mc.h.store
	b := &periodBatch{}
	var err error
	if b.evidence, err = st.EvidenceSinceAll(ctx, since); err != nil {
		return nil, err
	}
	if b.tells, err = st.ProjectDecisionsSinceAll(ctx, store.KindTell, since, 200); err != nil {
		return nil, err
	}
	if b.releases, err = st.ProjectDecisionsSinceAll(ctx, store.KindRelease, since, 100); err != nil {
		return nil, err
	}
	if b.proxied, err = st.ProxiedSinceAll(ctx, since); err != nil {
		return nil, err
	}
	mc.period = b
	return b, nil
}

func orEmpty[T any](v []T) []T {
	if v == nil {
		return []T{}
	}
	return v
}

// reqsOf：项目的需求（同 ListRequirements(projectID, nil)，全部需求一次取回后按项目分组）。
func (mc *morningCache) reqsOf(ctx context.Context, projectID string) ([]store.Requirement, error) {
	if mc.reqs == nil {
		all, err := mc.h.store.ListRequirements(ctx, "", nil)
		if err != nil {
			return nil, err
		}
		mc.reqs = map[string][]store.Requirement{}
		for _, x := range all {
			mc.reqs[x.ProjectID] = append(mc.reqs[x.ProjectID], x)
		}
	}
	return mc.reqs[projectID], nil
}

// holdReason 说明参谋为什么没代答 d（窗口 g 生效或刚结束时）；空表示参谋还可以代答。
func holdReason(d store.Decision, g store.Grant, at time.Time) (string, string) {
	if why := proxyHard(d, g.SessionKey); why != "" {
		return "hard", holdHard(why)
	}
	if store.RiskRank(store.NormRisk(d.Risk)) > store.RiskRank(g.MaxRisk) {
		return "scope", holdScope(riskZh[store.NormRisk(d.Risk)], d.Risk == "", riskZh[g.MaxRisk])
	}
	if !at.Before(grantEnd(g)) {
		return "ended", holdEnded(g.Code)
	}
	return "", ""
}

func (h *Hub) morningReport(ctx context.Context, mc *morningCache, p store.Project, at time.Time, out *morningOut) error {
	note := mc.note(ctx, p.ID)
	since, g, ok := h.morningWindow(ctx, mc, p.ID, note, at)
	if g != nil {
		pending, err := mc.pendingOf(ctx, p.ID)
		if err != nil {
			return err
		}
		for _, d := range pending {
			if d.CreatedAt.After(grantEnd(*g)) {
				continue // 窗口结束后才提的：和参谋无关
			}
			if kind, why := holdReason(d, *g, at); why != "" {
				out.Body.Holds = append(out.Body.Holds, morningHold{DecisionCode: d.Code, Kind: kind, Reason: why})
			}
		}
	}
	if !ok {
		return nil
	}
	r, err := h.periodReport(ctx, mc, p, since, g, note)
	if err != nil {
		return err
	}
	out.Body.Reports = append(out.Body.Reports, r)
	return nil
}

// periodReport 汇总项目 since 之后的一段时间：完成了什么、团队消息、替你定了什么、留给你的计数。
func (h *Hub) periodReport(ctx context.Context, mc *morningCache, p store.Project, since time.Time, g *store.Grant, note *store.MorningNote) (MorningReport, error) {
	if note != nil && note.At.Before(since) {
		note = nil // 上一次离开时写的总述
	}
	r := MorningReport{ProjectID: p.ID, ProjectName: p.Name, Since: since, Grant: g, Note: note, Done: []morningDone{}, Tells: []morningTell{}, Releases: []morningRelease{}, Blocked: []string{}}

	reqs, err := mc.reqsOf(ctx, p.ID)
	if err != nil {
		return r, err
	}
	byID := map[string]store.Requirement{}
	for _, x := range reqs {
		byID[x.ID] = x
		switch x.Status {
		case store.ReqReview:
			r.Review++
		case store.ReqBlocked:
			r.Blocked = append(r.Blocked, x.Code)
		}
	}
	sort.Strings(r.Blocked)

	// ① 完成了什么
	pb, err := mc.periodOf(ctx, since)
	if err != nil {
		return r, err
	}
	var evs []store.Delivery
	if pb != nil {
		evs = pb.evidence[p.ID]
	} else if evs, err = h.store.EvidenceSince(ctx, p.ID, since); err != nil {
		return r, err
	}
	idx := map[string]int{}
	for _, e := range evs {
		x, ok := byID[e.RequirementID]
		if !ok || (x.Status != store.ReqReview && x.Status != store.ReqDone) {
			continue
		}
		i, seen := idx[x.ID]
		if !seen {
			i = len(r.Done)
			idx[x.ID] = i
			r.Done = append(r.Done, morningDone{RequirementCode: x.Code, RequirementTitle: x.Title, Status: x.Status, Deliveries: []morningDelivery{}})
		}
		text := e.Verdict
		if text == "" {
			text = brief(e.Summary, 60)
		}
		r.Done[i].Deliveries = append(r.Done[i].Deliveries, morningDelivery{Kind: e.Kind, Text: text, Link: e.Link})
		r.Done[i].At = e.At
	}
	sort.SliceStable(r.Done, func(a, b int) bool { return r.Done[a].At.After(r.Done[b].At) })

	var tells []store.Decision
	if pb != nil {
		tells = pb.tells[p.ID]
	} else if tells, err = h.store.ProjectDecisionsSince(ctx, p.ID, store.KindTell, since, 200); err != nil {
		return r, err
	}
	r.TellCount = len(tells)
	for i, d := range tells {
		if i == morningTells {
			break
		}
		r.Tells = append(r.Tells, morningTell{Code: d.Code, To: d.SessionKey, Text: d.Answer, RequirementCode: d.RequirementCode, At: d.CreatedAt})
	}

	// ② 替你定了什么
	var rels []store.Decision
	if pb != nil {
		r.Proxied, rels = orEmpty(pb.proxied[p.ID]), pb.releases[p.ID]
	} else {
		if r.Proxied, err = h.store.ProxiedSince(ctx, p.ID, since); err != nil {
			return r, err
		}
		if rels, err = h.store.ProjectDecisionsSince(ctx, p.ID, store.KindRelease, since, 100); err != nil {
			return r, err
		}
	}
	for _, d := range rels {
		r.Releases = append(r.Releases, morningRelease{Code: d.Code, RequirementCode: d.RequirementCode, RequirementTitle: d.RequirementTitle, By: d.AssignedBy, Role: d.Context, Reason: d.Answer, Withdrawn: d.CloseReason != "", At: d.CreatedAt})
	}

	// ③④ 只计数
	pending, err := mc.pendingOf(ctx, p.ID)
	if err != nil {
		return r, err
	}
	r.Pending = len(pending)
	return r, nil
}

type adviceIn struct {
	Requirement string `json:"requirement" minLength:"1" doc:"待验收的需求编号（如 R-12）"`
	Verdict     string `json:"verdict" enum:"pass,return,clear" doc:"pass = 建议通过；return = 建议退回；clear = 撤掉建议"`
	Reason      string `json:"reason,omitempty" doc:"理由（pass / return 必填）：对照验收标准，一两句"`
}

type morningSetOut struct {
	Body struct {
		Message string `json:"message"`
	}
}

func (h *Hub) registerMorningWeb(api huma.API) {
	huma.Register(api, huma.Operation{OperationID: "get-morning", Method: http.MethodGet, Path: "/api/morning", Tags: []string{"morning"}, Summary: "离开期间小结：参谋做了什么、替你定了什么（R-165）"},
		func(ctx context.Context, in *struct {
			ProjectID string `query:"project_id" doc:"只看这个项目；不填为全部（隐藏的项目除外）"`
		}) (*morningOut, error) {
			out := &morningOut{}
			out.Body.Reports, out.Body.Holds = []MorningReport{}, []morningHold{}
			ps, err := h.store.ListProjects(ctx)
			if err != nil {
				return nil, err
			}
			at := time.Now()
			mc := h.newMorningCache()
			for _, p := range ps {
				if (in.ProjectID != "" && p.ID != in.ProjectID) || (in.ProjectID == "" && p.Hidden) {
					continue
				}
				if err := h.morningReport(ctx, mc, p, at, out); err != nil {
					return nil, err
				}
			}
			if out.Body.Advice, err = h.store.ReviewAdvice(ctx, in.ProjectID); err != nil {
				return nil, err
			}
			return out, nil
		})
}

func (h *Hub) registerAgentMorning(api huma.API) {
	huma.Register(api, huma.Operation{OperationID: "agent-set-morning", Method: http.MethodPost, Path: "/api/agent/morning", Tags: []string{"agent"}, Summary: "主助理写离开期间小结：总述与逐条验收建议（R-165、R-190）"},
		func(ctx context.Context, in *struct {
			Body struct {
				SessionKey string     `json:"session_key" minLength:"1" doc:"调用者（必须是本项目的主助理）"`
				Text       *string    `json:"text,omitempty" doc:"总述：一段话，先结论，建议 200 字以内，超出照常保存并提示压缩；不传表示不改"`
				Headline   *string    `json:"headline,omitempty" doc:"一句话结论（建议 40 字以内，R-212）：常驻条上只显示它；不传表示不改，空字符串清除"`
				Look       []string   `json:"look,omitempty" doc:"要用户先看的几条（建议 3 条以内，每条一句，写编号）；不传表示不改，传 [\"\"] 清空"`
				Advice     []adviceIn `json:"advice,omitempty" doc:"逐条验收建议，每条覆盖该需求上一份"`
			}
		}) (*morningSetOut, error) {
			me, err := h.store.GetSession(ctx, in.Body.SessionKey)
			if err != nil {
				return nil, apiErr(err)
			}
			if me.Role != roles.Aide || me.ProjectID == "" {
				return nil, huma.Error403Forbidden(notAide("写离开期间小结和验收建议", me.Role))
			}
			if in.Body.Text == nil && in.Body.Headline == nil && in.Body.Look == nil && len(in.Body.Advice) == 0 {
				return nil, huma.Error400BadRequest("什么都没写：传 headline（一句话结论）、text（总述）或 advice（验收建议）")
			}
			// 先全部检查，再一起写入：一条不合格就整次拒绝
			type item struct {
				r store.Requirement
				a adviceIn
			}
			var items []item
			for _, a := range in.Body.Advice {
				r, err := h.store.GetRequirement(ctx, a.Requirement)
				if err != nil {
					return nil, huma.Error400BadRequest("找不到需求 " + a.Requirement)
				}
				if r.ProjectID != me.ProjectID {
					return nil, huma.Error403Forbidden(r.Code + " 不在你所在的项目里")
				}
				a.Reason = strings.TrimSpace(a.Reason)
				if a.Verdict != "clear" {
					if r.Status != store.ReqReview {
						return nil, huma.Error400BadRequest(r.Code + " 不是待验收，只能给待验收的需求写验收建议")
					}
					if a.Reason == "" {
						return nil, huma.Error400BadRequest(r.Code + " 的验收建议要写理由：对照验收标准说明为什么建议通过 / 退回")
					}
				}
				items = append(items, item{r, a})
			}
			var done []string
			var hint string
			if in.Body.Text != nil || in.Body.Headline != nil || in.Body.Look != nil {
				// 没传的字段保留上一份（R-212）
				cur, _ := h.store.GetMorning(ctx, me.ProjectID)
				text, headline, look := cur.Text, cur.Headline, cur.Look
				if in.Body.Text != nil {
					text = strings.TrimSpace(*in.Body.Text)
					if utf8.RuneCountInString(text) > morningTextMax {
						text = string([]rune(text)[:morningTextMax])
					}
				}
				if in.Body.Headline != nil {
					headline = strings.TrimSpace(*in.Body.Headline)
				}
				if in.Body.Look != nil {
					look = cleanLook(in.Body.Look)
				}
				if err := h.store.SetMorning(ctx, me.ProjectID, text, me.Key, headline, look); err != nil {
					return nil, err
				}
				hint = shapeHints(headline, look, text, morningTextAdv)
				if text == "" && headline == "" && len(look) == 0 {
					done = append(done, "已清除总述")
				} else {
					done = append(done, "总述已更新")
				}
			}
			for _, x := range items {
				v := x.a.Verdict
				if v == "clear" {
					v = ""
				}
				if err := h.store.SetAdvice(ctx, x.r.ID, v, x.a.Reason, me.Key); err != nil {
					return nil, err
				}
				done = append(done, map[string]string{"pass": x.r.Code + " 建议通过", "return": x.r.Code + " 建议退回", "clear": x.r.Code + " 已撤掉建议"}[x.a.Verdict])
			}
			h.broker.publish("morning")
			out := &morningSetOut{}
			out.Body.Message = strings.Join(done, "；") + "。用户在工作台顶部的离开期间小结、「待处理」的验收卡上能看到。" + hint
			return out, nil
		})
}
