package e2e

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/client"
	"github.com/NekroAI/kitestring/internal/roles"
	"github.com/NekroAI/kitestring/internal/store"
)

// 子任务模型策略（R-228）：项目设置三档，默认均衡；改了之后 ks role 和恢复 / 压缩补发的身份提示词立刻是新档；
// 主调度、主助理、开发带，顾问不带；订阅了 settings 的会话收到一条只告知的说明；非法档位被拒。
func TestModelPolicy(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	c := client.New(30 * time.Second)
	ctx := context.Background()
	for _, sid := range []string{"pm", "dev", "adv"} {
		hook(t, "claude", map[string]any{"session_id": sid, "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "上线", "prompt_id": "p0"})
	}
	e.waitFor("会话入库", func() bool { return e.session("claude:dev").ProjectID != "" })
	project := e.session("claude:dev").ProjectID
	rolePrompt := func(key, role string) string {
		t.Helper()
		var out struct {
			Prompt string `json:"prompt"`
		}
		if err := c.Do(ctx, "POST", "/agent/sessions/role", map[string]any{"session_key": key, "role": role}, &out); err != nil {
			t.Fatal(err)
		}
		return out.Prompt
	}
	setPolicy := func(v string) (int, string) {
		return e.webTry("PUT", "/api/projects/"+project, map[string]any{"name": "kitestring", "doc_root": "", "model_policy": v})
	}

	// 默认均衡
	if p := rolePrompt("claude:pm", "pm"); !strings.Contains(p, "子任务模型策略") || !strings.Contains(p, "「均衡」") || !strings.Contains(p, "模型：<档位>") {
		t.Fatalf("默认应是均衡并写明派活标档位：%s", p)
	}
	if p := rolePrompt("claude:adv", "advisor"); strings.Contains(p, "子任务模型策略") {
		t.Fatalf("顾问不派子任务，不带策略：%s", p)
	}
	rolePrompt("claude:dev", "dev")
	woke(t, parkWait(t, e, c, "claude:pm"), "已上线待命") // 开发上线通知（要动手档）

	// 改成经济：PM 收到只告知的说明，不被叫醒；ks role / 压缩 / 恢复补发都是新档
	pmW := parkWait(t, e, c, "claude:pm")
	if code, body := setPolicy("economy"); code != 200 {
		t.Fatalf("改策略失败：%d %s", code, body)
	}
	drainQuiet(t, pmW, "PM（改设置只告知）", "pm", "子任务模型策略", "「均衡」改为「经济」")
	for _, role := range [][2]string{{"claude:pm", "pm"}, {"claude:dev", "dev"}} {
		p := rolePrompt(role[0], role[1])
		if !strings.Contains(p, "「经济」") || strings.Contains(p, "「均衡」") || !strings.Contains(p, roles.PolicyNote(role[1], "economy")) {
			t.Fatalf("%s 的 ks role 应是经济档：%s", role[1], p)
		}
	}
	for _, src := range []string{"compact", "resume"} {
		sid := "dev"
		e.waitFor("补发含经济档（"+src+"）", func() bool {
			return strings.Contains(hook(t, "claude", map[string]any{"session_id": sid, "cwd": cwd, "hook_event_name": "SessionStart", "source": src}), "「经济」")
		})
	}
	var ps struct {
		Projects []store.Project `json:"projects"`
	}
	e.call("GET", "/api/projects", nil, &ps)
	got := ""
	for _, p := range ps.Projects {
		if p.ID == project {
			got = p.ModelPolicy
		}
	}
	if got != "economy" {
		t.Fatalf("设置应已保存：%q", got)
	}

	// 不传 model_policy 的其它设置改动不会把它清掉；非法档位被拒
	if code, body := e.webTry("PUT", "/api/projects/"+project, map[string]any{"name": "kitestring", "doc_root": "", "hidden": false}); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	if p := rolePrompt("claude:pm", "pm"); !strings.Contains(p, "「经济」") {
		t.Fatalf("改别的设置不应重置策略：%s", p)
	}
	if code, _ := setPolicy("最贵"); code == 200 {
		t.Fatal("不认识的档位应被拒")
	}
	if code, _ := setPolicy("quality"); code != 200 {
		t.Fatal("质量优先应可设")
	}
	if p := rolePrompt("claude:pm", "pm"); !strings.Contains(p, "「质量优先」") {
		t.Fatalf("应是质量优先：%s", p)
	}
}
