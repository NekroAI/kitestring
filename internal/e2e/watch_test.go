//go:build !windows

package e2e

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/client"
	"github.com/NekroAI/kitestring/internal/store"
)

// R-249 P4：订阅需求（watch）——真实编译 ks 跑 watch / unwatch / watching；逐条改动路径（留言、改描述、证据、改状态、
// 评审意见、关联决策作答、用户退回）验「订阅者收到一句话提醒、引起变动的会话自己收不到」；另验：两档（要动手的叫醒）、
// 已被 tellEdited / 用户修改意见通知过的不重复送、60 秒内的只告知合并成一条、退订优先于因关联而来的默认订阅、--action-only。
func TestReqWatch(t *testing.T) {
	e := setup(t)
	bin := filepath.Join(t.TempDir(), "ks")
	if out, err := exec.Command("go", "build", "-o", bin, "../../cmd/ks").CombinedOutput(); err != nil {
		t.Fatalf("编译 ks 失败：%v\n%s", err, out)
	}
	nest := filepath.Join(t.TempDir(), "nest.sh")
	if err := os.WriteFile(nest, []byte("#!/bin/sh\nn=$1; shift\nif [ \"$n\" -gt 0 ]; then \"$0\" $((n-1)) \"$@\"; s=$?; exit $s; fi\nexec \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	baseEnv := []string{}
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "CLAUDE_CODE_SESSION_ID=") || strings.HasPrefix(kv, "DSH_") {
			continue
		}
		baseEnv = append(baseEnv, kv)
	}
	ks := func(sid string, args ...string) string {
		t.Helper()
		// 套几层 sh：daemon 沿父进程往上找 Claude Code / Codex 本体（最多 5 层）来认会话，跑测试的环境本身可能就在 Claude Code 里，
		// 那样所有调用都会被认成同一个会话；隔开之后 ks 才按 CLAUDE_CODE_SESSION_ID 认当前会话
		cmd := exec.Command(nest, append([]string{"6", bin}, args...)...)
		cmd.Env = append(append([]string{}, baseEnv...), "CLAUDE_CODE_SESSION_ID="+sid)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("ks %v 失败：%v\n%s", args, err, out)
		}
		return string(out)
	}
	cwd, _ := os.Getwd()
	c := client.New(30 * time.Second)
	ctx := context.Background()
	for _, sid := range []string{"wpm", "wdev", "wrev", "wobs"} {
		hook(t, "claude", map[string]any{"session_id": sid, "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "上线", "prompt_id": "p0"})
		e.waitFor("会话入库", func() bool { return e.session("claude:"+sid).ProjectID != "" })
	}
	for key, role := range map[string]string{"claude:wpm": "pm", "claude:wdev": "dev", "claude:wrev": "reviewer"} {
		if err := c.Do(ctx, "POST", "/agent/sessions/role", map[string]any{"session_key": key, "role": role}, nil); err != nil {
			t.Fatal(err)
		}
	}
	project := e.session("claude:wdev").ProjectID
	var r store.Requirement
	e.call("POST", "/api/requirements", map[string]any{"title": "订阅需求", "status": "in_progress", "project_id": project, "acceptance": "1. 能订阅"}, &r)

	watchTells := func(key string) []store.Decision {
		var out struct {
			Decisions []store.Decision `json:"decisions"`
		}
		e.call("GET", "/api/decisions", nil, &out)
		var got []store.Decision
		for _, d := range out.Decisions {
			if d.Kind == store.KindTell && d.Question == "需求订阅" && d.RequirementID == r.ID && d.SessionKey == key {
				got = append(got, d)
			}
		}
		return got
	}
	has := func(key, phrase string) bool {
		for _, d := range watchTells(key) {
			if strings.Contains(d.Answer, phrase) {
				return true
			}
		}
		return false
	}
	// got：key 收到了含 phrase 的提醒；notSelf：引起变动的 self 没收到
	got := func(key, phrase, self string) {
		t.Helper()
		e.waitFor(key+" 收到「"+phrase+"」", func() bool { return has(key, phrase) })
		if self != "" && has(self, phrase) {
			t.Fatalf("自己引起的变动不应通知自己（%s，%s）：%+v", self, phrase, watchTells(self))
		}
	}

	// 1. 显式订阅（ks req watch）；开发关联 = 默认订阅，watching 里写「因关联而订阅」
	if out := ks("wobs", "req", "watch", r.Code); !strings.Contains(out, "已订阅 "+r.Code) || !strings.Contains(out, "两档都收") {
		t.Fatalf("ks req watch：%s", out)
	}
	if err := c.Do(ctx, "POST", "/agent/sessions/claude:wdev/link", map[string]any{"requirement": r.Code}, nil); err != nil {
		t.Fatal(err)
	}
	if out := ks("wobs", "req", "watching"); !strings.Contains(out, r.Code) || !strings.Contains(out, "显式订阅") {
		t.Fatalf("ks req watching（显式）：%s", out)
	}
	if out := ks("wdev", "req", "watching"); !strings.Contains(out, r.Code) || !strings.Contains(out, "因关联而订阅") {
		t.Fatalf("ks req watching（因关联）：%s", out)
	}

	// 2. 留言：观察者留言，开发收到；观察者自己收不到
	var cm struct{ Code string }
	if err := c.Do(ctx, "POST", "/agent/requirements/"+r.Code+"/comments", map[string]any{"session_key": "claude:wobs", "text": "接口要分页"}, &cm); err != nil {
		t.Fatal(err)
	}
	got("claude:wdev", "有新留言 "+cm.Code, "claude:wobs")

	// 3. 改描述（edit.go）：开发改，观察者收到（只告知）
	if err := c.Do(ctx, "POST", "/agent/requirements/"+r.Code+"/edit", map[string]any{"session_key": "claude:wdev", "detail": "加分页"}, nil); err != nil {
		t.Fatal(err)
	}
	got("claude:wobs", "改了描述", "claude:wdev")

	// 4. 附证据：开发附，观察者收到；60 秒内的只告知并进同一条
	if err := c.Do(ctx, "POST", "/agent/requirements/"+r.Code+"/evidence", map[string]any{"kind": "test", "session_key": "claude:wdev", "verdict": "分页可用"}, nil); err != nil {
		t.Fatal(err)
	}
	got("claude:wobs", "附了证据：分页可用", "claude:wdev")
	if ts := watchTells("claude:wobs"); len(ts) != 1 || !ts[0].FYI || !strings.Contains(ts[0].Answer, "改了描述") || !strings.Contains(ts[0].Answer, "只告知") {
		t.Fatalf("60 秒内的只告知应合并成一条：%+v", ts)
	}

	// 5. 主调度改描述：开发在做，tellEdited 已告诉它「按新的做」，订阅提醒不重复送
	if err := c.Do(ctx, "POST", "/agent/requirements/"+r.Code+"/edit", map[string]any{"session_key": "claude:wpm", "detail": "加分页，每页 50"}, nil); err != nil {
		t.Fatal(err)
	}
	got("claude:wobs", "改了描述（主调度", "claude:wpm")
	if has("claude:wdev", "改了描述（主调度") {
		t.Fatal("已被 tellEdited 通知过的开发不应再收到订阅提醒")
	}

	// 6. 改状态（agent 路径）设为受阻：要动手，挂着 ks wait 的观察者被叫醒；开发自己收不到
	obsW := parkWait(t, e, c, "claude:wobs")
	if err := c.Do(ctx, "POST", "/agent/requirements/"+r.Code+"/status", map[string]any{"status": "blocked", "session_key": "claude:wdev", "reason": "等接口"}, nil); err != nil {
		t.Fatal(err)
	}
	woke(t, obsW, "要动手", r.Code, "被设为受阻")
	if has("claude:wdev", "被设为受阻") {
		t.Fatal("开发自己设的受阻不应通知自己")
	}
	// 只告知不叫醒：开发改回进行中（只告知），观察者挂着不醒
	obsW = parkWait(t, e, c, "claude:wobs")
	if err := c.Do(ctx, "POST", "/agent/requirements/"+r.Code+"/status", map[string]any{"status": "in_progress", "session_key": "claude:wdev"}, nil); err != nil {
		t.Fatal(err)
	}
	got("claude:wobs", "状态从受阻改为进行中", "claude:wdev")
	asleep(t, obsW, "观察者（状态变化只告知）")

	// 7. 评审意见：评审也订阅了；不通过 = 要动手，观察者、开发收到，评审自己收不到
	if out := ks("wrev", "req", "watch", r.Code); !strings.Contains(out, "已订阅") {
		t.Fatalf("评审订阅：%s", out)
	}
	if err := c.Do(ctx, "POST", "/agent/requirements/"+r.Code+"/status", map[string]any{"status": "review", "session_key": "claude:wdev"}, nil); err != nil {
		t.Fatal(err)
	}
	got("claude:wrev", "状态从进行中改为待验收", "claude:wdev")
	if err := c.Do(ctx, "POST", "/agent/requirements/"+r.Code+"/reviews", map[string]any{"session_key": "claude:wrev", "verdict": "1 条不通过",
		"blocks": []any{map[string]any{"type": "checklist", "items": []any{map[string]any{"criterion": 1, "text": "能订阅", "result": "fail", "why": "没有 watching"}}}}}, nil); err != nil {
		t.Fatal(err)
	}
	got("claude:wobs", "不通过 1 条", "claude:wrev")
	got("claude:wdev", "不通过 1 条", "")

	// 8. 用户退回（网页 hub 路径）：修改意见已送给开发，订阅提醒不重复送；观察者收到「被退回」（要动手）
	e.call("POST", "/api/reviews", map[string]any{"items": []map[string]any{{"ref": r.Code, "verdict": "revise", "comment": "补 watching"}}}, nil)
	got("claude:wobs", "被退回", "")
	if has("claude:wdev", "被退回") {
		t.Fatal("收到修改意见的开发不应再收到「被退回」提醒")
	}
	for _, d := range watchTells("claude:wobs") {
		if strings.Contains(d.Answer, "被退回") && (d.FYI || !strings.Contains(d.Answer, "（用户）")) {
			t.Fatalf("退回要动手、写明是用户：%+v", d)
		}
	}

	// 9. 关联决策被作答：开发提的决策，用户作答 → 观察者收到（要动手）；提问的开发从答复本身知道，不重复送
	var dec struct{ Code string }
	if err := c.Do(ctx, "POST", "/agent/decisions", map[string]any{"session_key": "claude:wdev", "question": "每页 50 还是 100", "requirement": r.Code}, &dec); err != nil {
		t.Fatal(err)
	}
	e.call("POST", "/api/decisions/answers", map[string]any{"answers": []map[string]any{{"ref": dec.Code, "answer": "50"}}}, nil)
	got("claude:wobs", "关联决策 "+dec.Code, "claude:wdev")

	// 9b. 用户在网页上留言：收到这条留言的会话（feedbackTarget）不重复送订阅提醒；别的订阅者收到
	ks("wpm", "req", "watch", r.Code)
	var note store.Decision
	e.call("POST", "/api/requirements/"+r.Code+"/notes", map[string]any{"comment": "先别动表头"}, &note)
	if note.SessionKey == "" {
		t.Fatalf("用户留言应送给某个会话：%+v", note)
	}
	for _, k := range []string{"claude:wobs", "claude:wpm", "claude:wdev"} {
		if k == note.SessionKey {
			continue
		}
		got(k, "用户留言 "+note.Code, "")
	}
	if has(note.SessionKey, "用户留言 "+note.Code) {
		t.Fatalf("收到用户留言的 %s 不应再收到订阅提醒", note.SessionKey)
	}

	// 10. 退订优先于因关联而来的默认订阅
	if out := ks("wdev", "req", "unwatch", r.Code); !strings.Contains(out, "已退订") || !strings.Contains(out, "退订优先") {
		t.Fatalf("ks req unwatch：%s", out)
	}
	if out := ks("wdev", "req", "watching"); !strings.Contains(out, "已退订："+r.Code) {
		t.Fatalf("watching 应列出退订：%s", out)
	}
	var cm2 struct{ Code string }
	if err := c.Do(ctx, "POST", "/agent/requirements/"+r.Code+"/comments", map[string]any{"session_key": "claude:wobs", "text": "watching 补好了吗"}, &cm2); err != nil {
		t.Fatal(err)
	}
	got("claude:wrev", "有新留言 "+cm2.Code, "claude:wobs")
	if has("claude:wdev", "有新留言 "+cm2.Code) {
		t.Fatal("退订后不应再收到提醒")
	}

	// 11. --action-only：只收要动手的，留言（只告知）不送
	if out := ks("wobs", "req", "watch", r.Code, "--action-only"); !strings.Contains(out, "只收要动手的") {
		t.Fatalf("--action-only：%s", out)
	}
	var cm3 struct{ Code string }
	if err := c.Do(ctx, "POST", "/agent/requirements/"+r.Code+"/comments", map[string]any{"session_key": "claude:wrev", "text": "我再看一遍"}, &cm3); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if has("claude:wobs", "有新留言 "+cm3.Code) {
		t.Fatal("只收要动手的订阅者不应收到留言提醒")
	}
	if out := ks("wobs", "req", "watching"); !strings.Contains(out, "只收要动手的") {
		t.Fatalf("watching 应写明档位：%s", out)
	}

	// 12. 开始做（session_link_req：待开始 → 进行中）和放行（主调度把草稿设为待开始）：引起的会话自己收不到
	var r2, r3 store.Requirement
	e.call("POST", "/api/requirements", map[string]any{"title": "开始做", "status": "draft", "project_id": project}, &r2)
	e.call("POST", "/api/requirements", map[string]any{"title": "放行", "status": "draft", "project_id": project}, &r3)
	e.allowRelease("claude:wpm")
	for _, x := range []string{r2.Code, r3.Code} {
		for _, sid := range []string{"wobs", "wpm", "wdev"} {
			ks(sid, "req", "watch", x)
		}
	}
	reqTell := func(key, reqID, phrase string) bool {
		var out struct {
			Decisions []store.Decision `json:"decisions"`
		}
		e.call("GET", "/api/decisions", nil, &out)
		for _, d := range out.Decisions {
			if d.Kind == store.KindTell && d.Question == "需求订阅" && d.RequirementID == reqID && d.SessionKey == key && strings.Contains(d.Answer, phrase) {
				return true
			}
		}
		return false
	}
	if err := c.Do(ctx, "POST", "/agent/requirements/"+r3.Code+"/status", map[string]any{"status": "ready", "session_key": "claude:wpm", "reason": "做法明确"}, nil); err != nil {
		t.Fatal(err)
	}
	e.waitFor("观察者收到放行", func() bool { return reqTell("claude:wobs", r3.ID, "状态从草稿改为待开始") })
	if reqTell("claude:wpm", r3.ID, "状态从草稿改为待开始") {
		t.Fatal("主调度自己放行不应通知自己")
	}
	if err := c.Do(ctx, "POST", "/agent/requirements/"+r2.Code+"/status", map[string]any{"status": "ready", "session_key": "claude:wpm", "reason": "做法明确"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.Do(ctx, "POST", "/agent/sessions/claude:wdev/link", map[string]any{"requirement": r2.Code}, nil); err != nil {
		t.Fatal(err)
	}
	e.waitFor("观察者收到开始做", func() bool { return reqTell("claude:wobs", r2.ID, "状态从待开始改为进行中") })
	if reqTell("claude:wdev", r2.ID, "状态从待开始改为进行中") {
		t.Fatal("开发自己开始做不应通知自己")
	}
}
