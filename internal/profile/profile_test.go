package profile

import (
	"path/filepath"
	"testing"
)

func TestNameAndDefault(t *testing.T) {
	home := "/h"
	if Name("/h/.claude-x") != "claude-x" || Name("/h/work/cfg") != "cfg" {
		t.Fatal("显示名应去掉开头的点")
	}
	if !IsDefault(home, "") || !IsDefault(home, "/h/.claude/") || IsDefault(home, "/h/.claude-x") {
		t.Fatal("默认目录判断不对")
	}
	if got := ConfigFile(home, "/h/.claude"); got != "/h/.claude.json" {
		t.Fatal(got)
	}
	if got := ConfigFile(home, "/h/.claude-x"); got != "/h/.claude-x/.claude.json" {
		t.Fatal(got)
	}
}

func TestExpand(t *testing.T) {
	if p, err := Expand("/h", "~/.claude-x"); err != nil || p != "/h/.claude-x" {
		t.Fatal(p, err)
	}
	if _, err := Expand("/h", "rel/dir"); err == nil {
		t.Fatal("相对路径应报错")
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
