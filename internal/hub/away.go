package hub

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NekroAI/kitestring/internal/notify"
	"github.com/NekroAI/kitestring/internal/roles"
	"github.com/NekroAI/kitestring/internal/store"
)

// 暂时离开（R-210，D-500 方案 B）：
//   - 你点「暂时离开」开始一次离开（起点 = 这一刻），选预估多久（30 分钟 / 2 小时 / 8 小时 / 不确定）、主助理能代答到哪一档
//     （不代答 / 低风险 / 中风险），可选「推送类也先攒着」。同时给各项目在岗的主助理开授权窗口（最长 24 小时，到期自动失效，离开保持）。
//   - 离开只由你点「结束离开」结束；期间任何设备上的审批 / 回答照常生效，记进报告（带设备），不结束离开。超过预估只标出来，不推送。
//   - 推送分三档（只在离开期间生效，不在离开时行为不变）：
//     推送：硬规则类（批准方案 / 计划、设计选型、标了 hard 的）、拿不准的（主助理不能代答的其余决策、终端等授权、设备失联、主助理上报的）；
//     勿扰时硬规则类先攒着，拿不准的仍推。
//     合并进报告：待验收、阶段验收。不推：主助理可以代答的决策、会话等回复 / 空闲。
//   - 能处理的都处理完（没有运行中的会话、没有排队的需求、待验收都有主助理建议）持续 10 分钟：生成阶段性报告，第一次时推 1 条，离开继续。

const (
	awayCalmFor   = 10 * time.Minute
	awayTickEvery = time.Minute
)

// awayNow 是离开相关判断用的时钟（测试里可替换）。
var awayNow = time.Now

// pushClass 是推送分级。
type pushClass int

const (
	pushDefault pushClass = iota // 按事件类型取
	pushHard                     // 硬规则：主助理处理不了，推（勿扰时先攒着）
	pushUnsure                   // 拿不准：一律推
	pushMerge                    // 合并进报告
	pushSkip                     // 不推
)

// pushMeta 是发通知时附带的分级信息。
type pushMeta struct {
	Class  pushClass
	Ref    string // 关联编号
	Reason string // 为什么归到这一档（报告里显示）
	Quiet  bool   // 不在离开时本来也不推（授权范围内、主助理可代答的决策）
}

// eventClass 是各事件默认的分级与原因。
func eventClass(event string) (pushClass, string) {
	switch event {
	case notify.EventDecisionRaised:
		return pushHard, "要你拍板"
	case notify.EventRequirementReview:
		return pushMerge, "待验收"
	case notify.EventWaitingReply:
		return pushSkip, "会话在等回复"
	case notify.EventWaitingApproval:
		return pushUnsure, "终端里在等你授权"
	case notify.EventDeviceLost:
		return pushUnsure, "设备失联"
	}
	return pushUnsure, ""
}

// refFromPath 从通知要打开的页面里取关联编号（需求 / 计划）。
func refFromPath(path string) string {
	u, err := url.Parse(path)
	if err != nil {
		return ""
	}
	q := u.Query()
	for _, k := range []string{"r", "plan", "d"} {
		if v := q.Get(k); v != "" {
			return v
		}
	}
	return ""
}

// awayGate：不在离开时按原样决定（Quiet 的不发）；离开期间按分级记进报告，返回要不要真的发。
func (h *Hub) awayGate(projectID string, m notify.Message, path string, pm pushMeta) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	a, err := h.store.ActiveAway(ctx)
	if err != nil {
		return !pm.Quiet
	}
	cls, reason := pm.Class, pm.Reason
	if cls == pushDefault {
		var r string
		cls, r = eventClass(m.Event)
		if reason == "" {
			reason = r
		}
	}
	tier := store.AwayPush
	switch cls {
	case pushHard:
		if a.HoldPush {
			tier = store.AwayHold
		}
	case pushMerge:
		tier = store.AwayMerge
	case pushSkip:
		tier = store.AwaySkip
	}
	ref := pm.Ref
	if ref == "" {
		ref = refFromPath(path)
	}
	if err := h.store.AddAwayEvent(ctx, a.ID, store.AwayEvent{Tier: tier, Event: m.Event, ProjectID: projectID, Ref: ref, Title: m.Title, Body: m.Body, Reason: reason, Normal: !pm.Quiet}); err != nil {
		h.log.Warn("记录离开期间的通知失败", "err", err)
	}
	if tier != store.AwaySkip {
		_ = h.store.MarkAwayWorked(ctx, a.ID)
	}
	h.broker.publish("away")
	return tier == store.AwayPush
}

// decisionPush 给一条刚提出的决策定推送分级。
func (h *Hub) decisionPush(ctx context.Context, d store.Decision) pushMeta {
	if h.proxyQuiet(ctx, d) {
		return pushMeta{Class: pushSkip, Ref: d.Code, Reason: "在授权范围内，主助理可以代答", Quiet: true}
	}
	if why := hardOf(d); why != "" {
		return pushMeta{Class: pushHard, Ref: d.Code, Reason: why}
	}
	if w := unsureOf(d); w != "" {
		return pushMeta{Class: pushUnsure, Ref: d.Code, Reason: "提到「" + w + "」，主助理拿不准"}
	}
	why := "主助理没有代答授权"
	if d.ProjectID != "" {
		if g, err := h.store.ActiveGrant(ctx, d.ProjectID); err == nil {
			if _, r := holdReason(d, g, time.Now()); r != "" {
				why = r
			}
		}
	}
	return pushMeta{Class: pushUnsure, Ref: d.Code, Reason: why}
}

// awayUser：离开期间你在某台设备上处理了一件事（答复、验收、批准计划……），记进报告；不结束离开。
func (h *Hub) awayUser(ctx context.Context, event, projectID, ref, title, body string) {
	a, err := h.store.ActiveAway(ctx)
	if err != nil {
		return
	}
	if err := h.store.AddAwayEvent(ctx, a.ID, store.AwayEvent{Tier: store.AwayUser, Event: event, ProjectID: projectID, Ref: ref, Title: title, Body: body, Device: clientFrom(ctx).Device}); err != nil {
		h.log.Warn("记录离开期间的处理失败", "err", err)
		return
	}
	_ = h.store.MarkAwayWorked(ctx, a.ID)
	h.broker.publish("away")
}

// deviceLabel 从网页请求认出设备类别；前端可用 X-Kitestring-Device 直接给名字。
func deviceLabel(r *http.Request) string {
	if v := strings.TrimSpace(r.Header.Get("X-Kitestring-Device")); v != "" {
		return brief(v, 30)
	}
	ua := r.Header.Get("User-Agent")
	switch {
	case ua == "":
		return ""
	case strings.Contains(ua, "iPhone"):
		return "iPhone"
	case strings.Contains(ua, "iPad"):
		return "iPad"
	case strings.Contains(ua, "Android") && strings.Contains(ua, "Mobile"):
		return "安卓手机"
	case strings.Contains(ua, "Android"):
		return "安卓平板"
	case strings.Contains(ua, "Macintosh"):
		return "Mac"
	case strings.Contains(ua, "Windows"):
		return "Windows 电脑"
	case strings.Contains(ua, "Linux"):
		return "Linux 电脑"
	}
	return ""
}

// awayGrant 返回这次离开给项目开的授权窗口（没有时取项目当前生效的窗口）。
func (h *Hub) awayGrant(ctx context.Context, a store.Away, projectID string) *store.Grant {
	gs, err := h.store.AwayGrants(ctx, a.ID)
	if err == nil {
		for i := len(gs) - 1; i >= 0; i-- {
			if gs[i].ProjectID == projectID {
				return &gs[i]
			}
		}
	}
	if g, err := h.store.ActiveGrant(ctx, projectID); err == nil {
		return &g
	}
	return nil
}

var expectZh = map[int]string{30: "30 分钟", 120: "2 小时", 480: "8 小时", 0: "不确定"}

// openAwayGrants：开始离开时给各项目在岗的主助理开授权窗口（scope = none 时只告诉它这次不代答）。
func (h *Hub) openAwayGrants(ctx context.Context, a store.Away) []string {
	ps, err := h.store.ListProjects(ctx)
	if err != nil {
		return nil
	}
	var notices []string
	expect := expectZh[a.ExpectMinutes]
	for _, p := range ps {
		if p.Hidden {
			continue
		}
		key := h.liveAide(ctx, p.ID)
		if key == "" {
			continue
		}
		if a.Scope == store.AwayScopeNone {
			if _, err := h.tell(ctx, key, p.ID, "", fmt.Sprintf("用户暂时离开了（%s，预估%s）。这次不代答：决策都留给用户；待验收的需求请照常写验收建议（away_set / ks away advise），拿不准、要用户尽快看的决策用 ks away escalate 推给用户。", a.Code, expect)); err != nil {
				h.log.Warn("通知主助理离开开始失败", "err", err)
			}
			continue
		}
		if g, err := h.store.ActiveGrant(ctx, p.ID); err == nil {
			notices = append(notices, fmt.Sprintf("%s 已有生效中的授权窗口 %s（至 %s），沿用", p.Name, g.Code, g.ExpiresAt.Local().Format("01-02 15:04")))
			continue
		}
		intro := fmt.Sprintf("用户暂时离开了（%s，预估%s）。", a.Code, expect)
		if _, err := h.openGrant(ctx, p.ID, key, a.Scope, a.StartedAt.Add(grantMaxSpan), a.ID, intro); err != nil {
			h.log.Warn("离开时开授权窗口失败", "err", err, "project", p.Name)
		}
	}
	return notices
}

// openGrant 开一个授权窗口并告诉被授权的主助理和订阅了 settings 的会话。awayID 非空表示是这次离开开的。
func (h *Hub) openGrant(ctx context.Context, projectID, key, maxRisk string, expires time.Time, awayID, intro string) (store.Grant, error) {
	g, err := h.store.CreateGrant(ctx, projectID, key, maxRisk, expires)
	if err != nil {
		return g, err
	}
	if awayID != "" {
		if err := h.store.LinkGrantAway(ctx, g.ID, awayID); err != nil {
			return g, err
		}
	}
	h.tellGrant(ctx, g, intro+fmt.Sprintf("用户开了参谋授权窗口 %s：到 %s 前，你可以代用户答复本项目%s以内的决策（decision_answer / ks decision answer，必须写理由、你自己的风险判断和硬规则核对 hard_check，风险档取提问方与你判断中更高的一档）。硬规则永不代答：批准方案 / 计划、设计选型、产品方向、语义 / 接口 / 权限变更、门禁 / 评测、不可逆或对外操作、验收。拿不准的留给用户，要用户尽快看的用 ks away escalate 推给用户。",
		g.Code, g.ExpiresAt.Local().Format("01-02 15:04"), riskZh[g.MaxRisk]))
	h.scheduleGrantExpiry(g)
	h.onSettingsChanged(ctx, g.ProjectID, fmt.Sprintf("用户开了参谋授权窗口 %s：授权给 %s，到 %s 前可代答%s以内的决策。", g.Code, h.actorName(ctx, g.SessionKey), g.ExpiresAt.Local().Format("01-02 15:04"), riskZh[g.MaxRisk]), g.SessionKey) // 被授权的主助理已由 tellGrant 告诉（R-171）
	return g, nil
}

// closeAwayGrants：结束离开时撤销这次离开开的、还生效的授权窗口。
func (h *Hub) closeAwayGrants(ctx context.Context, a store.Away) {
	gs, err := h.store.AwayGrants(ctx, a.ID)
	if err != nil {
		return
	}
	for _, g := range gs {
		if g.Status != store.GrantActive {
			continue
		}
		g, err := h.store.RevokeGrant(ctx, g.ID)
		if err != nil {
			h.log.Warn("结束离开时撤销授权窗口失败", "err", err, "grant", g.Code)
			continue
		}
		h.tellGrant(ctx, g, fmt.Sprintf("用户结束了离开（%s），授权窗口 %s 随之撤销：从现在起不能再代答决策。", a.Code, g.Code))
		h.onSettingsChanged(ctx, g.ProjectID, fmt.Sprintf("用户结束了离开，授权窗口 %s（授权给 %s）随之撤销，不能再代答决策。", g.Code, h.actorName(ctx, g.SessionKey)), g.SessionKey)
	}
}

// ---------- 能处理的都处理完 ----------

// AwayTick 供测试按指定时间跑一次「都处理完」判定。
func (h *Hub) AwayTick(ctx context.Context, now time.Time) { h.awayTick(ctx, now) }

func (h *Hub) awayLoop(ctx context.Context) {
	t := time.NewTicker(awayTickEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			h.awayTick(ctx, awayNow())
		}
	}
}

func (h *Hub) awayTick(ctx context.Context, now time.Time) {
	a, err := h.store.ActiveAway(ctx)
	if err != nil {
		return
	}
	c, err := h.store.AwayCalmCounts(ctx)
	if err != nil {
		h.log.Warn("判断离开期间是否处理完失败", "err", err)
		return
	}
	if !c.Calm() {
		if a.CalmSince != nil {
			_ = h.store.SetAwayCalm(ctx, a.ID, nil)
		}
		_ = h.store.MarkAwayWorked(ctx, a.ID)
		return
	}
	if a.CalmSince == nil {
		_ = h.store.SetAwayCalm(ctx, a.ID, &now)
		return
	}
	if now.Sub(*a.CalmSince) < awayCalmFor || !a.Worked || (a.SettledAt != nil && !a.SettledAt.Before(*a.CalmSince)) {
		return
	}
	first := a.SettledAt == nil
	if err := h.store.SetAwaySettled(ctx, a.ID, now); err != nil {
		return
	}
	r, err := h.awayReport(ctx, a, now)
	if err != nil {
		h.log.Warn("生成阶段性报告失败", "err", err)
		return
	}
	text := settledText(r.Counts)
	_ = h.store.AddAwayEvent(ctx, a.ID, store.AwayEvent{At: now, Tier: store.AwaySettled, Event: "settled", Title: "能处理的都处理完了", Body: text})
	h.broker.publish("away")
	if first {
		h.sendNotify("", notify.Message{Event: notify.EventAwaySettled, Title: "离开期间能处理的都处理完了", Body: text, Group: "kitestring"}, "/", pushMeta{Class: pushUnsure, Reason: "阶段性报告"})
	}
}

func settledText(c awayCounts) string {
	parts := []string{fmt.Sprintf("交付 %d 条", c.Delivered), fmt.Sprintf("主助理替你处理 %d 件", c.Proxied+c.Released+c.Advised)}
	if c.Waiting > 0 {
		parts = append(parts, fmt.Sprintf("还在等你 %d 件", c.Waiting))
	} else {
		parts = append(parts, "没有等你的事")
	}
	return strings.Join(parts, "，") + "。"
}

// ---------- 时段报告 ----------

type awayCounts struct {
	Proxied   int `json:"proxied" doc:"主助理代答的决策"`
	Released  int `json:"released" doc:"主调度 / 主助理放行的草稿"`
	Advised   int `json:"advised" doc:"主助理写的验收建议"`
	Delivered int `json:"delivered" doc:"交付（交了证据、现在待验收 / 已完成）的需求"`
	User      int `json:"user" doc:"你在各设备上处理的"`
	Pushed    int `json:"pushed" doc:"推送了几条"`
	Held      int `json:"held" doc:"勿扰时先攒着的"`
	Merged    int `json:"merged" doc:"合并进报告、没推的"`
	Skipped   int `json:"skipped" doc:"不推的（主助理可代答、会话空闲）"`
	Normal    int `json:"normal" doc:"不在离开时原本会推几条"`
	Waiting   int `json:"waiting" doc:"还在等你的"`
}

// awayItem 是一件还在等你的事。
type awayItem struct {
	Kind        string `json:"kind" enum:"decision,plan,review,blocked" doc:"待决策 / 待批准计划 / 待验收 / 受阻"`
	Ref         string `json:"ref"`
	Title       string `json:"title"`
	ProjectName string `json:"project_name"`
	First       bool   `json:"first" doc:"要你先看"`
	Reason      string `json:"reason,omitempty" doc:"为什么要你先看，或主助理的建议"`
	Pushed      bool   `json:"pushed" doc:"离开期间推送过"`
}

// AwayReport 是一次离开的时段报告：起点是点「离开」的时刻，离开中为截至现在（含阶段性报告之后追加的），结束后为最终报告。
type AwayReport struct {
	Away         store.Away        `json:"away"`
	Until        time.Time         `json:"until" doc:"结束时刻；离开中为现在"`
	Minutes      int               `json:"minutes" doc:"离开了多久（分钟）"`
	OverEstimate bool              `json:"over_estimate" doc:"已超过预估时间（只做标记，不推送）"`
	Final        bool              `json:"final" doc:"离开已结束：这是最终报告"`
	Grants       []store.Grant     `json:"grants" doc:"这次离开开的授权窗口"`
	Counts       awayCounts        `json:"counts"`
	Projects     []MorningReport   `json:"projects" doc:"各项目：替你定了什么（proxied 代答）、放行了什么（releases）、交付了什么（done）、团队消息、主助理总述"`
	Waiting      []awayItem        `json:"waiting" doc:"还在等你的；first = 要你先看的，排在前面"`
	Yours        []store.AwayEvent `json:"yours" doc:"你在各设备上处理了什么（device 是设备）"`
	Pushes       []store.AwayEvent `json:"pushes" doc:"推送了的"`
	Held         []store.AwayEvent `json:"held" doc:"勿扰时先攒着、没推的"`
	Settled      []store.AwayEvent `json:"settled" doc:"阶段性报告（能处理的都处理完了）的时间点"`
}

func overEstimate(a store.Away, at time.Time) bool {
	return a.ExpectMinutes > 0 && at.Sub(a.StartedAt) > time.Duration(a.ExpectMinutes)*time.Minute
}

func (h *Hub) awayReport(ctx context.Context, a store.Away, now time.Time) (AwayReport, error) {
	until := now
	if a.EndedAt != nil {
		until = *a.EndedAt
	}
	r := AwayReport{Away: a, Until: until, Minutes: int(until.Sub(a.StartedAt).Minutes()), OverEstimate: overEstimate(a, until), Final: !a.Active(),
		Projects: []MorningReport{}, Waiting: []awayItem{}, Yours: []store.AwayEvent{}, Pushes: []store.AwayEvent{}, Held: []store.AwayEvent{}, Settled: []store.AwayEvent{}}
	var err error
	if r.Grants, err = h.store.AwayGrants(ctx, a.ID); err != nil {
		return r, err
	}
	evs, err := h.store.AwayEvents(ctx, a.ID)
	if err != nil {
		return r, err
	}
	pushed := map[string]bool{}
	for _, e := range evs {
		if e.Normal {
			r.Counts.Normal++
		}
		switch e.Tier {
		case store.AwayPush:
			r.Pushes = append(r.Pushes, e)
			pushed[e.Ref] = true
		case store.AwayHold:
			r.Held = append(r.Held, e)
		case store.AwayMerge:
			r.Counts.Merged++
		case store.AwaySkip:
			r.Counts.Skipped++
		case store.AwayUser:
			r.Yours = append(r.Yours, e)
		case store.AwaySettled:
			r.Settled = append(r.Settled, e)
		}
	}
	r.Counts.Pushed, r.Counts.Held, r.Counts.User = len(r.Pushes), len(r.Held), len(r.Yours)
	if _, _, r.Counts.Advised, err = h.store.AwayHandled(ctx, a.StartedAt); err != nil {
		return r, err
	}
	ps, err := h.store.ListProjects(ctx)
	if err != nil {
		return r, err
	}
	advice, err := h.store.ReviewAdvice(ctx, "")
	if err != nil {
		return r, err
	}
	adv := map[string]store.Advice{}
	for _, x := range advice {
		adv[x.RequirementCode] = x
	}
	for _, p := range ps {
		if p.Hidden {
			continue
		}
		var note *store.MorningNote
		if n, err := h.store.GetMorning(ctx, p.ID); err == nil {
			note = &n
		}
		g := h.awayGrant(ctx, a, p.ID)
		pr, err := h.periodReport(ctx, p, a.StartedAt, g, note)
		if err != nil {
			return r, err
		}
		trimPeriod(&pr, until)
		r.Counts.Proxied += len(pr.Proxied)
		r.Counts.Released += len(pr.Releases)
		r.Counts.Delivered += len(pr.Done)
		items, err := h.awayWaiting(ctx, p, g, adv, pushed)
		if err != nil {
			return r, err
		}
		r.Waiting = append(r.Waiting, items...)
		if len(pr.Done)+len(pr.Proxied)+len(pr.Releases)+pr.TellCount+len(items) > 0 || pr.Note != nil {
			r.Projects = append(r.Projects, pr)
		}
	}
	kindOrder := map[string]int{"decision": 0, "plan": 1, "blocked": 2, "review": 3}
	sort.SliceStable(r.Waiting, func(i, j int) bool {
		a, b := r.Waiting[i], r.Waiting[j]
		if a.First != b.First {
			return a.First
		}
		return kindOrder[a.Kind] < kindOrder[b.Kind]
	})
	r.Counts.Waiting = len(r.Waiting)
	return r, nil
}

// trimPeriod 去掉离开结束之后的记录（最终报告只算离开期间）。
func trimPeriod(pr *MorningReport, until time.Time) {
	done := pr.Done[:0]
	for _, d := range pr.Done {
		if !d.At.After(until) {
			done = append(done, d)
		}
	}
	pr.Done = done
	px := pr.Proxied[:0]
	for _, d := range pr.Proxied {
		if d.ProxyAt == nil || !d.ProxyAt.After(until) {
			px = append(px, d)
		}
	}
	pr.Proxied = px
	rel := pr.Releases[:0]
	for _, d := range pr.Releases {
		if !d.At.After(until) {
			rel = append(rel, d)
		}
	}
	pr.Releases = rel
}

// awayWaiting 列出项目里还在等你的事：待决策、待批准的计划、受阻、待验收。
func (h *Hub) awayWaiting(ctx context.Context, p store.Project, g *store.Grant, adv map[string]store.Advice, pushed map[string]bool) ([]awayItem, error) {
	out := []awayItem{}
	pending, err := h.store.PendingDecisions(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	for _, d := range pending {
		it := awayItem{Kind: "decision", Ref: d.Code, Title: d.Question, ProjectName: p.Name, Pushed: pushed[d.Code]}
		switch {
		case hardOf(d) != "":
			it.First, it.Reason = true, hardOf(d)
		case unsureOf(d) != "":
			it.First, it.Reason = true, "提到「"+unsureOf(d)+"」，主助理拿不准"
		case g == nil || g.Status != store.GrantActive:
			it.First, it.Reason = true, "主助理没有代答授权"
		default:
			if _, why := holdReason(d, *g, time.Now()); why != "" {
				it.First, it.Reason = true, why
			}
		}
		out = append(out, it)
	}
	plans, err := h.store.ListPlans(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	for _, x := range plans {
		if x.Status == store.PlanDraft && x.CreatedBy == "agent" {
			out = append(out, awayItem{Kind: "plan", Ref: x.Code, Title: x.Title, ProjectName: p.Name, First: true, Reason: "批准计划只由用户操作", Pushed: pushed[x.Code]})
		}
	}
	reqs, err := h.store.ListRequirements(ctx, p.ID, []string{store.ReqReview, store.ReqBlocked})
	if err != nil {
		return nil, err
	}
	for _, x := range reqs {
		if x.Status == store.ReqBlocked {
			out = append(out, awayItem{Kind: "blocked", Ref: x.Code, Title: x.Title, ProjectName: p.Name, First: true, Reason: "受阻"})
			continue
		}
		it := awayItem{Kind: "review", Ref: x.Code, Title: x.Title, ProjectName: p.Name}
		switch a, ok := adv[x.Code]; {
		case !ok:
			it.First, it.Reason = true, "还没有主助理的验收建议"
		case a.Verdict == store.AdviceReturn:
			it.First, it.Reason = true, "主助理建议退回："+a.Reason
		default:
			it.Reason = "主助理建议通过：" + a.Reason
		}
		out = append(out, it)
	}
	return out, nil
}

// ---------- 接口 ----------

// awayStatus 是状态条要的数据。
type awayStatus struct {
	Away         *store.Away   `json:"away,omitempty" doc:"进行中的离开；不在离开时为空"`
	Last         *store.Away   `json:"last,omitempty" doc:"不在离开时：最近一次已结束的离开（看它的报告用）"`
	Minutes      int           `json:"minutes" doc:"已离开多久（分钟）"`
	OverEstimate bool          `json:"over_estimate" doc:"已超过预估时间（只做标记，不推送）"`
	Handled      int           `json:"handled" doc:"主助理已处理几件（代答 + 放行 + 验收建议）"`
	Yours        int           `json:"yours" doc:"你在各设备上处理了几件"`
	Pushed       int           `json:"pushed" doc:"推送了几条"`
	Grants       []store.Grant `json:"grants" doc:"这次离开开的授权窗口（含已到期的）"`
	GrantActive  bool          `json:"grant_active" doc:"还有生效中的授权窗口；最长 24 小时，到期后离开保持、主助理不再代答"`
	Notices      []string      `json:"notices,omitempty"`
}

type awayStatusOut struct{ Body awayStatus }

type awayReportOut struct{ Body AwayReport }

func (h *Hub) awayStatusOf(ctx context.Context) (awayStatus, error) {
	st := awayStatus{Grants: []store.Grant{}}
	a, err := h.store.ActiveAway(ctx)
	if errors.Is(err, store.ErrNotFound) {
		if last, err := h.store.LatestAway(ctx); err == nil {
			st.Last = &last
		}
		return st, nil
	}
	if err != nil {
		return st, err
	}
	now := awayNow()
	st.Away, st.Minutes, st.OverEstimate = &a, int(now.Sub(a.StartedAt).Minutes()), overEstimate(a, now)
	px, rel, advd, err := h.store.AwayHandled(ctx, a.StartedAt)
	if err != nil {
		return st, err
	}
	st.Handled = px + rel + advd
	evs, err := h.store.AwayEvents(ctx, a.ID)
	if err != nil {
		return st, err
	}
	for _, e := range evs {
		switch e.Tier {
		case store.AwayPush:
			st.Pushed++
		case store.AwayUser:
			st.Yours++
		}
	}
	if st.Grants, err = h.store.AwayGrants(ctx, a.ID); err != nil {
		return st, err
	}
	for _, g := range st.Grants {
		if g.Status == store.GrantActive {
			st.GrantActive = true
		}
	}
	return st, nil
}

func (h *Hub) reportFor(ctx context.Context, ref string) (AwayReport, error) {
	var a store.Away
	var err error
	if ref == "" {
		a, err = h.store.LatestAway(ctx)
	} else {
		a, err = h.store.GetAway(ctx, ref)
	}
	if errors.Is(err, store.ErrNotFound) {
		if ref == "" {
			return AwayReport{}, huma.Error404NotFound("还没有离开过")
		}
		return AwayReport{}, huma.Error404NotFound("找不到离开记录 " + ref)
	}
	if err != nil {
		return AwayReport{}, err
	}
	return h.awayReport(ctx, a, awayNow())
}

func (h *Hub) registerAwayWeb(api huma.API) {
	tag := []string{"away"}
	huma.Register(api, huma.Operation{OperationID: "get-away", Method: http.MethodGet, Path: "/api/away", Tags: tag, Summary: "暂时离开的状态：离开多久、主助理处理几件、推送几条（R-210）"},
		func(ctx context.Context, _ *struct{}) (*awayStatusOut, error) {
			st, err := h.awayStatusOf(ctx)
			return &awayStatusOut{Body: st}, err
		})
	huma.Register(api, huma.Operation{OperationID: "start-away", Method: http.MethodPost, Path: "/api/away", Tags: tag, Summary: "暂时离开：起点就是现在；同时给各项目的主助理开授权窗口（R-210）"},
		func(ctx context.Context, in *struct {
			Body struct {
				ExpectMinutes int    `json:"expect_minutes" enum:"30,120,480,0" doc:"预估离开多久（分钟）：30 / 120 / 480，0 = 不确定。只用来标「已超过预估」"`
				Scope         string `json:"scope" enum:"none,low,medium" doc:"主助理可以替你答到哪一档：none 不代答（只写建议）/ low 低风险 / medium 中风险"`
				HoldPush      bool   `json:"hold_push,omitempty" doc:"这次离开期间连推送类也先攒着（拿不准的仍推）"`
			}
		}) (*awayStatusOut, error) {
			b := in.Body
			a, err := h.store.StartAway(ctx, awayNow(), b.ExpectMinutes, b.Scope, b.HoldPush)
			if errors.Is(err, store.ErrAwayActive) {
				return nil, huma.Error409Conflict(err.Error())
			}
			if err != nil {
				return nil, err
			}
			notices := h.openAwayGrants(ctx, a)
			if c, err := h.store.AwayCalmCounts(ctx); err == nil && !c.Calm() {
				_ = h.store.MarkAwayWorked(ctx, a.ID)
			}
			h.broker.publish("away", "grants", "decisions", "morning")
			st, err := h.awayStatusOf(ctx)
			st.Notices = notices
			return &awayStatusOut{Body: st}, err
		})
	huma.Register(api, huma.Operation{OperationID: "end-away", Method: http.MethodPost, Path: "/api/away/end", Tags: tag, Summary: "结束离开：撤销这次离开开的授权窗口，返回最终时段报告（R-210）"},
		func(ctx context.Context, _ *struct{}) (*awayReportOut, error) {
			a, err := h.store.ActiveAway(ctx)
			if errors.Is(err, store.ErrNotFound) {
				return nil, huma.Error400BadRequest("现在不在离开中")
			}
			if err != nil {
				return nil, err
			}
			if a, err = h.store.EndAway(ctx, a.ID, awayNow()); err != nil {
				return nil, huma.Error400BadRequest(err.Error())
			}
			h.closeAwayGrants(ctx, a)
			h.broker.publish("away", "grants", "decisions", "morning")
			r, err := h.awayReport(ctx, a, awayNow())
			return &awayReportOut{Body: r}, err
		})
	huma.Register(api, huma.Operation{OperationID: "get-away-report", Method: http.MethodGet, Path: "/api/away/report", Tags: tag, Summary: "离开时段报告：替你定了什么、放行了什么、交付了什么、还在等你的、要你先看的、你在各设备上处理了什么（R-210）"},
		func(ctx context.Context, in *struct {
			Ref string `query:"ref" doc:"离开编号（A-x）；不填为最近一次"`
		}) (*awayReportOut, error) {
			r, err := h.reportFor(ctx, in.Ref)
			return &awayReportOut{Body: r}, err
		})
}

type agentAwayOut struct {
	Body struct {
		Status awayStatus  `json:"status"`
		Report *AwayReport `json:"report,omitempty" doc:"最近一次离开的时段报告；从没离开过时为空"`
	}
}

func (h *Hub) registerAgentAway(api huma.API) {
	tag := []string{"agent"}
	huma.Register(api, huma.Operation{OperationID: "agent-get-away", Method: http.MethodGet, Path: "/api/agent/away", Tags: tag, Summary: "查看用户是否暂时离开，以及离开时段报告（R-210）"},
		func(ctx context.Context, in *struct {
			Ref string `query:"ref" doc:"离开编号（A-x）；不填为最近一次"`
		}) (*agentAwayOut, error) {
			out := &agentAwayOut{}
			var err error
			if out.Body.Status, err = h.awayStatusOf(ctx); err != nil {
				return nil, err
			}
			r, err := h.reportFor(ctx, in.Ref)
			if err == nil {
				out.Body.Report = &r
			} else if in.Ref != "" {
				return nil, err
			}
			return out, nil
		})
	huma.Register(api, huma.Operation{OperationID: "agent-escalate-away", Method: http.MethodPost, Path: "/api/agent/away/escalate", Tags: tag, Summary: "主助理把拿不准、要用户尽快看的决策推给用户（R-210）"},
		func(ctx context.Context, in *struct {
			Body struct {
				SessionKey string `json:"session_key,omitempty"`
				Decision   string `json:"decision" minLength:"1" doc:"决策编号（D-x）"`
				Reason     string `json:"reason,omitempty" doc:"一句话：为什么要用户尽快看（必填）"`
			}
		}) (*morningSetOut, error) {
			b := in.Body
			me, err := h.store.GetSession(ctx, b.SessionKey)
			if err != nil {
				return nil, apiErr(err)
			}
			if me.Role != roles.Aide || me.ProjectID == "" {
				return nil, huma.Error403Forbidden(notAide("把决策推给用户", me.Role))
			}
			d, err := h.store.GetDecision(ctx, b.Decision)
			if err != nil {
				return nil, apiErr(err)
			}
			if d.ProjectID != me.ProjectID {
				return nil, huma.Error403Forbidden(d.Code + " 不在你所在的项目里")
			}
			if d.Kind != store.KindDecision || d.Status != store.DecPending {
				return nil, huma.Error400BadRequest(d.Code + " 不是待决策，不用推")
			}
			reason := strings.TrimSpace(b.Reason)
			if reason == "" {
				return nil, huma.Error400BadRequest("要写理由：reason（ks 用 --reason），一句话说明为什么要用户尽快看")
			}
			h.sendNotify(d.ProjectID, notify.Message{Event: notify.EventDecisionRaised, Title: "待决策 · " + d.Code, Body: brief(d.Question, 80) + "（主助理：" + brief(reason, 60) + "）", Group: "kitestring"}, decisionPath(d),
				pushMeta{Class: pushUnsure, Ref: d.Code, Reason: "主助理拿不准：" + brief(reason, 60)})
			out := &morningSetOut{}
			out.Body.Message = "已推给用户：" + d.Code + "。"
			return out, nil
		})
}

// userStatusZh 是你改需求状态时报告里的一句话。
func userStatusZh(from, to string) string {
	switch {
	case from == store.ReqReview && to == store.ReqDone:
		return "验收通过"
	case from == store.ReqReview && to == store.ReqInProgress:
		return "退回修改"
	case from == store.ReqDraft && to == store.ReqReady:
		return "放行（草稿 → 待开始）"
	}
	return reqStatusZh[from] + " → " + reqStatusZh[to]
}
