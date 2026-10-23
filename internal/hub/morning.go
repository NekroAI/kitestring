package hub

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NekroAI/kitestring/internal/roles"
	"github.com/NekroAI/kitestring/internal/store"
)

// 参谋早报（R-165）：你离开期间参谋做了什么、替你定了什么，工作台顶部集中呈现；你回来先看这里。
//   - 「离开期间」：项目最近一个参谋授权窗口的开启时间到现在（窗口生效中，或结束不到 12 小时）；
//     没有这样的窗口时，以参谋写总述的时间往前 12 小时；两者都没有就不出早报。
//   - 参谋只写两样（morning_set / ks morning，只有 Adv 能写）：一段总述，和逐条验收建议（写进对应需求）。
//   - 其余由 hub 汇总：① 完成了什么（期间交了证据、现在待验收 / 已完成的需求，附提交）、团队消息；
//     ② 替你定了什么（期间的 Adv 代答、PM / Adv 放行）；③④ 只给计数——未答决策与待验收本来就在「要你处理」里，不重复列。
//   - 「要你处理」里的卡片挂两种标注：参谋的验收建议（advice）、参谋没代答的原因（holds）。

const (
	morningTTL     = 12 * time.Hour // 窗口结束 / 总述写下超过这么久，早报不再出现
	morningTextMax = 800
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
	Role             string    `json:"role" doc:"放行时的身份：pm / advisor"`
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

// MorningReport 是一个项目的早报。
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
	Releases    []morningRelease   `json:"releases" doc:"② PM / Adv 放行的草稿（新的在前）"`
	Pending     int                `json:"pending" doc:"③ 留给你的：还没答的决策条数（在「要你处理」里）"`
	Blocked     []string           `json:"blocked" doc:"③ 受阻的需求编号"`
	Review      int                `json:"review" doc:"④ 待验收需求条数（在「要你处理」里）"`
}

// morningHold 是参谋没代答一条决策的原因，挂在「要你处理」的决策卡上。
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

// morningWindow 算项目的离开期间：返回起点、据以计算的窗口；ok = false 表示不出早报。
func (h *Hub) morningWindow(ctx context.Context, projectID string, note *store.MorningNote, at time.Time) (time.Time, *store.Grant, bool) {
	if gs, err := h.store.ListGrants(ctx, projectID, 1); err == nil && len(gs) > 0 {
		g := gs[0]
		if g.Status == store.GrantActive || at.Sub(grantEnd(g)) < morningTTL {
			return g.CreatedAt, &g, true
		}
	}
	if note != nil && at.Sub(note.At) < morningTTL {
		return note.At.Add(-morningTTL), nil, true
	}
	return time.Time{}, nil, false
}

// holdReason 说明参谋为什么没代答 d（窗口 g 生效或刚结束时）；空表示参谋还可以代答。
func holdReason(d store.Decision, g store.Grant, at time.Time) (string, string) {
	if why := proxyHard(d, g.SessionKey); why != "" {
		return "hard", "硬规则 · " + why
	}
	if store.RiskRank(store.NormRisk(d.Risk)) > store.RiskRank(g.MaxRisk) {
		src := riskZh[store.NormRisk(d.Risk)]
		if d.Risk == "" {
			src = "提问方没标风险档，按高风险算"
		}
		return "scope", fmt.Sprintf("超出授权 · %s，只授权到%s", src, riskZh[g.MaxRisk])
	}
	if !at.Before(grantEnd(g)) {
		return "ended", "授权窗口 " + g.Code + " 已结束"
	}
	return "", ""
}

func (h *Hub) morningReport(ctx context.Context, p store.Project, at time.Time, out *morningOut) error {
	var note *store.MorningNote
	if n, err := h.store.GetMorning(ctx, p.ID); err == nil {
		note = &n
	}
	since, g, ok := h.morningWindow(ctx, p.ID, note, at)
	if g != nil {
		pending, err := h.store.PendingDecisions(ctx, p.ID)
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
	if note != nil && note.At.Before(since) {
		note = nil // 上一次离开时写的总述
	}
	r := MorningReport{ProjectID: p.ID, ProjectName: p.Name, Since: since, Grant: g, Note: note, Done: []morningDone{}, Tells: []morningTell{}, Releases: []morningRelease{}, Blocked: []string{}}

	reqs, err := h.store.ListRequirements(ctx, p.ID, nil)
	if err != nil {
		return err
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
	evs, err := h.store.EvidenceSince(ctx, p.ID, since)
	if err != nil {
		return err
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

	tells, err := h.store.ProjectDecisionsSince(ctx, p.ID, store.KindTell, since, 200)
	if err != nil {
		return err
	}
	r.TellCount = len(tells)
	for i, d := range tells {
		if i == morningTells {
			break
		}
		r.Tells = append(r.Tells, morningTell{Code: d.Code, To: d.SessionKey, Text: d.Answer, RequirementCode: d.RequirementCode, At: d.CreatedAt})
	}

	// ② 替你定了什么
	if r.Proxied, err = h.store.ProxiedSince(ctx, p.ID, since); err != nil {
		return err
	}
	rels, err := h.store.ProjectDecisionsSince(ctx, p.ID, store.KindRelease, since, 100)
	if err != nil {
		return err
	}
	for _, d := range rels {
		r.Releases = append(r.Releases, morningRelease{Code: d.Code, RequirementCode: d.RequirementCode, RequirementTitle: d.RequirementTitle, By: d.AssignedBy, Role: d.Context, Reason: d.Answer, Withdrawn: d.CloseReason != "", At: d.CreatedAt})
	}

	// ③④ 只计数
	pending, err := h.store.PendingDecisions(ctx, p.ID)
	if err != nil {
		return err
	}
	r.Pending = len(pending)
	out.Body.Reports = append(out.Body.Reports, r)
	return nil
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
	huma.Register(api, huma.Operation{OperationID: "get-morning", Method: http.MethodGet, Path: "/api/morning", Tags: []string{"morning"}, Summary: "参谋早报：离开期间参谋做了什么、替你定了什么（R-165）"},
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
			for _, p := range ps {
				if (in.ProjectID != "" && p.ID != in.ProjectID) || (in.ProjectID == "" && p.Hidden) {
					continue
				}
				if err := h.morningReport(ctx, p, at, out); err != nil {
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
	huma.Register(api, huma.Operation{OperationID: "agent-set-morning", Method: http.MethodPost, Path: "/api/agent/morning", Tags: []string{"agent"}, Summary: "Adv 写早报：总述与逐条验收建议（R-165）"},
		func(ctx context.Context, in *struct {
			Body struct {
				SessionKey string     `json:"session_key" minLength:"1" doc:"调用者（必须是本项目的 Adv）"`
				Text       *string    `json:"text,omitempty" doc:"总述：一段话，先结论；不传表示不改，空字符串表示清除"`
				Advice     []adviceIn `json:"advice,omitempty" doc:"逐条验收建议，每条覆盖该需求上一份"`
			}
		}) (*morningSetOut, error) {
			me, err := h.store.GetSession(ctx, in.Body.SessionKey)
			if err != nil {
				return nil, apiErr(err)
			}
			if me.Role != roles.Advisor || me.ProjectID == "" {
				return nil, huma.Error403Forbidden("只有项目的 Adv（参谋）能写早报")
			}
			if in.Body.Text == nil && len(in.Body.Advice) == 0 {
				return nil, huma.Error400BadRequest("什么都没写：传 text（总述）或 advice（验收建议）")
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
			if in.Body.Text != nil {
				text := strings.TrimSpace(*in.Body.Text)
				if utf8.RuneCountInString(text) > morningTextMax {
					text = string([]rune(text)[:morningTextMax])
				}
				if err := h.store.SetMorning(ctx, me.ProjectID, text, me.Key); err != nil {
					return nil, err
				}
				if text == "" {
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
			out.Body.Message = strings.Join(done, "；") + "。用户在工作台顶部的参谋早报、「要你处理」的验收卡上能看到。"
			return out, nil
		})
}
