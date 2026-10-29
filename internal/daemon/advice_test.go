package daemon

import (
	"strings"
	"testing"

	"github.com/NekroAI/kitestring/internal/protocol"
)

// R-242：退回通知里，用户附言和主助理的验收建议分行写，建议标明出处。
func TestWriteItemFeedbackAdvice(t *testing.T) {
	d := &Daemon{}
	var b strings.Builder
	d.writeItem(&b, protocol.Decision{Kind: "feedback", ReqCode: "R-9", ReqTitle: "登录页", Answer: "（用户没写附言，按主助理建议退回）", Advice: "主助理的验收建议（不是用户原话）：建议退回，理由：第 2 条没达标"})
	want := "- R-9「登录页」修改意见：（用户没写附言，按主助理建议退回）\n  主助理的验收建议（不是用户原话）：建议退回，理由：第 2 条没达标\n"
	if b.String() != want {
		t.Fatalf("退回通知不符：\n%q\n想要：\n%q", b.String(), want)
	}
	b.Reset()
	d.writeItem(&b, protocol.Decision{Kind: "feedback", ReqCode: "R-9", ReqTitle: "登录页", Answer: "按钮太小"})
	if b.String() != "- R-9「登录页」修改意见：按钮太小\n" {
		t.Fatalf("没有建议时照旧：%q", b.String())
	}
}
