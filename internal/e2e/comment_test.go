package e2e

import (
	"context"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/client"
	"github.com/NekroAI/kitestring/internal/store"
)

// R-249 P1：会话在需求下留言（ks req comment / req_comment 同一个接口）——真实编译 ks 跑一遍：
// 署名由当前会话决定（识别得到时 --session 不一致就拒绝，识别不到才按 --session 并注明），附图和表达块保存下来；
// 留言和用户留言进同一条时间线、不送达任何会话；留言超 600 字、团队消息超 120 字照常保存 / 发送并提示。
func TestReqComment(t *testing.T) {
	e := setup(t)
	bin := filepath.Join(t.TempDir(), "ks")
	if out, err := exec.Command("go", "build", "-o", bin, "../../cmd/ks").CombinedOutput(); err != nil {
		t.Fatalf("编译 ks 失败：%v\n%s", err, out)
	}
	// 跑测试的环境本身可能在 Claude Code / DSH 里：去掉这些变量，ks 只按给的环境识别会话
	baseEnv := []string{}
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "CLAUDE_CODE_SESSION_ID=") || strings.HasPrefix(kv, "DSH_") {
			continue
		}
		baseEnv = append(baseEnv, kv)
	}
	ks := func(env []string, args ...string) (string, error) {
		cmd := exec.Command(bin, args...)
		cmd.Env = append(append([]string{}, baseEnv...), env...)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	cwd, _ := os.Getwd()
	c := client.New(30 * time.Second)
	ctx := context.Background()
	for _, sid := range []string{"cm-pm", "cm-dev"} {
		hook(t, "claude", map[string]any{"session_id": sid, "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "上线", "prompt_id": "p0"})
		e.waitFor("会话入库", func() bool { return e.session("claude:"+sid).Key != "" })
	}
	for key, role := range map[string]string{"claude:cm-pm": "pm", "claude:cm-dev": "dev"} {
		if err := c.Do(ctx, "POST", "/agent/sessions/role", map[string]any{"session_key": key, "role": role}, nil); err != nil {
			t.Fatal(err)
		}
	}
	var r store.Requirement
	if err := c.Do(ctx, "POST", "/agent/requirements", map[string]any{"title": "会话通信", "session_key": "claude:cm-dev", "status": "in_progress"}, &r); err != nil {
		t.Fatal(err)
	}
	// 用户在网页上留言（老的那种）
	e.call("POST", "/api/requirements/"+r.Code+"/notes", map[string]any{"comment": "用户：先做留言"}, nil)

	img := filepath.Join(t.TempDir(), "shot.png")
	f, _ := os.Create(img)
	_ = png.Encode(f, image.NewRGBA(image.Rect(0, 0, 4, 4)))
	f.Close()

	// 识别得到当前会话（Claude 环境变量）时：署名就是它
	devEnv := []string{"CLAUDE_CODE_SESSION_ID=cm-dev"}
	out, err := ks(devEnv, "req", "comment", r.Code, "接口改成分页：每页 50 条，游标翻页。", "--image", img, "--blocks", `[{"type":"table","columns":["项","值"],"rows":[["每页","50"]]}]`)
	if err != nil || !strings.Contains(out, "署名：开发 claude:cm-dev") {
		t.Fatalf("ks req comment 应署名为当前会话：%v\n%s", err, out)
	}
	// 冒用：识别得到 cm-dev，却用 --session 写成主调度 → 拒绝
	if out, err := ks(devEnv, "req", "comment", r.Code, "冒用", "--session", "claude:cm-pm"); err == nil || !strings.Contains(out, "不一致") {
		t.Fatalf("--session 与当前会话不一致应拒绝：%v\n%s", err, out)
	}
	// 识别不到（如部分 Codex 会话）时 ks 才按 --session 自报（session_claimed）：hub 校验后署名并注明；不存在的会话拒绝。
	// 测试进程里 daemon 按父进程总能认出会话，这一支直接调接口（ks 这边的判断见 cmd/ks 的 resolveTellSender 单测）
	var cr struct {
		Message string `json:"message"`
	}
	if err := c.Do(ctx, "POST", "/agent/requirements/"+r.Code+"/comments", map[string]any{"session_key": "claude:cm-pm", "text": "主调度补一句", "session_claimed": true}, &cr); err != nil || !strings.Contains(cr.Message, "已按 --session claude:cm-pm 署名") {
		t.Fatalf("识别不到时应按 --session 署名并注明：%+v %v", cr, err)
	}
	if err := c.Do(ctx, "POST", "/agent/requirements/"+r.Code+"/comments", map[string]any{"session_key": "claude:nobody", "text": "x", "session_claimed": true}, &cr); err == nil {
		t.Fatal("不存在的会话不能署名")
	}
	// 超 600 字照常保存并提示
	if out, err := ks(devEnv, "req", "comment", r.Code, strings.Repeat("长", 601)); err != nil || !strings.Contains(out, "留言 601 字，超过 600 字") {
		t.Fatalf("超长留言应保存并提示：%v\n%s", err, out)
	}

	var got store.Requirement
	if err := c.Do(ctx, "GET", "/agent/requirements/"+r.Code, nil, &got); err != nil {
		t.Fatal(err)
	}
	var comments []store.Decision
	notes := 0
	for _, d := range got.Feedback {
		switch d.Kind {
		case store.KindComment:
			comments = append(comments, d)
		case store.KindNote:
			notes++
		}
	}
	if len(comments) != 3 || notes != 1 {
		t.Fatalf("时间线应有 3 条会话留言、1 条用户留言：%+v", got.Feedback)
	}
	first := comments[2] // 时间倒序
	if first.AssignedBy != "claude:cm-dev" || first.FromRole != "dev" || first.Status != store.DecClosed || len(first.AnswerFiles) != 1 || len(first.Blocks) != 1 || !strings.Contains(first.Answer, "游标翻页") {
		t.Fatalf("第一条留言内容 / 署名不对：%+v", first)
	}
	if comments[1].AssignedBy != "claude:cm-pm" || comments[1].FromRole != "pm" {
		t.Fatalf("按 --session 署名的留言不对：%+v", comments[1])
	}

	// 团队消息超 120 字照常送出，返回里提示把细节写进留言；短消息不提示
	var tr tellRes
	if err := c.Do(ctx, "POST", "/agent/team/tell", map[string]any{"session_key": "claude:cm-dev", "text": strings.Repeat("细", 121), "requirement": r.Code}, &tr); err != nil || !tr.Delivered || !strings.Contains(tr.Message, "消息 121 字，超过 120 字") || !strings.Contains(tr.Message, "细节写到 ks req comment") {
		t.Fatalf("长消息应照常送出并提示：%+v %v", tr, err)
	}
	if err := c.Do(ctx, "POST", "/agent/team/tell", map[string]any{"session_key": "claude:cm-dev", "text": r.Code + " 有新留言，请看", "requirement": r.Code}, &tr); err != nil || !tr.Delivered || strings.Contains(tr.Message, "字数提示") {
		t.Fatalf("短消息不该提示：%+v %v", tr, err)
	}
	// ks team tell 走同一个接口，命令行也看得到提示
	if out, err := ks(devEnv, "team", "tell", strings.Repeat("话", 130)); err != nil || !strings.Contains(out, "消息 130 字") {
		t.Fatalf("ks team tell 长消息应提示：%v\n%s", err, out)
	}
}
