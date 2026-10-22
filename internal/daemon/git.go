package daemon

import (
	"context"
	"os/exec"
	"path/filepath"
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
	out, err := gitCmd(cwd, 2*time.Second, "rev-parse", "--show-toplevel", "--abbrev-ref", "HEAD", "--git-dir", "--git-common-dir")
	if err != nil {
		return protocol.Git{}
	}
	lines := strings.Split(out, "\n")
	if len(lines) < 4 {
		return protocol.Git{}
	}
	info := protocol.Git{Available: true, Root: lines[0], Branch: lines[1]}
	gitDir, common := absUnder(cwd, lines[2]), absUnder(cwd, lines[3])
	info.Worktree = gitDir != common
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
