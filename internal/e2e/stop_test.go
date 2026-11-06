package e2e

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/client"
	"github.com/NekroAI/kitestring/internal/daemon"
	"github.com/NekroAI/kitestring/internal/store"
)

// R-291：在工作台上让会话停下——只送提醒，不强行中止。
//   - 待命的会话马上收到停下提醒；运行中的在这一轮结束（Stop）时收到，不论身份；
//   - 停下中：Stop hook 不拦；它自己提的决策的答复、用户直接发给它的话照常送达，其他点名消息先排队；
//   - 写完交接，或收到提醒后结束本轮且不再待命，记为已下线；之后 ks wait 直接返回；
//   - 排队的消息在新会话接任时转过去；项目「全部停下」不改自动派发设置。
func TestStopSession(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	c := client.New(30 * time.Second)
	ctx := context.Background()
	ev := func(sid, name string, extra map[string]any) string {
		p := map[string]any{"session_id": sid, "cwd": cwd, "hook_event_name": name}
		for k, v := range extra {
			p[k] = v
		}
		return hook(t, "claude", p)
	}
	for _, sid := range []string{"st-pm", "st-aide", "st-dev", "st-plain", "st-new"} {
		ev(sid, "UserPromptSubmit", map[string]any{"prompt": "上线", "prompt_id": "p0"})
	}
	e.waitFor("会话入库", func() bool { return e.session("claude:st-new").ProjectID != "" })
	for _, kr := range [][2]string{{"claude:st-pm", "pm"}, {"claude:st-aide", "aide"}, {"claude:st-dev", "dev"}} {
		if err := c.Do(ctx, "POST", "/agent/sessions/role", map[string]any{"session_key": kr[0], "role": kr[1]}, nil); err != nil {
			t.Fatal(err)
		}
	}
	pid := e.session("claude:st-pm").ProjectID
	check := func(key string) waitCheckOut {
		t.Helper()
		var out waitCheckOut
		if err := c.Do(ctx, "GET", "/wait/check?session_key="+key, nil, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	// 1. 待命中的主调度：点停下后马上收到停下提醒，状态是停下中（还没下线）
	var pmW chan daemon.WaitResult
	for i := 0; ; i++ { // 先把已有的消息（开发上线等）收掉，再挂上一次不会马上返回的等待
		pmW = parkWait(t, e, c, "claude:st-pm")
		select {
		case <-pmW:
			if i > 5 {
				t.Fatal("主调度挂不上等待")
			}
			continue
		case <-time.After(300 * time.Millisecond):
		}
		break
	}
	ev("st-pm", "Stop", nil)
	e.waitFor("主调度待命", func() bool { return e.session("claude:st-pm").State == store.StateParked })
	e.call("POST", "/api/sessions/claude:st-pm/stop", nil, nil)
	select {
	case res := <-pmW:
		if res.Reason != "delivered" || !strings.Contains(res.Text, "用户让你停下") || !strings.Contains(res.Text, "handoff_write") ||
			!strings.Contains(res.Text, "会话已停下，等新会话接着做") || !strings.Contains(res.Text, "先停派新活") {
			t.Fatalf("待命中的主调度应马上收到停下提醒：%+v", res)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("停下提醒没有叫醒待命中的主调度")
	}
	if x := e.session("claude:st-pm"); x.StoppedAt == nil || x.OfflineAt != nil || x.Role != "pm" {
		t.Fatalf("应记为停下中、身份保留、还没下线：%+v %+v", x.StoppedAt, x.OfflineAt)
	}
	// 重复停下：拒绝
	if code := e.callStatus("POST", "/api/sessions/claude:st-pm/stop", nil); code != 409 {
		t.Fatalf("停下中的会话再停下应被拒绝：%d", code)
	}

	// 2. 停下中：团队消息排队不唤醒；它自己提的决策的答复照常送达（为等答复可以挂 ks wait）
	if out := check("claude:st-pm"); out.Retired {
		t.Fatalf("停下中为等答复应能挂 ks wait：%+v", out)
	}
	var d store.Decision
	if err := c.Do(ctx, "POST", "/agent/decisions", map[string]any{"session_key": "claude:st-pm", "question": "R-1 停在哪个分支？", "options": []string{"甲", "乙"}}, &d); err != nil {
		t.Fatal(err)
	}
	pmW = parkWait(t, e, c, "claude:st-pm")
	if err := c.Do(ctx, "POST", "/agent/team/tell", map[string]any{"session_key": "claude:st-aide", "to": "pm", "text": "R-1 再做一条"}, nil); err != nil {
		t.Fatal(err)
	}
	asleep(t, pmW, "停下中的主调度（团队消息应排队）")
	e.call("POST", "/api/decisions/answers", map[string]any{"answers": []map[string]any{{"ref": d.Code, "answer": "甲"}}}, nil)
	select {
	case res := <-pmW:
		if res.Reason != "delivered" || !strings.Contains(res.Text, "甲") || strings.Contains(res.Text, "再做一条") {
			t.Fatalf("停下中应只收到自己决策的答复，团队消息排队：%+v", res)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("停下中自己决策的答复没有送达")
	}

	// 3. 写完交接：记为已下线；ks wait 直接返回
	var msg struct {
		Message string `json:"message"`
	}
	if err := c.Do(ctx, "POST", "/agent/handoff", map[string]any{"session_key": "claude:st-pm", "doing": "R-1 做到联调，分支 st-line"}, &msg); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg.Message, "结束本轮") {
		t.Fatalf("停下的会话写完交接应被告知结束本轮：%s", msg.Message)
	}
	if x := e.session("claude:st-pm"); x.OfflineAt == nil {
		t.Fatal("写完交接应记为已下线")
	}
	if out := check("claude:st-pm"); !out.Retired || !strings.Contains(out.Hint, "用户已让你停下") {
		t.Fatalf("下线后 ks wait 应直接返回：%+v", out)
	}
	if strings.Contains(ev("st-pm", "Stop", nil), "block") {
		t.Fatal("下线后 Stop 不应被拦")
	}

	// 4. 运行中的开发：这一轮结束时 Stop 被拦下并收到提醒；再结束时不拦，记为已下线
	ev("st-dev", "UserPromptSubmit", map[string]any{"prompt": "做 R-2", "prompt_id": "d1"})
	e.waitFor("开发运行中", func() bool { return e.session("claude:st-dev").State == store.StateRunning })
	e.call("POST", "/api/sessions/claude:st-dev/stop", nil, nil)
	var first string
	e.waitFor("提醒下发到后台服务", func() bool {
		first = ev("st-dev", "Stop", nil)
		return strings.Contains(first, "用户让你停下")
	})
	if !strings.Contains(first, "block") {
		t.Fatalf("运行中的会话应在本轮结束时被拦下并收到提醒：%s", first)
	}
	if x := e.session("claude:st-dev"); x.OfflineAt != nil {
		t.Fatal("提醒刚送达、还在停下的这一轮里，不应记为已下线")
	}
	if out := ev("st-dev", "Stop", nil); strings.Contains(out, "block") {
		t.Fatalf("收到提醒后再结束不应被拦：%s", out)
	}
	e.waitFor("结束本轮不再待命后记为已下线", func() bool { return e.session("claude:st-dev").OfflineAt != nil })
	if st := e.session("claude:st-dev").State; st != store.StateIdle {
		t.Fatalf("停下后结束本轮应记为空闲，不是待回复：%s", st)
	}

	// 5. 没有身份的会话：提醒只要求结束，不要求写交接
	ev("st-plain", "UserPromptSubmit", map[string]any{"prompt": "随便看看", "prompt_id": "x1"})
	e.call("POST", "/api/sessions/claude:st-plain/stop", nil, nil)
	var plain string
	e.waitFor("无身份会话收到提醒", func() bool {
		plain = ev("st-plain", "Stop", nil)
		return strings.Contains(plain, "用户让你停下")
	})
	if strings.Contains(plain, "handoff_write") {
		t.Fatalf("没有身份的会话不应要求写交接：%s", plain)
	}
	ev("st-plain", "Stop", nil)
	e.waitFor("无身份会话下线", func() bool { return e.session("claude:st-plain").OfflineAt != nil })

	// 6. 下线后点名消息排队；新会话接任主调度时转过去
	var told struct {
		Recipients []struct {
			Reason string `json:"reason"`
		} `json:"recipients"`
	}
	if err := c.Do(ctx, "POST", "/agent/team/tell", map[string]any{"session_key": "claude:st-aide", "to": "pm", "text": "R-1 用户说先别合"}, &told); err != nil {
		t.Fatal(err)
	}
	if len(told.Recipients) != 1 || !strings.Contains(told.Recipients[0].Reason, "已停下") {
		t.Fatalf("下线后点名消息应排队并写明已停下：%+v", told)
	}
	var role struct {
		Prompt string `json:"prompt"`
	}
	if err := c.Do(ctx, "POST", "/agent/sessions/role", map[string]any{"session_key": "claude:st-new", "role": "pm"}, &role); err != nil {
		t.Fatal(err)
	}
	moved := 0
	for _, x := range tellsTo(e, "claude:st-new") {
		if strings.Contains(x.Answer, "再做一条") || strings.Contains(x.Answer, "先别合") {
			moved++
		}
	}
	if moved != 2 {
		t.Fatalf("停下期间排队的 2 条团队消息应转给接任的新会话：%d", moved)
	}
	if x := e.session("claude:st-pm"); x.StoppedAt != nil || x.OfflineAt != nil {
		t.Fatalf("接任后旧会话的停下 / 下线标记应清除：%+v", x)
	}

	// 7. 项目「全部停下」：停下还没结束的会话；不改项目的自动派发设置
	p, err := e.store.GetProject(ctx, pid)
	if err != nil {
		t.Fatal(err)
	}
	e.call("PUT", "/api/projects/"+pid, map[string]any{"name": p.Name, "doc_root": p.DocRoot, "auto_dispatch": true}, nil)
	var all struct {
		Stopped []string `json:"stopped"`
	}
	e.call("POST", "/api/projects/"+pid+"/stop", nil, &all)
	want := map[string]bool{"claude:st-aide": true, "claude:st-new": true}
	for _, k := range all.Stopped {
		if !want[k] && k != "claude:st-pm" {
			t.Fatalf("全部停下不应包含已下线的会话：%v", all.Stopped)
		}
		delete(want, k)
	}
	if len(want) > 0 {
		t.Fatalf("全部停下应停下没结束的会话：%v，缺 %v", all.Stopped, want)
	}
	if p, _ = e.store.GetProject(ctx, pid); !p.AutoDispatch {
		t.Fatal("全部停下不应关项目的自动派发")
	}
}
