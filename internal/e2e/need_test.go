package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/client"
	"github.com/NekroAI/kitestring/internal/hub"
	"github.com/NekroAI/kitestring/internal/store"
)

type recommendRes struct {
	Decision store.Decision `json:"decision"`
	Existing bool           `json:"existing"`
	Message  string         `json:"message"`
}

// 开 Dev 会话改为 PM 推荐、你限时批准（R-169）：
//   - 推荐是一条请求类审批（approval + request_kind = devs + 到期时间），不推送；同项目在等的推荐不重复建；非 PM 不能提；
//   - auto 派活找不到在线开发时不再推送提醒，只回「默认用子代理」；
//   - 批准 → 推送接入语、项目页横幅；PM 收到「批准」；开发上线够数后横幅清除；
//   - 拒绝 → PM 收到「用户明确拒绝」，不推送、没有横幅；
//   - 超时 → PM 收到「超时未批，按规则视为拒绝」，之后不能再答；三种文字互不相同；
//   - need_devs 兼容入口走同一流程，count = 0 撤回在等的推荐。
func TestRecommendDevs(t *testing.T) {
	e := setup(t)
	pushed := make(chan map[string]any, 8)
	bark := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m map[string]any
		_ = json.NewDecoder(r.Body).Decode(&m)
		pushed <- m
	}))
	defer bark.Close()
	e.call("PUT", "/api/settings", map[string]any{"public_url": "https://ks.example", "ai": map[string]any{}, "summary_base_url": "", "summary_model": "", "notify_default": map[string]any{"bark": map[string]any{"server": bark.URL, "key": "k"}}}, nil)

	cwd, _ := os.Getwd()
	c := client.New(30 * time.Second)
	ctx := context.Background()
	for _, sid := range []string{"pm", "dev"} {
		hook(t, "claude", map[string]any{"session_id": sid, "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "上线", "prompt_id": "p0"})
	}
	e.waitFor("会话入库", func() bool { return e.session("claude:dev").Key != "" })
	if err := c.Do(ctx, "POST", "/agent/sessions/role", map[string]any{"session_key": "claude:pm", "role": "pm"}, nil); err != nil {
		t.Fatal(err)
	}
	project := func() store.Project {
		var out struct {
			Projects []store.Project `json:"projects"`
		}
		e.call("GET", "/api/projects", nil, &out)
		for _, p := range out.Projects {
			if p.ID == e.session("claude:pm").ProjectID {
				return p
			}
		}
		return store.Project{}
	}
	got := func(want ...string) {
		t.Helper()
		select {
		case m := <-pushed:
			b, _ := json.Marshal(m)
			for _, w := range want {
				if !strings.Contains(string(b), w) {
					t.Fatalf("推送应包含 %q：%s", w, b)
				}
			}
		case <-time.After(10 * time.Second):
			t.Fatal("没有收到推送")
		}
	}
	quiet := func(why string) {
		t.Helper()
		select {
		case m := <-pushed:
			t.Fatalf("%s，不应推送：%v", why, m)
		case <-time.After(500 * time.Millisecond):
		}
	}
	recommend := func(body map[string]any) recommendRes {
		t.Helper()
		body["session_key"] = "claude:pm"
		var out recommendRes
		if err := c.Do(ctx, "POST", "/agent/team/recommend", body, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	answer := func(code, text string) {
		e.call("POST", "/api/decisions/answers", map[string]any{"answers": []map[string]any{{"ref": code, "answer": text}}}, nil)
	}
	// wakeFor：PM 挂 ks wait，直到收到提到 code 的消息（开发上线等团队消息可能先到）
	wakeFor := func(code string, want ...string) string {
		t.Helper()
		for i := 0; i < 5; i++ {
			if txt := woke(t, parkWait(t, e, c, "claude:pm")); strings.Contains(txt, code) {
				for _, w := range want {
					if !strings.Contains(txt, w) {
						t.Fatalf("送给 PM 的答复应包含 %q：%s", w, txt)
					}
				}
				return txt
			}
		}
		t.Fatalf("PM 没有收到 %s 的结果", code)
		return ""
	}

	if err := c.Do(ctx, "POST", "/agent/team/recommend", map[string]any{"session_key": "claude:dev", "count": 1, "reason": "x"}, nil); err == nil {
		t.Fatal("非 PM 不能推荐开开发会话")
	}
	if err := c.Do(ctx, "POST", "/agent/team/recommend", map[string]any{"session_key": "claude:pm", "count": 1}, nil); err == nil {
		t.Fatal("推荐必须写理由")
	}

	// auto 派活找不到在线开发：不推送，引导用子代理
	var r store.Requirement
	if err := c.Do(ctx, "POST", "/agent/requirements", map[string]any{"title": "导出 CSV", "session_key": "claude:pm"}, &r); err != nil {
		t.Fatal(err)
	}
	var as struct {
		Assigned bool   `json:"assigned"`
		Message  string `json:"message"`
	}
	e.allowRelease("claude:pm")
	if err := c.Do(ctx, "POST", "/agent/requirements/"+r.Code+"/assign", map[string]any{"session_key": "claude:pm", "reason": "做法明确", "to": "auto"}, &as); err != nil {
		t.Fatal(err)
	}
	if as.Assigned || !strings.Contains(as.Message, "子任务") || !strings.Contains(as.Message, "dev_recommend") {
		t.Fatalf("应未派出、引导用子代理：%+v", as)
	}
	quiet("auto 派活找不到开发")
	if p := project(); p.NeedDevs != 0 {
		t.Fatalf("不应出现缺开发横幅：%+v", p.NeedDevs)
	}

	// ① 推荐 → 批准
	before := time.Now()
	rec := recommend(map[string]any{"count": 2, "reason": "R-3、R-4 要和用户多轮看效果，互不相干可以并行", "work": "R-3 看板动效、R-4 手机布局", "duration": "两小时"})
	d := rec.Decision
	if rec.Existing || !d.Approval || d.RequestKind != store.RequestDevs || d.RequestCount != 2 || d.Status != store.DecPending || d.ExpiresAt == nil {
		t.Fatalf("推荐应是一条在等批准的请求：%+v", d)
	}
	if left := d.ExpiresAt.Sub(before); left < 29*time.Minute || left > 31*time.Minute {
		t.Fatalf("默认时限应是 30 分钟：%v", left)
	}
	if !strings.Contains(d.Context, "多轮看效果") || !strings.Contains(d.Context, "R-4 手机布局") || !strings.Contains(d.Context, "两小时") || !strings.Contains(d.Context, "ks role dev") {
		t.Fatalf("说明应写明为什么、做什么、多久、接入语：%s", d.Context)
	}
	quiet("提推荐")
	if again := recommend(map[string]any{"count": 1, "reason": "再提一次"}); !again.Existing || again.Decision.Code != d.Code {
		t.Fatalf("同项目已有在等的推荐时不应重复建：%+v", again)
	}
	if p := project(); p.NeedDevs != 0 {
		t.Fatalf("批准前不应出现横幅：%+v", p.NeedDevs)
	}
	answer(d.Code, "批准")
	got("已批准开启 2 个开发会话", "ks role dev", "https://ks.example/projects/")
	if p := project(); p.NeedDevs != 2 || p.NeedAt == nil {
		t.Fatalf("批准后项目页应显示接入语横幅：%+v", p)
	}
	approved := wakeFor(d.Code, "批准（用户批准了开启 2 个开发会话的推荐）", "req_assign")

	// 两个开发上线：够数，横幅清除
	if err := c.Do(ctx, "POST", "/agent/sessions/role", map[string]any{"session_key": "claude:dev", "role": "dev"}, nil); err != nil {
		t.Fatal(err)
	}
	if p := project(); p.NeedDevs != 2 {
		t.Fatalf("只上线 1 个开发，不应清除：%+v", p.NeedDevs)
	}
	hook(t, "claude", map[string]any{"session_id": "dev-2", "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "上线", "prompt_id": "p0"})
	e.waitFor("dev-2 入库", func() bool { return e.session("claude:dev-2").Key != "" })
	if err := c.Do(ctx, "POST", "/agent/sessions/role", map[string]any{"session_key": "claude:dev-2", "role": "dev"}, nil); err != nil {
		t.Fatal(err)
	}
	e.waitFor("横幅清除", func() bool { return project().NeedDevs == 0 })

	// ② 推荐 → 拒绝（附言）：不推送、没有横幅
	d = recommend(map[string]any{"count": 1, "reason": "R-5 想并行"}).Decision
	answer(d.Code, "拒绝；这两条子代理就够了")
	quiet("拒绝")
	if p := project(); p.NeedDevs != 0 {
		t.Fatalf("拒绝后不应出现横幅：%+v", p.NeedDevs)
	}
	rejected := wakeFor(d.Code, "拒绝（用户明确拒绝了开启 1 个开发会话的推荐）", "这两条子代理就够了", "继续用一次性子任务")

	// ③ 推荐 → 超时：PM 收到「超时未批，按规则视为拒绝」，之后不能再答
	hub.SetRequestUnit(300 * time.Millisecond)
	defer hub.SetRequestUnit(time.Minute)
	d = recommend(map[string]any{"count": 1, "reason": "R-6 想并行", "minutes": 1}).Decision
	timedOut := wakeFor(d.Code, "超时未批，按规则视为拒绝开启 1 个开发会话的推荐", "不是用户明确拒绝", "继续用一次性子任务")
	var cur store.Decision
	if err := c.Do(ctx, "GET", "/agent/decisions/"+d.Code, nil, &cur); err != nil {
		t.Fatal(err)
	}
	if !cur.TimedOut || cur.Status == store.DecPending || cur.Verdict != store.VerdictRejected {
		t.Fatalf("超时后应记为超时、视为拒绝（verdict = rejected，R-239）、不再待批：%+v", cur)
	}
	if code := e.callStatus("POST", "/api/decisions/answers", map[string]any{"answers": []map[string]any{{"ref": d.Code, "answer": "批准"}}}); code/100 == 2 {
		t.Fatal("超时后不能再答")
	}
	quiet("超时")
	if p := project(); p.NeedDevs != 0 {
		t.Fatalf("超时后不应出现横幅：%+v", p.NeedDevs)
	}

	// 三种结局送给 PM 的文字互不混淆
	if strings.Contains(approved, "拒绝") || strings.Contains(rejected, "超时") || strings.Contains(timedOut, "明确拒绝了") {
		t.Fatalf("批准 / 拒绝 / 超时的文字应能区分：\n%s\n%s\n%s", approved, rejected, timedOut)
	}

	// need_devs 兼容入口：走推荐流程；count = 0 撤回
	hub.SetRequestUnit(time.Minute)
	var old recommendRes
	if err := c.Do(ctx, "POST", "/agent/team/need", map[string]any{"session_key": "claude:pm", "count": 1, "reason": "旧入口"}, &old); err != nil {
		t.Fatal(err)
	}
	if old.Decision.RequestKind != store.RequestDevs || old.Decision.Status != store.DecPending {
		t.Fatalf("need_devs 应建一条推荐：%+v", old)
	}
	quiet("need_devs 推荐")
	if err := c.Do(ctx, "POST", "/agent/team/need", map[string]any{"session_key": "claude:pm", "count": 0}, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.Do(ctx, "GET", "/agent/decisions/"+old.Decision.Code, nil, &cur); err != nil {
		t.Fatal(err)
	}
	if cur.Status != store.DecClosed {
		t.Fatalf("count = 0 应撤回在等的推荐：%+v", cur.Status)
	}
}
