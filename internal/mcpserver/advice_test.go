package mcpserver

import (
	"encoding/json"
	"strings"
	"testing"
)

// R-242：req_get 带当前的主助理验收建议（标明不是用户原话），退回的修改意见下面带当时的建议原文。
func TestReqViewShowsAdvice(t *testing.T) {
	var r reqView
	raw := `{"code":"R-9","title":"登录页","status":"review","priority":"P1",
		"advice":{"verdict":"return","reason":"第 2 条没达标","by_title":"主助理"},
		"feedback":[{"code":"D-3","kind":"feedback","answer":"（用户没写附言，按主助理建议退回）","context":"主助理的验收建议（不是用户原话）：建议退回，理由：第 2 条没达标"}]}`
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		t.Fatal(err)
	}
	out := r.String()
	for _, want := range []string{"主助理的验收建议（参考，不是用户原话；验收通过 / 退回只有用户能定）：建议退回，理由：第 2 条没达标", "D-3 修改意见：（用户没写附言，按主助理建议退回）\n      主助理的验收建议（不是用户原话）：建议退回，理由：第 2 条没达标"} {
		if !strings.Contains(out, want) {
			t.Fatalf("req_get 缺少 %q：\n%s", want, out)
		}
	}
}
