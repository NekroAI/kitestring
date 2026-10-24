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

// 作废交错的证据（R-189）：交证据的会话能作废；不写理由被拒；别的 Dev 会话被拒；主调度 / 主助理能作废；
// 作废后证据上留着谁、什么身份、什么时候、为什么，网页接口里标记正确；已作废的不能重复作废。
func TestVoidEvidence(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	c := client.New(30 * time.Second)
	ctx := context.Background()
	for _, sid := range []string{"pm", "aide", "dev-a", "dev-b"} {
		hook(t, "claude", map[string]any{"session_id": sid, "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "开工", "prompt_id": "p0"})
	}
	e.waitFor("会话入库", func() bool { return e.session("claude:dev-b").Key != "" && e.session("claude:aide").Key != "" })
	for k, r := range map[string]string{"claude:pm": "pm", "claude:aide": "aide", "claude:dev-a": "dev", "claude:dev-b": "dev"} {
		if err := c.Do(ctx, "POST", "/agent/sessions/role", map[string]any{"session_key": k, "role": r}, nil); err != nil {
			t.Fatal(err)
		}
	}
	var r store.Requirement
	if err := c.Do(ctx, "POST", "/agent/requirements", map[string]any{"title": "导出 CSV", "session_key": "claude:dev-a"}, &r); err != nil {
		t.Fatal(err)
	}
	attach := func(sum string) string {
		var out struct {
			EvidenceID string `json:"evidence_id"`
		}
		if err := c.Do(ctx, "POST", "/agent/requirements/"+r.Code+"/evidence", map[string]any{"kind": "test", "summary": sum, "session_key": "claude:dev-a"}, &out); err != nil || out.EvidenceID == "" {
			t.Fatalf("交证据应返回证据 id：%v %q", err, out.EvidenceID)
		}
		return out.EvidenceID
	}
	void := func(id, key, reason string) error {
		return c.Do(ctx, "POST", "/agent/evidence/"+id+"/void", map[string]any{"session_key": key, "reason": reason}, nil)
	}
	get := func() store.Requirement {
		var got store.Requirement
		e.call("GET", "/api/requirements/"+r.Code, nil, &got)
		return got
	}
	ev1, ev2, ev3 := attach("x"), attach("第二份"), attach("第三份")

	// 没写理由被拒
	if err := void(ev1, "claude:dev-a", "  "); err == nil || !strings.Contains(err.Error(), "理由") {
		t.Fatalf("没写理由应被拒：%v", err)
	}
	// 别的 Dev 会话被拒
	if err := void(ev1, "claude:dev-b", "看着不对"); err == nil || !strings.Contains(err.Error(), "只有交这条证据") {
		t.Fatalf("别的 Dev 作废应被拒：%v", err)
	}
	for _, x := range get().Evidence {
		if x.Voided {
			t.Fatal("被拒后证据不应被作废")
		}
	}
	// 交证据的会话能作废
	if err := void(ev1, "claude:dev-a", "只写了 x，误交"); err != nil {
		t.Fatal(err)
	}
	// 主调度能作废别人交的；主助理也能
	if err := void(ev2, "claude:pm", "字数超标，已重交"); err != nil {
		t.Fatal(err)
	}
	if err := void(ev3, "claude:aide", "重复"); err != nil {
		t.Fatal(err)
	}
	// 已作废的不能重复作废
	if err := void(ev1, "claude:dev-a", "再来一次"); err == nil || !strings.Contains(err.Error(), "已经作废") {
		t.Fatalf("重复作废应被拒：%v", err)
	}

	got := get()
	if len(got.Evidence) != 3 {
		t.Fatalf("作废的证据仍保留原文，应有 3 条：%d", len(got.Evidence))
	}
	want := map[string][3]string{ev1: {"只写了 x，误交", "claude:dev-a", "dev"}, ev2: {"字数超标，已重交", "claude:pm", "pm"}, ev3: {"重复", "claude:aide", "aide"}}
	for _, x := range got.Evidence {
		w := want[x.ID]
		if !x.Voided || x.VoidReason != w[0] || x.VoidedBy != w[1] || x.VoidedRole != w[2] || x.VoidedAt == nil {
			t.Fatalf("作废留痕不符：%+v", x)
		}
	}
	// 没作废的证据照常
	ev4 := attach("合格的")
	if err := void(ev4, "claude:dev-b", "不是我交的"); err == nil {
		t.Fatal("别的 Dev 不能作废")
	}
	for _, x := range get().Evidence {
		if x.ID == ev4 && (x.Voided || x.VoidReason != "") {
			t.Fatalf("未作废的证据被改动：%+v", x)
		}
	}
}
