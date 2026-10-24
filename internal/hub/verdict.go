package hub

import (
	"fmt"
	"strings"

	"github.com/NekroAI/kitestring/internal/store"
)

// 否定类判断送给 Agent 的文字（R-176）：拒绝、退回、超时视为拒绝是三件不同的事，文字里必须写明是哪一种——
// Agent 不能把超时误当成用户明确拒绝，也不能把拒绝误当成退回修改。
//   - 拒绝（批准类决策、请求）：用户看过、不同意，不是要你改了再交；
//   - 退回（计划、阶段、需求验收）：用户要你按意见修改后重新提交；
//   - 超时视为拒绝（限时审批）：用户没有作答，按规则当作没批，不代表用户的意见。
// 界面上的「批准 / 拒绝」「通过 / 退回」用词见 web/src/i18n/zh.ts 的 VERB，这里的说法与它一致。

// rejectedText：用户拒绝了 what（例如「这个请求」）。附言可空。
func rejectedText(what, note string) string {
	if note = strings.TrimSpace(note); note != "" {
		return "拒绝（用户明确拒绝了" + what + "）。附言：" + note
	}
	return "拒绝（用户明确拒绝了" + what + "，没有附言）"
}

// returnedText：用户退回了 what，要求按意见修改。意见只有附图时 note 为空。
func returnedText(what, note string) string {
	if note = strings.TrimSpace(note); note != "" {
		return "用户退回了" + what + "，要求修改：" + note
	}
	return "用户退回了" + what + "，要求修改（意见见附图）"
}

// timeoutRejectText：限时审批到点没人作答，按规则视为拒绝（R-169 的限时审批用它）。
func timeoutRejectText(what string) string {
	return "超时未批，按规则视为拒绝" + what + "（用户没有作答，不是用户明确拒绝；需要的话可以换个时机再提）"
}

// approvalText：批准类决策（approval）送给 Agent 的答复。选「拒绝」的写成明确的拒绝，批准原样送达（与界面「批准」一致）。
// 单点时网页发来的是选项文字加附言（「拒绝」「拒绝；附言」），多点时是 pointsAnswer 拼的「拒绝：附言」。
func approvalText(d store.Decision) string {
	if d.RequestKind != "" {
		return requestText(d)
	}
	if !d.Approval {
		return d.Answer
	}
	a := strings.TrimSpace(d.Answer)
	rest, ok := strings.CutPrefix(a, "拒绝")
	if !ok {
		return d.Answer
	}
	rest = strings.TrimLeft(rest, "；;：:，, \n")
	return rejectedText("这个请求", rest)
}

// requestWhat：限时请求在文字里怎么称呼，例如「开 2 个 Dev 会话的推荐」。
func requestWhat(d store.Decision) string {
	if d.RequestKind == store.RequestDevs {
		return fmt.Sprintf("开 %d 个 Dev 会话的推荐", max(d.RequestCount, 1))
	}
	return "这个请求"
}

// requestApproved：用户批准了限时请求（答复以「批准」开头）。
func requestApproved(d store.Decision) bool {
	return !d.TimedOut && strings.HasPrefix(strings.TrimSpace(d.Answer), "批准")
}

// requestText：限时请求（R-169）送给提问方的答复——批准 / 拒绝 / 超时视为拒绝三种写法分明。
func requestText(d store.Decision) string {
	what := requestWhat(d)
	devs := d.RequestKind == store.RequestDevs
	if d.TimedOut {
		t := timeoutRejectText(what)
		if devs {
			t += "。不开 Dev 会话，继续用一次性子代理做。"
		}
		return t
	}
	a := strings.TrimSpace(d.Answer)
	if rest, ok := strings.CutPrefix(a, "拒绝"); ok {
		t := rejectedText(what, strings.TrimLeft(rest, "；;：:，, \n"))
		if devs {
			t += "。不开 Dev 会话，继续用一次性子代理做，不要再催用户开。"
		}
		return t
	}
	rest, ok := strings.CutPrefix(a, "批准")
	if !ok {
		return d.Answer
	}
	t := "批准（用户批准了" + what + "）"
	if devs {
		t += "。接入语已推送给用户；Dev 上线待命时你会收到通知，再用 req_assign 派活，在那之前照常用一次性子代理。"
	}
	if note := strings.TrimLeft(rest, "；;：:，, \n"); note != "" {
		t += "附言：" + note
	}
	return t
}
