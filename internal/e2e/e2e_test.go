// Package e2e 端到端验证 hub + daemon + hook 的决策闭环（不依赖真实 agent）。
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"regexp"
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
	h.SetFilesDir(dir + "/files")
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

// fakeModel 模拟 OpenAI 兼容接口：按系统提示词的内容返回不同结果，并记录调用次数。
func fakeModel(t *testing.T) (*httptest.Server, *int) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct{ Content string } `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		calls++
		sys := body.Messages[0].Content
		out := "？"
		switch {
		case strings.Contains(sys, "第一条指令"):
			out = "登录页记住我"
		case strings.Contains(sys, "输出一个 JSON"):
			out = "```json\n{\"summary\":\"已加记住我复选框\",\"phase\":\"testing\",\"done\":\"加了复选框和测试\",\"ask\":\"有效期选 7 天还是 30 天\"}\n```"
		case strings.Contains(sys, "短标题"):
			out = "“记住我”有效期"
		case strings.Contains(sys, "回顾"):
			out = "## 概览\n推进了登录页。"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": out}}}})
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func TestAIFeatures(t *testing.T) {
	e := setup(t)
	model, calls := fakeModel(t)
	cwd, _ := os.Getwd()
	ev := func(name string, extra map[string]any) map[string]any {
		m := map[string]any{"session_id": "ai-1", "cwd": cwd, "hook_event_name": name, "turn_id": "t1"}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}

	// 默认全部关闭：配了模型也不调用。
	settings := map[string]any{"public_url": "", "summary_base_url": model.URL + "/v1", "summary_model": "m", "notify_default": map[string]any{}, "ai": map[string]bool{}}
	e.call("PUT", "/api/settings", settings, nil)
	hook(t, "claude", ev("UserPromptSubmit", map[string]any{"prompt": "给登录页加记住我"}))
	time.Sleep(300 * time.Millisecond)
	if *calls != 0 {
		t.Fatalf("功能默认关闭时不应调用模型，调用了 %d 次", *calls)
	}

	settings["ai"] = map[string]bool{"summary": true, "title": true, "digest": true, "decision_title": true, "recap": true}
	e.call("PUT", "/api/settings", settings, nil)
	time.Sleep(300 * time.Millisecond) // 等 daemon 收到“上送最后回复”的配置

	hook(t, "claude", map[string]any{"session_id": "ai-2", "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "给登录页加记住我", "prompt_id": "p1"})
	e.waitFor("根据首条消息生成标题", func() bool { return e.session("claude:ai-2").Title == "登录页记住我" })

	hook(t, "claude", map[string]any{"session_id": "ai-2", "cwd": cwd, "hook_event_name": "Stop", "last_assistant_message": "已在登录表单加了记住我复选框并补了测试，有效期你想用多久？"})
	e.waitFor("一轮结束后生成摘要、阶段与要点", func() bool {
		s := e.session("claude:ai-2")
		return s.Summary == "已加记住我复选框" && s.Phase == "testing" && s.DigestDone == "加了复选框和测试" && s.DigestAsk == "有效期选 7 天还是 30 天"
	})

	hook(t, "claude", map[string]any{"session_id": "ai-2", "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "30 天", "prompt_id": "p2"})
	e.waitFor("新一轮开始时清掉要点", func() bool {
		s := e.session("claude:ai-2")
		return s.DigestAsk == "" && s.Title == "登录页记住我"
	})

	c := client.New(5 * time.Second)
	if err := c.Do(context.Background(), "POST", "/agent/decisions", map[string]any{"question": "记住我的有效期用 7 天还是 30 天？", "session_key": "claude:ai-2"}, nil); err != nil {
		t.Fatal(err)
	}
	e.waitFor("决策短标题", func() bool {
		var list struct {
			Decisions []store.Decision `json:"decisions"`
		}
		e.call("GET", "/api/decisions", nil, &list)
		return len(list.Decisions) == 1 && list.Decisions[0].ShortTitle == "记住我有效期"
	})

	var recap struct {
		Markdown string `json:"markdown"`
	}
	now := time.Now()
	e.call("POST", "/api/recap", map[string]any{"from": now.Add(-time.Hour), "to": now.Add(time.Minute)}, &recap)
	if !strings.Contains(recap.Markdown, "## 概览") {
		t.Fatalf("回顾内容异常：%q", recap.Markdown)
	}
}

// 1x1 PNG
var tinyPNG = []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, 0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4, 0x89, 0x00, 0x00, 0x00, 0x0d, 0x49, 0x44, 0x41, 0x54, 0x78, 0x9c, 0x63, 0xf8, 0xcf, 0xc0, 0xf0, 0x1f, 0x00, 0x05, 0x00, 0x01, 0xff, 0x89, 0x99, 0x3d, 0x1d, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4e, 0x44, 0xae, 0x42, 0x60, 0x82}

func (e *env) upload(t *testing.T, name string, data []byte) store.FileRef {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, _ := mw.CreateFormFile("file", name)
	part.Write(data)
	mw.Close()
	req, _ := http.NewRequest("POST", e.srv.URL+"/api/files", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := e.web.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("上传失败 %d：%s", resp.StatusCode, raw)
	}
	var f store.FileRef
	_ = json.Unmarshal(raw, &f)
	return f
}

func TestAttachmentsAndReviewFeedback(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	prompt := func(turn string) string {
		return hook(t, "claude", map[string]any{"session_id": "f-1", "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "继续", "prompt_id": turn})
	}
	prompt("p0")
	c := client.New(5 * time.Second)

	// 非图片被拒绝
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, _ := mw.CreateFormFile("file", "a.txt")
	part.Write([]byte("hello"))
	mw.Close()
	req, _ := http.NewRequest("POST", e.srv.URL+"/api/files", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if resp, _ := e.web.Do(req); resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("非图片应被拒绝，得到 %d", resp.StatusCode)
	}

	// 决策答复附图 → 注入里给出已下载的本地路径
	img := e.upload(t, "设计稿.png", tinyPNG)
	if err := c.Do(context.Background(), "POST", "/agent/decisions", map[string]any{"question": "按钮用哪个样式？", "session_key": "claude:f-1"}, nil); err != nil {
		t.Fatal(err)
	}
	e.call("POST", "/api/decisions/answers", map[string]any{"answers": []map[string]any{{"ref": "D-1", "answer": "按附图来", "files": []string{img.ID}}}}, nil)
	var out string
	e.waitFor("决策附图送达", func() bool { out = prompt("p1"); return strings.Contains(out, "按附图来") })
	m := regexp.MustCompile(`附图：(\S+\.png)`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("注入应包含本地附图路径：%q", out)
	}
	if b, err := os.ReadFile(strings.TrimRight(m[1], `\n"`)); err != nil || !bytes.Equal(b, tinyPNG) {
		t.Fatalf("附图应已下载到本地：%v", err)
	}

	// agent 上传截图作为证据，需求进入待验收
	shot := t.TempDir() + "/shot.png"
	os.WriteFile(shot, tinyPNG, 0o600)
	var ref store.FileRef
	if err := c.Upload(context.Background(), "/agent/files", shot, &ref); err != nil || ref.ID == "" {
		t.Fatalf("agent 上传失败：%v", err)
	}
	var r store.Requirement
	if err := c.Do(context.Background(), "POST", "/agent/requirements", map[string]any{"title": "登录页样式", "status": "in_progress", "session_key": "claude:f-1"}, &r); err != nil {
		t.Fatal(err)
	}
	if err := c.Do(context.Background(), "POST", "/agent/requirements/"+r.Code+"/evidence", map[string]any{"kind": "screenshot", "summary": "改好了", "files": []string{ref.ID}}, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.Do(context.Background(), "POST", "/agent/requirements/"+r.Code+"/evidence", map[string]any{"kind": "screenshot", "summary": "忘了附图"}, nil); err == nil || !strings.Contains(err.Error(), "必须附上") {
		t.Fatalf("截图证据没有附件应被拒绝：%v", err)
	}
	c.Do(context.Background(), "POST", "/agent/requirements/"+r.Code+"/status", map[string]any{"status": "review"}, nil)

	// 用户提修改意见（附图）→ 回到进行中、证据作废、意见送达该会话
	fb := e.upload(t, "问题.png", tinyPNG)
	var rv struct {
		Requirements []store.Requirement `json:"requirements"`
		Feedback     []store.Decision    `json:"feedback"`
	}
	e.call("POST", "/api/reviews", map[string]any{"items": []map[string]any{{"ref": r.Code, "verdict": "revise", "comment": "按钮太小，见附图", "files": []string{fb.ID}}}}, &rv)
	if rv.Requirements[0].Status != store.ReqInProgress || rv.Feedback[0].SessionKey != "claude:f-1" || rv.Feedback[0].Kind != store.KindFeedback {
		t.Fatalf("修改意见结果异常：%+v / %+v", rv.Requirements[0].Status, rv.Feedback[0])
	}
	e.waitFor("修改意见送达", func() bool {
		out = prompt("p2")
		return strings.Contains(out, "按钮太小") && strings.Contains(out, r.Code) && strings.Contains(out, "附图：")
	})
	var got store.Requirement
	if err := c.Do(context.Background(), "GET", "/agent/requirements/"+r.Code, nil, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Feedback) != 1 || !got.Evidence[0].Voided || len(got.Evidence[0].Files) != 1 {
		t.Fatalf("需求详情应含修改意见，证据已作废并保留截图：%+v", got)
	}

	// 录屏：可上传，支持 Range 读取，req_get 的附图列表不含录屏
	vid := t.TempDir() + "/demo.webm"
	webm := append([]byte{0x1A, 0x45, 0xDF, 0xA3}, bytes.Repeat([]byte{0}, 2048)...)
	os.WriteFile(vid, webm, 0o600)
	var vref store.FileRef
	if err := c.Upload(context.Background(), "/agent/files", vid, &vref); err != nil || vref.Mime != "video/webm" {
		t.Fatalf("录屏上传失败：%v %+v", err, vref)
	}
	rq, _ := http.NewRequest("GET", e.srv.URL+"/api/files/"+vref.ID, nil)
	rq.Header.Set("Range", "bytes=0-3")
	if resp, err := e.web.Do(rq); err != nil || resp.StatusCode != http.StatusPartialContent || resp.Header.Get("Content-Type") != "video/webm" {
		t.Fatalf("录屏应支持 Range：%v %v", err, resp)
	}

	// 留言：不改状态，按“留言”单独送达
	var nd store.Decision
	e.call("POST", "/api/requirements/"+r.Code+"/notes", map[string]any{"comment": "顺便把按钮颜色统一一下"}, &nd)
	if nd.Kind != store.KindNote || nd.SessionKey != "claude:f-1" {
		t.Fatalf("留言结果异常：%+v", nd)
	}
	var cur store.Requirement
	c.Do(context.Background(), "GET", "/agent/requirements/"+r.Code, nil, &cur)
	if cur.Status != store.ReqInProgress {
		t.Fatalf("留言不应改状态，得到 %s", cur.Status)
	}
	e.waitFor("留言送达", func() bool {
		out = prompt("p3")
		return strings.Contains(out, "留言：顺便把按钮颜色统一一下") && strings.Contains(out, "需求状态不变")
	})

	// 通过：直接完成
	e.call("POST", "/api/reviews", map[string]any{"items": []map[string]any{{"ref": r.Code, "verdict": "accept"}}}, &rv)
	if rv.Requirements[0].Status != store.ReqDone {
		t.Fatalf("验收通过应为已完成，得到 %s", rv.Requirements[0].Status)
	}
}

func TestWaitWakesOnAnswer(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	hook(t, "claude", map[string]any{"session_id": "w-1", "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "做登录页", "prompt_id": "p0"})
	c := client.New(30 * time.Second)
	var dec store.Decision
	if err := c.Do(context.Background(), "POST", "/agent/decisions", map[string]any{"question": "按钮用什么颜色？", "session_key": "claude:w-1"}, &dec); err != nil {
		t.Fatal(err)
	}

	// 挂起等待：会话显示为“挂起等你”
	done := make(chan daemon.WaitResult, 1)
	go func() {
		var res daemon.WaitResult
		_ = c.Do(context.Background(), "GET", "/wait?session_key=claude:w-1&timeout=20s", nil, &res)
		done <- res
	}()
	e.waitFor("登记挂起", func() bool { return e.session("claude:w-1").Parked })
	// 本轮结束（后台挂着 ks wait）→ 待命，而不是“后台运行”或“等你回复”
	hook(t, "claude", map[string]any{"session_id": "w-1", "cwd": cwd, "hook_event_name": "Stop", "background_tasks": []any{map[string]any{"description": "ks wait"}}})
	e.waitFor("会话待命", func() bool { return e.session("claude:w-1").State == store.StateParked })

	// 用户作答 → 等待立刻返回答复内容，并记为已送达（下次发消息不再重复注入）
	e.call("POST", "/api/decisions/answers", map[string]any{"answers": []map[string]any{{"ref": dec.Code, "answer": "朱红"}}}, nil)
	select {
	case res := <-done:
		if res.Reason != "delivered" || !strings.Contains(res.Text, "朱红") {
			t.Fatalf("应带着答复返回：%+v", res)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("作答后等待没有返回")
	}
	if out := hook(t, "claude", map[string]any{"session_id": "w-1", "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "继续", "prompt_id": "p1"}); strings.Contains(out, "朱红") {
		t.Fatalf("已经通过 ks wait 送达，不应重复注入：%q", out)
	}
	e.waitFor("等待结束后不再挂起", func() bool { return !e.session("claude:w-1").Parked })

	// 超时：安静返回
	var res daemon.WaitResult
	if err := c.Do(context.Background(), "GET", "/wait?session_key=claude:w-1&timeout=1s", nil, &res); err != nil || res.Reason != "timeout" {
		t.Fatalf("应超时返回：%+v %v", res, err)
	}
}

func TestAssignWakesParkedSession(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	hook(t, "claude", map[string]any{"session_id": "a-1", "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "先待命", "prompt_id": "p0"})
	c := client.New(30 * time.Second)

	// 你在网页上新建需求
	var r store.Requirement
	e.call("POST", "/api/requirements", map[string]any{"title": "导出 CSV", "detail": "订单列表加导出按钮", "acceptance": "能导出当前筛选结果", "status": "ready"}, &r)

	// 会话挂着 ks wait
	done := make(chan daemon.WaitResult, 1)
	go func() {
		var res daemon.WaitResult
		_ = c.Do(context.Background(), "GET", "/wait?session_key=claude:a-1&timeout=20s", nil, &res)
		done <- res
	}()
	e.waitFor("登记挂起", func() bool { return e.session("claude:a-1").Parked })

	// 交给这个会话 → 关联、进行中，并立刻唤醒
	var d store.Decision
	e.call("POST", "/api/requirements/"+r.Code+"/assign", map[string]any{"session_key": "claude:a-1"}, &d)
	if d.Kind != store.KindAssign {
		t.Fatalf("应生成交付记录：%+v", d)
	}
	select {
	case res := <-done:
		if res.Reason != "delivered" || !strings.Contains(res.Text, "交给你") || !strings.Contains(res.Text, "导出 CSV") || !strings.Contains(res.Text, "验收标准：能导出当前筛选结果") {
			t.Fatalf("应带着需求内容唤醒：%+v", res)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("交出需求后等待没有返回")
	}
	var got store.Requirement
	c.Do(context.Background(), "GET", "/agent/requirements/"+r.Code, nil, &got)
	if got.Status != store.ReqInProgress || len(got.Sessions) != 1 || got.Sessions[0] != "claude:a-1" {
		t.Fatalf("交出后应关联会话并进行中：%s %v", got.Status, got.Sessions)
	}
}

func TestDecisionOptionImages(t *testing.T) {
	setup(t)
	cwd, _ := os.Getwd()
	hook(t, "claude", map[string]any{"session_id": "o-1", "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "出方案", "prompt_id": "p0"})
	c := client.New(10 * time.Second)
	dir := t.TempDir()
	var ids []string
	for _, n := range []string{"a.png", "b.png"} {
		os.WriteFile(dir+"/"+n, tinyPNG, 0o600)
		var ref store.FileRef
		if err := c.Upload(context.Background(), "/agent/files", dir+"/"+n, &ref); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, ref.ID)
	}
	var d store.Decision
	if err := c.Do(context.Background(), "POST", "/agent/decisions", map[string]any{"question": "用哪个布局？", "options": []string{"A", "B", "都不对"}, "option_images": []string{ids[0], ids[1], ""}, "session_key": "claude:o-1"}, &d); err != nil {
		t.Fatal(err)
	}
	if len(d.OptionFiles) != 3 || len(d.OptionFiles[0]) != 1 || len(d.OptionFiles[1]) != 1 || len(d.OptionFiles[2]) != 0 || d.OptionFiles[1][0].ID != ids[1] {
		t.Fatalf("选项效果图应与选项一一对应：%+v", d.OptionFiles)
	}
}

func TestLinkStartsRequirement(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	hook(t, "claude", map[string]any{"session_id": "l-1", "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "开工", "prompt_id": "p0"})
	var r store.Requirement
	e.call("POST", "/api/requirements", map[string]any{"title": "导出 CSV", "status": "ready"}, &r)
	c := client.New(5 * time.Second)
	e.waitFor("会话已上报", func() bool { return e.session("claude:l-1").Key != "" })
	var got store.Requirement
	if err := c.Do(context.Background(), "POST", "/agent/sessions/claude:l-1/link", map[string]any{"requirement": r.Code}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status != store.ReqInProgress {
		t.Fatalf("Agent 关联后待开始的需求应变为进行中，得到 %s", got.Status)
	}
}

func TestApprovalAndAcceptWithComment(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	prompt := func(turn string) string {
		return hook(t, "claude", map[string]any{"session_id": "ap-1", "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "继续", "prompt_id": turn})
	}
	prompt("p0")
	c := client.New(10 * time.Second)

	// 请你批准：选项固定为批准 / 拒绝，可以附言
	var d store.Decision
	if err := c.Do(context.Background(), "POST", "/agent/decisions", map[string]any{"question": "按这个方案做项目入口？", "approval": true, "session_key": "claude:ap-1"}, &d); err != nil {
		t.Fatal(err)
	}
	if !d.Approval || len(d.Options) != 2 || d.Options[0] != "批准" {
		t.Fatalf("批准类决策异常：%+v", d)
	}
	e.call("POST", "/api/decisions/answers", map[string]any{"answers": []map[string]any{{"ref": d.Code, "answer": "批准；顺便把设置页的项目设置去掉"}}}, nil)
	var out string
	e.waitFor("批准送达", func() bool { out = prompt("p1"); return strings.Contains(out, "批准；顺便把设置页的项目设置去掉") })

	// 带意见通过：需求完成，意见作为留言送达
	var r store.Requirement
	c.Do(context.Background(), "POST", "/agent/requirements", map[string]any{"title": "项目入口", "status": "review", "session_key": "claude:ap-1"}, &r)
	e.call("POST", "/api/reviews", map[string]any{"items": []map[string]any{{"ref": r.Code, "verdict": "accept", "comment": "很好，下次按钮再大一点"}}}, nil)
	var got store.Requirement
	c.Do(context.Background(), "GET", "/agent/requirements/"+r.Code, nil, &got)
	if got.Status != store.ReqDone {
		t.Fatalf("带意见通过后应为已完成：%s", got.Status)
	}
	e.waitFor("通过附言送达", func() bool { out = prompt("p2"); return strings.Contains(out, "验收通过。附言：很好，下次按钮再大一点") })
}
