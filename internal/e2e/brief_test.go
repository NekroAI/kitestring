package e2e

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/client"
	"github.com/NekroAI/kitestring/internal/store"
)

// PM 简报（R-113）：PM 写入后项目上能读到正文、署名与时间；非 PM 不能写；写空字符串清除。
func TestBrief(t *testing.T) {
	e := setup(t)
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

	if err := c.Do(ctx, "POST", "/agent/brief", map[string]any{"session_key": "claude:dev", "text": "开发不该能写"}, nil); err == nil {
		t.Fatal("非 PM 写简报应被拒绝")
	}
	if err := c.Do(ctx, "POST", "/agent/brief", map[string]any{"session_key": "claude:pm", "text": "  R-1 已交，等你验收。  "}, nil); err != nil {
		t.Fatal(err)
	}
	p := project()
	if p.Brief != "R-1 已交，等你验收。" || p.BriefBy != "claude:pm" || p.BriefAt == nil {
		t.Fatalf("简报应写入并去掉首尾空白：%+v", p)
	}
	// 结构化字段（R-212）：超标照常保存并返回提示压缩；空行丢掉
	long := "这是一句明显超过四十个字的结论，写得又长又啰嗦，常驻条一行肯定放不下，应该被提示压缩一下才对"
	var out struct {
		Hint string `json:"hint"`
	}
	if err := c.Do(ctx, "POST", "/agent/brief", map[string]any{"session_key": "claude:pm", "text": "正文", "headline": long, "look": []string{"R-1 验收", " ", "R-2", "R-3", "R-4"}}, &out); err != nil {
		t.Fatal(err)
	}
	if p := project(); p.BriefHeadline != long || len(p.BriefLook) != 4 {
		t.Fatalf("超标的结论和要先看的应照常保存：%+v", p)
	}
	if out.Hint == "" {
		t.Fatal("超标应返回压缩提示")
	}
	if err := c.Do(ctx, "POST", "/agent/brief", map[string]any{"session_key": "claude:pm", "text": ""}, nil); err != nil {
		t.Fatal(err)
	}
	if p := project(); p.Brief != "" || p.BriefBy != "" || p.BriefAt != nil {
		t.Fatalf("写空字符串应清除简报：%+v", p)
	}
}
