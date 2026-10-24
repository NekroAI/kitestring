package e2e

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/client"
	"github.com/NekroAI/kitestring/internal/store"
)

// TestRecordSessionSnapshot：时间线上每条记录都写清是哪个会话做的（R-195）。记需求、改状态、交证据、改需求内容、验收建议
// 都带会话键、身份和动作当时的会话标题快照；会话标题后来变了，旧记录仍是当时的标题；你在网页上的留言不带会话；
// 上线前的旧记录（快照列为空）照常读出、不报错。
func TestRecordSessionSnapshot(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	c := client.New(10 * time.Second)
	ctx := context.Background()
	for _, sid := range []string{"pm", "dev", "aide"} {
		hook(t, "claude", map[string]any{"session_id": sid, "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "上线", "prompt_id": "p0"})
	}
	e.waitFor("会话入库", func() bool { return e.session("claude:aide").ProjectID != "" })
	for k, r := range map[string]string{"claude:pm": "pm", "claude:dev": "dev", "claude:aide": "aide"} {
		if err := c.Do(ctx, "POST", "/agent/sessions/role", map[string]any{"session_key": k, "role": r}, nil); err != nil {
			t.Fatal(err)
		}
	}
	title := func(key, v string) {
		t.Helper()
		if _, err := e.store.DB().Exec(`UPDATE sessions SET title = ? WHERE key = ?`, v, key); err != nil {
			t.Fatal(err)
		}
	}
	get := func(ref string) store.Requirement {
		var r store.Requirement
		e.call("GET", "/api/requirements/"+ref, nil, &r)
		return r
	}
	title("claude:pm", "R-187留样与暂缓合并")
	title("claude:dev", "抽屉时间线")
	title("claude:aide", "主助理值班")

	// 1. 记需求：主调度建的，状态变化带会话、身份和当时的标题
	var r store.Requirement
	if err := c.Do(ctx, "POST", "/agent/requirements", map[string]any{"title": "会话归属", "status": "in_progress", "acceptance": "1. 写清是谁", "session_key": "claude:pm"}, &r); err != nil {
		t.Fatal(err)
	}
	// 2. 主调度改了标题之后再改需求内容：修改记录记的是改的那一刻的标题
	title("claude:pm", "R-195 派活")
	if err := c.Do(ctx, "POST", "/agent/requirements/"+r.Code+"/edit", map[string]any{"detail": "新描述", "reason": "补充", "session_key": "claude:pm"}, nil); err != nil {
		t.Fatal(err)
	}
	// 3. 开发交证据、改为待验收
	if err := c.Do(ctx, "POST", "/agent/requirements/"+r.Code+"/evidence", map[string]any{"kind": "test", "summary": "测试通过", "session_key": "claude:dev"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.Do(ctx, "POST", "/agent/requirements/"+r.Code+"/status", map[string]any{"status": "review", "session_key": "claude:dev"}, nil); err != nil {
		t.Fatal(err)
	}
	// 4. 主助理写验收建议；5. 你在网页上留言
	if err := c.Do(ctx, "POST", "/agent/morning", map[string]any{"session_key": "claude:aide", "advice": []map[string]any{{"requirement": r.Code, "verdict": "pass", "reason": "1/1 达标"}}}, nil); err != nil {
		t.Fatal(err)
	}
	e.call("POST", "/api/requirements/"+r.Code+"/notes", map[string]any{"comment": "看过了"}, nil)
	// 之后所有会话都改了标题：旧记录不跟着变
	title("claude:pm", "后来的标题")
	title("claude:dev", "后来的标题")
	title("claude:aide", "后来的标题")

	got := get(r.Code)
	if len(got.StatusChanges) != 2 {
		t.Fatalf("应有建需求、改待验收两次状态变化：%+v", got.StatusChanges)
	}
	if c0 := got.StatusChanges[0]; c0.Via != "create" || c0.Session != "claude:pm" || c0.Role != "pm" || c0.SessionTitle != "R-187留样与暂缓合并" {
		t.Fatalf("建需求应记主调度和当时的标题：%+v", c0)
	}
	if c1 := got.StatusChanges[1]; c1.Session != "claude:dev" || c1.Role != "dev" || c1.SessionTitle != "抽屉时间线" {
		t.Fatalf("改状态应记开发和当时的标题：%+v", c1)
	}
	if len(got.Evidence) != 1 || got.Evidence[0].SessionKey != "claude:dev" || got.Evidence[0].SessionTitle != "抽屉时间线" || got.Evidence[0].Role != "dev" {
		t.Fatalf("证据应记会话、身份和当时的标题：%+v", got.Evidence)
	}
	var edit, note *store.Decision
	for i, f := range got.Feedback {
		switch f.Kind {
		case store.KindEdit:
			edit = &got.Feedback[i]
		case store.KindNote:
			note = &got.Feedback[i]
		}
	}
	if edit == nil || edit.AssignedBy != "claude:pm" || edit.Context != "pm" || edit.ByTitle != "R-195 派活" {
		t.Fatalf("修改记录应记改的会话、身份和改的那一刻的标题：%+v", edit)
	}
	if note == nil || note.AssignedBy != "" || note.ByTitle != "" {
		t.Fatalf("你在网页上的留言不带会话：%+v", note)
	}
	var m struct {
		Advice []store.Advice `json:"advice"`
	}
	e.call("GET", "/api/morning", nil, &m)
	if len(m.Advice) != 1 || m.Advice[0].By != "claude:aide" || m.Advice[0].ByRole != "aide" || m.Advice[0].ByTitle != "主助理值班" {
		t.Fatalf("验收建议应记主助理和当时的标题：%+v", m.Advice)
	}

	// 6. 旧数据：快照列为空（上线前写的），照常读出，标题为空交给网页兜底
	db := e.store.DB()
	for _, q := range []string{
		`UPDATE status_changes SET session_title = '', session_key = '', role = '' WHERE requirement_id = ?`,
		`UPDATE evidence SET session_title = '', role = '', session_key = NULL WHERE requirement_id = ?`,
		`UPDATE decisions SET by_title = '' WHERE requirement_id = ?`,
	} {
		if _, err := db.Exec(q, r.ID); err != nil {
			t.Fatal(err)
		}
	}
	old := get(r.Code)
	if old.StatusChanges[0].SessionTitle != "" || old.StatusChanges[0].Session != "" || old.Evidence[0].SessionTitle != "" || old.Evidence[0].SessionKey != "" {
		t.Fatalf("旧记录应没有会话与快照：%+v %+v", old.StatusChanges[0], old.Evidence[0])
	}
}
