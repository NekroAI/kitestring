package hub

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NekroAI/kitestring/internal/notify"
	"github.com/NekroAI/kitestring/internal/protocol"
	"github.com/NekroAI/kitestring/internal/store"
)

// 资源库批量审核（R-393）：
//   - 申请标为已在用（ks asset push --baseline）：推送后素材先挂在一条申请上，同一次推送提一条批准类决策给用户（件数、类别分布、
//     几张代表缩略图）；用户批准一次整批记为通过（审核记录写明是用户批准的历史导入，不给会话发意见），拒绝则转为普通待审（D-2074）。
//     批准类决策不能代答，所以 AI 自己跳不过审核。
//   - 批量通过 / 返工（网页，和用户授权的会话 ks asset review）：按素材列表或筛选条件，先预检件数，再一个事务写完；
//     意见按收件会话合并成一条送达；可撤销最近一次。

// ---------- 申请标为已在用 ----------

// startAssetBaseline：--baseline 推送入库后登记申请并提批准类决策。失败只记日志、在结果里注明，素材照常留在待审。
func (h *Hub) startAssetBaseline(ctx context.Context, p protocol.AssetPush, proj store.Project, sub store.AssetSubmitter, res protocol.AssetPushResult) *protocol.AssetBaselineInfo {
	codes := make([]string, 0, len(res.Items))
	for _, it := range res.Items {
		codes = append(codes, it.Code)
	}
	b, items, replay, err := h.store.StartAssetBaseline(ctx, p.PushID, proj.ID, sub, codes)
	if err != nil {
		h.log.Warn("登记「申请标为已在用」失败", "err", err)
		return &protocol.AssetBaselineInfo{Note: "申请没有登记成功（" + err.Error() + "），素材留在待审"}
	}
	if b.ID == "" {
		return &protocol.AssetBaselineInfo{Note: "这次推送的素材都已通过或在返工中，没有要申请标为已在用的"}
	}
	if replay {
		info := &protocol.AssetBaselineInfo{Count: len(items)}
		if d, err := h.store.GetDecision(ctx, b.DecisionID); err == nil {
			info.Decision = d.Code
		}
		return info
	}
	d, err := h.store.CreateDecision(ctx, store.Decision{
		Kind: store.KindDecision, SessionKey: p.SessionKey, ProjectID: proj.ID,
		Question: fmt.Sprintf("把 %d 件历史素材标为已在用（直接通过）", len(items)),
		Context:  baselineContext(proj, sub, items), ContextFiles: h.baselineThumbs(ctx, items),
		Approval: true, Options: []string{"批准", "拒绝"}, Risk: store.RiskHigh,
	})
	if err != nil {
		h.log.Warn("提「申请标为已在用」的决策失败", "err", err)
		return &protocol.AssetBaselineInfo{Count: len(items), Note: "决策没有提成功（" + err.Error() + "），素材留在待审"}
	}
	if err := h.store.LinkAssetBaseline(ctx, b.ID, d.ID); err != nil {
		h.log.Warn("记录「申请标为已在用」的决策失败", "err", err)
	}
	h.broker.publish("decisions", "sessions", "requirements", "assets")
	go h.aiOnDecision(d)
	h.onDecisionRaised(ctx, d)
	h.sendNotify(d.ProjectID, notify.Message{Event: notify.EventDecisionRaised, Title: pushDecisionTitle(d.Code), Body: d.Question, Group: "kitestring"}, decisionPath(d), h.decisionPush(ctx, d))
	return &protocol.AssetBaselineInfo{Decision: d.Code, Count: len(items)}
}

// baselineContext：申请的说明——谁推的、批准 / 拒绝各是什么结果、类别分布、前几件素材。
func baselineContext(proj store.Project, sub store.AssetSubmitter, items []store.BaselineItem) string {
	var b strings.Builder
	who := "会话"
	if sub.Name != "" {
		who += "「" + sub.Name + "」"
	}
	fmt.Fprintf(&b, "%s推送到项目「%s」时申请把这 %d 件素材标为已在用（游戏里已经在用的历史素材）。\n", who, proj.Name, len(items))
	b.WriteString("批准：整批记为通过，不进待审，审核记录写明是你批准的历史导入，不给会话发意见；之后有新版本照常回到待审。\n")
	b.WriteString("拒绝：素材留在待审，照常逐件审。\n")
	count := map[string]int{}
	var cats []string
	for _, it := range items {
		c := it.Category
		if c == "" {
			c = "未分类"
		}
		if count[c] == 0 {
			cats = append(cats, c)
		}
		count[c]++
	}
	slices.SortStableFunc(cats, func(x, y string) int { return count[y] - count[x] })
	parts := make([]string, 0, len(cats))
	for _, c := range cats {
		parts = append(parts, fmt.Sprintf("%s %d", c, count[c]))
	}
	b.WriteString("类别：" + strings.Join(parts, "、") + "\n素材：")
	const show = 10
	names := make([]string, 0, show)
	for i, it := range items {
		if i == show {
			break
		}
		names = append(names, it.Code+" "+it.Name())
	}
	b.WriteString(strings.Join(names, "、"))
	if len(items) > show {
		fmt.Fprintf(&b, " 等 %d 件", len(items))
	}
	return b.String()
}

// baselineThumbs：几张代表缩略图，尽量每个类别取一张，最多 4 张。
func (h *Hub) baselineThumbs(ctx context.Context, items []store.BaselineItem) []store.FileRef {
	const max = 4
	var out []store.FileRef
	used := map[string]bool{}
	for pass := 0; pass < 2 && len(out) < max; pass++ {
		for _, it := range items {
			if len(out) == max {
				break
			}
			if it.Thumb == "" || used[it.Thumb] || (pass == 0 && used["cat:"+it.Category]) {
				continue
			}
			f, err := h.store.GetFile(ctx, it.Thumb)
			if err != nil || !protocol.IsImage(f.Mime) {
				continue
			}
			used[it.Thumb], used["cat:"+it.Category] = true, true
			out = append(out, f)
		}
	}
	return out
}

// applyAssetBaseline：用户答了「申请标为已在用」的决策——批准整批记为通过，拒绝转为普通待审。不是这类决策时什么都不做。
func (h *Hub) applyAssetBaseline(ctx context.Context, d store.Decision) {
	if !d.Approval || d.Verdict == "" {
		return
	}
	b, err := h.store.AssetBaselineByDecision(ctx, d.ID)
	if err != nil {
		return
	}
	n, err := h.store.DecideAssetBaseline(ctx, b.ID, d.Verdict == store.VerdictApproved, d.Code)
	if err != nil {
		h.log.Warn("按答复处理「申请标为已在用」失败", "decision", d.Code, "err", err)
		return
	}
	h.log.Info("处理了「申请标为已在用」", "decision", d.Code, "verdict", d.Verdict, "approved", n)
	h.broker.publish("assets")
}

// ---------- 批量通过 / 返工 ----------

// AssetBatchFilter 是批量审核的筛选条件（与资源库列表的筛选相同）。
type AssetBatchFilter struct {
	Project  string `json:"project,omitempty" doc:"项目 ID（Agent 用时固定为会话所在项目）"`
	Batch    string `json:"batch,omitempty"`
	Category string `json:"category,omitempty"`
	Status   string `json:"status,omitempty" enum:"wip,pending,approved,rework,"`
	Kind     string `json:"kind,omitempty" enum:"part,image,"`
	ID       string `json:"id,omitempty" doc:"素材 id 或编号 A-x；末尾带 * 按前缀查"`
}

func (f AssetBatchFilter) store() store.AssetFilter {
	return store.AssetFilter{ProjectID: f.Project, Batch: f.Batch, Category: f.Category, Status: f.Status, Kind: f.Kind, ID: f.ID}
}

// AssetBatchIn 是一次批量审核的请求。
type AssetBatchIn struct {
	Assets  []string         `json:"assets,omitempty" maxItems:"1000" doc:"素材：编号 A-x、ID 或素材 id；不给时按 filter 选"`
	Filter  AssetBatchFilter `json:"filter,omitempty" doc:"筛选条件：当前筛选结果全选（与 assets 同时给时取交集）"`
	Verdict string           `json:"verdict" enum:"approved,rework" doc:"approved 通过 / rework 返工"`
	Comment string           `json:"comment,omitempty" maxLength:"20000" doc:"共用的意见；返工时必填（或附图）"`
	Files   []string         `json:"files,omitempty" maxItems:"20" doc:"附图（附件 ID）"`
	DryRun  bool             `json:"dry_run,omitempty" doc:"只预检：返回会改几件、跳过哪些，不写入"`
}

// AssetBatchOut 是批量审核的结果（预检时只有件数和跳过的）。
type AssetBatchOut struct {
	DryRun   bool              `json:"dry_run"`
	Count    int               `json:"count" doc:"会改（预检）/ 改了的件数"`
	Assets   []string          `json:"assets" doc:"会改 / 改了的素材编号"`
	Skipped  []store.AssetSkip `json:"skipped" doc:"跳过的素材和原因（已是这个状态、状态不能这样改、预检后变了）"`
	BatchID  string            `json:"batch_id,omitempty" doc:"这次批量审核的 ID，撤销用"`
	Messages int               `json:"messages" doc:"意见合并后送出的条数（每个收件会话一条）"`
}

type assetBatchOut struct{ Body AssetBatchOut }

// AssetBatchUndoOut 是撤销的结果。
type AssetBatchUndoOut struct {
	Batch    store.AssetBatch  `json:"batch"`
	Restored []string          `json:"restored" doc:"还原了的素材编号"`
	Kept     []store.AssetSkip `json:"kept" doc:"之后又有新版本或被改过、没有还原的素材"`
}

type assetBatchUndoOut struct{ Body AssetBatchUndoOut }

// reviewAssetBatch：选素材 → 预检 → 写入 → 意见按收件会话合并送达。projectID 不为空时只在这个项目里选（Agent）。
func (h *Hub) reviewAssetBatch(ctx context.Context, in AssetBatchIn, projectID string, author store.AssetAuthor) (AssetBatchOut, error) {
	out := AssetBatchOut{DryRun: in.DryRun, Assets: []string{}, Skipped: []store.AssetSkip{}}
	if in.Verdict != store.AssetApproved && in.Verdict != store.AssetRework {
		return out, huma.Error400BadRequest("审核结论只能是 approved（通过）或 rework（返工）")
	}
	if in.Verdict == store.AssetRework && strings.TrimSpace(in.Comment) == "" && len(in.Files) == 0 {
		return out, huma.Error400BadRequest("返工需写明修改意见")
	}
	f := in.Filter.store()
	if projectID != "" {
		f.ProjectID = projectID
	}
	if len(in.Assets) == 0 && f == (store.AssetFilter{ProjectID: f.ProjectID}) {
		return out, huma.Error400BadRequest("要指定素材或筛选条件（批次、类别、状态、类型、素材 id）")
	}
	xs, err := h.store.AssetTargets(ctx, projectID, in.Assets, f)
	if err != nil {
		if errors.Is(err, store.ErrAssetAmbiguous) {
			return out, huma.Error409Conflict(err.Error())
		}
		return out, huma.Error400BadRequest(err.Error())
	}
	if len(xs) > store.MaxAssetBatch {
		return out, huma.Error400BadRequest(fmt.Sprintf("一次最多审 %d 件，符合条件的超过了：缩小筛选范围后分批审", store.MaxAssetBatch))
	}
	ok, skip := store.PlanAssetBatch(xs, in.Verdict)
	out.Skipped = append(out.Skipped, skip...)
	if in.DryRun || len(ok) == 0 {
		for _, x := range ok {
			out.Assets = append(out.Assets, x.Code)
		}
		out.Count = len(ok)
		return out, nil
	}
	files, err := h.store.ResolveFiles(ctx, in.Files)
	if err != nil {
		return out, apiErr(err)
	}
	done, err := h.store.ReviewAssetBatch(ctx, store.NewAssetBatch{Assets: ok, Verdict: in.Verdict, Comment: in.Comment, Files: files, Author: author})
	if err != nil {
		return out, apiErr(err)
	}
	out.Skipped = append(out.Skipped, done.Skipped...)
	out.BatchID, out.Count = done.BatchID, len(done.Assets)
	for _, x := range done.Assets {
		out.Assets = append(out.Assets, x.Code)
	}
	out.Messages = h.sendAssetBatch(ctx, done, author)
	h.broker.publish("assets")
	return out, nil
}

// batchTarget 是一个收件会话和送给它的那几件。
type batchTarget struct {
	key, as, name string
	project       string
	assets        []store.AssetBrief
	reviews       []string
	pmFor         []string // 转给主调度时：提交这些素材的会话名
}

// sendAssetBatch：同一个收件会话的意见合并成一条送达（R-393 第 3 条）。返回送出的条数。
func (h *Hub) sendAssetBatch(ctx context.Context, done store.AssetBatchDone, author store.AssetAuthor) int {
	if len(done.Reviews) == 0 {
		return 0
	}
	targets := map[string]*batchTarget{}
	var order []string
	cache := map[string][3]string{} // 提交者 + 项目 → 收件人
	for i, x := range done.Assets {
		ck := x.Submitter.Kind + "|" + x.Submitter.Ref + "|" + x.ProjectID
		to, ok := cache[ck]
		if !ok {
			key, as, name := h.assetReviewTarget(ctx, store.Asset{ProjectID: x.ProjectID, Submitter: x.Submitter})
			to = [3]string{key, as, name}
			cache[ck] = to
		}
		if to[0] == "" {
			continue
		}
		t := targets[to[0]]
		if t == nil {
			t = &batchTarget{key: to[0], as: to[1], name: to[2], project: x.ProjectID}
			targets[to[0]] = t
			order = append(order, to[0])
		}
		t.assets = append(t.assets, x)
		t.reviews = append(t.reviews, done.Reviews[i].ID)
		if to[1] == store.SentToPM && x.Submitter.Name != "" && !slices.Contains(t.pmFor, x.Submitter.Name) {
			t.pmFor = append(t.pmFor, x.Submitter.Name)
		}
	}
	r := done.Reviews[0]
	sent := 0
	for _, k := range order {
		t := targets[k]
		dec, err := h.store.CreateDecision(ctx, store.Decision{Kind: store.KindAssetReview, SessionKey: t.key, ProjectID: t.project,
			Question: fmt.Sprintf("批量审核 %d 件素材", len(t.assets)), Answer: assetBatchText(t, r, author), AnswerFiles: r.Files,
			FYI: r.Verdict == store.AssetApproved})
		if err != nil {
			h.log.Warn("记录批量审核意见的送达失败", "to", t.key, "err", err)
			continue
		}
		if err := h.store.LinkAssetReviews(ctx, t.reviews, dec.ID, t.key, t.as, t.name); err != nil {
			h.log.Warn("记录批量审核意见的收件人失败", "to", t.key, "err", err)
		}
		h.decisionsChanged(ctx, dec)
		sent++
	}
	return sent
}

// assetBatchText 是批量审核合并送给一个会话的一段：第一句是事实，然后是共用的意见和逐件列表。
func assetBatchText(t *batchTarget, r store.AssetReview, author store.AssetAuthor) string {
	var b strings.Builder
	who := "用户"
	if author.Kind == store.SubmitterSession {
		who = "会话"
		if author.Name != "" {
			who += "「" + author.Name + "」"
		}
		who += "（经用户授权）"
	}
	verdict := "通过"
	if r.Verdict == store.AssetRework {
		verdict = "返工"
	}
	fmt.Fprintf(&b, "%s批量审核：%d 件素材%s", who, len(t.assets), verdict)
	switch {
	case r.Comment != "" && r.Verdict == store.AssetRework:
		b.WriteString("，意见：" + r.Comment)
	case r.Comment != "":
		b.WriteString("。附言：" + r.Comment)
	default:
		b.WriteString("。")
	}
	for _, x := range t.assets {
		fmt.Fprintf(&b, "\n  %s「%s」v%d", x.Code, x.Name(), x.Version)
	}
	if t.as == store.SentToPM {
		who := "提交素材的会话"
		if len(t.pmFor) > 0 {
			who += "「" + strings.Join(t.pmFor, "」「") + "」"
		}
		b.WriteString("\n" + who + "已结束，审核意见转给主调度")
		if r.Verdict == store.AssetRework {
			b.WriteString("：请安排会话按意见修改后重新推送")
		}
		b.WriteString("。")
	}
	return b.String()
}

// undoAssetBatch：撤销 author 最近一次批量审核（batchID 不为空时撤销指定的那次，须是 author 自己的）。
// 被撤销的意见：还没送达的关闭；已送达的再补一条「已撤销」告诉收件会话。
func (h *Hub) undoAssetBatch(ctx context.Context, batchID string, author store.AssetAuthor) (AssetBatchUndoOut, error) {
	out := AssetBatchUndoOut{Restored: []string{}, Kept: []store.AssetSkip{}}
	latest, err := h.store.LatestAssetBatch(ctx, author)
	if errors.Is(err, store.ErrNotFound) {
		return out, huma.Error404NotFound("没有可撤销的批量审核")
	}
	if err != nil {
		return out, err
	}
	if batchID != "" && batchID != latest.ID {
		return out, huma.Error409Conflict("只能撤销最近一次批量审核")
	}
	u, err := h.store.UndoAssetBatch(ctx, latest.ID)
	if err != nil {
		return out, apiErr(err)
	}
	out.Batch = u.Batch
	byDec := map[string][]string{}
	for _, x := range u.Restored {
		out.Restored = append(out.Restored, x.Code)
	}
	out.Kept = append(out.Kept, u.Kept...)
	for _, id := range u.Decisions {
		d, err := h.store.GetDecision(ctx, id)
		if err != nil {
			continue
		}
		left, _ := h.store.AssetReviewsByDecision(ctx, id)
		switch {
		case d.Status == store.DecAnswered && len(left) == 0: // 还没送达：直接关闭，会话不会收到
			if d, err := h.store.CloseDecision(ctx, id, "批量审核已撤销", ""); err == nil {
				h.decisionsChanged(ctx, d)
			}
		case d.SessionKey != "":
			byDec[d.SessionKey] = append(byDec[d.SessionKey], d.Code)
		}
	}
	for key, codes := range byDec {
		text := fmt.Sprintf("刚才的批量审核（%s）已撤销：%s 恢复为审核前的状态，按那条意见做的修改先停下", strings.Join(codes, "、"), strings.Join(out.Restored, "、"))
		if len(out.Kept) > 0 {
			text += "；之后又改过的素材不受影响"
		}
		d, err := h.store.CreateDecision(ctx, store.Decision{Kind: store.KindAssetReview, SessionKey: key, Question: "批量审核已撤销", Answer: text + "。"})
		if err == nil {
			h.decisionsChanged(ctx, d)
		}
	}
	h.broker.publish("assets")
	return out, nil
}

// ---------- 接口 ----------

type assetReviewersOut struct {
	Body struct {
		Sessions []string `json:"sessions" doc:"授权了批量审素材的会话键"`
	}
}

func (h *Hub) registerAssetBatchWeb(api huma.API) {
	tag := []string{"assets"}
	huma.Register(api, huma.Operation{OperationID: "review-asset-batch", Method: http.MethodPost, Path: "/api/asset-batches", Tags: tag,
		Summary: "批量通过 / 返工素材（按列表或当前筛选）；dry_run 只预检件数；意见按收件会话合并成一条送达"},
		func(ctx context.Context, in *struct{ Body AssetBatchIn }) (*assetBatchOut, error) {
			out, err := h.reviewAssetBatch(ctx, in.Body, "", store.AssetAuthor{Kind: store.SubmitterUser})
			if err != nil {
				return nil, err
			}
			return &assetBatchOut{Body: out}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "undo-asset-batch", Method: http.MethodPost, Path: "/api/asset-batches/undo", Tags: tag,
		Summary: "撤销最近一次批量审核：没再被改过的素材还原为审核前的状态，没送达的意见关闭、已送达的补一条「已撤销」"},
		func(ctx context.Context, in *struct {
			Body struct {
				BatchID string `json:"batch_id,omitempty" doc:"要撤销的那次（须是最近一次）；不写撤销最近一次"`
			}
		}) (*assetBatchUndoOut, error) {
			out, err := h.undoAssetBatch(ctx, in.Body.BatchID, store.AssetAuthor{Kind: store.SubmitterUser})
			if err != nil {
				return nil, err
			}
			return &assetBatchUndoOut{Body: out}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "list-asset-reviewers", Method: http.MethodGet, Path: "/api/asset-reviewers", Tags: tag,
		Summary: "授权了批量审素材的会话（ks asset review，默认没有）"},
		func(ctx context.Context, _ *struct{}) (*assetReviewersOut, error) {
			out := &assetReviewersOut{}
			var err error
			out.Body.Sessions, err = h.store.ListAssetReviewers(ctx)
			return out, err
		})
	huma.Register(api, huma.Operation{OperationID: "set-asset-reviewer", Method: http.MethodPut, Path: "/api/asset-reviewers/{key}", Tags: tag,
		Summary: "授权 / 收回会话批量审素材（只限本项目的素材）"},
		func(ctx context.Context, in *struct {
			Key  string `path:"key"`
			Body struct {
				Allow bool `json:"allow"`
			}
		}) (*assetReviewersOut, error) {
			if err := h.store.SetAssetReviewer(ctx, in.Key, in.Body.Allow); err != nil {
				return nil, apiErr(err)
			}
			h.broker.publish("sessions")
			out := &assetReviewersOut{}
			var err error
			out.Body.Sessions, err = h.store.ListAssetReviewers(ctx)
			return out, err
		})
}

// agentReviewer：会话要先由用户在网页上授权才能批量审素材，且只能审自己所在项目的。
func (h *Hub) agentReviewer(ctx context.Context, key string) (store.Session, error) {
	x, err := h.store.GetSession(ctx, key)
	if err != nil {
		return x, huma.Error400BadRequest("找不到会话：" + key)
	}
	ok, err := h.store.AssetReviewerAllowed(ctx, key)
	if err != nil {
		return x, err
	}
	if !ok {
		return x, huma.Error403Forbidden("这个会话没有批量审素材的授权：请用户在网页上授权后再用（默认不开）；没有授权时请用户在资源库里审")
	}
	if x.ProjectID == "" {
		return x, huma.Error400BadRequest("会话不属于任何项目，不能批量审素材")
	}
	return x, nil
}

func (h *Hub) registerAssetBatchAgent(api huma.API) {
	tag := []string{"agent"}
	huma.Register(api, huma.Operation{OperationID: "agent-review-asset-batch", Method: http.MethodPost, Path: "/api/agent/asset-batches", Tags: tag,
		Summary: "用户授权的会话批量通过 / 返工本项目的素材（ks asset review）；dry_run 只预检"},
		func(ctx context.Context, in *struct {
			Body struct {
				SessionKey string `json:"session_key" minLength:"1"`
				AssetBatchIn
			}
		}) (*assetBatchOut, error) {
			x, err := h.agentReviewer(ctx, in.Body.SessionKey)
			if err != nil {
				return nil, err
			}
			out, err := h.reviewAssetBatch(ctx, in.Body.AssetBatchIn, x.ProjectID, store.AssetAuthor{Kind: store.SubmitterSession, Ref: x.Key, Name: x.Title})
			if err != nil {
				return nil, err
			}
			return &assetBatchOut{Body: out}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "agent-undo-asset-batch", Method: http.MethodPost, Path: "/api/agent/asset-batches/undo", Tags: tag,
		Summary: "撤销本会话最近一次批量审核"},
		func(ctx context.Context, in *struct {
			Body struct {
				SessionKey string `json:"session_key" minLength:"1"`
			}
		}) (*assetBatchUndoOut, error) {
			x, err := h.agentReviewer(ctx, in.Body.SessionKey)
			if err != nil {
				return nil, err
			}
			out, err := h.undoAssetBatch(ctx, "", store.AssetAuthor{Kind: store.SubmitterSession, Ref: x.Key, Name: x.Title})
			if err != nil {
				return nil, err
			}
			return &assetBatchUndoOut{Body: out}, nil
		})
}
