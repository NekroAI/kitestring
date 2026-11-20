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

// 已搁置的需求不能被会话直接重开（R-403）：
//   - 会话（开发 / 主调度等）把已搁置的需求改成进行中、待开始、受阻、待验收被拒，报错说明用 ks req request 申请；
//   - 关联会话、主调度指派都不会把已搁置的需求带回进行中；
//   - 用户在网页上重开照常；用户批准的申请照常。
func TestShelvedReopenNeedsUser(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	c := client.New(30 * time.Second)
	ctx := context.Background()
	for _, sid := range []string{"pm", "dev"} {
		hook(t, "claude", map[string]any{"session_id": sid, "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "上线", "prompt_id": "p0"})
	}
	e.waitFor("会话入库", func() bool { return e.session("claude:dev").Key != "" && e.session("claude:pm").Key != "" })
	if err := c.Do(ctx, "POST", "/agent/sessions/role", map[string]any{"session_key": "claude:pm", "role": "pm"}, nil); err != nil {
		t.Fatal(err)
	}
	getReq := func(code string) store.Requirement {
		t.Helper()
		var r store.Requirement
		e.call("GET", "/api/requirements/"+code, nil, &r)
		return r
	}
	shelved := func(title string) store.Requirement {
		t.Helper()
		var r store.Requirement
		if err := c.Do(ctx, "POST", "/agent/requirements", map[string]any{"title": title, "detail": title + "的说明", "session_key": "claude:pm"}, &r); err != nil {
			t.Fatal(err)
		}
		e.call("PUT", "/api/requirements/"+r.Code, map[string]any{"title": r.Title, "status": "shelved", "project_id": r.ProjectID}, nil)
		if got := getReq(r.Code); got.Status != store.ReqShelved {
			t.Fatalf("准备失败：%s", got.Status)
		}
		return r
	}
	setStatus := func(code, status, session string) error {
		return c.Do(ctx, "POST", "/agent/requirements/"+code+"/status", map[string]any{"status": status, "session_key": session, "reason": "继续做"}, nil)
	}

	r1 := shelved("搁置的需求一")
	// 会话改成任何别的状态（含草稿）都被拒，报错写明申请方式，状态不变
	for _, sess := range []string{"claude:dev", "claude:pm"} {
		for _, to := range []string{"in_progress", "ready", "blocked", "review", "draft"} {
			err := setStatus(r1.Code, to, sess)
			if err == nil || !strings.Contains(err.Error(), "ks req request "+r1.Code+" ready --reason") {
				t.Fatalf("%s 把已搁置改成 %s 应被拒并提示申请：%v", sess, to, err)
			}
		}
	}
	if got := getReq(r1.Code); got.Status != store.ReqShelved {
		t.Fatalf("被拒后状态应不变：%s", got.Status)
	}

	// 关联会话：不会把已搁置的需求带进进行中
	if err := c.Do(ctx, "POST", "/agent/sessions/claude:dev/link", map[string]any{"requirement": r1.Code}, nil); err != nil {
		t.Fatal(err)
	}
	if got := getReq(r1.Code); got.Status != store.ReqShelved {
		t.Fatalf("关联会话不应重开已搁置的需求：%s", got.Status)
	}
	// 主调度指派：已搁置的被拒
	if err := c.Do(ctx, "POST", "/agent/requirements/"+r1.Code+"/assign", map[string]any{"session_key": "claude:pm", "to": "claude:dev"}, nil); err == nil {
		t.Fatal("指派已搁置的需求应被拒")
	}
	if got := getReq(r1.Code); got.Status != store.ReqShelved {
		t.Fatalf("指派被拒后状态应不变：%s", got.Status)
	}

	// 用户在网页上重开照常
	e.call("PUT", "/api/requirements/"+r1.Code, map[string]any{"title": r1.Title, "status": "in_progress", "project_id": r1.ProjectID}, nil)
	if got := getReq(r1.Code); got.Status != store.ReqInProgress {
		t.Fatalf("用户重开应照常：%s", got.Status)
	}

	// 用户批准的申请照常：已搁置 → 待开始
	r2 := shelved("搁置的需求二")
	var out struct {
		Decision store.Decision `json:"decision"`
	}
	if err := c.Do(ctx, "POST", "/agent/requirements/"+r2.Code+"/status-request", map[string]any{"session_key": "claude:dev", "to": "ready", "reason": "判断有误，重新打开"}, &out); err != nil {
		t.Fatal(err)
	}
	e.call("POST", "/api/decisions/answers", map[string]any{"answers": []map[string]any{{"ref": out.Decision.Code, "answer": "批准"}}}, nil)
	if got := getReq(r2.Code); got.Status != store.ReqReady {
		t.Fatalf("批准的申请应重开为待开始：%s", got.Status)
	}
}
