package e2e

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/client"
	"github.com/NekroAI/kitestring/internal/store"
)

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v：%v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// 需求关联提交、回滚与 CI（R-156）：标题写了（R-x）的提交由 daemon 读本地 git log 自动关联；
// revert（标题里写没写编号都算）被认出、被回滚的提交标上回滚它的提交并给出提示；Agent 可手动关联、上报 CI；
// 决策后果链里的提交带上同样的信息；纸鸢自始至终不改仓库。
func TestCommitsRevertCI(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@example.com")
	t.Setenv("KS_GITHUB_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	e := setup(t)
	repo, err := os.MkdirTemp("/tmp", "ks-repo-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(repo) })
	gitRun(t, repo, "init", "-q", "-b", "main")
	n := 0
	commit := func(msg ...string) string {
		t.Helper()
		n++
		f := repo + "/f" + strconv.Itoa(n)
		if err := os.WriteFile(f, []byte(msg[0]), 0o644); err != nil {
			t.Fatal(err)
		}
		gitRun(t, repo, "add", ".")
		args := []string{"commit", "-q"}
		for _, m := range msg {
			args = append(args, "-m", m)
		}
		gitRun(t, repo, args...)
		return gitRun(t, repo, "rev-parse", "HEAD")
	}
	commit("chore: 初始化")

	c := client.New(10 * time.Second)
	ctx := context.Background()
	stop := func() {
		hook(t, "claude", map[string]any{"session_id": "dev", "cwd": repo, "hook_event_name": "Stop"})
	}
	hook(t, "claude", map[string]any{"session_id": "dev", "cwd": repo, "hook_event_name": "SessionStart", "source": "startup"})
	e.waitFor("会话入库", func() bool { return e.session("claude:dev").ProjectID != "" })
	var r store.Requirement
	if err := c.Do(ctx, "POST", "/agent/requirements", map[string]any{"title": "加一个按钮", "session_key": "claude:dev", "status": "in_progress"}, &r); err != nil {
		t.Fatal(err)
	}
	commits := func() store.ReqCommits {
		var rc store.ReqCommits
		e.call("GET", "/api/requirements/"+r.Code+"/commits", nil, &rc)
		return rc
	}
	if rc := commits(); len(rc.Commits) != 0 || len(rc.Hints) != 0 {
		t.Fatalf("还没提交时应为空：%+v", rc)
	}

	// 标题末尾写（R-x）的提交自动关联；编号只是前缀（R-x0）的、没写编号的不算
	a := commit("feat: 加按钮（" + r.Code + "）")
	commit("feat: 别的（" + r.Code + "0）")
	plain := commit("chore: 没写编号")
	head := gitRun(t, repo, "rev-parse", "HEAD")
	stop()
	e.waitFor("提交关联到需求", func() bool { return len(commits().Commits) == 1 })
	rc := commits()
	if x := rc.Commits[0]; x.Link != a || x.Source != "git" || x.Summary != "feat: 加按钮（"+r.Code+"）" || x.RevertedBy != "" || x.CI != nil {
		t.Fatalf("自动关联的提交不对：%+v", x)
	}

	// Agent 手动关联没写编号的提交（短哈希）；参数不对时拒绝
	if err := c.Do(ctx, "POST", "/agent/commits", map[string]any{"session_key": "claude:dev", "requirement": r.Code, "sha": "xyz"}, nil); err == nil {
		t.Fatal("哈希不合法应被拒")
	}
	if err := c.Do(ctx, "POST", "/agent/commits", map[string]any{"session_key": "claude:dev", "sha": plain}, nil); err == nil {
		t.Fatal("既不关联也不上报 CI 应被拒")
	}
	var linked store.ReqCommits
	if err := c.Do(ctx, "POST", "/agent/commits", map[string]any{"session_key": "claude:dev", "requirement": r.Code, "sha": plain[:8], "subject": "chore: 没写编号"}, &linked); err != nil {
		t.Fatal(err)
	}
	if len(linked.Commits) != 2 || linked.Commits[1].Source != "agent" || linked.Commits[1].Link != plain[:8] {
		t.Fatalf("手动关联后应有 2 个提交：%+v", linked.Commits)
	}

	// revert：git revert 生成的（标题带编号）与手写的、标题没编号的都认出来
	gitRun(t, repo, "revert", "--no-edit", a)
	ra := gitRun(t, repo, "rev-parse", "HEAD")
	rp := commit("revert: 撤掉没写编号的那个", "This reverts commit "+plain+".")
	stop()
	e.waitFor("认出回滚", func() bool { return len(commits().Commits) == 4 })
	rc = commits()
	by := map[string]store.ChainCommit{}
	for _, x := range rc.Commits {
		by[x.Link] = x
	}
	if by[a].RevertedBy != ra || by[ra].Reverts != a || by[plain[:8]].RevertedBy != rp || by[rp].Reverts != plain {
		t.Fatalf("回滚关系不对：%+v", rc.Commits)
	}
	if len(rc.Hints) != 2 || !strings.Contains(rc.Hints[0], "git revert "+ra[:7]) || !strings.Contains(rc.Hints[0], "（"+r.Code+"）") {
		t.Fatalf("被回滚应提示该怎么做：%v", rc.Hints)
	}

	// 回滚的回滚：改动恢复了，不再提示
	gitRun(t, repo, "revert", "--no-edit", ra)
	rra := gitRun(t, repo, "rev-parse", "HEAD")
	stop()
	e.waitFor("回滚的回滚", func() bool { return len(commits().Commits) == 5 })
	rc = commits()
	for _, h := range rc.Hints {
		if strings.Contains(h, a[:7]) {
			t.Fatalf("回滚已被回滚（%s），不该再提示 %s：%v", rra[:7], a[:7], rc.Hints)
		}
	}

	// CI：Agent 上报（只读记录）；未通过时提示
	if err := c.Do(ctx, "POST", "/agent/commits", map[string]any{"session_key": "claude:dev", "sha": rra, "ci": "failure", "ci_url": "https://ci.example/1"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.Do(ctx, "POST", "/agent/commits", map[string]any{"session_key": "claude:dev", "sha": rra, "ci": "bogus"}, nil); err == nil {
		t.Fatal("CI 状态不合法应被拒")
	}
	rc = commits()
	var ciOK bool
	for _, x := range rc.Commits {
		if x.Link == rra && x.CI != nil && x.CI.State == "failure" && x.CI.Source == "agent" && x.CI.URL == "https://ci.example/1" {
			ciOK = true
		}
	}
	if !ciOK || !strings.Contains(strings.Join(rc.Hints, "\n"), "CI 未通过（https://ci.example/1）") {
		t.Fatalf("CI 结果应显示并提示：%+v %v", rc.Commits, rc.Hints)
	}

	// Agent 读同一份；决策后果链里的提交带上回滚信息
	var arc store.ReqCommits
	if err := c.Do(ctx, "GET", "/agent/requirements/"+r.Code+"/commits", nil, &arc); err != nil || len(arc.Commits) != 5 {
		t.Fatalf("Agent 读提交：%v %+v", err, arc)
	}
	var d store.Decision
	if err := c.Do(ctx, "POST", "/agent/decisions", map[string]any{"session_key": "claude:dev", "question": "按钮放哪", "requirement": r.Code}, &d); err != nil {
		t.Fatal(err)
	}
	var ch store.DecisionChain
	e.call("GET", "/api/decisions/"+d.Code+"/chain", nil, &ch)
	if len(ch.Requirements) != 1 || len(ch.Requirements[0].Commits) != 5 {
		t.Fatalf("后果链应带上需求的提交：%+v", ch.Requirements)
	}

	// daemon 重启后回看最近的提交，Hub 按哈希去重，不会重复
	hook(t, "claude", map[string]any{"session_id": "dev", "cwd": repo, "hook_event_name": "SessionStart", "source": "resume"})
	stop()
	time.Sleep(300 * time.Millisecond)
	if n := len(commits().Commits); n != 5 {
		t.Fatalf("重复上报不应重复记：%d", n)
	}

	// 纸鸢不改仓库：工作区干净，提交只有测试自己做的
	if st := gitRun(t, repo, "status", "--porcelain"); st != "" {
		t.Fatalf("仓库不应被改动：%s", st)
	}
	if cnt := gitRun(t, repo, "rev-list", "--count", "HEAD"); cnt != "7" || head == "" {
		t.Fatalf("提交数应为测试做的 7 个：%s", cnt)
	}
}
