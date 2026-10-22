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
