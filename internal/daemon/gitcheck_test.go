package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NekroAI/kitestring/internal/protocol"
)

// testRepo 在临时目录建一个仓库，run 执行 git 命令并返回输出。
func testRepo(t *testing.T) (string, func(args ...string) string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("没有 git")
	}
	dir := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@x", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@x", "GIT_CONFIG_GLOBAL=/dev/null")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v：%v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "-q", "-b", "main")
	return dir, run
}

func commitFile(t *testing.T, dir string, run func(...string) string, name, body, msg string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", name)
	run("commit", "-q", "-m", msg)
	return run("rev-parse", "HEAD")
}

// R-245：daemon 在本机仓库查合入 / 部署 / 推送——直接是 main 的祖先、或 cherry-pick 到 main 上补丁相同都算已合入；
// 部署按运行中 ks 的构建提交同样判；main 没有上游时标没有远端，有上游时数没推送的提交。
func TestCheckGit(t *testing.T) {
	dir, run := testRepo(t)
	m1 := commitFile(t, dir, run, "base.txt", "base\n", "初始")
	run("checkout", "-q", "-b", "line3")
	f1 := commitFile(t, dir, run, "a.txt", "a\n", "第二轮的改动（R-235）")
	f2 := commitFile(t, dir, run, "b.txt", "b\n", "还没合入的改动（R-235）")
	run("checkout", "-q", "main")
	commitFile(t, dir, run, "c.txt", "c\n", "main 上别的提交")
	run("cherry-pick", f1)
	m2 := run("rev-parse", "HEAD")

	res := checkGit(protocol.GitCheck{ReqID: "q1", Dir: dir, SHAs: []string{m1[:7], f1, f2, "deadbeef00"}, Deploy: m1})
	if res.ReqID != "q1" || res.Err != "" || res.Main != "main" || !res.NoRemote || res.Upstream != "" {
		t.Fatalf("仓库信息不对：%+v", res)
	}
	want := []protocol.CommitPlace{
		{SHA: m1[:7], Subject: "初始", Main: protocol.PlaceAncestor, Deploy: protocol.PlaceAncestor},
		{SHA: f1, Subject: "第二轮的改动（R-235）", Main: protocol.PlacePatch, Deploy: protocol.PlaceNo},
		{SHA: f2, Subject: "还没合入的改动（R-235）", Main: protocol.PlaceNo, Deploy: protocol.PlaceNo},
		{SHA: "deadbeef00", Main: protocol.PlaceUnknown, Deploy: protocol.PlaceUnknown},
	}
	for i, w := range want {
		if res.Commits[i] != w {
			t.Fatalf("第 %d 个提交：%+v，应为 %+v", i, res.Commits[i], w)
		}
	}
	// 部署版本是 cherry-pick 之后的 main：f1 补丁相同算已部署
	if res := checkGit(protocol.GitCheck{Dir: dir, SHAs: []string{f1}, Deploy: m2[:7]}); res.Commits[0].Deploy != protocol.PlacePatch {
		t.Fatalf("部署版本里有补丁相同的提交应算已部署：%+v", res.Commits)
	}
	// 部署版本不明（开发构建）：部署一律查不了
	if res := checkGit(protocol.GitCheck{Dir: dir, SHAs: []string{m1}, Deploy: ""}); res.Commits[0].Deploy != protocol.PlaceUnknown || res.Commits[0].Main != protocol.PlaceAncestor {
		t.Fatalf("部署版本不明时部署查不了：%+v", res.Commits)
	}
	// 有上游：main 比 origin/main 多 2 个提交
	run("config", "remote.origin.url", filepath.Join(dir, "nowhere"))
	run("config", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*")
	run("update-ref", "refs/remotes/origin/main", m1)
	run("config", "branch.main.remote", "origin")
	run("config", "branch.main.merge", "refs/heads/main")
	res = checkGit(protocol.GitCheck{Dir: dir})
	if res.NoRemote || res.Upstream != "origin/main" || res.Ahead != 2 {
		t.Fatalf("上游 / 未推送数不对：%+v", res)
	}
	// cherry-pick -x 摘过去时解决过冲突：补丁对不上，但说明里的来源记录认得出
	run("checkout", "-q", "line3")
	f3 := commitFile(t, dir, run, "c.txt", "c 分支上的写法\n", "和 main 冲突的改动（R-235）")
	run("checkout", "-q", "main")
	if err := os.WriteFile(filepath.Join(dir, "c.txt"), []byte("c 合入时手工合并后的写法\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "c.txt")
	run("commit", "-q", "-m", "和 main 冲突的改动（R-235）\n\n(cherry picked from commit "+f3+")")
	if res := checkGit(protocol.GitCheck{Dir: dir, SHAs: []string{f3, f2}}); res.Commits[0].Main != protocol.PlacePicked || res.Commits[1].Main != protocol.PlaceNo {
		t.Fatalf("cherry-pick -x 的来源记录应认作已合入：%+v", res.Commits)
	}
	// 不是仓库
	if res := checkGit(protocol.GitCheck{Dir: t.TempDir()}); res.Err == "" {
		t.Fatal("不是仓库应报错")
	}
}
