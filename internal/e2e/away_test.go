package e2e

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/client"
	"github.com/NekroAI/kitestring/internal/hub"
	"github.com/NekroAI/kitestring/internal/store"
)

// pushSink 订阅一个本地推送服务，数真正发出去的系统推送（R-49 的推送通道）。
type pushSink struct {
	mu  sync.Mutex
	got int
}

func (p *pushSink) count() int { p.mu.Lock(); defer p.mu.Unlock(); return p.got }

func (e *env) pushSink(t *testing.T) *pushSink {
	t.Helper()
	sink := &pushSink{}
	svc := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sink.mu.Lock()
		sink.got++
		sink.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(svc.Close)
	hub.SetPushClient(svc.Client())
	t.Cleanup(func() { hub.SetPushClient(http.DefaultClient) })
	key, _ := ecdh.P256().GenerateKey(rand.Reader)
	secret := make([]byte, 16)
	rand.Read(secret)
	b64 := base64.RawURLEncoding.EncodeToString
	e.call("POST", "/api/push/subscribe", map[string]any{"endpoint": svc.URL + "/push/1", "p256dh": b64(key.PublicKey().Bytes()), "auth": b64(secret), "name": "手机"}, nil)
	return sink
}

// webAs 用指定 User-Agent 调网页接口（模拟在手机上操作）。
func (e *env) webAs(ua, method, path string, in, out any) {
	e.t.Helper()
	b, _ := json.Marshal(in)
	req, _ := http.NewRequest(method, e.srv.URL+path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", ua)
	resp, err := e.web.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		e.t.Fatalf("%s %s：%d %s", method, path, resp.StatusCode, raw)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			e.t.Fatal(err)
		}
	}
}

type awayStatusResp struct {
	Away         *store.Away   `json:"away"`
	Last         *store.Away   `json:"last"`
	OverEstimate bool          `json:"over_estimate"`
	Handled      int           `json:"handled"`
	Yours        int           `json:"yours"`
	Pushed       int           `json:"pushed"`
	Grants       []store.Grant `json:"grants"`
	GrantActive  bool          `json:"grant_active"`
}

type awayReportResp struct {
	Away         store.Away `json:"away"`
	Final        bool       `json:"final"`
	OverEstimate bool       `json:"over_estimate"`
	Counts       struct {
		Proxied, Released, Advised, Delivered, User, Pushed, Held, Merged, Skipped, Normal, Waiting int
	} `json:"counts"`
	Waiting []struct {
		Kind, Ref, Reason string
		First, Pushed     bool
	} `json:"waiting"`
	Yours   []store.AwayEvent `json:"yours"`
	Pushes  []store.AwayEvent `json:"pushes"`
	Held    []store.AwayEvent `json:"held"`
	Settled []store.AwayEvent `json:"settled"`
	Grants  []store.Grant     `json:"grants"`
}

const iPhoneUA = "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1"

// 按 10-24 02:45～11:20 那次离开重放（R-210）：主调度连做 20 条需求、每条交验收，中间提了 1 条带效果图的设计选型（D-183），
// 会话做完一轮就空闲，主助理给 20 条都写了验收建议，你在手机上答了 D-183。原来约推 21 条；按新规则只推 2 条：
// D-183（硬规则：设计选型）和「都处理完了」。之后新的硬规则决策照推；结束离开后推送恢复原样。
func TestAwayReplayLastNight(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	c := client.New(10 * time.Second)
	ctx := context.Background()
	for _, sid := range []string{"pm", "adv", "dev"} {
		hook(t, "claude", map[string]any{"session_id": sid, "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "上线", "prompt_id": "p0"})
	}
	e.waitFor("会话入库", func() bool { return e.session("claude:dev").ProjectID != "" })
	for k, r := range map[string]string{"claude:pm": "pm", "claude:adv": "aide", "claude:dev": "dev"} {
		if err := c.Do(ctx, "POST", "/agent/sessions/role", map[string]any{"session_key": k, "role": r}, nil); err != nil {
			t.Fatal(err)
		}
	}
	project := e.session("claude:dev").ProjectID
	var reqs []store.Requirement
	for i := 1; i <= 20; i++ {
		var r store.Requirement
		e.call("POST", "/api/requirements", map[string]any{"title": fmt.Sprintf("需求 %d", i), "project_id": project, "status": "in_progress"}, &r)
		reqs = append(reqs, r)
	}
	sink := e.pushSink(t)

	// 02:45 点「暂时离开」：预估 8 小时，主助理可代答低风险；同时给主助理开授权窗口
	var st awayStatusResp
	e.call("POST", "/api/away", map[string]any{"expect_minutes": 480, "scope": "low"}, &st)
	if st.Away == nil || !st.GrantActive || len(st.Grants) != 1 || st.Grants[0].SessionKey != "claude:adv" || st.Grants[0].MaxRisk != "low" {
		t.Fatalf("开始离开应同时给主助理开低风险授权窗口：%+v", st)
	}
	if code, body := e.try("POST", "/api/away", map[string]any{"expect_minutes": 30, "scope": "none"}); code != 409 || !strings.Contains(body, "已经在离开中") {
		t.Fatalf("离开中不能再开一次：%d %s", code, body)
	}

	// 主调度连做 20 条：每条交证据、设为待验收（每条原本推 1 条）
	for i, r := range reqs {
		if err := c.Do(ctx, "POST", "/agent/requirements/"+r.Code+"/evidence", map[string]any{"kind": "test", "summary": "go test 通过", "session_key": "claude:dev"}, nil); err != nil {
			t.Fatal(err)
		}
		if err := c.Do(ctx, "POST", "/agent/requirements/"+r.Code+"/status", map[string]any{"status": "review", "session_key": "claude:dev"}, nil); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			// 02:47 D-183：带效果图的设计选型
			shot := t.TempDir() + "/b.png"
			os.WriteFile(shot, tinyPNG, 0o600)
			var img store.FileRef
			if err := c.Upload(ctx, "/agent/files", shot, &img); err != nil {
				t.Fatal(err)
			}
			var d store.Decision
			if err := c.Do(ctx, "POST", "/agent/decisions", map[string]any{"session_key": "claude:dev", "question": "引用印记改成哪种样式？", "risk": "low",
				"options": []string{"方案 A · 圆角胶囊", "方案 B · 小方印", "方案 C · 纯文字下划线"}, "option_images": []string{img.ID, img.ID, img.ID}}, &d); err != nil {
				t.Fatal(err)
			}
			// 主助理不能代答设计选型
			err := c.Do(ctx, "POST", "/agent/decisions/"+d.Code+"/answer", map[string]any{"session_key": "claude:adv", "answer": "方案 B", "reason": "更紧凑", "risk": "low", "hard_check": "none"}, nil)
			if err == nil || !strings.Contains(err.Error(), "设计选型") {
				t.Fatalf("主助理代答设计选型应被拒：%v", err)
			}
			e.waitFor("D-183 推送", func() bool { return sink.count() == 1 })
			// 03:02 你在手机上答了：照常生效、记进报告，不结束离开
			e.webAs(iPhoneUA, "POST", "/api/decisions/answers", map[string]any{"answers": []map[string]any{{"ref": d.Code, "answer": "方案 B · 小方印"}}}, nil)
		}
	}
	// 每轮做完会话空闲（原来是「等回复」）；常驻身份没挂 ks wait 时 Stop 会被拦两次再放行
	for _, sid := range []string{"pm", "adv", "dev"} {
		for i := 0; i < 3; i++ {
			hook(t, "claude", map[string]any{"session_id": sid, "cwd": cwd, "hook_event_name": "Stop", "last_assistant_message": "做完了"})
		}
	}
	e.waitFor("会话都停下", func() bool {
		for _, k := range []string{"claude:pm", "claude:adv", "claude:dev"} {
			if s := e.session(k).State; s == store.StateRunning || s == store.StateBackground {
				return false
			}
		}
		return true
	})
	// 主助理给 20 条都写验收建议
	var advice []map[string]any
	for _, r := range reqs {
		advice = append(advice, map[string]any{"requirement": r.Code, "verdict": "pass", "reason": "对照验收标准都满足"})
	}
	if err := c.Do(ctx, "POST", "/agent/morning", map[string]any{"session_key": "claude:adv", "advice": advice}, nil); err != nil {
		t.Fatal(err)
	}
	e.call("GET", "/api/away", nil, &st)
	if st.Away == nil || st.Yours != 1 || st.Pushed != 1 || st.Handled != 20 {
		t.Fatalf("手机上处理后离开应继续，状态条：你处理 1 件、推送 1 条、主助理处理 20 件：%+v", st)
	}

	// 能处理的都处理完，持续 10 分钟 → 阶段性报告 + 推 1 条
	now := time.Now()
	e.hub.AwayTick(ctx, now)
	e.hub.AwayTick(ctx, now.Add(5*time.Minute))
	time.Sleep(100 * time.Millisecond)
	if sink.count() != 1 {
		t.Fatalf("不到 10 分钟不应推「都处理完了」：%d", sink.count())
	}
	e.hub.AwayTick(ctx, now.Add(11*time.Minute))
	e.waitFor("都处理完了", func() bool { return sink.count() == 2 })
	e.hub.AwayTick(ctx, now.Add(40*time.Minute))
	time.Sleep(200 * time.Millisecond)
	if n := sink.count(); n != 2 {
		t.Fatalf("按昨晚的事件重放只应推 2 条，实际 %d", n)
	}

	var rep awayReportResp
	e.call("GET", "/api/away/report", nil, &rep)
	if rep.Final || rep.Counts.Pushed != 2 || rep.Counts.Merged != 20 || rep.Counts.Normal < 21 || len(rep.Settled) != 1 {
		t.Fatalf("阶段性报告：推 2 条、20 条待验收合并、原本至少推 21 条、1 次阶段性报告：%+v", rep.Counts)
	}
	if rep.Pushes[0].Reason == "" || !strings.Contains(rep.Pushes[0].Reason, "设计选型") || rep.Pushes[1].Event != "away_settled" {
		t.Fatalf("推了的是 D-183（设计选型）和「都处理完了」：%+v", rep.Pushes)
	}
	if len(rep.Yours) != 1 || rep.Yours[0].Device != "iPhone" || rep.Yours[0].Event != "answer" {
		t.Fatalf("报告应记下你在 iPhone 上答了 D-183：%+v", rep.Yours)
	}
	if rep.Counts.Advised != 20 || rep.Counts.Delivered != 20 || rep.Counts.Waiting != 20 {
		t.Fatalf("交付 20 条、建议 20 条、还在等你验收 20 条：%+v", rep.Counts)
	}
	for _, w := range rep.Waiting {
		if w.Kind != "review" || w.First || !strings.Contains(w.Reason, "建议通过") {
			t.Fatalf("有建议通过的待验收不算「要你先看」：%+v", w)
		}
	}

	// 处理完之后：新的硬规则决策照推；标了 hard 的主助理不能代答
	var d2 store.Decision
	if err := c.Do(ctx, "POST", "/agent/decisions", map[string]any{"session_key": "claude:dev", "question": "导出改成异步任务？", "risk": "low", "hard": "product"}, &d2); err != nil {
		t.Fatal(err)
	}
	if d2.Risk != "high" || d2.Hard != "product" {
		t.Fatalf("标了 hard 风险档按 high：%+v", d2)
	}
	e.waitFor("新的硬规则决策照推", func() bool { return sink.count() == 3 })

	// 结束离开：只有你能结束；授权窗口随之撤销，报告变为最终报告；之后推送恢复原样
	var fin awayReportResp
	e.call("POST", "/api/away/end", nil, &fin)
	if !fin.Final || fin.Counts.Pushed != 3 || len(fin.Grants) != 1 || fin.Grants[0].Status != store.GrantRevoked {
		t.Fatalf("结束离开：最终报告、授权窗口撤销：%+v %+v", fin.Counts, fin.Grants)
	}
	if code, _ := e.try("POST", "/api/away/end", nil); code != 400 {
		t.Fatalf("不在离开中时结束应报错：%d", code)
	}
	var r store.Requirement
	e.call("POST", "/api/requirements", map[string]any{"title": "回来后的需求", "project_id": project, "status": "in_progress"}, &r)
	if err := c.Do(ctx, "POST", "/agent/requirements/"+r.Code+"/status", map[string]any{"status": "review", "session_key": "claude:dev"}, nil); err != nil {
		t.Fatal(err)
	}
	e.waitFor("回来后待验收照推", func() bool { return sink.count() == 4 })
	st = awayStatusResp{}
	e.call("GET", "/api/away", nil, &st)
	if st.Away != nil || st.Last == nil || st.Last.EndedAt == nil {
		t.Fatalf("结束后不在离开中，last 是刚结束的那次：%+v", st)
	}
}

// 勿扰（推送类也先攒着）：硬规则决策先攒着，拿不准的（问题里提到删除、终端等授权）仍推；
// 兜底词命中的决策主助理不能代答；hard_check 填了某一类被拒；超过预估只标记；授权窗口到期离开保持。
func TestAwayHoldAndHardRules(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	c := client.New(10 * time.Second)
	ctx := context.Background()
	for _, sid := range []string{"adv", "dev"} {
		hook(t, "claude", map[string]any{"session_id": sid, "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "上线", "prompt_id": "p0"})
	}
	e.waitFor("会话入库", func() bool { return e.session("claude:dev").ProjectID != "" })
	for k, r := range map[string]string{"claude:adv": "aide", "claude:dev": "dev"} {
		if err := c.Do(ctx, "POST", "/agent/sessions/role", map[string]any{"session_key": k, "role": r}, nil); err != nil {
			t.Fatal(err)
		}
	}
	sink := e.pushSink(t)
	e.call("POST", "/api/away", map[string]any{"expect_minutes": 30, "scope": "medium", "hold_push": true}, nil)
	raise := func(body map[string]any) store.Decision {
		t.Helper()
		body["session_key"] = "claude:dev"
		var d store.Decision
		if err := c.Do(ctx, "POST", "/agent/decisions", body, &d); err != nil {
			t.Fatal(err)
		}
		return d
	}
	answer := func(d store.Decision, check string) error {
		return c.Do(ctx, "POST", "/agent/decisions/"+d.Code+"/answer", map[string]any{"session_key": "claude:adv", "answer": "好", "reason": "理由", "risk": "low", "hard_check": check}, nil)
	}
	// 硬规则（批准方案）：勿扰时先攒着
	raise(map[string]any{"question": "批准这个方案？", "approval": true})
	// 兜底词：没标 hard，但提到「删除」→ 拿不准，仍推；主助理不能代答
	del := raise(map[string]any{"question": "要不要删除旧的导出文件？", "risk": "low"})
	e.waitFor("拿不准的仍推", func() bool { return sink.count() == 1 })
	if err := answer(del, "none"); err == nil || !strings.Contains(err.Error(), "拿不准") || !strings.Contains(err.Error(), "删除") {
		t.Fatalf("兜底词命中的决策不能代答：%v", err)
	}
	// 授权范围内：不推；hard_check 必填，填了某一类被拒，none 才能代答
	ok := raise(map[string]any{"question": "空状态写「暂无数据」？", "risk": "low"})
	if err := answer(ok, ""); err == nil || !strings.Contains(err.Error(), "hard_check") {
		t.Fatalf("没填 hard_check 应被拒：%v", err)
	}
	if err := answer(ok, "semantics"); err == nil || !strings.Contains(err.Error(), "语义 / 接口变更") {
		t.Fatalf("hard_check 填了某一类应被拒：%v", err)
	}
	if err := answer(ok, "none"); err != nil {
		t.Fatal(err)
	}
	// 终端里等授权：拿不准，仍推
	hook(t, "claude", map[string]any{"session_id": "dev", "cwd": cwd, "hook_event_name": "PermissionRequest", "tool_name": "Bash", "tool_input": map[string]any{"command": "rm -rf build"}})
	e.waitFor("等授权仍推", func() bool { return sink.count() == 2 })
	time.Sleep(150 * time.Millisecond)
	var rep awayReportResp
	e.call("GET", "/api/away/report", nil, &rep)
	if sink.count() != 2 || rep.Counts.Held != 1 || rep.Counts.Pushed != 2 || rep.Counts.Skipped < 1 || rep.Counts.Proxied != 1 {
		t.Fatalf("勿扰：批准方案攒着 1 条，删除、等授权推 2 条，可代答的不推：push=%d %+v", sink.count(), rep.Counts)
	}
	first := 0
	for _, w := range rep.Waiting {
		if w.First {
			first++
		}
	}
	if first != 2 {
		t.Fatalf("要你先看的：批准方案、提到删除的 2 条：%+v", rep.Waiting)
	}

	// 超过预估只标记；授权窗口到期（这里直接改到期时间）后离开保持
	e.store.DB().Exec(`UPDATE aways SET started_at = ?`, time.Now().Add(-45*time.Minute).UTC().Format(time.RFC3339Nano))
	e.store.DB().Exec(`UPDATE grants SET expires_at = ?`, time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano))
	var st awayStatusResp
	e.call("GET", "/api/away", nil, &st)
	if st.Away == nil || !st.OverEstimate || st.GrantActive {
		t.Fatalf("超过预估只标记、授权到期后离开保持：%+v", st)
	}
	if n := sink.count(); n != 2 {
		t.Fatalf("超过预估不推送：%d", n)
	}
}
