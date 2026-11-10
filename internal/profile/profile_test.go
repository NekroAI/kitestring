package profile

import (
	"path/filepath"
	"runtime"
	"testing"
)

// root 是测试里的家目录：要是本机的绝对路径（Windows 上带盘符）。
func root() string {
	if runtime.GOOS == "windows" {
		return `C:\h`
	}
	return "/h"
}

func TestNameAndDefault(t *testing.T) {
	home := root()
	j := func(p ...string) string { return filepath.Join(append([]string{home}, p...)...) }
	if Name(j(".claude-x")) != "claude-x" || Name(j("work", "cfg")) != "cfg" {
		t.Fatal("显示名应去掉开头的点")
	}
	if !IsDefault(home, "") || !IsDefault(home, j(".claude")+string(filepath.Separator)) || IsDefault(home, j(".claude-x")) {
		t.Fatal("默认目录判断不对")
	}
	if got := ConfigFile(home, j(".claude")); got != j(".claude.json") {
		t.Fatal(got)
	}
	if got := ConfigFile(home, j(".claude-x")); got != j(".claude-x", ".claude.json") {
		t.Fatal(got)
	}
}

func TestExpand(t *testing.T) {
	if p, err := Expand(root(), "~/.claude-x"); err != nil || p != filepath.Join(root(), ".claude-x") {
		t.Fatal(p, err)
	}
	if _, err := Expand(root(), "rel/dir"); err == nil {
		t.Fatal("相对路径应报错")
	}
}

// Windows：~\ 也展开；路径比较不分大小写、/ 与 \ 等同。
func TestWindowsPaths(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("只在 Windows 上")
	}
	if p, err := Expand(`C:\h`, `~\.claude-x`); err != nil || p != `C:\h\.claude-x` {
		t.Fatal(p, err)
	}
	if !IsDefault(`C:\Users\Me`, `c:/users/me/.claude`) {
		t.Fatal("大小写、斜杠不同也是默认目录")
	}
}

func TestSaveLoadMergeResolve(t *testing.T) {
	home := t.TempDir()
	x := filepath.Join(home, ".claude-x")
	list := Merge(nil, Dir{Path: x, Launcher: "claude-x"})
	list = Merge(list, Dir{Path: x}) // 不带 launcher 重复接入不丢旧的
	if len(list) != 1 || list[0].Launcher != "claude-x" {
		t.Fatalf("%+v", list)
	}
	if err := Save(home, list); err != nil {
		t.Fatal(err)
	}
	got, _ := Load(home)
	if len(got) != 1 || got[0].Path != x {
		t.Fatalf("%+v", got)
	}
	if r := Resolve(home, x); r.Name != "claude-x" || r.Launcher != "claude-x" || r.Dir != x {
		t.Fatalf("%+v", r)
	}
	if r := Resolve(home, ""); r != (Info{}) {
		t.Fatalf("默认目录不应有来源：%+v", r)
	}
	if r := Resolve(home, filepath.Join(home, ".claude")); r != (Info{}) {
		t.Fatalf("默认目录不应有来源：%+v", r)
	}
	if r := Resolve(home, filepath.Join(home, ".other")); r.Name != "other" || r.Launcher != "" {
		t.Fatalf("%+v", r)
	}
	if err := Save(home, Remove(got, x)); err != nil {
		t.Fatal(err)
	}
	if got, _ := Load(home); len(got) != 0 {
		t.Fatal("清空后应删除记录")
	}
}
