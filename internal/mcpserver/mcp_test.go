package mcpserver

import (
	"encoding/json"
	"reflect"
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
