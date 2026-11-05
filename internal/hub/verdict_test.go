package hub

import (
	"strings"
	"testing"

	"github.com/NekroAI/kitestring/internal/store"
)

// R-176：拒绝、退回、超时视为拒绝送给 Agent 的文字要能分清是哪一种。
func TestVerdictTextsDistinct(t *testing.T) {
	rej := rejectedText("这个请求", "方向不对")
	ret := returnedText("这个计划", "先拆小")
	tmo := timeoutRejectText("这个请求")
	if rej != "拒绝（用户明确拒绝了这个请求）。附言：方向不对" {
		t.Fatalf("拒绝：%s", rej)
	}
	if ret != "用户退回了这个计划，要求修改：先拆小" {
		t.Fatalf("退回：%s", ret)
	}
	if !strings.HasPrefix(tmo, "超时未批，按规则视为拒绝") || !strings.Contains(tmo, "用户没有作答") || !strings.Contains(tmo, "不是用户明确拒绝") {
		t.Fatalf("超时：%s", tmo)
	}
	// 三种互不混淆：拒绝不说退回修改，退回不说拒绝，超时不说用户明确拒绝了
	if strings.Contains(rej, "退回") || strings.Contains(rej, "修改") || strings.Contains(rej, "超时") {
		t.Fatalf("拒绝混进了别的说法：%s", rej)
	}
	if strings.Contains(ret, "拒绝") || strings.Contains(ret, "超时") {
		t.Fatalf("退回混进了别的说法：%s", ret)
	}
	if strings.Contains(tmo, "用户明确拒绝了") || strings.Contains(tmo, "退回") {
		t.Fatalf("超时混进了别的说法：%s", tmo)
	}
	if got := rejectedText("这个请求", " "); got != "拒绝（用户明确拒绝了这个请求，没有附言）" {
		t.Fatalf("没有附言的拒绝：%s", got)
	}
	if got := returnedText("阶段 1", ""); got != "用户退回了阶段 1，要求修改（意见见附件）" {
		t.Fatalf("只有附图的退回：%s", got)
	}
}

func TestApprovalText(t *testing.T) {
	// 批准还是拒绝读结论字段 verdict（R-239），文字只用来取附言
	const ok, no = store.VerdictApproved, store.VerdictRejected
	cases := []struct {
		approval bool
		verdict  string
		answer   string
		want     string
	}{
		{true, ok, "批准", "批准"},
		{true, ok, "批准；按第二版做", "批准；按第二版做"},
		{true, no, "拒绝", "拒绝（用户明确拒绝了这个请求，没有附言）"},
		{true, no, "拒绝；先别做", "拒绝（用户明确拒绝了这个请求）。附言：先别做"},
		{true, no, "拒绝：方向不对", "拒绝（用户明确拒绝了这个请求）。附言：方向不对"},
		{true, no, "范围太大", "拒绝（用户明确拒绝了这个请求）。附言：范围太大"}, // 文字不以「拒绝」开头也按字段算拒绝
		{true, ok, "拒绝不了，就这么做", "拒绝不了，就这么做"},          // 字段是批准，文字开头像拒绝也不算
		{false, "", "拒绝", "拒绝"},                       // 选项类决策的选项就叫「拒绝」时原样送达
	}
	for _, c := range cases {
		if got := approvalText(store.Decision{Approval: c.approval, Verdict: c.verdict, Answer: c.answer}); got != c.want {
			t.Errorf("%v %q → %q，应为 %q", c.approval, c.answer, got, c.want)
		}
	}
	// 用户亲手作答的理由照旧接在后面
	if got := proxyText(store.Decision{Approval: true, Verdict: no, Answer: "拒绝；先别做", AnswerReason: "成本高"}); got != "拒绝（用户明确拒绝了这个请求）。附言：先别做\n用户的理由：成本高" {
		t.Fatalf("带理由：%q", got)
	}
}

// 限时请求（R-169）：批准 / 拒绝 / 超时视为拒绝三种答复文字分明，参谋代答的包装也照用。
func TestRequestText(t *testing.T) {
	base := store.Decision{Approval: true, RequestKind: store.RequestDevs, RequestCount: 2, Options: []string{"批准", "拒绝"}}
	ok := base
	ok.Answer, ok.Verdict = "批准；先做 R-3", store.VerdictApproved
	no := base
	no.Answer, no.Verdict = "拒绝；子代理够了", store.VerdictRejected
	late := base
	late.TimedOut = true
	late.Answer, late.Verdict = timeoutRejectText(requestWhat(late)), store.VerdictRejected
	for _, c := range []struct {
		d          store.Decision
		want, deny []string
	}{
		{ok, []string{"批准（用户批准了开启 2 个开发会话的推荐）", "附言：先做 R-3", "req_assign"}, []string{"拒绝", "超时"}},
		{no, []string{"拒绝（用户明确拒绝了开启 2 个开发会话的推荐）", "子代理够了", "继续用一次性子任务"}, []string{"超时", "批准（"}},
		{late, []string{"超时未批，按规则视为拒绝开启 2 个开发会话的推荐", "不是用户明确拒绝", "继续用一次性子任务"}, []string{"明确拒绝了", "批准（"}},
	} {
		got := proxyText(c.d)
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("应包含 %q：%s", w, got)
			}
		}
		for _, w := range c.deny {
			if strings.Contains(got, w) {
				t.Errorf("不应包含 %q：%s", w, got)
			}
		}
		if requestApproved(c.d) != (c.d.Answer == ok.Answer) {
			t.Errorf("requestApproved 判断错：%+v", c.d)
		}
	}
}
