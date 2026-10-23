package hub

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NekroAI/kitestring/internal/notify"
	"github.com/NekroAI/kitestring/internal/store"
)

// 计划（R-43）：需求之上的一层。一个计划分成有顺序的阶段，阶段内可以放多条需求。
// 推进规则：开始 / 批准 → 第一个阶段解锁；阶段内需求都做完（待验收 / 已完成）后 Agent 交阶段小结；
// 你通过整个阶段 → 其中还没验收的需求一并通过，下一阶段解锁并交给负责会话；最后一个阶段通过 → 计划完成。
// 已通过阶段里的需求被重开时计划暂停，修好之前不能往下走。计划进展作为 kind=plan 的送达注入负责会话。

type planOut struct {
	Body store.Plan
}

type plansOut struct {
	Body struct {
		Plans []store.Plan `json:"plans"`
	}
}

// planReqIn 是阶段里的一条需求：给 ref 表示已有需求，否则按标题等字段新建。
type planReqIn struct {
	Ref        string `json:"ref,omitempty" doc:"已有需求的编号（如 R-12）或 ID"`
	Title      string `json:"title,omitempty" doc:"新建需求的标题（不给 ref 时必填）"`
	Detail     string `json:"detail,omitempty"`
	Acceptance string `json:"acceptance,omitempty"`
	Priority   string `json:"priority,omitempty" enum:"P0,P1,P2,P3"`
	Kind       string `json:"kind,omitempty" enum:"design,feature"`
}

type stageIn struct {
	ID           string      `json:"id,omitempty" doc:"已有阶段的 ID；不给表示新阶段"`
	Title        string      `json:"title"`
	Requirements []planReqIn `json:"requirements"`
}

type planFields struct {
	Title        string    `json:"title" minLength:"1"`
	Goal         string    `json:"goal,omitempty"`
	ProjectID    string    `json:"project_id,omitempty"`
	OwnerSession string    `json:"owner_session,omitempty" doc:"负责会话：阶段解锁后需求自动交给它"`
	Stages       []stageIn `json:"stages,omitempty" doc:"按顺序排列的阶段；编辑时不给表示不改阶段"`
}

type planCreateBody struct {
	Title        string    `json:"title" minLength:"1"`
	Goal         string    `json:"goal,omitempty"`
	ProjectID    string    `json:"project_id,omitempty"`
	OwnerSession string    `json:"owner_session,omitempty" doc:"负责会话：阶段解锁后需求自动交给它"`
	Stages       []stageIn `json:"stages" minItems:"1" doc:"按顺序排列的阶段"`
	Start        bool      `json:"start,omitempty" doc:"为真时建好后立即开始"`
}

type commentBody struct {
	Comment string `json:"comment,omitempty"`
}

// resolveStages 把输入的阶段转成存储层的格式；没有 ref 的需求就地新建（草稿）。
func (h *Hub) resolveStages(ctx context.Context, in []stageIn, projectID, createdBy, sessionKey string) ([]store.StageInput, error) {
	out := make([]store.StageInput, 0, len(in))
	for _, st := range in {
		si := store.StageInput{ID: st.ID, Title: st.Title, Requirements: []string{}}
		for _, ri := range st.Requirements {
			if ri.Ref != "" {
				r, err := h.store.GetRequirement(ctx, ri.Ref)
				if err != nil {
					return nil, huma.Error400BadRequest("需求不存在：" + ri.Ref)
				}
				si.Requirements = append(si.Requirements, r.ID)
				continue
			}
			if strings.TrimSpace(ri.Title) == "" {
				return nil, huma.Error400BadRequest("阶段里的新需求需要标题")
			}
			r, err := h.store.CreateRequirement(ctx, store.Requirement{
				ProjectID: projectID, Title: strings.TrimSpace(ri.Title), Detail: ri.Detail, Acceptance: ri.Acceptance,
				Priority: ri.Priority, Kind: ri.Kind, CreatedBy: createdBy,
			})
			if err != nil {
				return nil, apiErr(err)
			}
			if sessionKey != "" {
				_ = h.store.LinkSession(ctx, r.ID, sessionKey)
			}
			si.Requirements = append(si.Requirements, r.ID)
		}
		out = append(out, si)
	}
	return out, nil
}

// planMsg 记下一条给负责会话的计划进展（没有负责会话时不记）；由 planChanged 统一下发。
func (h *Hub) planMsg(ctx context.Context, x store.Plan, text string) {
	if x.OwnerSession == "" {
		return
	}
	s, err := h.store.GetSession(ctx, x.OwnerSession)
	if err != nil {
		return
	}
	if _, err := h.store.CreateDecision(ctx, store.Decision{
		Kind: store.KindPlan, SessionKey: s.Key, ProjectID: x.ProjectID, PlanID: x.ID, Question: x.Title, Answer: text,
	}); err != nil {
		h.log.Warn("记录计划进展失败", "err", err)
	}
}

func stageName(st store.Stage) string { return fmt.Sprintf("阶段 %d「%s」", st.Seq, st.Title) }

// unlockStage 让阶段进入进行中：其中没做完的需求交给负责会话（关联、待开始并送达）；
// 没有负责会话时只把草稿改为待开始，等你手动交出。
func (h *Hub) unlockStage(ctx context.Context, x store.Plan, st store.Stage) error {
	if err := h.store.SetStageStatus(ctx, st.ID, store.StageActive, ""); err != nil {
		return err
	}
	for _, r := range st.Requirements {
		if r.Status == store.ReqDone {
			continue
		}
		if x.OwnerSession != "" {
			if _, err := h.assignQuiet(ctx, r.ID, x.OwnerSession, fmt.Sprintf("属于计划 %s「%s」%s。", x.Code, x.Title, stageName(st))); err == nil {
				continue
			}
		}
		if r.Status == store.ReqDraft {
			if _, err := h.store.SetRequirementStatus(ctx, r.ID, store.ReqReady); err != nil {
				return err
			}
		}
	}
	return nil
}

func unfinished(st store.Stage) []string {
	var out []string
	for _, r := range st.Requirements {
		if !r.Finished() {
			out = append(out, r.Code)
		}
	}
	return out
}

func withComment(text, comment string) string {
	if c := strings.TrimSpace(comment); c != "" {
		return text + "附言：" + c
	}
	return text
}

const stageHowTo = "阶段内需求都做完（待验收）后，用 stage_submit 交阶段小结；用户验收通过后，下一阶段会自动交给你。"

// startPlan 开始（或批准）一个计划：第一个阶段解锁。
func (h *Hub) startPlan(ctx context.Context, ref, comment string) (store.Plan, error) {
	x, err := h.store.GetPlan(ctx, ref)
	if err != nil {
		return x, apiErr(err)
	}
	if x.Status != store.PlanDraft && x.Status != store.PlanShelved {
		return x, huma.Error400BadRequest(x.Code + " 已经开始了")
	}
	if len(x.Stages) == 0 {
		return x, huma.Error400BadRequest("计划至少要有一个阶段")
	}
	if err := h.store.SetPlanStatus(ctx, x.ID, store.PlanActive); err != nil {
		return x, err
	}
	st, _ := x.Stage(x.Current)
	verb := "开始"
	if x.CreatedBy == "agent" {
		verb = "已获用户批准，开始"
	}
	h.planMsg(ctx, x, withComment(fmt.Sprintf("计划%s：共 %d 个阶段，现在是%s，其中的需求已交给你。%s", verb, len(x.Stages), stageName(st), stageHowTo), comment))
	if err := h.unlockStage(ctx, x, st); err != nil {
		return x, err
	}
	return h.planChanged(ctx, x.ID)
}

// rejectPlan 不批准 Agent 拆出的计划：搁置，并把附言送给它。
func (h *Hub) rejectPlan(ctx context.Context, ref, comment string) (store.Plan, error) {
	x, err := h.store.GetPlan(ctx, ref)
	if err != nil {
		return x, apiErr(err)
	}
	if x.Status != store.PlanDraft {
		return x, huma.Error400BadRequest(x.Code + " 不在待批准状态")
	}
	if err := h.store.SetPlanStatus(ctx, x.ID, store.PlanShelved); err != nil {
		return x, err
	}
	h.planMsg(ctx, x, withComment("用户没有批准这个计划（已搁置）。如需调整，请按附言重新用 plan_create 提交。", comment))
	return h.planChanged(ctx, x.ID)
}

// stageFor 取当前可操作的阶段：只能是计划的当前阶段。
func stageFor(x store.Plan, seq int) (store.Stage, error) {
	if x.Status != store.PlanActive {
		return store.Stage{}, huma.Error400BadRequest(x.Code + " 不在进行中")
	}
	st, ok := x.Stage(seq)
	if !ok {
		return st, huma.Error404NotFound(fmt.Sprintf("%s 没有阶段 %d", x.Code, seq))
	}
	if seq != x.Current {
		return st, huma.Error400BadRequest(fmt.Sprintf("%s 当前是阶段 %d，不能操作阶段 %d", x.Code, x.Current, seq))
	}
	return st, nil
}

// submitStage：Agent 交阶段小结，阶段进入待阶段验收。
func (h *Hub) submitStage(ctx context.Context, ref string, seq int, summary string) (store.Plan, error) {
	x, err := h.store.GetPlan(ctx, ref)
	if err != nil {
		return x, apiErr(err)
	}
	st, err := stageFor(x, seq)
	if err != nil {
		return x, err
	}
	if left := unfinished(st); len(left) > 0 {
		return x, huma.Error400BadRequest("阶段里还有需求没做完：" + strings.Join(left, "、") + "。做完并设为待验收后再交阶段小结")
	}
	if strings.TrimSpace(summary) == "" {
		return x, huma.Error400BadRequest("请写阶段小结：这一阶段做了什么、怎么验证的")
	}
	if err := h.store.SetStageStatus(ctx, st.ID, store.StageReview, strings.TrimSpace(summary)); err != nil {
		return x, err
	}
	h.sendNotify(x.ProjectID, notify.Message{Event: notify.EventRequirementReview, Title: fmt.Sprintf("待阶段验收 · %s 阶段 %d", x.Code, st.Seq), Body: x.Title + " · " + st.Title, Group: "kitestring"}, "/requirements?plan="+x.Code)
	return h.planChanged(ctx, x.ID)
}

// passStage：你验收通过整个阶段。还没单独验收的需求一并通过，下一阶段解锁；最后一个阶段通过则计划完成。
func (h *Hub) passStage(ctx context.Context, ref string, seq int, comment string) (store.Plan, error) {
	x, err := h.store.GetPlan(ctx, ref)
	if err != nil {
		return x, apiErr(err)
	}
	st, err := stageFor(x, seq)
	if err != nil {
		return x, err
	}
	if x.Paused {
		return x, huma.Error400BadRequest("前面已通过的阶段里有需求被重开了，修好并验收后再继续")
	}
	if left := unfinished(st); len(left) > 0 {
		return x, huma.Error400BadRequest("阶段里还有需求没做完：" + strings.Join(left, "、"))
	}
	for _, r := range st.Requirements {
		if r.Status == store.ReqReview {
			if _, err := h.store.SetRequirementStatus(ctx, r.ID, store.ReqDone); err != nil {
				return x, err
			}
		}
	}
	if err := h.store.SetStageStatus(ctx, st.ID, store.StagePassed, ""); err != nil {
		return x, err
	}
	next, ok := x.Stage(seq + 1)
	if !ok {
		if err := h.store.SetPlanStatus(ctx, x.ID, store.PlanDone); err != nil {
			return x, err
		}
		h.planMsg(ctx, x, withComment(stageName(st)+"已验收通过，计划全部完成。", comment))
		return h.planChanged(ctx, x.ID)
	}
	h.planMsg(ctx, x, withComment(fmt.Sprintf("%s已验收通过。现在进入%s，其中的需求已交给你。%s", stageName(st), stageName(next), stageHowTo), comment))
	if err := h.unlockStage(ctx, x, next); err != nil {
		return x, err
	}
	return h.planChanged(ctx, x.ID)
}

// reviseStage：退回整个阶段，附意见；阶段回到进行中。
func (h *Hub) reviseStage(ctx context.Context, ref string, seq int, comment string) (store.Plan, error) {
	x, err := h.store.GetPlan(ctx, ref)
	if err != nil {
		return x, apiErr(err)
	}
	st, err := stageFor(x, seq)
	if err != nil {
		return x, err
	}
	if strings.TrimSpace(comment) == "" {
		return x, huma.Error400BadRequest("请写下退回的意见")
	}
	if err := h.store.SetStageStatus(ctx, st.ID, store.StageActive, ""); err != nil {
		return x, err
	}
	h.planMsg(ctx, x, fmt.Sprintf("用户退回了%s：%s\n按意见修改后，重新用 stage_submit 交阶段小结。", stageName(st), strings.TrimSpace(comment)))
	return h.planChanged(ctx, x.ID)
}

// settlePlan 在编辑后整理进行中的计划：当前阶段还没解锁就解锁；全部通过就完成；完成后又加了阶段就重新进行。
func (h *Hub) settlePlan(ctx context.Context, x store.Plan) error {
	if x.Status != store.PlanActive && x.Status != store.PlanDone {
		return nil
	}
	if x.Current == 0 {
		if x.Status != store.PlanDone {
			return h.store.SetPlanStatus(ctx, x.ID, store.PlanDone)
		}
		return nil
	}
	if x.Status == store.PlanDone {
		if err := h.store.SetPlanStatus(ctx, x.ID, store.PlanActive); err != nil {
			return err
		}
	}
	if st, _ := x.Stage(x.Current); st.Status == store.StageLocked {
		return h.unlockStage(ctx, x, st)
	}
	return nil
}

// reopenStage 在阶段里的需求改状态后调用（before 是改之前的需求）：
// 需求离开待验收 / 已完成时，待阶段验收的阶段回到进行中——小结已经过时。
func (h *Hub) reopenStage(ctx context.Context, r store.Requirement, status string) {
	if r.PlanID == "" || r.StageStatus != store.StageReview || status == store.ReqReview || status == store.ReqDone {
		return
	}
	x, err := h.store.GetPlan(ctx, r.PlanID)
	if err != nil {
		return
	}
	if st, ok := x.Stage(r.StageSeq); ok {
		_ = h.store.SetStageStatus(ctx, st.ID, store.StageActive, "")
	}
}

// planChanged 在计划变化后一次性下发负责会话的送达（进展 + 交出的需求），并通知网页刷新。
func (h *Hub) planChanged(ctx context.Context, id string) (store.Plan, error) {
	x, err := h.store.GetPlan(ctx, id)
	if err != nil {
		return x, apiErr(err)
	}
	if s, err := h.store.GetSession(ctx, x.OwnerSession); err == nil {
		h.pushDecisions(ctx, s.DeviceID)
	}
	h.broker.publish("plans", "requirements", "decisions", "sessions")
	return x, nil
}

func (h *Hub) registerPlans(api huma.API) {
	tag := []string{"plans"}
	huma.Register(api, huma.Operation{OperationID: "list-plans", Method: http.MethodGet, Path: "/api/plans", Tags: tag},
		func(ctx context.Context, in *struct {
			ProjectID string `query:"project_id"`
		}) (*plansOut, error) {
			out := &plansOut{}
			var err error
			out.Body.Plans, err = h.store.ListPlans(ctx, in.ProjectID)
			return out, err
		})
	huma.Register(api, huma.Operation{OperationID: "create-plan", Method: http.MethodPost, Path: "/api/plans", Tags: tag, Summary: "新建计划（start 为真时立即开始）"},
		func(ctx context.Context, in *struct {
			Body planCreateBody
		}) (*planOut, error) {
			b := in.Body
			if len(b.Stages) == 0 {
				return nil, huma.Error400BadRequest("计划至少要有一个阶段")
			}
			stages, err := h.resolveStages(ctx, b.Stages, b.ProjectID, "user", "")
			if err != nil {
				return nil, err
			}
			x, err := h.store.CreatePlan(ctx, store.Plan{Title: b.Title, Goal: b.Goal, ProjectID: b.ProjectID, OwnerSession: b.OwnerSession, CreatedBy: "user"}, stages)
			if err != nil {
				return nil, apiErr(err)
			}
			if b.Start {
				x, err = h.startPlan(ctx, x.ID, "")
				if err != nil {
					return nil, err
				}
				return &planOut{Body: x}, nil
			}
			x, err = h.planChanged(ctx, x.ID)
			return &planOut{Body: x}, err
		})
	huma.Register(api, huma.Operation{OperationID: "get-plan", Method: http.MethodGet, Path: "/api/plans/{ref}", Tags: tag},
		func(ctx context.Context, in *refPath) (*planOut, error) {
			x, err := h.store.GetPlan(ctx, in.Ref)
			return &planOut{Body: x}, apiErr(err)
		})
	huma.Register(api, huma.Operation{OperationID: "update-plan", Method: http.MethodPut, Path: "/api/plans/{ref}", Tags: tag, Summary: "编辑计划；给出 stages 时按它重排阶段与需求"},
		func(ctx context.Context, in *struct {
			Ref  string `path:"ref"`
			Body planFields
		}) (*planOut, error) {
			cur, err := h.store.GetPlan(ctx, in.Ref)
			if err != nil {
				return nil, apiErr(err)
			}
			b := in.Body
			var stages []store.StageInput
			if b.Stages != nil {
				if stages, err = h.resolveStages(ctx, b.Stages, b.ProjectID, "user", ""); err != nil {
					return nil, err
				}
			}
			x, err := h.store.UpdatePlan(ctx, store.Plan{ID: cur.ID, Title: b.Title, Goal: b.Goal, ProjectID: b.ProjectID, OwnerSession: b.OwnerSession}, stages)
			if err != nil {
				return nil, apiErr(err)
			}
			if err := h.settlePlan(ctx, x); err != nil {
				return nil, apiErr(err)
			}
			x, err = h.planChanged(ctx, x.ID)
			return &planOut{Body: x}, err
		})
	huma.Register(api, huma.Operation{OperationID: "delete-plan", Method: http.MethodDelete, Path: "/api/plans/{ref}", Tags: tag, Summary: "删除计划（其中的需求保留）"},
		func(ctx context.Context, in *refPath) (*okBody, error) {
			x, err := h.store.GetPlan(ctx, in.Ref)
			if err != nil {
				return nil, apiErr(err)
			}
			if err := h.store.DeletePlan(ctx, x.ID); err != nil {
				return nil, apiErr(err)
			}
			h.broker.publish("plans", "requirements")
			return ok(), nil
		})
	huma.Register(api, huma.Operation{OperationID: "start-plan", Method: http.MethodPost, Path: "/api/plans/{ref}/start", Tags: tag, Summary: "开始 / 批准计划：第一个阶段解锁并交给负责会话"},
		func(ctx context.Context, in *struct {
			Ref string `path:"ref"`
			Body commentBody
		}) (*planOut, error) {
			x, err := h.startPlan(ctx, in.Ref, in.Body.Comment)
			return &planOut{Body: x}, err
		})
	huma.Register(api, huma.Operation{OperationID: "reject-plan", Method: http.MethodPost, Path: "/api/plans/{ref}/reject", Tags: tag, Summary: "不批准 Agent 拆出的计划（搁置，附言送达）"},
		func(ctx context.Context, in *struct {
			Ref string `path:"ref"`
			Body commentBody
		}) (*planOut, error) {
			x, err := h.rejectPlan(ctx, in.Ref, in.Body.Comment)
			return &planOut{Body: x}, err
		})
	huma.Register(api, huma.Operation{OperationID: "pass-stage", Method: http.MethodPost, Path: "/api/plans/{ref}/stages/{seq}/pass", Tags: tag, Summary: "验收通过整个阶段，下一阶段自动开始"},
		func(ctx context.Context, in *struct {
			Ref string `path:"ref"`
			Seq int    `path:"seq"`
			Body commentBody
		}) (*planOut, error) {
			x, err := h.passStage(ctx, in.Ref, in.Seq, in.Body.Comment)
			return &planOut{Body: x}, err
		})
	huma.Register(api, huma.Operation{OperationID: "revise-stage", Method: http.MethodPost, Path: "/api/plans/{ref}/stages/{seq}/revise", Tags: tag, Summary: "退回整个阶段（附意见）"},
		func(ctx context.Context, in *struct {
			Ref string `path:"ref"`
			Seq int    `path:"seq"`
			Body commentBody
		}) (*planOut, error) {
			x, err := h.reviseStage(ctx, in.Ref, in.Seq, in.Body.Comment)
			return &planOut{Body: x}, err
		})
}

func (h *Hub) registerAgentPlans(api huma.API) {
	tag := []string{"agent"}
	huma.Register(api, huma.Operation{OperationID: "agent-create-plan", Method: http.MethodPost, Path: "/api/agent/plans", Tags: tag, Summary: "Agent 把大目标拆成计划，等用户批准后开工"},
		func(ctx context.Context, in *struct {
			Body struct {
				Title      string    `json:"title" minLength:"1"`
				Goal       string    `json:"goal,omitempty"`
				Stages     []stageIn `json:"stages" minItems:"1"`
				SessionKey string    `json:"session_key,omitempty"`
			}
		}) (*planOut, error) {
			b := in.Body
			project := ""
			if b.SessionKey != "" {
				if x, err := h.store.GetSession(ctx, b.SessionKey); err == nil {
					project = x.ProjectID
				}
			}
			for _, st := range b.Stages {
				for _, ri := range st.Requirements {
					if ri.Ref != "" {
						continue
					}
					if strings.TrimSpace(ri.Title) == "" {
						return nil, huma.Error400BadRequest("阶段里的新需求需要标题")
					}
				}
			}
			stages, err := h.resolveStages(ctx, b.Stages, project, "agent", b.SessionKey)
			if err != nil {
				return nil, err
			}
			x, err := h.store.CreatePlan(ctx, store.Plan{Title: b.Title, Goal: b.Goal, ProjectID: project, OwnerSession: b.SessionKey, CreatedBy: "agent"}, stages)
			if err != nil {
				return nil, apiErr(err)
			}
			h.sendNotify(x.ProjectID, notify.Message{Event: notify.EventDecisionRaised, Title: "待批准计划 · " + x.Code, Body: x.Title, Group: "kitestring"}, "/requirements?plan="+x.Code)
			x, err = h.planChanged(ctx, x.ID)
			return &planOut{Body: x}, err
		})
	huma.Register(api, huma.Operation{OperationID: "agent-get-plan", Method: http.MethodGet, Path: "/api/agent/plans/{ref}", Tags: tag},
		func(ctx context.Context, in *refPath) (*planOut, error) {
			x, err := h.store.GetPlan(ctx, in.Ref)
			return &planOut{Body: x}, apiErr(err)
		})
	huma.Register(api, huma.Operation{OperationID: "agent-submit-stage", Method: http.MethodPost, Path: "/api/agent/plans/{ref}/stages/{seq}/submit", Tags: tag, Summary: "交阶段小结，阶段进入待阶段验收"},
		func(ctx context.Context, in *struct {
			Ref  string `path:"ref"`
			Seq  int    `path:"seq"`
			Body struct {
				Summary    string `json:"summary"`
				SessionKey string `json:"session_key,omitempty"`
			}
		}) (*planOut, error) {
			x, err := h.submitStage(ctx, in.Ref, in.Seq, in.Body.Summary)
			return &planOut{Body: x}, err
		})
}
