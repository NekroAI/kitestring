package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/client"
	"github.com/NekroAI/kitestring/internal/hub"
	"github.com/NekroAI/kitestring/internal/roles"
)

// 身份提示词可配置（R-98）：网页改写后 ks role 与压缩补发都用改写版；恢复默认后回到内置模板；
// Agent（设备令牌）改不了；默认模板更新后，改写过的身份标出“默认模板已更新”。
func TestRolePromptCustom(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	hook(t, "claude", map[string]any{"session_id": "dev-1", "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "开工", "prompt_id": "p0"})
	e.waitFor("会话入库", func() bool { return e.session("claude:dev-1").Key != "" })
	c := client.New(30 * time.Second)
	ctx := context.Background()
	var out struct {
		Prompt string `json:"prompt"`
	}
	setRole := func() string {
		t.Helper()
		if err := c.Do(ctx, "POST", "/agent/sessions/role", map[string]any{"session_key": "claude:dev-1", "role": "dev"}, &out); err != nil {
			t.Fatal(err)
		}
		return out.Prompt
	}
	compact := func() string {
		return hook(t, "claude", map[string]any{"session_id": "dev-1", "cwd": cwd, "hook_event_name": "SessionStart", "source": "compact"})
	}
	if p := setRole(); p != roles.Prompt(roles.Dev) {
		t.Fatalf("没改写时 ks role 应是默认模板")
	}

	var list []hub.RolePromptView
	e.call("GET", "/api/role-prompts", nil, &list)
	if len(list) != 4 || list[1].Role != "dev" || list[1].Custom || list[1].Body != roles.DefaultBody(roles.Dev) {
		t.Fatalf("应列出 4 个身份、dev 未改写：%+v", list)
	}

	// 改写：ks role 与压缩补发都用新文案，抬头仍在
	const mine = "我自己写的 Dev 提示词：先读需求再动手。"
	var v hub.RolePromptView
	e.call("PUT", "/api/role-prompts/dev", map[string]any{"body": mine}, &v)
	if !v.Custom || v.Body != mine || v.Outdated {
		t.Fatalf("改写后应为自定义：%+v", v)
	}
	if p := setRole(); !strings.Contains(p, mine) || !strings.Contains(p, "你的会话身份：Dev") || strings.Contains(p, strings.SplitN(roles.DefaultBody(roles.Dev), "\n", 2)[0]) {
		t.Fatalf("ks role 应用改写版：%s", p)
	}
	e.waitFor("压缩补发用改写版", func() bool { return strings.Contains(compact(), mine) })

	// 空内容不接受
	if err := tryWeb(e, "PUT", "/api/role-prompts/dev", map[string]any{"body": "  "}); err == 0 || err/100 == 2 {
		t.Fatalf("空提示词应被拒绝：%d", err)
	}

	// Agent 不能改
	for _, call := range [][2]string{{"PUT", "/role-prompts/dev"}, {"DELETE", "/role-prompts/dev"}, {"PUT", "/agent/role-prompts/dev"}} {
		if err := c.Do(ctx, call[0], call[1], map[string]any{"body": "被 Agent 篡改"}, nil); err == nil {
			t.Fatalf("Agent 不应能 %s %s", call[0], call[1])
		}
	}
	if p := setRole(); !strings.Contains(p, mine) || strings.Contains(p, "被 Agent 篡改") {
		t.Fatalf("Agent 的改动不应生效：%s", p)
	}

	// 默认模板更新：把记下的基准指纹改旧，等价于“改写之后默认模板又变了”
	if err := e.store.SetSetting(ctx, "role_prompt:dev", `{"body":"`+mine+`","base_hash":"old"}`); err != nil {
		t.Fatal(err)
	}
	e.call("GET", "/api/role-prompts", nil, &list)
	if !list[1].Custom || !list[1].Outdated || list[1].Default != roles.DefaultBody(roles.Dev) {
		t.Fatalf("应提示默认模板已更新并带上默认模板：%+v", list[1])
	}
	if list[0].Outdated {
		t.Fatal("没改写的身份不应提示更新")
	}

	// 恢复默认：新文案立即失效
	e.call("DELETE", "/api/role-prompts/dev", nil, &v)
	if v.Custom || v.Outdated || v.Body != roles.DefaultBody(roles.Dev) {
		t.Fatalf("恢复后应是默认：%+v", v)
	}
	if p := setRole(); p != roles.Prompt(roles.Dev) {
		t.Fatalf("恢复默认后 ks role 应是默认模板")
	}
	e.waitFor("压缩补发回到默认", func() bool {
		return !strings.Contains(compact(), mine) && strings.Contains(compact(), "你的会话身份：Dev")
	})

	// 改成与默认一致等同恢复默认
	e.call("PUT", "/api/role-prompts/pm", map[string]any{"body": roles.DefaultBody(roles.PM) + "\n"}, &v)
	if v.Custom {
		t.Fatalf("与默认一致不应算改写：%+v", v)
	}
}

// tryWeb 用网页登录态发请求，返回 HTTP 状态码（不因非 2xx 失败）。
func tryWeb(e *env, method, path string, in any) int {
	b, _ := json.Marshal(in)
	req, _ := http.NewRequest(method, e.srv.URL+path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.web.Do(req)
	if err != nil {
		return 0
	}
	resp.Body.Close()
	return resp.StatusCode
}
