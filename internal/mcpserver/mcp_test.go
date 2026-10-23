package mcpserver

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestImageIDs(t *testing.T) {
	var r map[string]any
	_ = json.Unmarshal([]byte(`{
		"files":[{"id":"desc"}],
		"feedback":[{"answer_files":[{"id":"fb1"}]}],
		"evidence":[{"voided":true,"files":[{"id":"old"}]},{"voided":false,"files":[{"id":"shot","mime":"image/png"},{"id":"rec","mime":"video/webm"}]}]
	}`), &r)
	if got := imageIDs(r); !reflect.DeepEqual(got, []string{"desc", "fb1", "shot"}) {
		t.Fatalf("应包含描述、修改意见、未作废证据的附图（不含录屏），得到 %v", got)
	}
}

func TestPlanViewIsCompact(t *testing.T) {
	var p planView
	raw := `{"code":"P-2","title":"支付回调重构","goal":"回调幂等","status":"active","paused":true,"stages":[
		{"seq":1,"title":"梳理","status":"passed","summary":"列出 4 条回调路径","requirements":[{"code":"R-7","title":"列出回调路径","status":"done","detail":"很长的说明不应出现"}]},
		{"seq":2,"title":"改造","status":"active","requirements":[{"code":"R-8","title":"幂等处理","status":"in_progress"}]}]}`
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatal(err)
	}
	want := "P-2「支付回调重构」进行中（暂停：已通过阶段里有需求被重开）\n目标：回调幂等\n" +
		"阶段 1「梳理」已通过\n  - R-7 列出回调路径 [done]\n  小结：列出 4 条回调路径\n" +
		"阶段 2「改造」进行中\n  - R-8 幂等处理 [in_progress]\n"
	if got := p.String(); got != want {
		t.Fatalf("计划精简视图不符：\n%s", got)
	}
}

func TestReqViewIsCompact(t *testing.T) {
	var r reqView
	raw := `{"code":"R-9","title":"登录页","status":"review","priority":"P1","kind":"design","plan_code":"P-1","stage_seq":2,"stage_title":"网页",
		"detail":"记住我","acceptance":"三端可用","sessions":["claude:a"],"files":[{"id":"desc","mime":"image/png"}],
		"evidence":[{"kind":"test","summary":"旧的","voided":true},{"kind":"screenshot","summary":"截图","files":[{"id":"shot","mime":"image/png"}]}],
		"feedback":[{"code":"D-3","kind":"feedback","answer":"按钮太小","answer_files":[{"id":"fb","mime":"image/png"}]},{"code":"D-4","kind":"queue","answer":"P1"},{"code":"D-5","kind":"decision","question":"用哪个？","answer":""}],
		"created_at":"2026-10-23T00:00:00Z","id":"01X","next_actor":"user"}`
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		t.Fatal(err)
	}
	if got := r.line(); got != "R-9「登录页」待验收 · P1 · 设计 · P-1 阶段 2" {
		t.Fatalf("一行概要不符：%s", got)
	}
	out := r.String()
	for _, want := range []string{"说明：记住我", "验收标准：三端可用", "证据（1 条）", "[screenshot] 截图（1 个附件）", "D-3 修改意见：按钮太小（附图 1 张）", "D-5 决策：用哪个？ → （待你答复）"} {
		if !strings.Contains(out, want) {
			t.Fatalf("精简视图缺少 %q：\n%s", want, out)
		}
	}
	for _, no := range []string{"旧的", "D-4", "01X", "created_at"} {
		if strings.Contains(out, no) {
			t.Fatalf("精简视图不应包含 %q：\n%s", no, out)
		}
	}
	if got := r.images(); !reflect.DeepEqual(got, []string{"desc", "fb"}) {
		t.Fatalf("默认附图应只含描述与用户意见里的图，得到 %v", got)
	}
}
