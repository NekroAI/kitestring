package daemon

import (
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/NekroAI/kitestring/internal/protocol"
)

// 合入 / 部署 / 推送判定（R-245）：Hub 把需求最近一轮关联的提交发过来，daemon 在本机仓库里只读地查：
//   - 已合入 main：提交是 main 的祖先，或 main 上有补丁相同的提交（cherry-pick / rebase 过去的，按 git cherry 的 patch-id 比），
//     或 main 上有用 cherry-pick -x 摘它的提交（解决过冲突、补丁对不上时，说明里留下的来源记录，D-1043）
//   - 已部署：同样的判法，对照正在运行的 ks 的构建提交
//   - 没推送：main 比它的上游多几个提交；没有上游时标「没有远端」
const gitCheckMax = 200 // 一次最多查这么多个提交

func checkGit(q protocol.GitCheck) protocol.GitResult {
	res := protocol.GitResult{ReqID: q.ReqID}
	if q.Dir == "" {
		res.Err = "不知道仓库在哪"
		return res
	}
	if _, err := gitCmd(q.Dir, 2*time.Second, "rev-parse", "--git-dir"); err != nil {
		res.Err = "这里不是 git 仓库：" + q.Dir
		return res
	}
	res.Main = mainBranch(q.Dir)
	if res.Main != "" {
		up, err := gitCmd(q.Dir, 2*time.Second, "rev-parse", "--abbrev-ref", "--symbolic-full-name", res.Main+"@{upstream}")
		if err != nil || up == "" {
			res.NoRemote = true
		} else {
			res.Upstream = up
			if n, err := gitCmd(q.Dir, 3*time.Second, "rev-list", "--count", up+".."+res.Main); err == nil {
				res.Ahead, _ = strconv.Atoi(n)
			}
		}
	}
	deploy := ""
	if q.Deploy != "" {
		deploy = resolveCommit(q.Dir, q.Deploy)
	}
	shas := q.SHAs
	if len(shas) > gitCheckMax {
		shas = shas[:gitCheckMax]
	}
	res.Commits = make([]protocol.CommitPlace, len(shas))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 6) // 并行查，几十个提交也在一两秒内查完
	for i, sha := range shas {
		wg.Add(1)
		go func(i int, sha string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			p := protocol.CommitPlace{SHA: sha, Main: protocol.PlaceUnknown, Deploy: protocol.PlaceUnknown}
			if full := resolveCommit(q.Dir, sha); full != "" {
				p.Subject, _ = gitCmd(q.Dir, 2*time.Second, "log", "-1", "--format=%s", full)
				if res.Main != "" {
					p.Main = commitOn(q.Dir, full, "refs/heads/"+res.Main)
				}
				if deploy != "" {
					p.Deploy = commitOn(q.Dir, full, deploy)
				}
			}
			res.Commits[i] = p
		}(i, sha)
	}
	wg.Wait()
	return res
}

// mainBranch 取主线分支名：main，没有就 master；都没有时为空。
func mainBranch(dir string) string {
	for _, b := range []string{"main", "master"} {
		if _, err := gitCmd(dir, 2*time.Second, "rev-parse", "--verify", "-q", "refs/heads/"+b); err == nil {
			return b
		}
	}
	return ""
}

// resolveCommit 把（短）哈希解析成完整哈希；本机仓库里没有这个提交时为空。
func resolveCommit(dir, sha string) string {
	if strings.HasPrefix(sha, "-") {
		return ""
	}
	full, err := gitCmd(dir, 2*time.Second, "rev-parse", "--verify", "-q", sha+"^{commit}")
	if err != nil {
		return ""
	}
	return full
}

// commitOn 判断提交 sha 是否已在 target 上：是祖先，或 target 上有补丁相同的提交。
func commitOn(dir, sha, target string) string {
	_, err := gitCmd(dir, 3*time.Second, "merge-base", "--is-ancestor", sha, target)
	if err == nil {
		return protocol.PlaceAncestor
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 1 {
		return protocol.PlaceUnknown
	}
	// cherry <target> <sha> <sha^>：只列出 sha 这一个提交，前面是「-」表示 target 上已有补丁相同的提交
	if out, err := gitCmd(dir, 10*time.Second, "cherry", target, sha, sha+"^"); err == nil { // 根提交没有父提交时跳过
		same := true
		f := strings.Fields(out)
		for i := 0; i+1 < len(f); i += 2 {
			same = same && f[i] == "-"
		}
		if same {
			return protocol.PlacePatch
		}
	}
	// cherry-pick -x 留下的来源记录：摘的时候解决过冲突、补丁对不上，说明里仍写着「(cherry picked from commit <sha>)」
	if out, err := gitCmd(dir, 10*time.Second, "log", target, "--format=%H", "-n", "1", "-F", "--grep=(cherry picked from commit "+sha+")"); err == nil && out != "" {
		return protocol.PlacePicked
	}
	return protocol.PlaceNo
}
