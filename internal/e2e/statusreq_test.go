package e2e

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/client"
	"github.com/NekroAI/kitestring/internal/store"
)

// Agent 申请改需求状态（R-188）：
//   - 申请是一条请求类审批（approval + request_kind = status + 申请时状态 / 目标状态），出现在待处理里；同一条需求不重复建；
//   - 非法目标状态（进行中、待验收、已完成、不认识的、与当前相同）和没写理由的被拒；
//   - 批准 → 纸鸢改状态，状态历史记成用户经由 request、ref = 申请的 D-x；申请方收到「批准」；
//   - 拒绝 → 状态不变，申请方收到「用户明确拒绝」；
//   - 批准前状态被改动 → 申请自动作废（voided），申请方收到「已作废」，之后不能再答；
//   - 已搁置的可以申请重新打开（→ 草稿）。
func TestStatusRequest(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	c := client.New(30 * time.Second)
	ctx := context.Background()
	for _, sid := range []string{"adv", "dev"} {
		hook(t, "claude", map[string]any{"session_id": sid, "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "上线", "prompt_id": "p0"})
	}
	e.waitFor("会话入库", func() bool { return e.session("claude:dev").Key != "" })
	if err := c.Do(ctx, "POST", "/agent/sessions/role", map[string]any{"session_key": "claude:adv", "role": "advisor"}, nil); err != nil {
		t.Fatal(err)
	}
	// 顾问第一次挂 ks wait、项目又没有主助理时，Hub 会发一次身份调整提示（R-190）。先收掉它，
	// 否则它可能晚到一点、没赶上第一次唤醒，下一次等待就被它叫醒，而不是被要验证的答复叫醒。
	woke(t, parkWait(t, e, c, "claude:adv"), "身份调整")
	newReq := func(title string) store.Requirement {
		t.Helper()
		var r store.Requirement
		if err := c.Do(ctx, "POST", "/agent/requirements", map[string]any{"title": title, "detail": title + "的说明", "session_key": "claude:adv"}, &r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	getReq := func(code string) store.Requirement {
		t.Helper()
		var r store.Requirement
		e.call("GET", "/api/requirements/"+code, nil, &r)
		return r
	}
	type reqRes struct {
		Decision store.Decision `json:"decision"`
		Existing bool           `json:"existing"`
		Message  string         `json:"message"`
	}
	ask := func(code, to, reason string) (reqRes, error) {
		var out reqRes
		err := c.Do(ctx, "POST", "/agent/requirements/"+code+"/status-request", map[string]any{"session_key": "claude:adv", "to": to, "reason": reason}, &out)
		return out, err
	}
	answer := func(code, text string) {
		t.Helper()
		e.call("POST", "/api/decisions/answers", map[string]any{"answers": []map[string]any{{"ref": code, "answer": text}}}, nil)
	}
	decision := func(code string) store.Decision {
		t.Helper()
		var d store.Decision
		var out struct {
			Decisions []store.Decision `json:"decisions"`
		}
		e.call("GET", "/api/decisions", nil, &out)
		for _, x := range out.Decisions {
			if x.Code == code {
				d = x
			}
		}
		return d
	}
	pendingHas := func(code string) bool {
		var out struct {
			Decisions []store.Decision `json:"decisions"`
		}
		e.call("GET", "/api/decisions?status=pending", nil, &out)
		for _, x := range out.Decisions {
			if x.Code == code {
				return true
			}
		}
		return false
	}

	// 非法目标状态、没写理由被拒
	r1 := newReq("导出 CSV")
	for _, to := range []string{"done", "in_progress", "review", "blocked", "bogus", "draft", "shelved"} {
		if _, err := ask(r1.Code, to, "理由"); err == nil {
			t.Fatalf("草稿申请改为 %s 应被拒", to)
		}
	}
	if _, err := ask(r1.Code, "ready", "  "); err == nil || !strings.Contains(err.Error(), "理由") {
		t.Fatalf("没写理由应被拒：%v", err)
	}

	// 申请 → 出现在待处理；同一条需求不重复建
	a1, err := ask(r1.Code, "ready", "做法已和用户对齐")
	if err != nil {
		t.Fatal(err)
	}
	d1 := a1.Decision
	if a1.Existing || !d1.Approval || d1.RequestKind != store.RequestStatus || d1.RequestFrom != store.ReqDraft || d1.RequestTo != store.ReqReady || d1.Status != store.DecPending || d1.RequirementCode != r1.Code || d1.ExpiresAt != nil {
		t.Fatalf("申请应是一条不限时的改状态请求：%+v", d1)
	}
	if !pendingHas(d1.Code) {
		t.Fatal("申请应出现在待处理里")
	}
	if again, err := ask(r1.Code, "ready", "再申请一次"); err != nil || !again.Existing || again.Decision.Code != d1.Code {
		t.Fatalf("同一条需求不应重复建申请：%+v %v", again, err)
	}

	// 批准 → 状态改好，状态历史记成用户批准了 Agent 的申请；申请方收到批准
	answer(d1.Code, "批准")
	got := getReq(r1.Code)
	if got.Status != store.ReqReady {
		t.Fatalf("批准后应改为待开始：%s", got.Status)
	}
	last := got.StatusChanges[len(got.StatusChanges)-1]
	if last.From != store.ReqDraft || last.To != store.ReqReady || last.Actor != store.ActorUser || last.Via != "request" || last.Ref != d1.Code {
		t.Fatalf("时间线应记成用户批准了申请：%+v", last)
	}
	woke(t, parkWait(t, e, c, "claude:adv"), d1.Code, "批准（用户批准了把 "+r1.Code+" 从草稿改为待开始的申请）", "不用你再改")
	if pendingHas(d1.Code) {
		t.Fatal("批准后不应还在待处理里")
	}

	// 拒绝 → 状态不变，申请方收到明确拒绝
	r2 := newReq("批量删除")
	a2, err := ask(r2.Code, "ready", "可以排期")
	if err != nil {
		t.Fatal(err)
	}
	answer(a2.Decision.Code, "拒绝；先别做")
	if got := getReq(r2.Code); got.Status != store.ReqDraft {
		t.Fatalf("拒绝后应保持草稿：%s", got.Status)
	}
	woke(t, parkWait(t, e, c, "claude:adv"), a2.Decision.Code, "用户明确拒绝了把 "+r2.Code, "先别做", "保持草稿")

	// 批准前状态被改动 → 自动作废，申请方收到已作废；作废后不能再答
	r3 := newReq("夜间模式")
	a3, err := ask(r3.Code, "ready", "可以排期")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Do(ctx, "POST", "/agent/requirements/"+r3.Code+"/status", map[string]any{"status": "shelved", "reason": "和 R-1 重复", "reason_kind": "duplicate", "session_key": "claude:dev"}, nil); err != nil {
		t.Fatal(err)
	}
	d3 := decision(a3.Decision.Code)
	if !d3.Voided || d3.Status == store.DecPending {
		t.Fatalf("状态被改动后申请应自动作废：%+v", d3)
	}
	if pendingHas(d3.Code) {
		t.Fatal("作废的申请不应还在待处理里")
	}
	woke(t, parkWait(t, e, c, "claude:adv"), d3.Code, "已作废")
	if code, _ := e.try("POST", "/api/decisions/answers", map[string]any{"answers": []map[string]any{{"ref": d3.Code, "answer": "批准"}}}); code != 400 {
		t.Fatalf("作废的申请不能再答：%d", code)
	}
	if got := getReq(r3.Code); got.Status != store.ReqShelved {
		t.Fatalf("作废的申请不应改状态：%s", got.Status)
	}

	// 已搁置的可以申请重新打开为草稿；非法的（已搁置 → 进行中）被拒
	if _, err := ask(r3.Code, "in_progress", "理由"); err == nil {
		t.Fatal("已搁置申请改为进行中应被拒")
	}
	a4, err := ask(r3.Code, "draft", "重复判断有误，重新打开")
	if err != nil {
		t.Fatal(err)
	}
	if a4.Decision.RequestFrom != store.ReqShelved || a4.Decision.RequestTo != store.ReqDraft {
		t.Fatalf("重新打开的申请：%+v", a4.Decision)
	}
	answer(a4.Decision.Code, "批准")
	if got := getReq(r3.Code); got.Status != store.ReqDraft {
		t.Fatalf("批准后应重新打开为草稿：%s", got.Status)
	}
}
