package hub

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/protocol"
	"github.com/NekroAI/kitestring/internal/store"
)

// reviewMsgs：送给会话的审核意见（kind = asset_review）。
func (e *shareEnv) reviewMsgs(key string) []store.Decision {
	e.t.Helper()
	all, err := e.st.ListDecisions(e.ctx, nil, 500)
	if err != nil {
		e.t.Fatal(err)
	}
	var out []store.Decision
	for _, d := range all {
		if d.Kind == store.KindAssetReview && d.SessionKey == key {
			out = append(out, d)
		}
	}
	return out
}

func (e *shareEnv) answer(code, verdict string) {
	e.t.Helper()
	label := map[string]string{store.VerdictApproved: "批准", store.VerdictRejected: "拒绝"}[verdict]
	e.do(e.owner, "POST", "/api/decisions/answers", map[string]any{"answers": []map[string]any{{"ref": code, "answer": label, "verdict": verdict}}}, nil, 200)
}

// R-393 第 1 条：--baseline 推送的素材挂在一条批准类决策上；用户批准后整批通过（审核记录写明是批准的历史导入，不发意见），
// 补传重放不重复申请；之后有新版本回到待审；拒绝则留在待审。
func TestAssetBaseline(t *testing.T) {
	e := newShareEnv(t)
	e.pushFixture()
	glb := e.assetFile("carbine.glb", glbMime, append(append([]byte{}, glbHead...), 'q'))
	png := e.assetFile("carbine.png", "image/png", append(append([]byte{}, pngHead...), 'q'))
	items := []protocol.AssetItem{
		{Key: "core:weapon.carbine", Kind: protocol.AssetPart, Category: "weapon", File: &glb, Previews: []protocol.AssetFile{png}},
		{Key: "core:weapon.pistol", Kind: protocol.AssetPart, Category: "weapon", File: &glb},
		{Key: "core:prop.box", Kind: protocol.AssetPart, Category: "prop", File: &glb},
	}
	p := protocol.AssetPush{PushID: "push-1", ByID: true, Project: "p1", SessionKey: e.dev1, Baseline: true, Items: items}
	if _, err := e.h.pushAssets(e.ctx, protocol.AssetPush{ByID: true, Project: "p1", SessionKey: e.dev1, Baseline: true, Status: store.AssetWIP, Items: items}); err == nil {
		t.Fatal("--baseline 和 --status wip 不能一起用")
	}
	res, err := e.h.pushAssets(e.ctx, p)
	if err != nil || res.Baseline == nil || res.Baseline.Decision == "" || res.Baseline.Count != 3 {
		t.Fatalf("应提一条申请：%+v %v", res.Baseline, err)
	}
	d, err := e.st.GetDecision(e.ctx, res.Baseline.Decision)
	if err != nil || !d.Approval || d.SessionKey != e.dev1 || !strings.Contains(d.Context, "weapon 2、prop 1") || len(d.ContextFiles) != 1 {
		t.Fatalf("批准类决策写件数、类别分布、代表图：%+v %v", d, err)
	}
	a, _ := e.st.FindAsset(e.ctx, "p1", "core:weapon.carbine")
	if a.Status != store.AssetPending || a.Baseline != d.Code {
		t.Fatalf("批准前是待审、挂着申请：%s %q", a.Status, a.Baseline)
	}
	// 补传重放：同一 push_id 不重复申请
	again, err := e.h.pushAssets(e.ctx, p)
	if err != nil || again.Baseline == nil || again.Baseline.Decision != d.Code {
		t.Fatalf("重放应返回同一条申请：%+v %v", again.Baseline, err)
	}
	if hardOf(d) == "" {
		t.Fatal("申请是批准类，不能代答")
	}
	e.answer(d.Code, store.VerdictApproved)
	a, _ = e.st.FindAsset(e.ctx, "p1", "core:weapon.carbine")
	if a.Status != store.AssetApproved || a.Baseline != "" || len(a.Reviews) != 1 {
		t.Fatalf("批准后整批通过：%+v", a.Asset)
	}
	if r := a.Reviews[0]; r.Author.Kind != store.SubmitterUser || !strings.Contains(r.Comment, "用户批准的历史导入（"+d.Code+"）") || !strings.Contains(r.Comment, "分享后端") {
		t.Fatalf("审核记录写明是批准的历史导入和推送会话：%+v", r)
	}
	if n := len(e.reviewMsgs(e.dev1)); n != 0 {
		t.Fatalf("不给会话发意见消息，发了 %d 条", n)
	}
	// 之后改版回到待审
	glb2 := e.assetFile("carbine2.glb", glbMime, append(append([]byte{}, glbHead...), 'w'))
	items[0].File = &glb2
	if _, err := e.h.pushAssets(e.ctx, protocol.AssetPush{ByID: true, Project: "p1", SessionKey: e.dev1, Items: items[:1]}); err != nil {
		t.Fatal(err)
	}
	if a, _ = e.st.FindAsset(e.ctx, "p1", "core:weapon.carbine"); a.Status != store.AssetPending {
		t.Fatalf("改版后回到待审：%s", a.Status)
	}
	// 拒绝：留在待审
	glb3 := e.assetFile("carbine3.glb", glbMime, append(append([]byte{}, glbHead...), 'e'))
	items[0].File = &glb3
	res, err = e.h.pushAssets(e.ctx, protocol.AssetPush{PushID: "push-2", ByID: true, Project: "p1", SessionKey: e.dev1, Baseline: true, Items: items[:2]})
	if err != nil || res.Baseline == nil || res.Baseline.Count != 1 {
		t.Fatalf("已通过的不再申请，只申请待审的那件：%+v %v", res.Baseline, err)
	}
	e.answer(res.Baseline.Decision, store.VerdictRejected)
	if a, _ = e.st.FindAsset(e.ctx, "p1", "core:weapon.carbine"); a.Status != store.AssetPending || a.Baseline != "" {
		t.Fatalf("拒绝后是普通待审：%s %q", a.Status, a.Baseline)
	}
}

// R-393 第 2、3 条：按筛选批量返工先预检件数；同一个会话只收到一条合并的意见；撤销最近一次还原状态、没送达的意见关闭。
func TestAssetBatchReview(t *testing.T) {
	e := newShareEnv(t)
	x := e.pushFixture()
	var pre AssetBatchOut
	e.do(e.owner, "POST", "/api/asset-batches", map[string]any{"filter": map[string]string{"project": "p1", "batch": "S1"}, "verdict": "rework", "comment": "颜色太亮", "dry_run": true}, &pre, 200)
	if !pre.DryRun || pre.Count != 2 {
		t.Fatalf("预检 S1 应是 2 件：%+v", pre)
	}
	if a, _ := e.st.GetAsset(e.ctx, x.wheel.ID); a.Status != store.AssetPending {
		t.Fatal("预检不写入")
	}
	e.do(e.owner, "POST", "/api/asset-batches", map[string]any{"filter": map[string]string{"project": "p1"}, "verdict": "rework"}, nil, 400) // 返工要意见
	var noNote AssetBatchOut
	e.do(e.owner, "POST", "/api/asset-batches", map[string]any{"filter": map[string]string{"project": "p1", "batch": "S1"}, "verdict": "rework", "dry_run": true}, &noNote, 200) // 预检可以先不写意见
	if noNote.Count != 2 {
		t.Fatalf("不写意见也能预检返工：%+v", noNote)
	}
	var out AssetBatchOut
	e.do(e.owner, "POST", "/api/asset-batches", map[string]any{"assets": []string{x.wheel.Code, x.crate.Code, x.other.Code}, "verdict": "rework", "comment": "颜色太亮"}, &out, 200)
	if out.Count != 3 || out.BatchID == "" || out.Messages != 1 {
		t.Fatalf("3 件返工、合并成 1 条：%+v", out)
	}
	msgs := e.reviewMsgs(e.dev1)
	if len(msgs) != 1 || !strings.Contains(msgs[0].Answer, x.wheel.Code) || !strings.Contains(msgs[0].Answer, x.other.Code) || !strings.Contains(msgs[0].Answer, "颜色太亮") {
		t.Fatalf("同一会话只收到一条合并消息：%+v", msgs)
	}
	if e.h.assetReviewStale(e.ctx, msgs[0]) {
		t.Fatal("还在返工的合并意见恢复后应补发")
	}
	// 再审一次：已是返工的跳过
	var again AssetBatchOut
	e.do(e.owner, "POST", "/api/asset-batches", map[string]any{"assets": []string{x.wheel.Code}, "verdict": "rework", "comment": "再看看", "dry_run": true}, &again, 200)
	if again.Count != 0 || len(again.Skipped) != 1 {
		t.Fatalf("已是返工的跳过：%+v", again)
	}
	var undo AssetBatchUndoOut
	e.do(e.owner, "POST", "/api/asset-batches/undo", map[string]any{}, &undo, 200)
	if len(undo.Restored) != 3 {
		t.Fatalf("撤销还原 3 件：%+v", undo)
	}
	if a, _ := e.st.GetAsset(e.ctx, x.wheel.ID); a.Status != store.AssetPending || len(a.Reviews) != 0 {
		t.Fatalf("撤销后回到待审、审核记录删掉：%s %d", a.Status, len(a.Reviews))
	}
	if d, _ := e.st.GetDecision(e.ctx, msgs[0].ID); d.Status != store.DecClosed {
		t.Fatalf("没送达的合并意见关闭：%s", d.Status)
	}
	e.do(e.owner, "POST", "/api/asset-batches/undo", map[string]any{}, nil, 404)
}

// R-393 第 2 条：200 件以内一次通过不超过 3 秒。
func TestAssetBatch200(t *testing.T) {
	e := newShareEnv(t)
	e.pushFixture()
	glb := e.assetFile("bulk.glb", glbMime, append(append([]byte{}, glbHead...), 'k'))
	var items []protocol.AssetItem
	for i := range 200 {
		items = append(items, protocol.AssetItem{Key: fmt.Sprintf("bulk%03d", i), Kind: protocol.AssetPart, Category: "道具", File: &glb})
	}
	if _, err := e.st.PushAssets(e.ctx, store.NewAssetPush{ProjectID: "p1", Submitter: store.AssetSubmitter{Kind: store.SubmitterSession, Ref: e.dev1}, Batch: "BULK", Items: items}); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	var out AssetBatchOut
	e.do(e.owner, "POST", "/api/asset-batches", map[string]any{"filter": map[string]string{"project": "p1", "batch": "BULK"}, "verdict": "approved"}, &out, 200)
	if took := time.Since(start); out.Count != 200 || took > 3*time.Second {
		t.Fatalf("200 件通过：%d 件，用了 %v", out.Count, took)
	}
	if n := len(e.reviewMsgs(e.dev1)); n != 1 {
		t.Fatalf("200 件只发一条：%d", n)
	}
}

// R-393 第 4 条：会话要用户在网页上授权才能批量审，且只审自己项目的素材。
func TestAssetBatchAgentGrant(t *testing.T) {
	e := newShareEnv(t)
	x := e.pushFixture()
	if _, err := e.st.DB().Exec(`UPDATE sessions SET project_id = 'p1' WHERE key = ?`, e.dev2); err != nil {
		t.Fatal(err)
	}
	if _, err := e.h.agentReviewer(e.ctx, e.dev2); err == nil || !strings.Contains(err.Error(), "授权") {
		t.Fatalf("默认不开：%v", err)
	}
	e.do(e.owner, "PUT", "/api/asset-reviewers/"+e.dev2, map[string]any{"allow": true}, nil, 200)
	s, err := e.h.agentReviewer(e.ctx, e.dev2)
	if err != nil {
		t.Fatal(err)
	}
	out, err := e.h.reviewAssetBatch(e.ctx, AssetBatchIn{Filter: AssetBatchFilter{Project: "other", Batch: "S1"}, Verdict: store.AssetApproved}, s.ProjectID, store.AssetAuthor{Kind: store.SubmitterSession, Ref: s.Key, Name: s.Title})
	if err != nil || out.Count != 2 {
		t.Fatalf("只在会话所在项目里选：%+v %v", out, err)
	}
	if a, _ := e.st.GetAsset(e.ctx, x.wheel.ID); a.Status != store.AssetApproved || a.Reviews[0].Author.Ref != e.dev2 {
		t.Fatalf("审核记录作者是会话：%+v", a.Reviews)
	}
	if msgs := e.reviewMsgs(e.dev1); len(msgs) != 1 || !strings.Contains(msgs[0].Answer, "经用户授权") {
		t.Fatalf("意见写明是授权的会话审的：%+v", msgs)
	}
	e.do(e.owner, "PUT", "/api/asset-reviewers/"+e.dev2, map[string]any{"allow": false}, nil, 200)
	if _, err := e.h.agentReviewer(e.ctx, e.dev2); err == nil {
		t.Fatal("收回后不能再审")
	}
}
