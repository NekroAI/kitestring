package main

import "testing"

func TestParseHandoffFile(t *testing.T) {
	f, err := parseHandoffFile([]byte(`{"doing":"a","下一步":"b","notes":"c"}`))
	if err != nil || f.Doing != "a" || f.Next != "b" || f.Notes != "c" {
		t.Fatalf("JSON 解析不对：%+v %v", f, err)
	}
	f, _ = parseHandoffFile([]byte("## 在做的\n- R-1\n- R-2\n## 下一步：\n交验收"))
	if f.Doing != "- R-1\n- R-2" || f.Next != "交验收" {
		t.Fatalf("markdown 解析不对：%+v", f)
	}
	if _, err := parseHandoffFile([]byte("{坏")); err == nil {
		t.Fatal("坏 JSON 应报错")
	}
}
