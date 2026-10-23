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

// 撤回决策（R-136）：Agent 只能撤回自己提的、用户还没答复的决策，可注明被新决策取代；
// 网页上也能关闭待决策；撤回后不再待决策，需求时间线能读到原因与取代关系。
func TestWithdrawDecision(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	c := client.New(10 * time.Second)
	ctx := context.Background()
	for _, id := range []string{"a", "b"} {
		hook(t, "claude", map[string]any{"session_id": id, "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "hi", "prompt_id": "p0"})
	}
	e.waitFor("会话入库", func() bool { return e.session("claude:a").ProjectID != "" && e.session("claude:b").ProjectID != "" })
	raise := func(key, q string) store.Decision {
		var d store.Decision
		if err := c.Do(ctx, "POST", "/agent/decisions", map[string]any{"question": q, "options": []string{"甲", "乙"}, "session_key": key}, &d); err != nil {
			t.Fatal(err)
		}
		return d
	}
	withdraw := func(ref string, body map[string]any) (store.Decision, error) {
		var d store.Decision
		err := c.Do(ctx, "POST", "/agent/decisions/"+ref+"/withdraw", body, &d)
		return d, err
	}
	status := func(ref string) store.Decision {
		var d store.Decision
		if err := c.Do(ctx, "GET", "/agent/decisions/"+ref, nil, &d); err != nil {
			t.Fatal(err)
		}
		return d
	}

	old := raise("claude:a", "方案一？")
	nw := raise("claude:a", "方案一（修订）？")

	// 别的会话不能撤回；撤回自己的，注明原因与被谁取代
	if _, err := withdraw(old.Code, map[string]any{"session_key": "claude:b"}); err == nil || !strings.Contains(err.Error(), "不是本会话提的") {
		t.Fatalf("别的会话不应能撤回：%v", err)
	}
	if _, err := withdraw(old.Code, map[string]any{"session_key": "claude:a", "replaced_by": "D-999"}); err == nil || !strings.Contains(err.Error(), "不存在") {
		t.Fatalf("取代它的决策不存在应报错：%v", err)
	}
	got, err := withdraw(old.Code, map[string]any{"session_key": "claude:a", "reason": "提错了", "replaced_by": nw.Code})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.DecClosed || got.CloseReason != "提错了" || got.ReplacedBy != nw.Code {
		t.Fatalf("撤回后应已关闭并写明原因与取代：%+v", got)
	}
	if st := status(old.Code); st.Status != store.DecClosed || st.ReplacedBy != nw.Code {
		t.Fatalf("重新读取应仍是已关闭：%+v", st)
	}
	if _, err := withdraw(old.Code, map[string]any{"session_key": "claude:a"}); err == nil || !strings.Contains(err.Error(), "已关闭") {
		t.Fatalf("已关闭的不能再撤回：%v", err)
	}

	// 用户已答复的不能被 Agent 撤回
	e.call("POST", "/api/decisions/answers", map[string]any{"answers": []map[string]any{{"ref": nw.Code, "answer": "甲"}}}, nil)
	if _, err := withdraw(nw.Code, map[string]any{"session_key": "claude:a"}); err == nil || !strings.Contains(err.Error(), "已答复") {
		t.Fatalf("已答复的不应能撤回：%v", err)
	}
	if st := status(nw.Code); st.Status == store.DecClosed {
		t.Fatalf("已答复的决策不应被关闭：%+v", st)
	}

	// 网页关闭待决策，可注明被取代
	third := raise("claude:b", "又一个？")
	var closed store.Decision
	e.call("POST", "/api/decisions/"+third.Code+"/close", map[string]any{"reason": "不用问了", "replaced_by": nw.Code}, &closed)
	if closed.Status != store.DecClosed || closed.CloseReason != "不用问了" || closed.ReplacedBy != nw.Code {
		t.Fatalf("网页关闭应写入原因与取代：%+v", closed)
	}
	var list struct {
		Decisions []store.Decision `json:"decisions"`
	}
	e.call("GET", "/api/decisions?status=pending", nil, &list)
	for _, d := range list.Decisions {
		if d.Code == old.Code || d.Code == third.Code {
			t.Fatalf("已撤回 / 已关闭的不应还在待决策里：%s", d.Code)
		}
	}
}
