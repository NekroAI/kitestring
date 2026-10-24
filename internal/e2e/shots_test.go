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

// TestEvidenceShots：证据的附图带标题和改动框（R-183）。images 按给的顺序排在 files 后面；写错的框丢掉并提示，证据照常保存；
// 没写标题的图在 image_hints 里提示；figure 块的标题和框随图并进附件；旧写法（只给 files）照常保存、没有标题。
func TestEvidenceShots(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	hook(t, "claude", map[string]any{"session_id": "sh-1", "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "开工", "prompt_id": "p0"})
	c := client.New(10 * time.Second)
	ctx := context.Background()
	key := "claude:sh-1"
	up := func(name string) store.FileRef {
		p := t.TempDir() + "/" + name
		os.WriteFile(p, tinyPNG, 0o600)
		var f store.FileRef
		if err := c.Upload(ctx, "/agent/files", p, &f); err != nil {
			t.Fatal(err)
		}
		return f
	}
	a, b, f := up("legacy.png"), up("drawer.png"), up("fig.png")
	var r store.Requirement
	if err := c.Do(ctx, "POST", "/agent/requirements", map[string]any{"title": "截图标题", "status": "in_progress", "session_key": key}, &r); err != nil {
		t.Fatal(err)
	}
	// 旧写法：只有 files
	if err := c.Do(ctx, "POST", "/agent/requirements/"+r.Code+"/evidence", map[string]any{"kind": "screenshot", "summary": "旧证据", "files": []string{a.ID}, "session_key": key}, nil); err != nil {
		t.Fatal(err)
	}
	var out struct {
		store.Requirement
		BlockWarnings []string `json:"block_warnings"`
		ImageHints    []string `json:"image_hints"`
	}
	err := c.Do(ctx, "POST", "/agent/requirements/"+r.Code+"/evidence", map[string]any{
		"kind": "screenshot", "session_key": key, "verdict": "截图带标题",
		"files": []string{a.ID},
		"images": []any{
			map[string]any{"id": b.ID, "title": "审批抽屉 · 桌面", "boxes": []any{
				map[string]any{"x": 0.1, "y": 0.2, "w": 0.3, "h": 0.1, "label": "标题"},
				map[string]any{"x": 300, "y": 20, "w": 100, "h": 40},
			}},
		},
		"blocks": []any{map[string]any{"type": "figure", "image": f.ID, "title": "工作台", "boxes": []any{[]any{0.5, 0.5, 0.2, 0.2, "这里"}}}},
	}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.ImageHints) != 2 || !strings.Contains(out.ImageHints[0], "像素坐标") || !strings.Contains(out.ImageHints[1], "legacy.png") {
		t.Fatalf("应提示写错的框和没标题的图：%v", out.ImageHints)
	}
	var got store.Requirement
	e.call("GET", "/api/requirements/"+r.Code, nil, &got)
	if len(got.Evidence) != 2 {
		t.Fatalf("两份证据都应保存：%d", len(got.Evidence))
	}
	if old := got.Evidence[0].Files; len(old) != 1 || old[0].Title != "" || old[0].Boxes != nil {
		t.Fatalf("旧证据不应有标题和框：%+v", old)
	}
	fs := got.Evidence[1].Files
	if len(fs) != 3 || fs[0].ID != a.ID || fs[1].ID != b.ID || fs[2].ID != f.ID {
		t.Fatalf("附图顺序应是 files、images、块里的图：%+v", fs)
	}
	if fs[1].Title != "审批抽屉 · 桌面" || len(fs[1].Boxes) != 1 || fs[1].Boxes[0].Label != "标题" {
		t.Fatalf("images 的标题和框不对：%+v", fs[1])
	}
	if fs[2].Title != "工作台" || len(fs[2].Boxes) != 1 || fs[2].Boxes[0].X != 0.5 {
		t.Fatalf("figure 块的标题和框应随图并进附件：%+v", fs[2])
	}
	if img := got.Evidence[1].Payload.Blocks[0].Image; img == nil || len(img.Boxes) != 1 {
		t.Fatalf("块里的图应带框：%+v", img)
	}
}
