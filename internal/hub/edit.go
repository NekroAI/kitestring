package hub

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NekroAI/kitestring/internal/roles"
	"github.com/NekroAI/kitestring/internal/store"
)

// 改需求内容（R-134）：Agent 可以改标题、描述、验收标准、优先级、类型。规则：
//   - 谁能改：关联了这条需求的会话（创建者建需求时自动关联）、同项目的 主调度 / 主助理；其他会话（包括没关联的 Dev）拒绝；
//   - 已完成、已搁置的需求不能改；
//   - 进行中 / 阻塞 / 待验收时改验收标准，要带 approval = 这条需求上一条用户已批准的批准类决策（Adv 不能代答批准类），
//     免得 Agent 悄悄放宽验收标准；草稿 / 待开始可以直接改；
//   - 标题沿用 R-129 的清洗：去掉开头的编号前缀；
//   - 每次修改记成一条修改记录进时间线：谁、什么身份、改了哪些字段的改前改后、理由。

type editIn struct {
	Ref  string `path:"ref"`
	Body struct {
		SessionKey string  `json:"session_key" doc:"改需求的会话"`
		Title      *string `json:"title,omitempty" doc:"新标题；开头的编号前缀会被去掉（R-129）；不传表示不改"`
		Detail     *string `json:"detail,omitempty" doc:"新描述；不传表示不改"`
		Acceptance *string `json:"acceptance,omitempty" doc:"新验收标准；进行中之后改要带 approval；不传表示不改"`
		Priority   *string `json:"priority,omitempty" enum:"P0,P1,P2,P3" doc:"新优先级；不传表示不改"`
		Kind       *string `json:"kind,omitempty" enum:"feature,design" doc:"新类型：design = 设计类，feature = 普通；不传表示不改"`
		Reason     string  `json:"reason,omitempty" doc:"为什么改（建议写，会显示在时间线上）"`
		Approval   string  `json:"approval,omitempty" doc:"进行中 / 阻塞 / 待验收时改验收标准必填：这条需求上用户已批准的批准类决策编号（D-x）"`
	}
}

// editApprovalHint 是进行中之后改验收标准被拒时给的下一步。
func editApprovalHint(r store.Requirement) string {
	return fmt.Sprintf(`先请用户批准新的验收标准，批准后带上决策编号再改。示例（MCP）：decision_raise {"requirement": "%s", "approval": true, "question": "%s 的验收标准要改成下面这样，批准吗？", "context": "原验收标准：……\n新验收标准：……\n为什么改：……"}；用户批准后：req_edit {"ref": "%s", "acceptance": "……", "approval": "D-x"}（ks：ks req edit %s --acceptance ... --approval D-x）`,
		r.Code, r.Code, r.Code, r.Code)
}

// editCheck 检查 callerKey 能不能改 r，返回调用者会话。
func (h *Hub) editCheck(ctx context.Context, r store.Requirement, callerKey string) (store.Session, error) {
	if callerKey == "" {
		return store.Session{}, huma.Error400BadRequest("改需求要知道是哪个会话：请带上 session_key")
	}
	x, err := h.store.GetSession(ctx, callerKey)
	if err != nil {
		return x, apiErr(err)
	}
	if r.Status == store.ReqDone || r.Status == store.ReqShelved {
		return x, huma.Error400BadRequest(fmt.Sprintf("%s 已%s，Agent 不能再改它的内容；需要调整请新建需求，或请用户重新打开", r.Code, map[string]string{store.ReqDone: "完成", store.ReqShelved: "搁置"}[r.Status]))
	}
	if slices.Contains(r.Sessions, x.Key) {
		return x, nil
	}
	if (x.Role == roles.PM || x.Role == roles.Aide || x.Role == roles.Advisor) && r.ProjectID != "" && x.ProjectID == r.ProjectID {
		return x, nil
	}
	return x, huma.Error403Forbidden(fmt.Sprintf("只有关联了 %s 的会话（创建者、在做的会话）或项目主调度 / 主助理 / 顾问能改它；你没有关联这条需求。要改请告诉主调度（pm_notify），或先 session_link_req 接手", r.Code))
}

// editApproved 检查 ref 是不是 r 上一条用户已批准的批准类决策。
func (h *Hub) editApproved(ctx context.Context, r store.Requirement, ref string) error {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return huma.Error403Forbidden(fmt.Sprintf("%s 已在%s，改验收标准要用户批准。%s", r.Code, map[string]string{store.ReqInProgress: "进行中", store.ReqBlocked: "阻塞中", store.ReqReview: "待验收"}[r.Status], editApprovalHint(r)))
	}
	d, err := h.store.GetDecision(ctx, ref)
	if err != nil {
		return huma.Error400BadRequest(fmt.Sprintf("找不到决策 %s。%s", ref, editApprovalHint(r)))
	}
	switch {
	case d.RequirementID != r.ID:
		return huma.Error403Forbidden(fmt.Sprintf("%s 不是 %s 上的决策，不能用来批准改验收标准。%s", d.Code, r.Code, editApprovalHint(r)))
	case d.Kind != store.KindDecision || !d.Approval:
		return huma.Error403Forbidden(fmt.Sprintf("%s 不是批准类决策（approval = true）。%s", d.Code, editApprovalHint(r)))
	case d.Status == store.DecPending:
		return huma.Error403Forbidden(fmt.Sprintf("%s 用户还没答复，等用户批准后再改", d.Code))
	case d.ProxyBy != "" && d.OverriddenAt == nil:
		return huma.Error403Forbidden(fmt.Sprintf("%s 是 主助理代答的，改验收标准要用户本人批准。%s", d.Code, editApprovalHint(r)))
	case d.Verdict != store.VerdictApproved: // 读结论字段，不看答复文字（R-239）
		return huma.Error403Forbidden(fmt.Sprintf("用户没有批准 %s（答复：%s），不能改验收标准", d.Code, brief(d.Answer, 60)))
	}
	return nil
}

var editLabels = map[string]string{"title": "标题", "detail": "描述", "acceptance": "验收标准", "priority": "优先级", "kind": "类型"}

func kindLabel(k string) string {
	if k == "design" {
		return "设计类"
	}
	return "普通"
}

func (h *Hub) registerAgentEdit(api huma.API) {
	huma.Register(api, huma.Operation{OperationID: "agent-edit-requirement", Method: http.MethodPost, Path: "/api/agent/requirements/{ref}/edit", Tags: []string{"agent"},
		Summary: "Agent 改需求的标题 / 描述 / 验收标准 / 优先级 / 类型，时间线留下改前改后（R-134）"},
		func(ctx context.Context, in *editIn) (*reqOut, error) {
			cur, err := h.store.GetRequirement(ctx, in.Ref)
			if err != nil {
				return nil, apiErr(err)
			}
			b := in.Body
			x, err := h.editCheck(ctx, cur, b.SessionKey)
			if err != nil {
				return nil, err
			}
			next := cur
			var changes []store.FieldChange
			add := func(field, before, after string) {
				if before != after {
					changes = append(changes, store.FieldChange{Field: field, Label: editLabels[field], Before: before, After: after})
				}
			}
			if b.Title != nil {
				t := store.CleanTitle(*b.Title)
				if t == "" {
					return nil, huma.Error400BadRequest("标题不能为空")
				}
				add("title", cur.Title, t)
				next.Title = t
			}
			if b.Detail != nil {
				add("detail", cur.Detail, *b.Detail)
				next.Detail = *b.Detail
			}
			if b.Acceptance != nil {
				add("acceptance", cur.Acceptance, *b.Acceptance)
				next.Acceptance = *b.Acceptance
			}
			if b.Priority != nil && *b.Priority != "" {
				add("priority", cur.Priority, *b.Priority)
				next.Priority = *b.Priority
			}
			if b.Kind != nil && *b.Kind != "" {
				k := ""
				if *b.Kind == "design" {
					k = "design"
				}
				if k != cur.Kind {
					changes = append(changes, store.FieldChange{Field: "kind", Label: editLabels["kind"], Before: kindLabel(cur.Kind), After: kindLabel(k)})
				}
				next.Kind = *b.Kind
			}
			if len(changes) == 0 {
				return nil, huma.Error400BadRequest(fmt.Sprintf("没有要改的内容：给出 title / detail / acceptance / priority / kind 中至少一项，且与 %s 现在的不同", cur.Code))
			}
			approval := ""
			if next.Acceptance != cur.Acceptance && (cur.Status == store.ReqInProgress || cur.Status == store.ReqBlocked || cur.Status == store.ReqReview) {
				if err := h.editApproved(ctx, cur, b.Approval); err != nil {
					return nil, err
				}
				approval = strings.TrimSpace(b.Approval)
			}
			next.Status = "" // 只改内容，不动状态
			if _, err := h.store.UpdateRequirement(ctx, next); err != nil {
				return nil, apiErr(err)
			}
			names := make([]string, 0, len(changes))
			for _, c := range changes {
				names = append(names, c.Label)
			}
			note := "改了" + strings.Join(names, "、")
			if r := strings.TrimSpace(b.Reason); r != "" {
				note += "：" + r
			}
			if approval != "" {
				if d, err := h.store.GetDecision(ctx, approval); err == nil {
					approval = d.Code
				}
				note += "（验收标准已获用户批准 " + approval + "）"
			}
			if _, err := h.store.CreateEdit(ctx, cur, x.Key, x.Role, note, changes); err != nil {
				return nil, apiErr(err)
			}
			r, err := h.store.GetRequirement(ctx, cur.ID)
			if err != nil {
				return nil, apiErr(err)
			}
			h.tellEdited(ctx, r, x, note)
			h.broker.publish("requirements", "sessions", "plans")
			return &reqOut{Body: r}, nil
		})
}

// tellEdited：需求有别的会话在做（进行中 / 阻塞）时，告诉它们内容被改了，按新的内容做。
func (h *Hub) tellEdited(ctx context.Context, r store.Requirement, by store.Session, note string) {
	if r.Status != store.ReqInProgress && r.Status != store.ReqBlocked {
		return
	}
	who := roles.Label(by.Role)
	if who == "" {
		who = "会话"
	}
	msg := fmt.Sprintf("%s「%s」被 %s %s %s，按新的内容做（req_get %s 查看）。", r.Code, brief(r.Title, 40), who, by.Key, note, r.Code)
	for _, k := range r.Sessions {
		if k == by.Key {
			continue
		}
		s, err := h.store.GetSession(ctx, k)
		if err != nil || s.State == store.StateEnded || s.State == store.StateLost || s.DoneAt != nil {
			continue
		}
		if _, err := h.tell(ctx, k, r.ProjectID, r.ID, msg); err != nil {
			h.log.Warn("通知需求被修改失败", "err", err)
		}
	}
}
