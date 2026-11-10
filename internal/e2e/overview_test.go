package e2e

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/client"
	"github.com/NekroAI/kitestring/internal/store"
)

// R-245：ks role 接任时纸鸢按身份从记录生成接手概览。
//   - 主调度：进行中 / 受阻、已放行待排（按优先级）、在跑的子任务、已验收但没合入 main / 没部署、main 没推送的提交；
//     合入只认最近一轮关联的提交，由 daemon 在本机仓库查（祖先或补丁相同都算）——多轮验收时早一轮已在 main 不算数
//   - 主助理：待验收和写过的验收建议、主助理提的未答决策、在等评审的
//   - 当前安排：会话记、接任时自动读到；压缩后的关键状态也带上
func TestTakeoverOverview(t *testing.T) {
	for k, v := range map[string]string{"GIT_CONFIG_GLOBAL": "/dev/null", "GIT_CONFIG_NOSYSTEM": "1", "GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@example.com", "GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@example.com"} {
		t.Setenv(k, v)
	}
	e := setup(t)
	repo, err := os.MkdirTemp(sockTmp(), "ks-repo-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(repo) })
	gitRun(t, repo, "init", "-q", "-b", "main")
	n := 0
	commit := func(msg string) string {
		t.Helper()
		n++
		if err := os.WriteFile(repo+"/f"+strconv.Itoa(n), []byte(msg), 0o644); err != nil {
			t.Fatal(err)
		}
		gitRun(t, repo, "add", ".")
		gitRun(t, repo, "commit", "-q", "-m", msg)
		return gitRun(t, repo, "rev-parse", "HEAD")
	}
	base := commit("chore: 初始化")

	c := client.New(30 * time.Second)
	ctx := context.Background()
	ev := func(sid, name string, extra map[string]any) string {
		p := map[string]any{"session_id": sid, "cwd": repo, "hook_event_name": name}
		for k, v := range extra {
			p[k] = v
		}
		return hook(t, "claude", p)
	}
	for _, sid := range []string{"dev", "pm1", "aide1", "rev"} {
		ev(sid, "SessionStart", map[string]any{"source": "startup"})
	}
	e.waitFor("会话入库", func() bool { return e.session("claude:rev").ProjectID != "" })
	role := func(key, r string) string {
		t.Helper()
		var out struct {
			Prompt string `json:"prompt"`
		}
		if err := c.Do(ctx, "POST", "/agent/sessions/role", map[string]any{"session_key": key, "role": r}, &out); err != nil {
			t.Fatal(err)
		}
		return out.Prompt
	}
	role("claude:pm1", "pm")
	role("claude:aide1", "aide")
	role("claude:rev", "reviewer")
	newReq := func(title, status, prio string) store.Requirement {
		t.Helper()
		var r store.Requirement
		e.call("POST", "/api/requirements", map[string]any{"title": title, "project_id": e.session("claude:dev").ProjectID, "status": status, "priority": prio}, &r)
		return r
	}
	status := func(ref, st string) {
		t.Helper()
		if err := c.Do(ctx, "POST", "/agent/requirements/"+ref+"/status", map[string]any{"status": st, "session_key": "claude:dev"}, nil); err != nil {
			t.Fatal(err)
		}
	}
	review := func(ref, verdict, comment string) {
		t.Helper()
		e.call("POST", "/api/reviews", map[string]any{"items": []map[string]any{{"ref": ref, "verdict": verdict, "comment": comment}}}, nil)
	}
	recorded := func(ref string, n int) {
		t.Helper()
		ev("dev", "Stop", nil) // daemon 在事件里带上新提交
		e.waitFor(ref+" 关联到提交", func() bool {
			var rc store.ReqCommits
			e.call("GET", "/api/requirements/"+ref+"/commits", nil, &rc)
			return len(rc.Commits) == n
		})
	}

	// 多轮：第一轮的提交直接在 main 上，被退回；第二轮在分支上提交、验收通过，还没合入 main
	shipped := newReq("会话别把拍板的事埋在对话里", "in_progress", "P2")
	commit("第一轮（" + shipped.Code + "）")
	recorded(shipped.Code, 1)
	status(shipped.Code, "review")
	review(shipped.Code, "revise", "提示词再短一点")
	time.Sleep(1100 * time.Millisecond) // 提交时间只精确到秒
	gitRun(t, repo, "checkout", "-q", "-b", "line3")
	round2 := commit("第二轮（" + shipped.Code + "）")
	recorded(shipped.Code, 2)
	status(shipped.Code, "review")
	review(shipped.Code, "accept", "")
	e.hub.SetDeployCommit(base)

	// 其它记录：进行中 / 受阻、放行待排（顺序按优先级）、没有提交记录的已验收需求、在跑的子任务
	going := newReq("工作台简报条", "in_progress", "P2")
	blocked := newReq("等定稿的界面", "in_progress", "P2")
	if err := c.Do(ctx, "POST", "/agent/requirements/"+blocked.Code+"/status", map[string]any{"status": "blocked", "session_key": "claude:dev", "waiting_on": going.Code, "reason": "等简报条定稿"}, nil); err != nil {
		t.Fatal(err)
	}
	later := newReq("排在后面的", "ready", "P2")
	first := newReq("先做的", "ready", "P1")
	noRec := newReq("没记提交的", "in_progress", "P2")
	status(noRec.Code, "review")
	review(noRec.Code, "accept", "")
	ev("pm1", "SubagentStart", map[string]any{"agent_id": "a-ov", "agent_type": "general-purpose"})
	e.waitFor("子任务在跑", func() bool { return len(e.session("claude:pm1").Tasks) == 1 })

	// 当前安排：会话记下，接任时自动读到
	var arr struct {
		Arrangement store.Arrangement `json:"arrangement"`
		Message     string            `json:"message"`
	}
	if err := c.Do(ctx, "POST", "/agent/arrangements", map[string]any{"session_key": "claude:pm1", "text": "额度紧，做完 " + first.Code + " 就停", "until": "6h"}, &arr); err != nil || arr.Arrangement.Code != "N-1" || !strings.Contains(arr.Message, "接任的会话会自动读到") {
		t.Fatalf("记安排：%+v %v", arr, err)
	}

	// 新会话接任主调度
	ev("pm2", "SessionStart", map[string]any{"source": "startup"})
	e.waitFor("新主调度入库", func() bool { return e.session("claude:pm2").ProjectID != "" })
	p := role("claude:pm2", "pm")
	idx := func(s string) int { return strings.Index(p, s) }
	for _, want := range []string{
		"## 接手概览",
		"### 当前安排", "N-1「额度紧，做完 " + first.Code + " 就停」（主调度", "至 ",
		"### 进行中 / 受阻的需求", going.Code + "「工作台简报条」进行中", blocked.Code + "「等定稿的界面」受阻，在等 " + going.Code + "：等简报条定稿",
		"### 已放行待排", "1. " + first.Code + "「先做的」P1", "2. " + later.Code + "「排在后面的」P2",
		"### 在跑的子任务", "「general-purpose」（已跑",
		"### 已验收但没合入 main / 没部署",
		shipped.Code + "「会话别把拍板的事埋在对话里」：没合入 main（最近一轮的 " + round2[:7] + " 不在 main 上）",
		noRec.Code + "「没记提交的」：最近一轮没有关联的提交记录",
		"### 推送", "main 没有远端",
	} {
		if !strings.Contains(p, want) {
			t.Fatalf("主调度接手概览缺 %q：\n%s", want, p)
		}
	}
	if idx("1. "+first.Code) > idx("2. "+later.Code) || idx("## 接手概览") < idx("你的会话身份") {
		t.Fatalf("顺序不对：\n%s", p)
	}

	// 今天 cherry-pick 合入 main：最近一轮补丁相同，算已合入；部署的还是旧版本，标没部署
	gitRun(t, repo, "checkout", "-q", "main")
	commit("chore: main 上别的提交") // 不然父提交、时间都相同时 cherry-pick 出来的就是同一个提交
	gitRun(t, repo, "cherry-pick", round2)
	picked := gitRun(t, repo, "rev-parse", "HEAD")
	recorded(shipped.Code, 3)
	p = role("claude:pm2", "pm")
	if !strings.Contains(p, shipped.Code+"「会话别把拍板的事埋在对话里」：已合入 main，还没部署") || strings.Contains(p, "没合入 main（") {
		t.Fatalf("cherry-pick 合入后应算已合入、没部署：\n%s", p)
	}
	// 部署了合入后的 main：不再单列，计入「其余」
	e.hub.SetDeployCommit(picked)
	p = role("claude:pm2", "pm")
	if strings.Contains(p, shipped.Code+"「会话别把拍板的事埋在对话里」：") || !strings.Contains(p, "其余 1 条已合入 main 并已部署") {
		t.Fatalf("合入并部署后应计入其余：\n%s", p)
	}

	// 主助理：待验收和写过的建议、上一任提的未答决策、在等评审的
	adv := newReq("待验收有建议", "in_progress", "P2")
	status(adv.Code, "review")
	plain := newReq("待验收没建议", "in_progress", "P2")
	status(plain.Code, "review")
	if err := c.Do(ctx, "POST", "/agent/morning", map[string]any{"session_key": "claude:aide1", "advice": []map[string]any{{"requirement": adv.Code, "verdict": "pass", "reason": "3/3 达标"}}}, nil); err != nil {
		t.Fatal(err)
	}
	e.call("POST", "/api/requirements/"+plain.Code+"/reviewer", map[string]any{"session_key": "claude:rev"}, nil)
	var d store.Decision
	if err := c.Do(ctx, "POST", "/agent/decisions", map[string]any{"session_key": "claude:aide1", "question": "简报条放顶部还是侧栏", "requirement": "none"}, &d); err != nil {
		t.Fatal(err)
	}
	var other store.Decision
	if err := c.Do(ctx, "POST", "/agent/decisions", map[string]any{"session_key": "claude:dev", "question": "开发自己提的", "requirement": "none"}, &other); err != nil {
		t.Fatal(err)
	}
	ev("aide2", "SessionStart", map[string]any{"source": "startup"})
	e.waitFor("新主助理入库", func() bool { return e.session("claude:aide2").ProjectID != "" })
	p = role("claude:aide2", "aide")
	for _, want := range []string{
		"## 接手概览", "### 当前安排", "N-1「",
		"### 待验收", adv.Code + "「待验收有建议」：已写建议通过——3/3 达标", plain.Code + "「待验收没建议」：还没写验收建议",
		"### 主助理提的、还没答的决策", d.Code + "「简报条放顶部还是侧栏」",
		"### 在等评审的需求", plain.Code + "「待验收没建议」：评审",
	} {
		if !strings.Contains(p, want) {
			t.Fatalf("主助理接手概览缺 %q：\n%s", want, p)
		}
	}
	// 别人提的决策不进「主助理提的」，单列在「别的会话提的」（R-272：下线期间新提的决策接任时一并查冲突）
	mine, others, _ := strings.Cut(p, "### 别的会话提的、还没答的决策")
	if strings.Contains(mine, other.Code) || !strings.Contains(others, other.Code) || strings.Contains(p, "### 已放行待排") {
		t.Fatalf("主助理概览：别人提的决策应单列在「别的会话提的」，不该有主调度的栏目：\n%s", p)
	}

	// 撤销安排后接任不再读到；压缩后的关键状态带上概览和安排
	if err := c.Do(ctx, "POST", "/agent/arrangements", map[string]any{"session_key": "claude:aide2", "text": "这次可以 push"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.Do(ctx, "POST", "/agent/arrangements/N-1/revoke", map[string]any{"session_key": "claude:aide2"}, nil); err != nil {
		t.Fatal(err)
	}
	e.waitFor("压缩后补上概览和安排", func() bool {
		got := ev("pm2", "SessionStart", map[string]any{"source": "compact"})
		return strings.Contains(got, "按身份要接着管的事") && strings.Contains(got, "N-2「这次可以 push」（用户") == false && strings.Contains(got, "N-2「这次可以 push」（主助理") && !strings.Contains(got, "N-1「") && strings.Contains(got, "### 已放行待排")
	})
	var list struct {
		Arrangements []store.Arrangement `json:"arrangements"`
	}
	e.call("GET", "/api/arrangements?all=true", nil, &list)
	if len(list.Arrangements) != 2 || list.Arrangements[1].Status != store.ArrangeRevoked || list.Arrangements[1].RevokedBy != "claude:aide2" {
		t.Fatalf("撤销留痕：%+v", list.Arrangements)
	}
	// 用户在网页上加 / 撤：署名「用户」
	var web struct {
		Arrangement store.Arrangement `json:"arrangement"`
	}
	e.call("POST", "/api/arrangements", map[string]any{"project_id": e.session("claude:dev").ProjectID, "text": "今晚别部署", "until": "2h"}, &web)
	if web.Arrangement.BySession != "" || web.Arrangement.ExpiresAt == nil {
		t.Fatalf("网页加的安排：%+v", web.Arrangement)
	}
	p = role("claude:pm2", "pm")
	if !strings.Contains(p, web.Arrangement.Code+"「今晚别部署」（用户记") {
		t.Fatalf("网页加的安排接任时读到：\n%s", p)
	}
	e.call("POST", "/api/arrangements/"+web.Arrangement.Code+"/revoke", nil, &web)
	if web.Arrangement.Status != store.ArrangeRevoked || web.Arrangement.RevokedBy != store.ArrangeByUser {
		t.Fatalf("网页撤销：%+v", web.Arrangement)
	}
}
