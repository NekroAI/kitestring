package e2e

import (
	"testing"

	"github.com/NekroAI/kitestring/internal/store"
)

// R-51：误识别的目录可以隐藏，或并入真正的项目（会话、需求跟着走）。
func TestProjectHideAndMerge(t *testing.T) {
	e := setup(t)
	a, b := t.TempDir(), t.TempDir() // 两个不在 git 里的目录：各自成为一个项目
	hook(t, "claude", map[string]any{"session_id": "pm-a", "cwd": a, "hook_event_name": "UserPromptSubmit", "prompt": "x", "prompt_id": "p"})
	hook(t, "claude", map[string]any{"session_id": "pm-b", "cwd": b, "hook_event_name": "UserPromptSubmit", "prompt": "x", "prompt_id": "p"})
	e.waitFor("两个会话都归了项目", func() bool { return e.session("claude:pm-a").ProjectID != "" && e.session("claude:pm-b").ProjectID != "" })
	pa, pb := e.session("claude:pm-a").ProjectID, e.session("claude:pm-b").ProjectID
	if pa == pb {
		t.Fatal("两个目录应是两个项目")
	}
	var r store.Requirement
	e.call("POST", "/api/requirements", map[string]any{"title": "误放的需求", "project_id": pa}, &r)

	list := func() []store.Project {
		var out struct {
			Projects []store.Project `json:"projects"`
		}
		e.call("GET", "/api/projects", nil, &out)
		return out.Projects
	}
	find := func(id string) *store.Project {
		for _, p := range list() {
			if p.ID == id {
				return &p
			}
		}
		return nil
	}

	// 隐藏：只改 hidden，其它设置不变
	e.call("PUT", "/api/projects/"+pa, map[string]any{"name": "a", "doc_root": "", "hidden": true}, nil)
	if p := find(pa); p == nil || !p.Hidden || !p.AutoDispatch {
		t.Fatalf("隐藏后应标记 hidden、自动送达不变：%+v", p)
	}

	// 不能合并到自己
	if code, _ := e.try("POST", "/api/projects/"+pa+"/merge", map[string]any{"into": pa}); code != 400 {
		t.Fatalf("合并到自己应被拒绝：%d", code)
	}
	// 合并：a 消失，会话和需求归到 b
	e.call("POST", "/api/projects/"+pa+"/merge", map[string]any{"into": pb}, nil)
	if find(pa) != nil {
		t.Fatal("合并后原项目应删除")
	}
	if got := e.session("claude:pm-a").ProjectID; got != pb {
		t.Fatalf("会话应改归目标项目：%s", got)
	}
	var got store.Requirement
	e.call("GET", "/api/requirements/"+r.Code, nil, &got)
	if got.ProjectID != pb {
		t.Fatalf("需求应改归目标项目：%s", got.ProjectID)
	}
	// 之后该目录的新会话也归到 b
	hook(t, "claude", map[string]any{"session_id": "pm-a2", "cwd": a, "hook_event_name": "UserPromptSubmit", "prompt": "x", "prompt_id": "p"})
	e.waitFor("新会话归到合并后的项目", func() bool { return e.session("claude:pm-a2").ProjectID == pb })
}
