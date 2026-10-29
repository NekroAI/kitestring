package daemon

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/NekroAI/kitestring/internal/protocol"
)

// R-251：一个 DSH Host 同时开着多个会话，它们共用同一个“进程号@启动时间”。
// Host 崩溃时每个会话都要补发结束；还活着的 Claude 会话不动。
func TestDSHHostCrashEndsAllSessions(t *testing.T) {
	d := newQDaemon(t, t.TempDir())
	defer d.q.close()
	host := "4242@Thu Oct 29 10:00:00 2026"
	d.mu.Lock()
	for _, key := range []string{"dsh:a", "dsh:b", "dsh:c"} {
		d.instances[host] = key // 一对一的表只记得最后一个
		d.noteDSHInstanceLocked(host, key, false)
		d.latest[key] = host
	}
	d.instances["7@claude"] = "claude:x"
	d.mu.Unlock()

	d.checkInstances(func(inst string) bool { return inst == "7@claude" })
	got := sessionEnds(t, d)
	for _, key := range []string{"dsh:a", "dsh:b", "dsh:c"} {
		if got[key] != "process_gone|"+host {
			t.Fatalf("Host 崩溃应结束它承载的全部会话，%s 没有结束：%v", key, got)
		}
	}
	if _, ok := got["claude:x"]; ok {
		t.Fatal("还活着的 Claude 会话不该被结束")
	}
	if len(d.dshInst) != 0 || d.latest["dsh:a"] != "" {
		t.Fatalf("Host 退出后应清掉实例记录：dshInst=%v latest=%v", d.dshInst, d.latest)
	}
}

// 会话在 Host 里自己结束了，就从这个 Host 的集合里去掉。
func TestDSHInstanceForgetsEndedSession(t *testing.T) {
	d := newQDaemon(t, t.TempDir())
	defer d.q.close()
	d.mu.Lock()
	d.noteDSHInstanceLocked("1@h", "dsh:a", false)
	d.noteDSHInstanceLocked("1@h", "dsh:b", false)
	d.noteDSHInstanceLocked("1@h", "dsh:a", true)
	d.mu.Unlock()
	if len(d.dshInst["1@h"]) != 1 || !d.dshInst["1@h"]["dsh:b"] {
		t.Fatalf("得到 %v", d.dshInst)
	}
}

// R-251：两个 Host 开同一个 DSH 会话时，较早的那个 Host 挂 /wait 直接得到 stale，不再和新 Host 互抢。
func TestDSHPluginWaitStaleHost(t *testing.T) {
	d := newQDaemon(t, t.TempDir())
	defer d.q.close()
	d.latest["dsh:s"] = "2@new"
	rec := httptest.NewRecorder()
	d.handleWait(rec, httptest.NewRequest("GET", "/wait?session_key=dsh:s&via=plugin&timeout=1s&instance=1@old", nil))
	var res WaitResult
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.Reason != "stale" {
		t.Fatalf("较早的 Host 应得到 stale，得到 %+v", res)
	}
	// 模型自己跑 ks wait 仍然直接得到 disabled，不受实例影响
	rec = httptest.NewRecorder()
	d.handleWait(rec, httptest.NewRequest("GET", "/wait?session_key=dsh:s&instance=1@old", nil))
	res = WaitResult{}
	_ = json.Unmarshal(rec.Body.Bytes(), &res)
	if res.Reason != "disabled" {
		t.Fatalf("模型自己的 ks wait 应为 disabled，得到 %+v", res)
	}
}

// R-251：DSH 的 PostCompact 要重发此前已送达、仍然有效的答复（Claude / Codex 靠 SessionStart compact）。
func TestDSHPostCompactRedelivers(t *testing.T) {
	d := newQDaemon(t, t.TempDir())
	defer d.q.close()
	d.decisions = []protocol.Decision{{ID: "d1", SessionKey: "dsh:s", Question: "先做哪个", Answer: "先做 A", Delivered: true}}
	if got := d.injection(protocol.Event{Tool: protocol.ToolDSH, SessionID: "s", Name: "PostCompact"}); got == "" {
		t.Fatal("PostCompact 后应重发已送达的答复")
	}
	d.decisions[0].SessionKey = "claude:s"
	if got := d.injection(protocol.Event{Tool: protocol.ToolClaude, SessionID: "s", Name: "PostCompact"}); got != "" {
		t.Fatalf("Claude 的 PostCompact 不应重发，得到 %q", got)
	}
}
