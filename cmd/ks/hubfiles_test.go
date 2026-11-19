package main

import (
	"os"
	"path/filepath"
	"testing"
)

// R-353：--files-dir 优先于 KS_FILES_DIR，都不写用默认目录；~/ 展开，相对路径转绝对路径
func TestHubFilesDir(t *testing.T) {
	home, _ := os.UserHomeDir()
	abs := func(p string) string { a, _ := filepath.Abs(p); return a }
	for _, c := range []struct{ flag, env, want string }{
		{"", "", ""},
		{"/mnt/a", "/mnt/b", abs("/mnt/a")},
		{"", "/mnt/b", abs("/mnt/b")},
		{"~/x", "", filepath.Join(home, "x")},
		{"rel/dir", "", abs("rel/dir")},
	} {
		got, err := hubFilesDir(c.flag, c.env)
		if err != nil || got != c.want {
			t.Errorf("hubFilesDir(%q, %q) = %q, %v；应为 %q", c.flag, c.env, got, err, c.want)
		}
	}
}
