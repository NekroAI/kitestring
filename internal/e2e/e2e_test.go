// Package e2e 端到端验证 hub + daemon + hook 的决策闭环（不依赖真实 agent）。
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/client"
	"github.com/NekroAI/kitestring/internal/daemon"
	"github.com/NekroAI/kitestring/internal/hookcli"
	"github.com/NekroAI/kitestring/internal/hub"
	"github.com/NekroAI/kitestring/internal/store"
)

type env struct {
	t      *testing.T
	srv    *httptest.Server
	web    *http.Client
	cancel context.CancelFunc
}

func setup(t *testing.T) *env {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "ks-e2e-") // unix socket 路径需要短
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	t.Setenv("KS_HOME", dir)

	st, err := store.Open(dir + "/hub.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := hub.New(st, log)
	srv := httptest.NewServer(h.Handler(nil))
	t.Cleanup(srv.Close)

	jar, _ := cookiejar.New(nil)
	e := &env{t: t, srv: srv, web: &http.Client{Jar: jar}}
	e.call("POST", "/api/auth/setup", map[string]any{"password": "password123"}, nil)

	var dev struct {
		Token string `json:"token"`
	}
	e.call("POST", "/api/devices", map[string]any{"name": "test-device"}, &dev)

	d, err := daemon.New(daemon.Config{HubURL: srv.URL, Token: dev.Token}, log)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	e.cancel = cancel
	done := make(chan struct{})
	go func() { _ = d.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	e.waitFor("daemon 连上 Hub", func() bool {
		var st map[string]any
		err := client.New(time.Second).Do(context.Background(), "GET", "/status", nil, &st)
		return err == nil && st["connected"] == true
	})
	return e
}

func (e *env) call(method, path string, in, out any) {
	e.t.Helper()
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, body)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := e.web.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		e.t.Fatalf("%s %s → %d：%s", method, path, resp.StatusCode, raw)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			e.t.Fatalf("解析 %s 响应失败：%v", path, err)
		}
	}
}

func (e *env) waitFor(what string, cond func() bool) {
	e.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	e.t.Fatalf("等待超时：%s", what)
}

// hook 模拟 agent 调用 ks hook，返回 stdout。
func hook(t *testing.T, tool string, payload map[string]any) string {
	t.Helper()
	b, _ := json.Marshal(payload)
	var out bytes.Buffer
	hookcli.Run([]string{tool, payload["hook_event_name"].(string)}, bytes.NewReader(b), &out)
	return out.String()
}

func (e *env) session(key string) store.Session {
	var out struct {
		Sessions []store.Session `json:"sessions"`
	}
	e.call("GET", "/api/sessions", nil, &out)
	for _, s := range out.Sessions {
		if s.Key == key {
			return s
		}
	}
	return store.Session{}
}

func TestDecisionLoop(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	base := func(name string) map[string]any {
		return map[string]any{"session_id": "s-1", "cwd": cwd, "hook_event_name": name, "model": "gpt-test", "turn_id": "turn-" + name}
	}

	// 1. 会话开始、提交 prompt → 实例墙出现运行中的会话
	hook(t, "codex", base("SessionStart"))
	p := base("UserPromptSubmit")
	p["prompt"] = "实现登录页"
	if out := hook(t, "codex", p); out != "" {
		t.Fatalf("没有决策时不应注入，得到 %q", out)
	}
	e.waitFor("会话进入运行中", func() bool { return e.session("codex:s-1").State == store.StateRunning })
	s := e.session("codex:s-1")
	if s.LastPrompt != "实现登录页" || s.Model != "gpt-test" || s.ProjectName == "" || s.Branch == "" {
		t.Fatalf("会话字段不完整：%+v", s)
	}

	// 2. agent 经 daemon 提出决策并新建需求
	c := client.New(5 * time.Second)
	var req store.Requirement
	if err := c.Do(context.Background(), "POST", "/agent/requirements", map[string]any{"title": "登录页", "status": "in_progress", "session_key": "codex:s-1"}, &req); err != nil {
		t.Fatal(err)
	}
	var dec store.Decision
	if err := c.Do(context.Background(), "POST", "/agent/decisions", map[string]any{"question": "要不要记住我？", "options": []string{"要", "不要"}, "session_key": "codex:s-1", "requirement": req.Code}, &dec); err != nil {
		t.Fatal(err)
	}
	if dec.Code != "D-1" || dec.Status != store.DecPending || dec.ProjectID == "" {
		t.Fatalf("决策字段异常：%+v", dec)
	}

	// 3. 回合结束 → 等待回复；用户在 Web 作答
	hook(t, "codex", base("Stop"))
	e.waitFor("会话等待回复", func() bool { return e.session("codex:s-1").State == store.StateWaitReply })
	if e.session("codex:s-1").PendingDecisions != 1 {
		t.Fatal("实例卡片应显示 1 条待决决策")
	}
	e.call("POST", "/api/decisions/answers", map[string]any{"answers": []map[string]string{{"ref": "D-1", "answer": "要，默认勾选"}}}, nil)

	// 4. 其他会话不会收到；同一会话下一次 prompt 注入
	other := map[string]any{"session_id": "s-2", "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "x"}
	e.waitFor("决策下行到 daemon", func() bool {
		var st map[string]any
		_ = c.Do(context.Background(), "GET", "/status", nil, &st)
		return st["decisions"] == float64(1)
	})
	if out := hook(t, "codex", other); out != "" {
		t.Fatalf("其他会话不应收到决策：%q", out)
	}
	out := hook(t, "codex", base("UserPromptSubmit"))
	if !strings.Contains(out, "D-1") || !strings.Contains(out, "要，默认勾选") || !strings.Contains(out, "additionalContext") {
		t.Fatalf("应注入决策，得到 %q", out)
	}
	if again := hook(t, "codex", base("UserPromptSubmit")); again != "" {
		t.Fatalf("同一决策不应重复注入：%q", again)
	}
	e.waitFor("决策标记为已接收", func() bool {
		var d store.Decision
		var list struct {
			Decisions []store.Decision `json:"decisions"`
		}
		e.call("GET", "/api/decisions", nil, &list)
		for _, x := range list.Decisions {
			if x.Code == "D-1" {
				d = x
			}
		}
		return d.Status == store.DecDelivered && d.DeliveredTurn == "turn-UserPromptSubmit"
	})

	// 5. 上下文压缩后补注入
	e.waitFor("daemon 收到已接收状态", func() bool {
		cmp := base("SessionStart")
		cmp["source"] = "compact"
		return strings.Contains(hook(t, "codex", cmp), "上下文压缩")
	})

	// 6. agent 把需求设为待验收；用户退回时证据作废
	if err := c.Do(context.Background(), "POST", "/agent/requirements/"+req.Code+"/evidence", map[string]any{"kind": "test", "summary": "测试通过"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.Do(context.Background(), "POST", "/agent/requirements/"+req.Code+"/status", map[string]any{"status": "done"}, nil); err == nil {
		t.Fatal("agent 不应能直接设为 done")
	}
	if err := c.Do(context.Background(), "POST", "/agent/requirements/"+req.Code+"/status", map[string]any{"status": "review"}, nil); err != nil {
		t.Fatal(err)
	}
	var r store.Requirement
	e.call("PUT", "/api/requirements/"+req.Code, map[string]any{"title": "登录页", "status": "in_progress"}, &r)
	if len(r.Evidence) != 1 || !r.Evidence[0].Voided {
		t.Fatalf("退回后证据应作废：%+v", r.Evidence)
	}

	// 7. 会话结束
	hook(t, "codex", base("SessionEnd"))
	e.waitFor("会话结束", func() bool { return e.session("codex:s-1").State == store.StateEnded })
}

// 会话结束后才作答，同一会话恢复（session_id 不变）时仍应收到（D15）。
func TestDecisionAfterSessionEndDeliveredOnResume(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	ev := func(name string) map[string]any {
		return map[string]any{"session_id": "r-1", "cwd": cwd, "hook_event_name": name, "prompt_id": "p-" + name}
	}
	hook(t, "claude", ev("UserPromptSubmit"))
	c := client.New(5 * time.Second)
	if err := c.Do(context.Background(), "POST", "/agent/decisions", map[string]any{"question": "用哪个方案？", "session_key": "claude:r-1"}, nil); err != nil {
		t.Fatal(err)
	}
	hook(t, "claude", ev("SessionEnd"))
	e.waitFor("会话结束", func() bool { return e.session("claude:r-1").State == store.StateEnded })
	e.call("POST", "/api/decisions/answers", map[string]any{"answers": []map[string]string{{"ref": "D-1", "answer": "方案 B"}}}, nil)
	e.waitFor("恢复时注入", func() bool {
		start := ev("SessionStart")
		start["source"] = "resume"
		return strings.Contains(hook(t, "claude", start), "方案 B")
	})
}

func TestHookFailsOpenWithoutDaemon(t *testing.T) {
	t.Setenv("KS_HOME", t.TempDir())
	start := time.Now()
	out := hook(t, "claude", map[string]any{"session_id": "x", "hook_event_name": "UserPromptSubmit", "prompt": "hi"})
	if out != "" {
		t.Fatalf("daemon 不在时应静默放行，得到 %q", out)
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Fatalf("daemon 不在时应立即返回，耗时 %v", time.Since(start))
	}
}

// 公网加固：同一来源 IP 连续输错 5 次后锁定，正确密码也会被拒；其他 IP 不受影响。
func TestLoginLockout(t *testing.T) {
	e := setup(t)
	login := func(pw, ip string) int {
		req, _ := http.NewRequest("POST", e.srv.URL+"/api/auth/login", strings.NewReader(`{"password":"`+pw+`"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Real-IP", ip) // httptest 来自 127.0.0.1，视为本机反向代理
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	for i := 1; i <= 5; i++ {
		want := http.StatusUnauthorized
		if i == 5 {
			want = http.StatusTooManyRequests // 第 5 次失败即进入锁定并提示
		}
		if code := login("wrong-password", "203.0.113.9"); code != want {
			t.Fatalf("第 %d 次错误密码应返回 %d，得到 %d", i, want, code)
		}
	}
	if code := login("password123", "203.0.113.9"); code != http.StatusTooManyRequests {
		t.Fatalf("锁定期间正确密码也应被拒（429），得到 %d", code)
	}
	if code := login("password123", "198.51.100.7"); code != http.StatusOK {
		t.Fatalf("其他 IP 应能正常登录，得到 %d", code)
	}
}
