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
	return "用户退回了" + what + "，要求修改（意见见附件）"
}

// timeoutRejectText：限时审批到点没人作答，按规则视为拒绝（R-169 的限时审批用它）。
func timeoutRejectText(what string) string {
	return "超时未批，按规则视为拒绝" + what + "（用户没有作答，不是用户明确拒绝；需要的话可以换个时机再提）"
}

// answerVerdict：作答请求里批准类决策的结论（R-239）。明确给了就用；多点决策整体拒绝（reject）是拒绝；
// 都没有时返回空，由 store 按所选选项文字记一次（旧客户端兼容）。
func answerVerdict(verdict string, reject bool) string {
	switch {
	case verdict == store.VerdictApproved || verdict == store.VerdictRejected:
		return verdict
	case reject:
		return store.VerdictRejected
	}
	return ""
}

// answerNote：答复里所选选项（「批准」「拒绝」）后面的附言。批准还是拒绝看 d.Verdict（R-239），这里只取附言。
// 单点时网页发来的是选项文字加附言（「拒绝」「拒绝；附言」），多点时是 pointsAnswer 拼的「拒绝：附言」。
func answerNote(answer, label string) string {
	a := strings.TrimSpace(answer)
	if rest, ok := strings.CutPrefix(a, label); ok {
		return strings.TrimLeft(rest, "；;：:，, \n")
	}
	return a
}

// approvalText：批准类决策（approval）送给 Agent 的答复。结论读 d.Verdict：拒绝的写成明确的拒绝，批准原样送达（与界面「批准」一致）。
func approvalText(d store.Decision) string {
	if d.RequestKind != "" {
		return requestText(d)
	}
	if !d.Approval || d.Verdict != store.VerdictRejected {
		return d.Answer
	}
	return rejectedText("这个请求", answerNote(d.Answer, "拒绝"))
}

// requestWhat：限时请求在文字里怎么称呼，例如「开 2 个 Dev 会话的推荐」。
func requestWhat(d store.Decision) string {
	if d.RequestKind == store.RequestDevs {
		return fmt.Sprintf("开 %d 个 Dev 会话的推荐", max(d.RequestCount, 1))
	}
	if d.RequestKind == store.RequestStatus {
		return statusRequestWhat(d)
	}
	return "这个请求"
}

// requestApproved：用户批准了请求（读结论字段 verdict，R-239）。
func requestApproved(d store.Decision) bool {
	return !d.TimedOut && !d.Voided && d.Verdict == store.VerdictApproved
}

// requestText：限时请求（R-169）送给提问方的答复——批准 / 拒绝 / 超时视为拒绝三种写法分明。
func requestText(d store.Decision) string {
	if d.RequestKind == store.RequestStatus {
		return statusRequestText(d)
	}
	what := requestWhat(d)
	devs := d.RequestKind == store.RequestDevs
	if d.TimedOut {
		t := timeoutRejectText(what)
		if devs {
			t += "。不开 Dev 会话，继续用一次性子代理做。"
		}
		return t
	}
	switch d.Verdict {
	case store.VerdictRejected:
		t := rejectedText(what, answerNote(d.Answer, "拒绝"))
		if devs {
			t += "。不开 Dev 会话，继续用一次性子代理做，不要再催用户开。"
		}
		return t
	case store.VerdictApproved:
	default:
		return d.Answer
	}
	t := "批准（用户批准了" + what + "）"
	if devs {
		t += "。接入语已推送给用户；Dev 上线待命时你会收到通知，再用 req_assign 派活，在那之前照常用一次性子代理。"
	}
	if note := answerNote(d.Answer, "批准"); note != "" {
		t += "附言：" + note
	}
	return t
}
