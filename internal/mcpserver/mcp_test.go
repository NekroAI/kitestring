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
