package hub

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NekroAI/kitestring/internal/roles"
	"github.com/NekroAI/kitestring/internal/store"
)

// 参谋授权窗口（R-163）：你不在时在网页上授权一个 Adv 会话，到期前代你答复决策。规则：
//   - 窗口按项目开，同一项目同时只有一个；有到期时间（最长 24 小时）和范围（最高风险档：low 或 medium）；随时可撤销，到期自动失效。
//   - 只有被授权的那个 Adv 会话能代答，别的身份、别的会话一律拒绝；必须写理由，并给出自己的风险判断——
//     风险档只升不降：取提问方标的和参谋判断中更高的一档，超出窗口范围就拒绝。提问方没标风险档按 high 算。
//   - 硬规则永不代答：批准方案 / 计划（approval）、设计选型（选项或要拍板的点带效果图）、非决策类、已答复 / 已关闭、参谋自己提的；
//     要拍板的点（points）整体判定，任何一点命中硬规则整条都不代答。验收、批准计划本来不走决策接口，代答碰不到。
//   - 代答留痕在决策上（谁、理由、风险档、原文、时间），网页标「Adv 代答」；你可以推翻（重新作答），更正会送回提问方。
//   - 窗口开着时，参谋可以代答的决策先不推送通知（不吵醒你），但仍留在收件箱里；参谋答了就移走。

const grantMaxSpan = 24 * time.Hour

var riskZh = map[string]string{store.RiskLow: "低风险", store.RiskMedium: "中风险", store.RiskHigh: "高风险"}

// proxyHard 返回 d 命中的硬规则（不能代答的原因）；adv 是参谋的会话键。没命中时为空。
func proxyHard(d store.Decision, adv string) string {
	owner := d.OriginSession
	if owner == "" {
		owner = d.SessionKey
	}
	switch {
	case d.Kind != store.KindDecision:
		return "这不是决策，不能代答"
	case d.Status == store.DecClosed:
		return d.Code + " 已关闭"
	case d.Status != store.DecPending:
		return d.Code + " 已经答复过了，不能再代答"
	case owner != "" && owner == adv:
		return "参谋自己提的决策不能自己代答"
	case d.Approval:
		return "批准方案 / 计划只由用户操作"
	}
	for _, fs := range d.OptionFiles {
		if len(fs) > 0 {
			return "设计选型（选项带效果图）只由用户拍板"
		}
	}
	for _, p := range d.Points {
		for _, it := range p.Items {
			if it.Image != nil {
				return "设计选型（要拍板的点带效果图）只由用户拍板"
			}
		}
	}
	return ""
}

// proxyQuiet：d 所在项目有生效中的窗口、d 不命中硬规则、提问方标的风险档在范围内——参谋可以代答，先不推送。
func (h *Hub) proxyQuiet(ctx context.Context, d store.Decision) bool {
	if d.ProjectID == "" {
		return false
	}
	g, err := h.store.ActiveGrant(ctx, d.ProjectID)
	if err != nil {
		return false
	}
	return proxyHard(d, g.SessionKey) == "" && store.RiskRank(store.NormRisk(d.Risk)) <= store.RiskRank(g.MaxRisk)
}

// proxyCheck 检查 caller 能不能代答 d，返回生效的风险档（提问方与参谋判断中更高的一档）。
func (h *Hub) proxyCheck(ctx context.Context, d store.Decision, caller, advRisk, reason string) (store.Session, string, error) {
	if caller == "" {
		return store.Session{}, "", huma.Error400BadRequest("代答要知道是哪个会话：请带上 session_key")
	}
	x, err := h.store.GetSession(ctx, caller)
	if err != nil {
		return x, "", apiErr(err)
	}
	if x.Role != roles.Advisor {
		return x, "", huma.Error403Forbidden("只有 Adv（参谋）能在用户的授权窗口内代答决策；其它身份不能代答，要用户拍板的事请等用户答复")
	}
	if d.ProjectID == "" || d.ProjectID != x.ProjectID {
		return x, "", huma.Error403Forbidden(d.Code + " 不属于你所在的项目，不能代答")
	}
	g, err := h.store.ActiveGrant(ctx, d.ProjectID)
	if err != nil {
		return x, "", huma.Error403Forbidden("用户没有开授权窗口，或窗口已到期 / 已撤销：不能代答，等用户答复")
	}
	if g.SessionKey != caller {
		return x, "", huma.Error403Forbidden("用户的授权窗口给的是另一个 Adv 会话，你不能代答")
	}
	if why := proxyHard(d, caller); why != "" {
		return x, "", huma.Error403Forbidden("硬规则，不能代答：" + why + "；留给用户")
	}
	switch advRisk {
	case store.RiskLow, store.RiskMedium, store.RiskHigh:
	default:
		return x, "", huma.Error400BadRequest("代答要写你自己的风险判断：risk = low / medium / high（产品方向、设计选型、语义 / 接口 / 权限变更、不可逆或对外操作都是 high）")
	}
	eff := store.MaxRisk(d.Risk, advRisk)
	if store.RiskRank(eff) > store.RiskRank(g.MaxRisk) {
		src := "你的判断"
		if store.RiskRank(store.NormRisk(d.Risk)) >= store.RiskRank(advRisk) {
			src = "提问方标的"
			if d.Risk == "" {
				src = "提问方没标风险档，按高风险算"
			}
		}
		return x, "", huma.Error403Forbidden(fmt.Sprintf("超出授权范围：%s 按%s（%s），用户只授权代答到%s；留给用户", d.Code, riskZh[eff], src, riskZh[g.MaxRisk]))
	}
	if strings.TrimSpace(reason) == "" {
		return x, "", huma.Error400BadRequest("代答必须写理由：reason（ks 用 --reason），说明为什么这样答、为什么不必等用户")
	}
	return x, eff, nil
}

// proxyText 是送给提问方的答复：参谋代答时注明是代答；你推翻后写明更正、以你的为准。
// 批准类决策选了「拒绝」时写明是用户明确拒绝（R-176）。
func proxyText(d store.Decision) string {
	d.Answer = approvalText(d)
	if d.AnswerReason != "" && (d.ProxyBy == "" || d.OverriddenAt != nil) {
		d.Answer += "\n用户的理由：" + d.AnswerReason // 用户亲手作答时附的一句话理由（R-157），随答复送给 Agent
	}
	if d.ProxyBy == "" {
		return d.Answer
	}
	if d.OverriddenAt != nil {
		return fmt.Sprintf("更正：用户推翻了参谋的代答（原代答：%s），以此为准：%s", brief(d.ProxyAnswer, 80), d.Answer)
	}
	return fmt.Sprintf("（参谋代答）%s\n（用户开了授权窗口，由 Adv 代为答复；理由：%s。用户可能事后推翻，届时会送来更正）", d.Answer, brief(d.ProxyReason, 80))
}

type grantsOut struct {
	Body struct {
		Grants  []store.Grant    `json:"grants" doc:"授权窗口（新的在前）：生效中、已撤销、已到期都在"`
		Proxied []store.Decision `json:"proxied" doc:"参谋代答过的决策（新的在前）"`
	}
}

type grantOut struct{ Body store.Grant }

func (h *Hub) registerGrantWeb(api huma.API) {
	tag := []string{"grants"}
	huma.Register(api, huma.Operation{OperationID: "list-grants", Method: http.MethodGet, Path: "/api/grants", Tags: tag, Summary: "参谋授权窗口与代答记录（R-163）"},
		func(ctx context.Context, in *struct {
			ProjectID string `query:"project_id" doc:"只看这个项目；不填为全部"`
		}) (*grantsOut, error) {
			out := &grantsOut{}
			var err error
			if out.Body.Grants, err = h.store.ListGrants(ctx, in.ProjectID, 50); err != nil {
				return nil, err
			}
			out.Body.Proxied, err = h.store.ProxiedDecisions(ctx, in.ProjectID, 100)
			return out, err
		})
	huma.Register(api, huma.Operation{OperationID: "create-grant", Method: http.MethodPost, Path: "/api/grants", Tags: tag, Summary: "开一个参谋授权窗口（R-163）"},
		func(ctx context.Context, in *struct {
			Body struct {
				ProjectID  string    `json:"project_id" minLength:"1"`
				SessionKey string    `json:"session_key" minLength:"1" doc:"被授权的 Adv 会话"`
				ExpiresAt  time.Time `json:"expires_at" doc:"到期时间（最长 24 小时后）"`
				MaxRisk    string    `json:"max_risk" enum:"low,medium" doc:"可代答的最高风险档"`
			}
		}) (*grantOut, error) {
			b := in.Body
			x, err := h.store.GetSession(ctx, b.SessionKey)
			if err != nil {
				return nil, huma.Error400BadRequest("找不到这个会话")
			}
			if x.Role != roles.Advisor {
				return nil, huma.Error400BadRequest("只能授权身份为 Adv 的会话")
			}
			if x.ProjectID != b.ProjectID {
				return nil, huma.Error400BadRequest("这个 Adv 会话不在该项目里")
			}
			if x.State == store.StateEnded || x.State == store.StateLost || x.DoneAt != nil {
				return nil, huma.Error400BadRequest("这个 Adv 会话已结束")
			}
			now := time.Now()
			if !b.ExpiresAt.After(now) {
				return nil, huma.Error400BadRequest("到期时间要晚于现在")
			}
			if b.ExpiresAt.Sub(now) > grantMaxSpan+time.Minute {
				return nil, huma.Error400BadRequest("授权最长 24 小时")
			}
			g, err := h.store.CreateGrant(ctx, b.ProjectID, b.SessionKey, b.MaxRisk, b.ExpiresAt)
			if err != nil {
				return nil, huma.Error400BadRequest(err.Error())
			}
			h.tellGrant(ctx, g, fmt.Sprintf("用户开了参谋授权窗口 %s：到 %s 前，你可以代用户答复本项目%s以内的决策（decision_answer / ks decision answer，必须写理由和你自己的风险判断，风险档取提问方与你判断中更高的一档）。硬规则永不代答：批准方案 / 计划、设计选型、产品方向、语义 / 接口 / 权限变更、不可逆或对外操作、验收。拿不准的留给用户。",
				g.Code, g.ExpiresAt.Local().Format("01-02 15:04"), riskZh[g.MaxRisk]))
			h.broker.publish("grants", "decisions")
			return &grantOut{Body: g}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "revoke-grant", Method: http.MethodPost, Path: "/api/grants/{ref}/revoke", Tags: tag, Summary: "撤销参谋授权窗口（R-163）"},
		func(ctx context.Context, in *struct {
			Ref string `path:"ref"`
		}) (*grantOut, error) {
			g, err := h.store.RevokeGrant(ctx, in.Ref)
			if err != nil {
				if err == store.ErrNotFound {
					return nil, apiErr(err)
				}
				return nil, huma.Error400BadRequest(err.Error())
			}
			h.tellGrant(ctx, g, fmt.Sprintf("用户撤销了参谋授权窗口 %s：从现在起不能再代答决策。", g.Code))
			h.broker.publish("grants", "decisions")
			return &grantOut{Body: g}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "override-decision", Method: http.MethodPost, Path: "/api/decisions/{ref}/override", Tags: []string{"decisions"}, Summary: "推翻参谋的代答：以你的答复为准，更正送回提问方（R-163）"},
		func(ctx context.Context, in *struct {
			Ref  string `path:"ref"`
			Body struct {
				Answer  string   `json:"answer" doc:"你的答复；有要拍板的点时是附言，可空"`
				Files   []string `json:"files,omitempty" doc:"附件 ID"`
				Choices []int    `json:"choices,omitempty" doc:"要拍板的点：逐点选第几个（从 0 开始）"`
				Reject  bool     `json:"reject,omitempty"`
				Reason  string   `json:"reason,omitempty" doc:"可选的一句话理由（R-157）"`
			}
		}) (*decisionOut, error) {
			d0, err := h.store.GetDecision(ctx, in.Ref)
			if err != nil {
				return nil, apiErr(err)
			}
			if d0.ProxyBy == "" {
				return nil, huma.Error400BadRequest(d0.Code + " 不是参谋代答的，直接作答即可")
			}
			if d0.Status == store.DecClosed {
				return nil, huma.Error400BadRequest(d0.Code + " 已关闭")
			}
			files, err := h.store.ResolveFiles(ctx, in.Body.Files)
			if err != nil {
				return nil, err
			}
			answer, choices := in.Body.Answer, []int(nil)
			if len(d0.Points) > 0 && (len(in.Body.Choices) > 0 || in.Body.Reject) {
				if answer, err = pointsAnswer(d0, in.Body.Choices, in.Body.Answer, in.Body.Reject); err != nil {
					return nil, huma.Error400BadRequest(err.Error())
				}
				choices = in.Body.Choices
			}
			d, err := h.store.AnswerDecisionPoints(ctx, d0.ID, answer, files, choices, in.Body.Reason)
			if err != nil {
				return nil, apiErr(err)
			}
			h.decisionsChanged(ctx, d)
			if x, err := h.store.GetSession(ctx, d.ProxyBy); err == nil && x.State != store.StateEnded && x.DoneAt == nil {
				if _, err := h.tell(ctx, x.Key, d.ProjectID, d.RequirementID, fmt.Sprintf("你代答的 %s「%s」被用户推翻了：你答的是「%s」，用户改为「%s」。更正已送给提问方。", d.Code, brief(d.Question, 40), brief(d.ProxyAnswer, 40), brief(d.Answer, 60))); err != nil {
					h.log.Warn("通知参谋代答被推翻失败", "err", err)
				}
			}
			return &decisionOut{Body: d}, nil
		})
}

// tellGrant 给被授权的 Adv 会话送一句说明（走团队消息，不进你的收件箱）。
func (h *Hub) tellGrant(ctx context.Context, g store.Grant, text string) {
	if _, err := h.tell(ctx, g.SessionKey, g.ProjectID, "", text); err != nil {
		h.log.Warn("通知参谋授权变化失败", "err", err)
	}
}

type agentGrantOut struct {
	Body struct {
		Grant  *store.Grant `json:"grant,omitempty" doc:"会话所在项目当前生效中的授权窗口；没有时为空"`
		Mine   bool         `json:"mine" doc:"这个窗口是不是授权给调用者的"`
		Notice string       `json:"notice"`
	}
}

func (h *Hub) registerAgentGrant(api huma.API) {
	tag := []string{"agent"}
	huma.Register(api, huma.Operation{OperationID: "agent-get-grant", Method: http.MethodGet, Path: "/api/agent/grant", Tags: tag, Summary: "查看本项目当前的参谋授权窗口（R-163）"},
		func(ctx context.Context, in *struct {
			SessionKey string `query:"session_key"`
		}) (*agentGrantOut, error) {
			out := &agentGrantOut{}
			x, err := h.store.GetSession(ctx, in.SessionKey)
			if err != nil {
				return nil, apiErr(err)
			}
			g, err := h.store.ActiveGrant(ctx, x.ProjectID)
			if err != nil {
				out.Body.Notice = "本项目没有生效中的参谋授权窗口：决策都等用户答复"
				return out, nil
			}
			out.Body.Grant, out.Body.Mine = &g, g.SessionKey == x.Key
			out.Body.Notice = fmt.Sprintf("授权窗口 %s 生效中：至 %s，可代答%s以内的决策", g.Code, g.ExpiresAt.Local().Format("01-02 15:04"), riskZh[g.MaxRisk])
			if !out.Body.Mine {
				out.Body.Notice += "（授权给的是另一个会话，你不能代答）"
			}
			return out, nil
		})
	huma.Register(api, huma.Operation{OperationID: "agent-answer-decision", Method: http.MethodPost, Path: "/api/agent/decisions/{ref}/answer", Tags: tag, Summary: "参谋在用户的授权窗口内代答决策（R-163）"},
		func(ctx context.Context, in *struct {
			Ref  string `path:"ref"`
			Body struct {
				SessionKey string `json:"session_key,omitempty"`
				Answer     string `json:"answer,omitempty" doc:"答复；有要拍板的点时是附言，可空"`
				Reason     string `json:"reason" doc:"为什么这样答、为什么不必等用户（必填）"`
				Risk       string `json:"risk" doc:"你自己的风险判断：low / medium / high（必填）；取它和提问方标的中更高的一档"`
				Choices    []int  `json:"choices,omitempty" doc:"要拍板的点：逐点选第几个（从 0 开始）"`
			}
		}) (*decisionOut, error) {
			d0, err := h.store.GetDecision(ctx, in.Ref)
			if err != nil {
				return nil, apiErr(err)
			}
			b := in.Body
			x, eff, err := h.proxyCheck(ctx, d0, b.SessionKey, b.Risk, b.Reason)
			if err != nil {
				return nil, err
			}
			answer, choices := strings.TrimSpace(b.Answer), []int(nil)
			if len(d0.Points) > 0 {
				if answer, err = pointsAnswer(d0, b.Choices, b.Answer, false); err != nil {
					return nil, huma.Error400BadRequest(err.Error() + "（代答逐点决策要用 choices 每点选一个，从 0 开始）")
				}
				choices = b.Choices
			}
			if answer == "" {
				return nil, huma.Error400BadRequest("答复不能为空")
			}
			d, err := h.store.ProxyAnswerDecision(ctx, d0.ID, x.Key, answer, b.Reason, eff, choices)
			if err != nil {
				return nil, huma.Error400BadRequest(err.Error())
			}
			h.decisionsChanged(ctx, d)
			h.broker.publish("grants")
			return &decisionOut{Body: d}, nil
		})
}
