package mcpserver

import (
	"encoding/json"
	"strings"
	"testing"
)

// R-281：放入需求的访客留言，req_get 默认输出写明来源（来自谁、经哪个分享、由用户放入、针对什么），带原话和附言。
func TestReqGetGuestNote(t *testing.T) {
	raw := `{"code":"R-12","title":"分享","status":"in_progress","feedback":[
		{"code":"D-30","kind":"note","answer":"撤回链接后访客页还能看到内容吗？","guest":{"name":"小王","share":"分享功能进展","target_kind":"dec","target_code":"D-20","original":"这个好像不太对吧","addend":"先确认一下"}},
		{"code":"D-31","kind":"note","answer":"我自己的留言"}]}`
	var r reqView
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		t.Fatal(err)
	}
	s := r.String()
	for _, want := range []string{
		"D-30 访客留言：来自「小王」（经分享链接「分享功能进展」），由用户放入，针对 D-20：撤回链接后访客页还能看到内容吗？",
		"访客原话：这个好像不太对吧", "用户附言：先确认一下", "D-31 留言：我自己的留言",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("缺少 %q：\n%s", want, s)
		}
	}
}
