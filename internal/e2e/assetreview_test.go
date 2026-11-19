package e2e

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/client"
	"github.com/NekroAI/kitestring/internal/hub"
	"github.com/NekroAI/kitestring/internal/store"
)

type reviewOut struct {
	Asset  store.AssetDetail `json:"asset"`
	Review store.AssetReview `json:"review"`
}

// R-351 第 4 步：通过 / 返工写进审核记录（作者是你），意见用 asset_review 送回提交素材的会话；
// 那个会话已结束时送同项目主调度；返工必须写意见；旧状态、旧版本上的审核 409；返工后重推回到待审，旧意见不再补发。
func TestAssetReview(t *testing.T) {
	e := setup(t)
	key := assetSession(t, e, "asset-rv")
	dir := copyFixture(t)
	if _, err := pushAssets(t, dir, key, "S1"); err != nil {
		t.Fatal(err)
	}
	var list hub.AssetListView
	e.call("GET", "/api/assets?batch=S1", nil, &list)
	code := map[string]string{}
	for _, a := range list.Assets {
		code[a.Key] = a.Code
	}
	wheel, crate := code["wheel"], code["crate"]
	if wheel == "" || crate == "" {
		t.Fatalf("样例里应有 wheel、crate：%v", code)
	}
	review := func(ref string, body map[string]any) (int, string) {
		return e.try("POST", "/api/assets/"+ref+"/review", body)
	}

	// 1. 返工不写意见：拒绝
	if st, msg := review(wheel, map[string]any{"verdict": "rework"}); st != http.StatusBadRequest || !strings.Contains(msg, "返工需写明修改意见") {
		t.Fatalf("返工不写意见应拒绝：%d %s", st, msg)
	}
	// 2. 返工：状态变返工，审核记录作者是你，送给提交素材的会话
	var out reviewOut
	e.call("POST", "/api/assets/"+wheel+"/review", map[string]any{"verdict": "rework", "comment": "轮毂颜色太暗，提亮一档", "version": 1, "from": "pending"}, &out)
	if out.Asset.Status != store.AssetRework || len(out.Asset.Reviews) != 1 {
		t.Fatalf("返工后：%+v", out.Asset)
	}
	r := out.Review
	if r.Verdict != store.AssetRework || r.Author.Kind != store.SubmitterUser || r.SentTo != key || r.SentAs != store.SentToSubmitter || r.Version != 1 || r.Delivery != store.DecAnswered || r.DecisionCode == "" {
		t.Fatalf("审核记录：%+v", r)
	}
	// 会话下一轮开始时送达，像决策答复一样注入
	cwd, _ := os.Getwd()
	var got string
	e.waitFor("审核意见送达会话", func() bool {
		got = hook(t, "claude", map[string]any{"session_id": "asset-rv", "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "继续", "prompt_id": "p1"})
		return strings.Contains(got, "轮毂颜色太暗")
	})
	if !strings.Contains(got, "用户在资源库审核了以下素材") || !strings.Contains(got, wheel+"「wheel」（批次 S1，v1）返工：轮毂颜色太暗，提亮一档") {
		t.Fatalf("送达的文字：%s", got)
	}
	e.waitFor("记为已送达", func() bool {
		var d store.AssetDetail
		e.call("GET", "/api/assets/"+wheel, nil, &d)
		return len(d.Reviews) == 1 && d.Reviews[0].Delivery == store.DecDelivered
	})

	// 3. 看到的状态已过时：409，状态不变
	if st, _ := review(wheel, map[string]any{"verdict": "approved", "from": "pending"}); st != http.StatusConflict {
		t.Fatalf("旧状态上的审核应 409：%d", st)
	}
	// 4. 返工后重推（模拟改了 GLB）：第 2 版、回到待审；旧版本上的审核 409
	glb := filepath.Join(dir, "assets", "wheel.glb")
	b, _ := os.ReadFile(glb)
	os.WriteFile(glb, append(b, 0, 0, 0, 0), 0o600)
	if _, err := pushAssets(t, dir, key, "S1"); err != nil {
		t.Fatal(err)
	}
	var w2 store.AssetDetail
	e.call("GET", "/api/assets/"+wheel, nil, &w2)
	if w2.Status != store.AssetPending || w2.Version != 2 || len(w2.Reviews) != 1 {
		t.Fatalf("返工后重推：%+v", w2)
	}
	if st, msg := review(wheel, map[string]any{"verdict": "approved", "version": 1}); st != http.StatusConflict || !strings.Contains(msg, "已有新版本 v2") {
		t.Fatalf("旧版本上的审核应 409：%d %s", st, msg)
	}
	// 已送达的返工意见在素材回到待审后不再补发（恢复 / 压缩时）
	resume := hook(t, "claude", map[string]any{"session_id": "asset-rv", "cwd": cwd, "hook_event_name": "SessionStart", "source": "compact"})
	if strings.Contains(resume, "轮毂颜色太暗") {
		t.Fatalf("重推后旧的返工意见不应补发：%s", resume)
	}
	// 5. 通过第 2 版，附言随通过送达
	e.call("POST", "/api/assets/"+wheel+"/review", map[string]any{"verdict": "approved", "comment": "可以", "version": 2, "from": "pending"}, &out)
	if out.Asset.Status != store.AssetApproved || len(out.Asset.Reviews) != 2 || out.Asset.Reviews[0].Version != 2 {
		t.Fatalf("通过后：%+v", out.Asset)
	}
	// 通过只告知（不唤醒待命的会话，会话结束后也不进待转交）；返工要动手
	if d, err := e.store.GetDecision(context.Background(), out.Review.DecisionCode); err != nil || !d.FYI || d.Kind != store.KindAssetReview {
		t.Fatalf("通过的审核意见应只告知：%+v %v", d, err)
	}
	if d, _ := e.store.GetDecision(context.Background(), r.DecisionCode); d.FYI {
		t.Fatal("返工的审核意见不应只告知")
	}

	// 6. 提交素材的会话已结束：意见送同项目主调度，正文写明转交
	pm := "claude:asset-pm"
	hook(t, "claude", map[string]any{"session_id": "asset-pm", "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "上线", "prompt_id": "p0"})
	e.waitFor("主调度入库", func() bool { return e.session(pm).ProjectID != "" })
	if err := client.New(10*time.Second).Do(context.Background(), "POST", "/agent/sessions/role", map[string]any{"session_key": pm, "role": "pm"}, nil); err != nil {
		t.Fatal(err)
	}
	hook(t, "claude", map[string]any{"session_id": "asset-rv", "cwd": cwd, "hook_event_name": "SessionEnd"})
	e.waitFor("提交的会话结束", func() bool { return e.session(key).State == store.StateEnded })
	e.call("POST", "/api/assets/"+crate+"/review", map[string]any{"verdict": "rework", "comment": "箱子太新，加点磨损"}, &out)
	if r := out.Review; r.SentTo != pm || r.SentAs != store.SentToPM {
		t.Fatalf("提交的会话已结束时应送主调度：%+v", r)
	}
	e.waitFor("主调度收到", func() bool {
		got = hook(t, "claude", map[string]any{"session_id": "asset-pm", "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "继续", "prompt_id": "p1"})
		return strings.Contains(got, "箱子太新")
	})
	if !strings.Contains(got, "已结束，审核意见转给主调度：请安排会话按意见修改后重新推送。") {
		t.Fatalf("转给主调度的文字：%s", got)
	}

	// 7. 侧边栏的各状态数
	var counts struct {
		Counts map[string]int `json:"counts"`
	}
	e.call("GET", "/api/asset-counts", nil, &counts)
	if counts.Counts["approved"] != 1 || counts.Counts["rework"] != 1 || counts.Counts["pending"] != 3 {
		t.Fatalf("各状态数：%+v", counts.Counts)
	}
}
