package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/client"
	"github.com/NekroAI/kitestring/internal/store"
)

// 没有可用开发时提醒你（R-87）：PM 请你开开发会话 → 记在项目上并推送（含原因、接入语、项目页链接）；
// auto 派活找不到在线开发时自动提醒；开发上线够数后自动清除；非 PM 不能提。
func TestNeedDevs(t *testing.T) {
	e := setup(t)
	pushed := make(chan map[string]any, 4)
	bark := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m map[string]any
		_ = json.NewDecoder(r.Body).Decode(&m)
		pushed <- m
	}))
	defer bark.Close()
	e.call("PUT", "/api/settings", map[string]any{"public_url": "https://ks.example", "ai": map[string]any{}, "summary_base_url": "", "summary_model": "", "notify_default": map[string]any{"bark": map[string]any{"server": bark.URL, "key": "k"}}}, nil)

	cwd, _ := os.Getwd()
	c := client.New(30 * time.Second)
	ctx := context.Background()
	for _, sid := range []string{"pm", "dev"} {
		hook(t, "claude", map[string]any{"session_id": sid, "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "上线", "prompt_id": "p0"})
	}
	e.waitFor("会话入库", func() bool { return e.session("claude:dev").Key != "" })
	if err := c.Do(ctx, "POST", "/agent/sessions/role", map[string]any{"session_key": "claude:pm", "role": "pm"}, nil); err != nil {
		t.Fatal(err)
	}
	project := func() store.Project {
		var out struct {
			Projects []store.Project `json:"projects"`
		}
		e.call("GET", "/api/projects", nil, &out)
		for _, p := range out.Projects {
			if p.ID == e.session("claude:pm").ProjectID {
				return p
			}
		}
		return store.Project{}
	}
	got := func(want ...string) {
		t.Helper()
		select {
		case m := <-pushed:
			b, _ := json.Marshal(m)
			for _, w := range want {
				if !strings.Contains(string(b), w) {
					t.Fatalf("推送应包含 %q：%s", w, b)
				}
			}
		case <-time.After(10 * time.Second):
			t.Fatal("没有收到推送")
		}
	}

	if err := c.Do(ctx, "POST", "/agent/team/need", map[string]any{"session_key": "claude:dev", "count": 1, "reason": "x"}, nil); err == nil {
		t.Fatal("非 PM 不能请用户开开发会话")
	}
	// auto 派活找不到在线开发 → 自动提醒
	var r store.Requirement
	if err := c.Do(ctx, "POST", "/agent/requirements", map[string]any{"title": "导出 CSV", "session_key": "claude:pm"}, &r); err != nil {
		t.Fatal(err)
	}
	var out struct {
		Assigned bool   `json:"assigned"`
		Message  string `json:"message"`
	}
	e.allowRelease("claude:pm")
	if err := c.Do(ctx, "POST", "/agent/requirements/"+r.Code+"/assign", map[string]any{"session_key": "claude:pm", "reason": "做法明确", "to": "auto"}, &out); err != nil {
		t.Fatal(err)
	}
	if out.Assigned || !strings.Contains(out.Message, "已推送提醒") {
		t.Fatalf("应未派出并已提醒：%+v", out)
	}
	got("需要 1 个 Dev 会话", "导出 CSV", "ks role dev", "https://ks.example/projects/")
	if p := project(); p.NeedDevs != 1 || !strings.Contains(p.NeedReason, "导出 CSV") || p.NeedAt == nil {
		t.Fatalf("项目上应记下需要 1 个开发：%+v", p)
	}

	// PM 改为需要 2 个：重新推送
	if err := c.Do(ctx, "POST", "/agent/team/need", map[string]any{"session_key": "claude:pm", "count": 2, "reason": "5 条前端需求排队，改动大、要你频繁看效果"}, nil); err != nil {
		t.Fatal(err)
	}
	got("需要 2 个 Dev 会话", "要你频繁看效果")

	// 一个开发上线：还不够，不清除
	if err := c.Do(ctx, "POST", "/agent/sessions/role", map[string]any{"session_key": "claude:dev", "role": "dev"}, nil); err != nil {
		t.Fatal(err)
	}
	if p := project(); p.NeedDevs != 2 {
		t.Fatalf("只上线 1 个开发，不应清除：%+v", p.NeedDevs)
	}
	// 第二个开发上线：够数，清除
	hook(t, "claude", map[string]any{"session_id": "dev-2", "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "上线", "prompt_id": "p0"})
	e.waitFor("dev-2 入库", func() bool { return e.session("claude:dev-2").Key != "" })
	if err := c.Do(ctx, "POST", "/agent/sessions/role", map[string]any{"session_key": "claude:dev-2", "role": "dev"}, nil); err != nil {
		t.Fatal(err)
	}
	e.waitFor("提醒清除", func() bool { return project().NeedDevs == 0 })
}
