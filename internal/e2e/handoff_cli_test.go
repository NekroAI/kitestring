package e2e

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/client"
	"github.com/NekroAI/kitestring/internal/store"
)

// R-224 兜底：会话的 MCP 是旧版、没有 handoff_write 时，用 ks handoff 写交接——
// 真实编译 ks 命令行跑一遍：写成功、显示在时间线，拒收条件与 handoff_write 一致。
func TestHandoffCLI(t *testing.T) {
	e := setup(t)
	bin := filepath.Join(t.TempDir(), ksExe())
	if out, err := exec.Command("go", "build", "-o", bin, "../../cmd/ks").CombinedOutput(); err != nil {
		t.Fatalf("编译 ks 失败：%v\n%s", err, out)
	}
	ks := func(args ...string) (string, error) {
		out, err := exec.Command(bin, args...).CombinedOutput()
		return string(out), err
	}
	cwd, _ := os.Getwd()
	c := client.New(30 * time.Second)
	ctx := context.Background()
	ev := func(sid, name string, extra map[string]any) {
		p := map[string]any{"session_id": sid, "cwd": cwd, "hook_event_name": name}
		for k, v := range extra {
			p[k] = v
		}
		hook(t, "claude", p)
	}
	ev("hc-pm", "UserPromptSubmit", map[string]any{"prompt": "PM", "prompt_id": "p0"})
	ev("hc-none", "UserPromptSubmit", map[string]any{"prompt": "无身份", "prompt_id": "p0"})
	e.waitFor("会话入库", func() bool { return e.session("claude:hc-pm").Key != "" && e.session("claude:hc-none").Key != "" })
	if err := c.Do(ctx, "POST", "/agent/sessions/role", map[string]any{"session_key": "claude:hc-pm", "role": "pm"}, nil); err != nil {
		t.Fatal(err)
	}

	// 拒收：没有身份 / doing 和 next 都空 / 主调度有子任务在跑
	if out, err := ks("handoff", "--session", "claude:hc-none", "--doing", "x", "--next", "y"); err == nil || !strings.Contains(out, "没有身份") {
		t.Fatalf("没有身份应拒收：%v %s", err, out)
	}
	if out, err := ks("handoff", "--session", "claude:hc-pm"); err == nil || !strings.Contains(out, "交接是空的") || !strings.Contains(out, "--caveats") {
		t.Fatalf("什么都没写应拒收：%v %s", err, out)
	}
	sub := map[string]any{"agent_id": "a-hc", "agent_type": "general-purpose"}
	ev("hc-pm", "SubagentStart", sub)
	e.waitFor("子任务在跑", func() bool { return len(e.session("claude:hc-pm").Tasks) == 1 })
	if out, err := ks("handoff", "--session", "claude:hc-pm", "--doing", "R-1 做到一半", "--next", "交验收"); err == nil || !strings.Contains(out, "子任务在跑") {
		t.Fatalf("主调度有子任务在跑应拒收：%v %s", err, out)
	}
	ev("hc-pm", "SubagentStop", sub)
	e.waitFor("子任务交完", func() bool { return len(e.session("claude:hc-pm").Tasks) == 0 })

	// 参数写成功，显示在时间线
	if out, err := ks("handoff", "--session", "claude:hc-pm", "--doing", "R-1 做到一半", "--next", "交验收", "--notes", "别停 5199"); err != nil || !strings.Contains(out, "交接已存") {
		t.Fatalf("ks handoff 应写成功：%v %s", err, out)
	}
	// --file markdown 覆盖上一份，命令行参数优先于文件
	md := filepath.Join(t.TempDir(), "h.md")
	os.WriteFile(md, []byte("# 交接\n## 在做的\nR-2 联调\n\n## 等用户的\nD-9\n## 约定和规矩\n部署只走脚本\n## 下一步\n跑全量检查\n## 注意事项\n别碰线上\n"), 0o644)
	if out, err := ks("handoff", "--session", "claude:hc-pm", "--file", md, "--next", "命令行的下一步"); err != nil {
		t.Fatalf("--file 应写成功：%v %s", err, out)
	}
	js := filepath.Join(t.TempDir(), "h.json")
	os.WriteFile(js, []byte(`{"doing":"R-3 JSON 来的","next":"收尾"}`), 0o644)
	if out, err := ks("handoff", "--session", "claude:hc-pm", "--file", js); err != nil {
		t.Fatalf("JSON --file 应写成功：%v %s", err, out)
	}
	var detail struct {
		Handoffs []store.Handoff `json:"handoffs"`
	}
	e.call("GET", "/api/sessions/claude:hc-pm", nil, &detail)
	if len(detail.Handoffs) != 1 || detail.Handoffs[0].Doing != "R-3 JSON 来的" || detail.Handoffs[0].Next != "收尾" {
		t.Fatalf("时间线上应有最后一份交接：%+v", detail.Handoffs)
	}
	// markdown 解析：单独验证落库内容
	if out, err := ks("handoff", "--session", "claude:hc-pm", "--file", md, "--next", "命令行的下一步"); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	e.call("GET", "/api/sessions/claude:hc-pm", nil, &detail)
	h := detail.Handoffs[0]
	if h.Doing != "R-2 联调" || h.Waiting != "D-9" || h.Rules != "部署只走脚本" || h.Next != "命令行的下一步" || h.Caveats != "别碰线上" {
		t.Fatalf("markdown 分栏不对：%+v", h)
	}

	// R-245：三栏——做到一半的事、口头约定、注意事项；只写三栏时不提示旧栏目，写多了提示压缩
	md3 := filepath.Join(t.TempDir(), "h3.md")
	os.WriteFile(md3, []byte("## 做到一半的事\nR-4 做到联调\n## 口头约定\n用户说这次可以 push\n## 注意事项\n别停 5199\n"), 0o644)
	out, err := ks("handoff", "--session", "claude:hc-pm", "--file", md3)
	if err != nil || strings.Contains(out, "旧栏目") || strings.Contains(out, "字数提示") {
		t.Fatalf("三栏交接应直接存下、不带提示：%v %s", err, out)
	}
	e.call("GET", "/api/sessions/claude:hc-pm", nil, &detail)
	if h := detail.Handoffs[0]; h.Doing != "R-4 做到联调" || h.Agreements != "用户说这次可以 push" || h.Caveats != "别停 5199" {
		t.Fatalf("三栏分栏不对：%+v", h)
	}
	if out, err := ks("handoff", "--session", "claude:hc-pm", "--doing", strings.Repeat("很长的交接", 70)); err != nil || !strings.Contains(out, "超过 300 字") {
		t.Fatalf("写多了应照常保存并提示压缩：%v %s", err, out)
	}
}
