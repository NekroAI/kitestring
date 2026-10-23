package hub

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/protocol"
	"github.com/NekroAI/kitestring/internal/store"
)

// GitHub CI 只读查询（R-156）：没有 token 时不查；有 token 时只用 GET check-runs，汇总为进行中 / 通过 / 未通过；
// 查不到（例如提交还没推上去）时不显示、不报错，并节流不反复查。
func TestGitHubCIReadOnly(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	h := New(st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx := context.Background()
	if _, err := st.DB().Exec(`INSERT INTO projects(id, name, remote, created_at) VALUES('p1', 'r', 'github.com/o/r', '2026-10-24T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	okSHA, failSHA, missSHA := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40)
	if _, err := st.RecordCommits(ctx, "p1", "", []protocol.Commit{{SHA: okSHA, Subject: "feat: x（R-1）"}, {SHA: failSHA, Subject: "fix: y（R-1）"}, {SHA: missSHA, Subject: "fix: z（R-1）"}}); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("只能用带 token 的 GET：%s %s", r.Method, r.Header.Get("Authorization"))
		}
		switch r.URL.Path {
		case "/repos/o/r/commits/" + okSHA + "/check-runs":
			_, _ = io.WriteString(w, `{"check_runs":[{"status":"completed","conclusion":"success"},{"status":"completed","conclusion":"skipped"}]}`)
		case "/repos/o/r/commits/" + failSHA + "/check-runs":
			_, _ = io.WriteString(w, `{"check_runs":[{"status":"completed","conclusion":"success"},{"status":"completed","conclusion":"failure","html_url":"https://github.com/o/r/runs/9"}]}`)
		default:
			http.Error(w, `{"message":"No commit found"}`, http.StatusUnprocessableEntity)
		}
	}))
	defer srv.Close()
	old := githubAPI
	githubAPI = srv.URL
	defer func() { githubAPI = old }()

	cs := func() map[string]store.ChainCommit {
		rc := mustCommits(t, st, ctx)
		m := map[string]store.ChainCommit{}
		for _, c := range rc.Commits {
			m[c.Link] = c
		}
		return m
	}
	// 没有 token：不查
	t.Setenv("KS_GITHUB_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	h.refreshCI("p1", mustCommits(t, st, ctx).Commits)
	time.Sleep(100 * time.Millisecond)
	if calls.Load() != 0 {
		t.Fatal("没有 token 时不应访问 GitHub")
	}
	t.Setenv("GITHUB_TOKEN", "tok")
	h.refreshCI("p1", mustCommits(t, st, ctx).Commits)
	deadline := time.Now().Add(3 * time.Second)
	for calls.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	m := cs()
	if c := m[okSHA].CI; c == nil || c.State != "success" || c.Source != "github" {
		t.Fatalf("全部通过 / 跳过应算通过：%+v", c)
	}
	if c := m[failSHA].CI; c == nil || c.State != "failure" || c.URL != "https://github.com/o/r/runs/9" {
		t.Fatalf("有失败应算未通过并给出链接：%+v", c)
	}
	if m[missSHA].CI != nil {
		t.Fatalf("GitHub 上查不到时不显示：%+v", m[missSHA].CI)
	}
	// 有结论的不再查，查不到的 2 分钟内不再查
	n := calls.Load()
	h.refreshCI("p1", mustCommits(t, st, ctx).Commits)
	time.Sleep(150 * time.Millisecond)
	if calls.Load() != n {
		t.Fatalf("不应反复查：%d → %d", n, calls.Load())
	}
}

func mustCommits(t *testing.T, st *store.Store, ctx context.Context) store.ReqCommits {
	t.Helper()
	// 直接按项目读：需求 R-1 不存在时用一条临时需求
	var rc store.ReqCommits
	r, err := st.GetRequirement(ctx, "R-1")
	if err != nil {
		if r, err = st.CreateRequirement(ctx, store.Requirement{ProjectID: "p1", Title: "x", Status: store.ReqInProgress}); err != nil {
			t.Fatal(err)
		}
	}
	if rc, err = st.GetReqCommits(ctx, r.Code); err != nil {
		t.Fatal(err)
	}
	return rc
}
