package hub

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NekroAI/kitestring/internal/store"
)

// R-358：按项目名、git remote、项目 ID 找要记需求的项目；找不到、重名时报错并列出可选项目，隐藏的项目不算重名、不列出。
func TestTargetProject(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	h := New(st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx := context.Background()
	if _, err := st.DB().Exec(`INSERT INTO projects(id, name, remote, hidden, created_at) VALUES
		('p1', 'kite', 'github.com/o/kite', 0, ''),
		('p2', 'roll', 'github.com/a/roll', 0, ''),
		('p3', 'roll', 'github.com/b/roll', 0, ''),
		('p4', 'Solo', '', 0, ''),
		('p5', 'solo', 'github.com/x/stray', 1, ''),
		('p6', 'junk', '', 1, '')`); err != nil {
		t.Fatal(err)
	}
	ok := map[string]string{
		"kite":                         "p1",
		"KITE":                         "p1",
		"git@github.com:o/kite.git":    "p1",
		"https://github.com/a/roll":    "p2",
		"b/roll":                       "p3",
		"p3":                           "p3",
		"solo":                         "p4", // 重名里排除隐藏的，剩一个
		"  github.com/o/kite.git  ":    "p1",
		"ssh://git@github.com/b/roll/": "p3",
	}
	for q, want := range ok {
		p, err := h.targetProject(ctx, q)
		if err != nil || p.ID != want {
			t.Errorf("%q 应找到 %s：%v %+v", q, want, err, p)
		}
	}
	_, err = h.targetProject(ctx, "roll")
	if err == nil || !strings.Contains(err.Error(), "「roll」对应多个项目：「roll」（github.com/a/roll，ID p2）、「roll」（github.com/b/roll，ID p3）") {
		t.Errorf("重名应列出各项目的 remote 和 ID：%v", err)
	}
	_, err = h.targetProject(ctx, "nope")
	if err == nil || !strings.Contains(err.Error(), "找不到项目「nope」。可选项目：") || !strings.Contains(err.Error(), "「kite」（github.com/o/kite）") || strings.Contains(err.Error(), "junk") {
		t.Errorf("找不到应列出未隐藏的项目：%v", err)
	}
}
