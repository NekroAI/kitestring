package main

import (
	"flag"
	"testing"
)

func TestParseAnywhere(t *testing.T) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	kind := fs.String("kind", "manual", "")
	link := fs.String("link", "", "")
	if err := parseAnywhere(fs, []string{"R-3", "测试通过", "--kind", "test", "--link", "abc"}); err != nil {
		t.Fatal(err)
	}
	if *kind != "test" || *link != "abc" || fs.NArg() != 2 || fs.Arg(0) != "R-3" || fs.Arg(1) != "测试通过" {
		t.Fatalf("解析错误：kind=%s link=%s args=%v", *kind, *link, fs.Args())
	}
}

func TestEditBody(t *testing.T) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	fs.String("title", "", "")
	fs.String("detail", "", "")
	fs.String("acceptance", "", "")
	fs.String("type", "", "")
	fs.String("priority", "", "")
	if err := parseAnywhere(fs, []string{"R-3", "--title", "新标题", "--detail", "", "--type", "design"}); err != nil {
		t.Fatal(err)
	}
	b := editBody(fs, "claude:x", "定稿", "")
	if b["title"] != "新标题" || b["detail"] != "" || b["kind"] != "design" || b["reason"] != "定稿" || b["session_key"] != "claude:x" {
		t.Fatalf("请求体不对：%v", b)
	}
	if _, ok := b["acceptance"]; ok {
		t.Fatalf("没给的字段不应带上：%v", b)
	}
	if _, ok := b["priority"]; ok {
		t.Fatalf("没给的字段不应带上：%v", b)
	}
	if _, ok := b["approval"]; ok {
		t.Fatalf("没给的字段不应带上：%v", b)
	}
}
