package daemon

import (
	"context"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/NekroAI/kitestring/internal/protocol"
)

// gitCache 缓存每个工作目录的 git 状态。分支 / remote / 根目录同步获取（很快），
// 脏文件数在后台刷新，避免大仓库的 git status 拖慢 hook。
type gitCache struct {
	mu      sync.Mutex
	entries map[string]*gitEntry
}

type gitEntry struct {
	info      protocol.Git
	fetched   time.Time
	dirtyAt   time.Time
	dirtyBusy bool
}

const gitTTL = 15 * time.Second

func newGitCache() *gitCache { return &gitCache{entries: map[string]*gitEntry{}} }

func (g *gitCache) get(cwd string) *protocol.Git {
	if cwd == "" {
		return nil
	}
	g.mu.Lock()
	e := g.entries[cwd]
	if e == nil {
		e = &gitEntry{}
		g.entries[cwd] = e
	}
	stale := time.Since(e.fetched) > gitTTL
	g.mu.Unlock()

	if stale {
		info := readGitBasics(cwd)
		g.mu.Lock()
		dirty := e.info.Dirty
		e.info = info
		e.info.Dirty = dirty
		e.fetched = time.Now()
		g.mu.Unlock()
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	if e.info.Available && !e.dirtyBusy && time.Since(e.dirtyAt) > gitTTL {
		e.dirtyBusy = true
		go g.refreshDirty(cwd, e)
	}
	out := e.info
	return &out
}

func (g *gitCache) refreshDirty(cwd string, e *gitEntry) {
	n := countDirty(cwd)
	g.mu.Lock()
	e.info.Dirty = n
	e.dirtyAt = time.Now()
	e.dirtyBusy = false
	g.mu.Unlock()
}

func gitCmd(cwd string, timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", cwd}, args...)...)
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

func readGitBasics(cwd string) protocol.Git {
	out, err := gitCmd(cwd, 2*time.Second, "rev-parse", "--show-toplevel", "--git-dir", "--git-common-dir")
	if err != nil {
		return protocol.Git{}
	}
	lines := strings.Split(out, "\n")
	if len(lines) < 3 {
		return protocol.Git{}
	}
	info := protocol.Git{Available: true, Root: lines[0]}
	gitDir, common := absUnder(cwd, lines[1]), absUnder(cwd, lines[2])
	info.Worktree = gitDir != common
	// symbolic-ref 在尚无提交的新仓库里也能取到分支名；分离 HEAD 时退回短哈希。
	if b, err := gitCmd(cwd, 2*time.Second, "symbolic-ref", "--short", "-q", "HEAD"); err == nil && b != "" {
		info.Branch = b
	} else if h, err := gitCmd(cwd, 2*time.Second, "rev-parse", "--short", "HEAD"); err == nil {
		info.Branch = "(" + h + ")"
	}
	info.Remote, _ = gitCmd(cwd, 2*time.Second, "config", "--get", "remote.origin.url")
	if info.Remote == "" {
		// 没有 origin 时取第一个 remote
		if names, _ := gitCmd(cwd, 2*time.Second, "remote"); names != "" {
			info.Remote, _ = gitCmd(cwd, 2*time.Second, "remote", "get-url", strings.Split(names, "\n")[0])
		}
	}
	return info
}

func absUnder(base, p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Clean(filepath.Join(base, p))
}

func countDirty(cwd string) int {
	out, err := gitCmd(cwd, 10*time.Second, "status", "--porcelain", "--untracked-files=normal")
	if err != nil || out == "" {
		return 0
	}
	return strings.Count(out, "\n") + 1
}

// 提交采集（R-156）：daemon 记住每个仓库上次看到的 HEAD，变了就读这段 git log，
// 挑出标题写了需求编号（R-x）或是 revert 的提交随事件上报，Hub 关联到需求。只读，不改仓库。
// daemon 刚启动、第一次见到某个仓库时回看最近 commitBackfill 条（Hub 按哈希去重）。
const (
	commitBackfill = 50
	commitMax      = 200
)

type commitScan struct {
	mu    sync.Mutex
	heads map[string]string // 仓库根目录 → 上次看到的 HEAD
}

func newCommitScan() *commitScan { return &commitScan{heads: map[string]string{}} }

var (
	reqRefRe  = regexp.MustCompile(`(?i)(^|[^A-Za-z0-9-])R-[0-9]+($|[^0-9A-Za-z])`)
	revertsRe = regexp.MustCompile(`(?i)This reverts commit ([0-9a-f]{7,40})`)
)

// scan 返回仓库 root 自上次以来的新提交（只含相关的）；HEAD 没变或读不到时返回 nil。
func (c *commitScan) scan(root string) []protocol.Commit {
	if root == "" {
		return nil
	}
	head, err := gitCmd(root, 2*time.Second, "rev-parse", "--verify", "-q", "HEAD")
	if err != nil || head == "" {
		return nil
	}
	c.mu.Lock()
	prev := c.heads[root]
	c.heads[root] = head
	c.mu.Unlock()
	if prev == head {
		return nil
	}
	return readCommits(root, prev, head)
}

func readCommits(root, prev, head string) []protocol.Commit {
	const format = "--format=%H%x1f%cI%x1f%s%x1f%b%x1e"
	var out string
	var err error
	if prev != "" {
		out, err = gitCmd(root, 3*time.Second, "log", "-n", strconv.Itoa(commitMax), format, prev+".."+head)
	}
	if prev == "" || err != nil { // 第一次见到，或旧 HEAD 已不存在（reset / gc 之后）
		out, err = gitCmd(root, 3*time.Second, "log", "-n", strconv.Itoa(commitBackfill), format, head)
		if err != nil {
			return nil
		}
	}
	return parseCommits(out)
}

// parseCommits 解析 git log 输出（字段以 0x1f 分隔、记录以 0x1e 分隔），按时间从旧到新返回相关提交。
func parseCommits(out string) []protocol.Commit {
	var cs []protocol.Commit
	for _, rec := range strings.Split(out, "\x1e") {
		f := strings.SplitN(strings.TrimLeft(rec, "\n"), "\x1f", 4)
		if len(f) < 3 || f[0] == "" {
			continue
		}
		c := protocol.Commit{SHA: f[0], Subject: strings.TrimSpace(f[2])}
		c.At, _ = time.Parse(time.RFC3339, f[1])
		if len(f) == 4 {
			if m := revertsRe.FindStringSubmatch(f[3]); m != nil {
				c.Reverts = strings.ToLower(m[1])
			}
		}
		if c.Reverts == "" && !reqRefRe.MatchString(c.Subject) {
			continue
		}
		if r := []rune(c.Subject); len(r) > 300 {
			c.Subject = string(r[:300]) + "…"
		}
		cs = append(cs, c)
	}
	for i, j := 0, len(cs)-1; i < j; i, j = i+1, j-1 {
		cs[i], cs[j] = cs[j], cs[i]
	}
	return cs
}
