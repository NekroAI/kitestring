package main

import (
	"strings"
	"testing"

	"github.com/NekroAI/kitestring/internal/store"
)

// R-235：两张反馈截图里的原文。第一张是真在问用户；第二张是报告排期，措辞「等你确认」。
const (
	feedbackAsk    = "主调度正在部署这两条，部署时 hub 会重启约 1 分钟。\n\n失败提示改中文这件事，要不要我记成一条需求草稿？我在后台继续等新决策和交验收。"
	feedbackReport = "主调度已经排好了：R-233 先做，R-234 紧跟其后。\n\nR-235 和 R-236 等你确认后再排。"
)

func TestAskQuoteFeedbackAsk(t *testing.T) {
	got := askQuote(store.AskTail(feedbackAsk))
	for _, want := range []string{"像在问用户的那句原话", "要不要我记成一条需求草稿？", "decision_raise", "改成陈述句"} {
		if !strings.Contains(got, want) {
			t.Errorf("提醒缺少 %q：%s", want, got)
		}
	}
	t.Log(got)
}

// 第二张：判断口径仍是 store.AskTail 的关键词（R-239 只收紧了给用户看的面板，没动提醒会话的判断），
// 「等你确认」会命中，提醒里引用原话，让会话改成陈述。
func TestAskQuoteFeedbackReport(t *testing.T) {
	got := askQuote(store.AskTail(feedbackReport))
	if !strings.Contains(got, "R-235 和 R-236 等你确认后再排") || !strings.Contains(got, "只是报告状态就改成陈述句") {
		t.Errorf("应引用原话并提示改成陈述：%s", got)
	}
	t.Log(got)
}

func TestAskQuoteEmpty(t *testing.T) {
	if askQuote("") != "" || askQuote("  \n") != "" {
		t.Error("没有原话时不输出")
	}
	if askQuote(store.AskTail("已提交 a1b2c3，继续待命。")) != "" {
		t.Error("纯汇报不引用")
	}
}
