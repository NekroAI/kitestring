package hub

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NekroAI/kitestring/internal/blocks"

	"github.com/NekroAI/kitestring/internal/roles"
	"github.com/NekroAI/kitestring/internal/store"
)

// 审查员（R-88）：你在网页上把一条需求交给本项目的审查员会话，它做审查分析，结果作为“审查意见”挂在需求上。
// 审查是旁路：不改需求状态、不关联会话（不会因此成为需求的负责会话或排队对象）、不拦截验收；审查员也不是必须参与。
// R-232 起评审还做验证（逐条实测、截图，checklist 挂到需求上），交验收时自动派给在线评审；交给评审的待验收需求，
// 评审交意见前不进用户的「要你处理」，最多等 30 分钟（R-237，见 verify.go）。
// 审查意见存在 evidence 表里（kind = review），读出时和证据分开（store.ListReviews）。

// reviewTaskNote 是送达给审查员的说明，提醒它这是审查任务。
const reviewTaskNote = "这是审查任务（审查 + 验证）：读代码和改动找问题（正确性、边界情况、潜在风险），再按验收标准逐条实测——跑全部测试、起演示环境（真实数据快照、独立端口和 /tmp 下的短路径，不碰线上服务）、操作界面、截图。只验证：不要改代码，不要改需求状态，也不要 session_link_req。做完用 review_submit 交：verdict 一句结论，blocks 里放一个 checklist 逐条 pass / fail / ask 加一句依据，附截图（每张一句话标题，用 boxes 框出问题或改动处）；旧版 MCP 的 review_submit 没有这些字段时，用命令行 ks req review <R-编号> --verdict <结论> --blocks <清单.json> --image <截图>。有不通过的，纸鸢自动通知主调度退回给开发修；验收仍由用户决定。"

// askReview 把需求交给审查员：只送达一条审查任务，需求状态、关联会话都不动。auto：交验收时纸鸢自动派的（R-232）。
func (h *Hub) askReview(ctx context.Context, ref, sessionKey, comment string, auto bool) (store.Decision, error) {
	r, err := h.store.GetRequirement(ctx, ref)
	if err != nil {
		return store.Decision{}, apiErr(err)
	}
	x, err := h.store.GetSession(ctx, sessionKey)
	if err != nil {
		return store.Decision{}, apiErr(err)
	}
	if x.Role != roles.Reviewer {
		return store.Decision{}, huma.Error400BadRequest("这个会话不是 Rev：先在它的对话里运行 ks role reviewer")
	}
	if x.State == store.StateEnded || x.State == store.StateLost {
		return store.Decision{}, huma.Error400BadRequest("这个 Rev 会话已结束")
	}
	if r.ProjectID != "" && x.ProjectID != r.ProjectID {
		return store.Decision{}, huma.Error400BadRequest(fmt.Sprintf("这个 Rev 会话不属于 %s 所在的项目", r.Code))
	}
	if r.Status == store.ReqShelved {
		return store.Decision{}, huma.Error400BadRequest(fmt.Sprintf("%s 已搁置，不用审查", r.Code))
	}
	body := []string{reviewTaskNote, "需求当前状态：" + reqStatusZh[r.Status] + "（保持不变）。"}
	if r.Status == store.ReqReview {
		body = append(body, fmt.Sprintf("你交意见前，这条需求不进用户的「要你处理」；%d 分钟内没交意见会照常弹给用户，标「评审未完成」。", int(store.ReviewWaitLimit/time.Minute)))
	}
	if c := strings.TrimSpace(comment); c != "" && auto {
		body = append(body, c)
	} else if c != "" {
		body = append(body, "用户交代："+c)
	}
	if d := strings.TrimSpace(r.Detail); d != "" {
		body = append(body, "说明："+d)
	}
	if a := strings.TrimSpace(r.Acceptance); a != "" {
		body = append(body, "验收标准："+a)
	}
	body = append(body, "证据、修改意见等完整内容用 req_get 查看。")
	d, err := h.store.CreateDecision(ctx, store.Decision{
		Kind: store.KindReviewTask, SessionKey: x.Key, ProjectID: r.ProjectID, RequirementID: r.ID,
		Question: r.Title, Answer: strings.Join(body, "\n"), AnswerFiles: r.Files, AssignedBy: autoMark(auto),
	})
	if err != nil {
		return d, apiErr(err)
	}
	h.pushDecisions(ctx, x.DeviceID)
	h.broker.publish("requirements", "decisions", "sessions")
	return d, nil
}

var reqStatusZh = map[string]string{store.ReqDraft: "草稿", store.ReqReady: "待开始", store.ReqInProgress: "进行中", store.ReqBlocked: "受阻", store.ReqReview: "待验收", store.ReqDone: "已完成", store.ReqShelved: "已搁置"}

func autoMark(auto bool) string {
	if auto {
		return store.ReviewAuto
	}
	return ""
}

// reviewBody 是评审交的意见：正文和 R-97 的结构化字段（一句结论、checklist 等表达块、带标题和框的截图）。
type reviewBody struct {
	SessionKey string   `json:"session_key" doc:"提交意见的会话（身份必须是 reviewer）"`
	Summary    string   `json:"summary,omitempty" doc:"审查意见正文（可用 Markdown）；给了 verdict / blocks 时可省略，由 hub 拼出纯文本"`
	Files      []string `json:"files,omitempty" doc:"附图 ID（先上传到 /api/agent/files）"`
	Images     []any    `json:"images,omitempty" doc:"带标题与框的截图（R-183）：[{id: 附件 ID, title: 一句话标题, boxes: [{x, y, w, h, label, criterion}]}]，框用图片原始尺寸的比例坐标（0～1）"`
	Verdict    string   `json:"verdict,omitempty" doc:"一句结论（40 字以内）"`
	Details    string   `json:"details,omitempty" doc:"怎么验的（跑了哪些测试、演示环境怎么起的），默认折叠"`
	Blocks     []any    `json:"blocks,omitempty" doc:"表达块（R-97）：验证结果放一个 checklist，逐条 pass / fail / ask 加一句依据；也可以附 figure / compare 截图。不合法的块丢掉并在 block_warnings 里说明，意见照常保存"`
}

type reviewSubmitOut struct {
	Body struct {
		store.Requirement
		ReviewID      string   `json:"review_id"`
		BlockWarnings []string `json:"block_warnings,omitempty"`
		ImageHints    []string `json:"image_hints,omitempty"`
	}
}

// submitReview 审查员交审查意见 / 验证结果：只记录，不改需求状态。只有审查员身份的会话能交。
func (h *Hub) submitReview(ctx context.Context, ref string, b reviewBody) (*reviewSubmitOut, error) {
	sessionKey := b.SessionKey
	if sessionKey == "" {
		return nil, huma.Error400BadRequest("无法识别当前会话：审查意见必须由 Rev 会话提交")
	}
	x, err := h.store.GetSession(ctx, sessionKey)
	if err != nil {
		return nil, apiErr(err)
	}
	if x.Role != roles.Reviewer {
		return nil, huma.Error403Forbidden("只有 Rev 会话能交审查意见（ks role reviewer）；验收证据请用 evidence_attach")
	}
	r, err := h.store.GetRequirement(ctx, ref)
	if err != nil {
		return nil, apiErr(err)
	}
	if r.ProjectID != "" && x.ProjectID != r.ProjectID {
		return nil, huma.Error400BadRequest(fmt.Sprintf("%s 不属于本会话所在的项目", r.Code))
	}
	files, err := h.store.ResolveFiles(ctx, b.Files)
	if err != nil {
		return nil, err
	}
	shots, hints := blocks.ParseImages(b.Images, h.blockFile(ctx))
	files = mergeFiles(files, shots)
	payload, warns := blocks.ParsePayload(b.Verdict, nil, b.Details, nil, b.Blocks, h.blockFile(ctx))
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
		return nil, huma.Error400BadRequest("审查意见需要正文：summary，或 verdict（一句结论）加 blocks（checklist）")
	}
	added, err := h.store.AddEvidence(ctx, store.Evidence{RequirementID: r.ID, SessionKey: x.Key, Kind: store.EvidenceReview, Summary: summary, Files: files, Payload: payload})
	if err != nil {
		return nil, apiErr(err)
	}
	h.refreshDelivered(ctx, r)
	h.pushDecisions(ctx, x.DeviceID) // 审查任务交完意见后不再在压缩 / 恢复时补发
	h.broker.publish("requirements", "decisions")
	r, err = h.store.GetRequirement(ctx, r.ID)
	if err != nil {
		return nil, apiErr(err)
	}
	h.onReviewSubmitted(ctx, r, x, added.ID)
	out := &reviewSubmitOut{}
	out.Body.Requirement, out.Body.ReviewID, out.Body.BlockWarnings, out.Body.ImageHints = r, added.ID, warns, hints
	return out, nil
}

// reviewDone 判断审查员在收到这条审查任务之后是否已经交过意见。
func reviewDone(r store.Requirement, d store.Decision) bool {
	for _, e := range r.Reviews {
		if e.SessionKey == d.SessionKey && !e.CreatedAt.Before(d.CreatedAt) {
			return true
		}
	}
	return false
}

func (h *Hub) registerReviewerWeb(api huma.API) {
	huma.Register(api, huma.Operation{OperationID: "ask-review", Method: http.MethodPost, Path: "/api/requirements/{ref}/reviewer", Tags: []string{"requirements"}, Summary: "把需求交给 Rev 审查（R-88）：只送达审查任务，不改状态、不关联会话"},
		func(ctx context.Context, in *struct {
			Ref  string `path:"ref"`
			Body struct {
				SessionKey string `json:"session_key" minLength:"1" doc:"Rev 会话键（身份必须是 reviewer）"`
				Comment    string `json:"comment,omitempty" doc:"交代给 Rev 的话，例如重点看哪里"`
			}
		}) (*decisionOut, error) {
			d, err := h.askReview(ctx, in.Ref, in.Body.SessionKey, in.Body.Comment, false)
			if err != nil {
				return nil, err
			}
			return &decisionOut{Body: d}, nil
		})
}

func (h *Hub) registerReviewerAgent(api huma.API) {
	huma.Register(api, huma.Operation{OperationID: "agent-submit-review", Method: http.MethodPost, Path: "/api/agent/requirements/{ref}/reviews", Tags: []string{"agent"}, Summary: "Rev 交审查意见 / 验证结果（R-88、R-232）：不改需求状态；交给评审的待验收需求这时进用户的验收卡，有不通过的通知主调度"},
		func(ctx context.Context, in *struct {
			Ref  string `path:"ref"`
			Body reviewBody
		}) (*reviewSubmitOut, error) {
			return h.submitReview(ctx, in.Ref, in.Body)
		})
}
