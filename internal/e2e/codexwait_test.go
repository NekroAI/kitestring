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

// Codex 在前台运行 ks wait（R-74）：等待开始就算待命（这一轮还在跑命令）；最多等 30 分钟；
// 交出需求、留言、验收都能把它唤醒。
func TestCodexWait(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	hook(t, "codex", map[string]any{"session_id": "cx-1", "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "做完先待命", "turn_id": "t0"})
	e.waitFor("会话运行中", func() bool { return e.session("codex:cx-1").State == store.StateRunning })
	c := client.New(30 * time.Second)
	key := "codex:cx-1"
	wait := func(timeout string) chan daemon.WaitResult {
		done := make(chan daemon.WaitResult, 1)
		go func() {
			var res daemon.WaitResult
			_ = c.Do(context.Background(), "GET", "/wait?session_key="+key+"&timeout="+timeout, nil, &res)
			done <- res
		}()
		e.waitFor("登记挂起", func() bool { return e.session(key).Parked })
		return done
	}
	got := func(name string, done chan daemon.WaitResult, want ...string) {
		t.Helper()
		select {
		case res := <-done:
			if res.Reason != "delivered" {
				t.Fatalf("%s：应被唤醒，得到 %+v", name, res)
			}
			for _, w := range want {
				if !strings.Contains(res.Text, w) {
					t.Fatalf("%s：唤醒内容应包含 %q：%q", name, w, res.Text)
				}
			}
		case <-time.After(15 * time.Second):
			t.Fatalf("%s：等待没有返回", name)
		}
		e.waitFor(name+"后不再挂起", func() bool { return !e.session(key).Parked })
	}

	// 要求等 2 小时，Codex 最多 30 分钟；这一轮还在运行，也显示待命
	done := wait("2h")
	s := e.session(key)
	if s.State != store.StateParked {
		t.Fatalf("Codex 前台等待时应显示待命，得到 %s", s.State)
	}
	if s.ParkedUntil == nil || time.Until(*s.ParkedUntil) < 29*time.Minute || time.Until(*s.ParkedUntil) > 31*time.Minute {
		t.Fatalf("Codex 最多等 30 分钟：%v", s.ParkedUntil)
	}

	// 交出需求 → 唤醒
	var r store.Requirement
	e.call("POST", "/api/requirements", map[string]any{"title": "导出 CSV", "detail": "订单列表加导出", "status": "ready"}, &r)
	e.call("POST", "/api/requirements/"+r.Code+"/assign", map[string]any{"session_key": key}, nil)
	got("交出需求", done, "导出 CSV")
	if st := e.session(key).State; st != store.StateRunning {
		t.Fatalf("唤醒后应继续运行，得到 %s", st)
	}
	var cur store.Requirement
	c.Do(context.Background(), "POST", "/agent/sessions/"+key+"/link", map[string]any{"requirement": r.Code}, &cur)

	// 留言 → 唤醒
	done = wait("20s")
	e.call("POST", "/api/requirements/"+r.Code+"/notes", map[string]any{"comment": "按钮放右上角"}, nil)
	got("留言", done, "按钮放右上角")

	// 交验收后用户退回修改 → 唤醒；验收通过 → 也唤醒
	c.Do(context.Background(), "POST", "/agent/requirements/"+r.Code+"/status", map[string]any{"status": "review", "session_key": key}, &cur)
	done = wait("20s")
	e.call("POST", "/api/reviews", map[string]any{"items": []map[string]any{{"ref": r.Code, "verdict": "revise", "comment": "文件名带上日期"}}}, nil)
	got("退回修改", done, "文件名带上日期")
	c.Do(context.Background(), "POST", "/agent/requirements/"+r.Code+"/status", map[string]any{"status": "review", "session_key": key}, &cur)
	done = wait("20s")
	e.call("POST", "/api/reviews", map[string]any{"items": []map[string]any{{"ref": r.Code, "verdict": "accept", "comment": "可以了"}}}, nil)
	got("验收通过", done, "可以了")

	// 超时：带回实际等待时长，会话转为等你回复
	var res daemon.WaitResult
	if err := c.Do(context.Background(), "GET", "/wait?session_key="+key+"&timeout=1s", nil, &res); err != nil || res.Reason != "timeout" || res.Timeout != "1s" {
		t.Fatalf("应超时返回并带上时长：%+v %v", res, err)
	}
	e.waitFor("超时后不再挂起", func() bool { s := e.session(key); return !s.Parked && s.ParkedUntil == nil })
}
