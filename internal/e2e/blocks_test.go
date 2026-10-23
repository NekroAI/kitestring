package e2e

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/blocks"
	"github.com/NekroAI/kitestring/internal/client"
	"github.com/NekroAI/kitestring/internal/store"
)

// TestEvidenceBlocks：证据、决策、阶段小结都能附表达块（R-97）。写错的块丢掉并说明原因，其余照常保存；
// 块里的图并进证据附件；只给 verdict + blocks 时 hub 拼出纯文本正文；旧证据没有 payload。
func TestEvidenceBlocks(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	hook(t, "claude", map[string]any{"session_id": "bk-1", "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "开工", "prompt_id": "p0"})
	c := client.New(10 * time.Second)
	ctx := context.Background()
	key := "claude:bk-1"

	shot := t.TempDir() + "/shot.png"
	os.WriteFile(shot, tinyPNG, 0o600)
	var img store.FileRef
	if err := c.Upload(ctx, "/agent/files", shot, &img); err != nil {
		t.Fatal(err)
	}
	var r store.Requirement
	if err := c.Do(ctx, "POST", "/agent/requirements", map[string]any{"title": "会话身份", "acceptance": "1. 能声明身份\n2. 压缩后不丢", "status": "in_progress", "session_key": key}, &r); err != nil {
		t.Fatal(err)
	}
	// 旧写法：只有 summary
	if err := c.Do(ctx, "POST", "/agent/requirements/"+r.Code+"/evidence", map[string]any{"kind": "test", "summary": "单测通过", "session_key": key}, nil); err != nil {
		t.Fatal(err)
	}

	var out struct {
		store.Requirement
		BlockWarnings []string `json:"block_warnings"`
	}
	err := c.Do(ctx, "POST", "/agent/requirements/"+r.Code+"/evidence", map[string]any{
		"kind": "screenshot", "session_key": key,
		"verdict": "会话可以声明身份；压缩后不丢",
		"todo":    []string{"看一眼提示词措辞"},
		"aside":   []string{"修了 --data 被忽略"},
		"details": "sessions.role 字段",
		"blocks": []any{
			map[string]any{"type": "checklist", "items": []any{
				map[string]any{"criterion": 1, "text": "能声明身份", "result": "pass", "why": "徽章可见", "see": "fig1"},
				map[string]any{"criterion": 2, "result": "ask"},
			}},
			map[string]any{"id": "fig1", "type": "figure", "image": img.ID, "caption": "图 1"},
			map[string]any{"type": "flow", "mermaid": "flowchart LR; A[ks role]-->B[hub]"},
			map[string]any{"type": "chart", "data": []int{1}},
		},
	}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.BlockWarnings) != 1 || !strings.Contains(out.BlockWarnings[0], "「chart」不认识") {
		t.Fatalf("写错的块应给出提示：%v", out.BlockWarnings)
	}
	var got store.Requirement
	e.call("GET", "/api/requirements/"+r.Code, nil, &got)
	if len(got.Evidence) != 2 || got.Evidence[0].Payload != nil {
		t.Fatalf("旧证据不应有 payload：%+v", got.Evidence)
	}
	ev := got.Evidence[1]
	p := ev.Payload
	if p == nil || p.Verdict != "会话可以声明身份；压缩后不丢" || len(p.Blocks) != 3 || p.Blocks[1].Image == nil || p.Blocks[1].Image.Mime != "image/png" {
		t.Fatalf("结构化证据保存不对：%+v", p)
	}
	if pass, total := p.Score(); pass != 1 || total != 2 {
		t.Fatalf("清单 %d/%d", pass, total)
	}
	if len(ev.Files) != 1 || ev.Files[0].ID != img.ID {
		t.Fatalf("块里的图应并进证据附件（截图类证据因此能交上）：%+v", ev.Files)
	}
	if !strings.HasPrefix(ev.Summary, "会话可以声明身份") || !strings.Contains(ev.Summary, "能声明身份：达标") || !strings.Contains(ev.Summary, "要你做的：看一眼提示词措辞") {
		t.Fatalf("没写 summary 时应拼出纯文本：%q", ev.Summary)
	}

	// 既没有 summary 也没有 verdict：拒绝
	if err := c.Do(ctx, "POST", "/agent/requirements/"+r.Code+"/evidence", map[string]any{"kind": "test", "blocks": []any{map[string]any{"type": "flow", "mermaid": "graph TD; A-->B"}}}, nil); err == nil || !strings.Contains(err.Error(), "verdict") {
		t.Fatalf("没有正文的证据应被拒绝并说明：%v", err)
	}

	// 决策附块
	var d struct {
		store.Decision
		BlockWarnings []string `json:"block_warnings"`
	}
	if err := c.Do(ctx, "POST", "/agent/decisions", map[string]any{"question": "旧 PM 被接管后变成什么？", "session_key": key, "requirement": r.Code,
		"blocks": []any{map[string]any{"type": "options", "items": []any{map[string]any{"label": "无身份", "recommended": true, "image": img.ID}, map[string]any{"label": "降为开发"}}}, map[string]any{"type": "table"}},
	}, &d); err != nil {
		t.Fatal(err)
	}
	if len(d.Blocks) != 1 || d.Blocks[0].Type != blocks.Options || d.Blocks[0].Items[0].Image == nil || len(d.BlockWarnings) != 1 {
		t.Fatalf("决策的块不对：%+v %v", d.Blocks, d.BlockWarnings)
	}
	var dd store.Decision
	if err := c.Do(ctx, "GET", "/agent/decisions/"+d.Code, nil, &dd); err != nil {
		t.Fatal(err)
	}
	if len(dd.Blocks) != 1 {
		t.Fatalf("决策的块应能读回：%+v", dd)
	}

	// 阶段小结附块
	var x store.Plan
	e.call("POST", "/api/plans", map[string]any{"title": "身份", "owner_session": key, "stages": []map[string]any{{"title": "后端", "requirements": []map[string]any{{"ref": r.Code}}}}}, &x)
	e.call("POST", "/api/plans/"+x.Code+"/start", map[string]any{}, &x)
	if err := c.Do(ctx, "POST", "/agent/requirements/"+r.Code+"/status", map[string]any{"status": "review", "session_key": key}, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.Do(ctx, "POST", "/agent/plans/"+x.Code+"/stages/1/submit", map[string]any{"summary": "阶段做完", "session_key": key,
		"blocks": []any{map[string]any{"type": "metrics", "items": []any{map[string]any{"label": "e2e", "value": 2}, map[string]any{"label": "go test", "value": "全过"}}}},
	}, nil); err != nil {
		t.Fatal(err)
	}
	e.call("GET", "/api/plans/"+x.Code, nil, &x)
	if len(x.Stages[0].SummaryBlocks) != 1 || x.Stages[0].SummaryBlocks[0].Type != blocks.Metrics {
		t.Fatalf("阶段小结的块应保存：%+v", x.Stages[0])
	}
	// 阶段验收自动汇总：取最近一份结构化证据的清单（R-111）
	if pr := x.Stages[0].Requirements[0]; pr.CheckPass != 1 || pr.CheckTotal != 2 {
		t.Fatalf("阶段里的需求应带清单汇总 1/2：%+v", pr)
	}
}
