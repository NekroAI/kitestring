package hub

import (
	"fmt"
	"strings"

	"github.com/NekroAI/kitestring/internal/store"
)

// 后端生成、显示在网页和推送上的文字集中在这里（R-277），按 docs/copywriting.md 写；copy_test.go 用
// docs/copywriting-rules.json（与前端 copy.test.ts 共用）逐条检查。
//
// 不在这里的：
//   - 网页和会话都读的文字（纸鸢提醒、团队消息、决策答复）：正文以会话为读者，按 copywriting.md 1.3 只改标签、标题和第一句，
//     写在各自的文件里；通讯页靠 comms.go 的 sysHead / watchHead 拆它们的前缀，改句式要一起改。
//   - 只给会话看的回执、报错和指令（ks / MCP 返回）：写法自定，术语与第 5 节一致。
//   - HTTP 错误说明：就近写在接口里，句式见 copywriting.md 3.8。

// ---------- 状态标签（copywriting.md 第 5 节的状态集合） ----------

// deliveryLabel 是团队消息的送达状态（5.5，statusSets.delivery）。
var deliveryLabel = map[string]string{DeliveredSt: "已送达", PendingWakeSt: "待送达", OfflineSt: "无法送达"}

// availLabel 是团队会话列表上的状态（5.2，statusSets.roster）。
var availLabel = map[string]string{store.AvailStandby: "待命", store.AvailBusy: "运行中", store.AvailOnline: "空闲", store.AvailOffline: "已结束"}

// reqStatusZh 是需求状态（5.3，statusSets.req）；纸鸢提醒、时间线、离开报告共用。
var reqStatusZh = map[string]string{store.ReqDraft: "草稿", store.ReqReady: "待开始", store.ReqInProgress: "进行中", store.ReqBlocked: "受阻", store.ReqReview: "待验收", store.ReqDone: "已完成", store.ReqShelved: "已搁置"}

// riskZh 是决策的风险档。
var riskZh = map[string]string{store.RiskLow: "低风险", store.RiskMedium: "中风险", store.RiskHigh: "高风险"}

// ---------- 通讯页的类别（copywriting.md 3.7、5.8） ----------

// sysLabel 把纸鸢提醒前缀里写给会话的事件名换成网页上的类别名。
var sysLabel = map[string]string{"有人提决策": "新决策", "交验收": "待验收", "需求受阻": "受阻", "阶段交小结": "阶段小结", "用户改项目设置": "项目设置变更", "用户改需求状态": "需求状态变更"}

const (
	commsWatchLabel  = "订阅提醒" // 需求订阅提醒
	commsNoticeLabel = "通知"   // 其余纸鸢提醒
)

// ---------- 推送（copywriting.md 3.7：标题 = 事件名 + 空格 + 编号，正文 = 一条时间线句子） ----------

func pushDecisionTitle(code string) string { return "新决策 " + code }

func pushReviewTitle(code string) string { return "待验收 " + code }

// pushReviewBody 是需求待验收的推送正文；评审结果放在标题前。total 为 0 表示没有评审清单。
func pushReviewBody(title string, pass, total int) string {
	if total > 0 {
		return fmt.Sprintf("评审通过 %d / %d：%s", pass, total, title)
	}
	return title
}

// pushReviewUnfinishedBody 是评审超时、照常弹给用户验收时的推送正文。
func pushReviewUnfinishedBody(title string) string { return "评审未完成：" + title }

func pushStageTitle(code string, seq int) string {
	return fmt.Sprintf("待验收 %s 阶段 %d", code, seq)
}

func pushStageBody(plan, stage string) string { return plan + "：" + stage }

// pushPlanTitle 是会话提交计划、等用户批准的推送标题；revised 为按意见修改后重新提交。
func pushPlanTitle(code string, revised bool) string {
	if revised {
		return "待批准 " + code + "（已按意见修改）"
	}
	return "待批准 " + code
}

// pushSessionTitle 是会话等待授权 / 待回复的推送标题：事件名加会话所在项目。
func pushSessionTitle(event, project string) string { return event + "「" + project + "」" }

const (
	pushWaitApproval = "等待授权"
	pushWaitReply    = "待回复"
)

// pushSessionBody：「设备」上的工具，加会话给的一句说明。
func pushSessionBody(tool, device, detail string) string {
	head := tool
	if device != "" {
		head = "「" + device + "」上的 " + tool
	}
	if detail = strings.TrimSpace(detail); detail == "" {
		return head
	}
	return head + "：" + detail
}

func pushDeviceLostTitle(device string) string { return "设备离线「" + device + "」" }

const pushDeviceLostBody = "设备上有未结束的会话。"

// pushNeedDevsTitle 是用户批准开开发会话后推送的接入语标题。
func pushNeedDevsTitle(n int, project string) string {
	return fmt.Sprintf("已批准开启 %d 个开发会话「%s」", n, project)
}

func pushNeedDevsBody(phrase string) string {
	return "在项目目录打开 Claude Code，输入：" + phrase
}

const (
	pushTestTitle   = "测试推送"
	pushTestBody    = "收到这条通知，说明这台设备的系统推送已开启。"
	notifyTestTitle = "纸鸢测试通知"
	notifyTestBody  = "收到这条消息，说明通知渠道配置正确。"
)

// ---------- 离开报告（copywriting.md 5.8） ----------

// 各事件在离开期间的默认推送原因（报告里显示）。
var awayEventReason = map[string]string{
	"decision": "新决策",
	"review":   "待验收",
	"reply":    "会话待回复",
	"approval": "等待授权",
	"device":   "设备离线",
}

const (
	awaySettledTitle     = "可处理的事项已处理完"
	awaySettledPushTitle = "离开期间可处理的事项已处理完"
	awaySettledReason    = "阶段性报告"
	awayProxyQuiet       = "在授权范围内，主助理可以代答"
	awayNoGrant          = "主助理没有代答授权"
	awayPlanUserOnly     = "批准计划只由用户操作"
	awayNoAdvice         = "暂无主助理的验收建议"
)

// awayUnsure：决策提到了硬规则相关的词，主助理不代答。
func awayUnsure(word string) string { return "提到「" + word + "」，主助理不代答" }

// awayEscalated：主助理推给用户的决策，推送正文和原因。
func awayEscalatedBody(question, reason string) string {
	return brief(question, 80) + "（主助理：" + brief(reason, 60) + "）"
}

func awayEscalatedReason(reason string) string {
	return "主助理请你尽快查看：" + brief(reason, 60)
}

func awayAdvice(pass bool, reason string) string {
	if pass {
		return "主助理建议通过：" + reason
	}
	return "主助理建议退回：" + reason
}

// settledText 是「可处理的事项已处理完」的一句话汇总。
func settledText(c awayCounts) string {
	parts := []string{fmt.Sprintf("已交付 %d 条需求", c.Delivered), fmt.Sprintf("主助理处理 %d 项", c.Proxied+c.Released+c.Advised)}
	if c.Waiting > 0 {
		parts = append(parts, fmt.Sprintf("待处理 %d 项", c.Waiting))
	} else {
		parts = append(parts, "暂无待处理事项")
	}
	return strings.Join(parts, "，") + "。"
}

// 离开期间你在各设备上处理的事（报告里「你处理的」一行）。
const (
	awayPlanApproved = "批准了计划"
	awayPlanRejected = "退回了计划"
)

func awayStagePassed(seq int) string   { return fmt.Sprintf("通过了阶段 %d", seq) }
func awayStageReturned(seq int) string { return fmt.Sprintf("退回了阶段 %d", seq) }

// userStatusZh 是你改需求状态时报告里的一句话。
func userStatusZh(from, to string) string {
	switch {
	case from == store.ReqReview && to == store.ReqDone:
		return "验收通过"
	case from == store.ReqReview && to == store.ReqInProgress:
		return "退回修改"
	case from == store.ReqDraft && to == store.ReqReady:
		return "放行（草稿 → 待开始）"
	}
	return reqStatusZh[from] + " → " + reqStatusZh[to]
}

// 主助理没代答的原因（待处理决策卡上「主助理未代答」后面的一句）。
func holdHard(why string) string { return "硬规则：" + why }

func holdScope(risk string, unmarked bool, max string) string {
	if unmarked {
		return "超出授权：提问方未标风险档，按高风险计，授权上限为" + max
	}
	return "超出授权：" + risk + "，授权上限为" + max
}

func holdEnded(code string) string { return "授权窗口 " + code + " 已结束" }

// ---------- 会话时间线 ----------

// handoffHow：身份已由新会话接任（handed 为写了交接再接任）。
func handoffHow(by string, handed bool) string {
	if handed {
		return "已交接给「" + by + "」"
	}
	return "已由「" + by + "」接任"
}

// ---------- 分享链接的预览摘要（R-286）：IM 抓取 /s/<token> 时看到的标题和简介 ----------

const (
	// 通用宣传摘要：设了访问密码、失效、撤回、到期、链接无效时一律用它，不露分享名称和内容。
	ogPromoTitle = "纸鸢 Kitestring · 开源的多 agent 驾驶舱"
	ogPromoDesc  = "一块看板管住你每台机器上的 Claude Code 和 Codex 会话：看清每个会话在做什么，手机上就能回答会话等待的决策、指派下一条需求。AGPL-3.0 开源，github.com/NekroAI/kitestring"
	ogSiteName   = "纸鸢 Kitestring"
)

// ogShareDesc 是没设密码的有效分享的简介：几条需求、完成几条、最近更新时间（updated 为空表示没有更新记录）。
func ogShareDesc(reqs, done int, updated string) string {
	if reqs == 0 {
		return "暂无需求"
	}
	s := fmt.Sprintf("共 %d 条需求，已完成 %d 条", reqs, done)
	if updated != "" {
		s += "，最近更新 " + updated
	}
	return s
}

// copyTexts 列出本文件的全部文字（函数用占位参数取一次），给 copy_test.go 检查。
// ---------- 分享访客页（R-281）：免登录访客看到的提示 ----------

const (
	shareLinkInvalid    = "链接无效，请向分享者确认链接"
	shareLinkRevoked    = "这条链接已撤回"
	shareRevokedText    = "分享已撤回"
	shareExpiredText    = "分享已到期"
	sharePasswordNeeded = "请输入访问密码"
	sharePasswordWrong  = "访问密码错误"
	shareNotIncluded    = "不在这个分享里"
	shareReadOnly       = "分享只能查看，不能修改"
	// 访客留言（R-281 第 4 段）
	shareCommentEmpty   = "留言不能为空"
	shareCommentTarget  = "只能在需求、决策、证据或阶段上留言"
	shareCommentTooMany = "留言过于频繁，10 分钟后再试"
	shareCommentPickReq = "选择要放入的需求（这个阶段里的需求）"
)

// ---------- 凭据（R-295）：取用被拒的原因，记在取用记录里、显示在凭据详情的「结果」列 ----------

const (
	secretDenyPending    = "未填写值"
	secretDenyRevoked    = "授权已撤回"
	secretDenyExpired    = "授权已到期"
	secretDenyProject    = "项目不在授权范围内"
	secretDenyCommand    = "不在允许范围内"
	secretDenyDisk       = "未授权写入磁盘"
	secretDenyUnreadable = "值无法解密"
)

// shareCommentTooLong 是访客留言超出字数上限时的提示。
func shareCommentTooLong(n int) string { return fmt.Sprintf("留言最多 %d 字", n) }

// sharePasswordLocked 是访问密码错误次数过多后的提示。
func sharePasswordLocked(minutes int) string {
	return fmt.Sprintf("访问密码错误次数过多，请 %d 分钟后再试", minutes)
}

func copyTexts() map[string]string {
	out := map[string]string{}
	add := func(prefix string, m map[string]string) {
		for k, v := range m {
			out[prefix+"."+k] = v
		}
	}
	add("deliveryLabel", deliveryLabel)
	add("availLabel", availLabel)
	add("reqStatusZh", reqStatusZh)
	add("riskZh", riskZh)
	add("sysLabel", sysLabel)
	add("awayEventReason", awayEventReason)
	c := awayCounts{Delivered: 3, Proxied: 1, Released: 1, Advised: 1, Waiting: 2}
	for k, v := range map[string]string{
		"commsWatchLabel": commsWatchLabel, "commsNoticeLabel": commsNoticeLabel,
		"pushDecisionTitle": pushDecisionTitle("D-12"), "pushReviewTitle": pushReviewTitle("R-12"),
		"pushReviewBody": pushReviewBody("标题", 2, 3), "pushReviewBody0": pushReviewBody("标题", 0, 0),
		"pushReviewUnfinishedBody": pushReviewUnfinishedBody("标题"),
		"pushStageTitle":           pushStageTitle("P-3", 2), "pushStageBody": pushStageBody("计划", "阶段"),
		"pushPlanTitle": pushPlanTitle("P-3", false), "pushPlanTitleRevised": pushPlanTitle("P-3", true),
		"pushSessionTitle": pushSessionTitle(pushWaitApproval, "甲") + pushSessionTitle(pushWaitReply, "甲"),
		"pushSessionBody":  pushSessionBody("Claude Code", "甲", "摘要。"), "pushSessionBody0": pushSessionBody("Codex", "", ""),
		"pushDeviceLostTitle": pushDeviceLostTitle("甲"), "pushDeviceLostBody": pushDeviceLostBody,
		"pushNeedDevsTitle": pushNeedDevsTitle(2, "甲"), "pushNeedDevsBody": pushNeedDevsBody("甲"),
		"pushTestTitle": pushTestTitle, "pushTestBody": pushTestBody, "notifyTestTitle": notifyTestTitle, "notifyTestBody": notifyTestBody,
		"awaySettledTitle": awaySettledTitle, "awaySettledPushTitle": awaySettledPushTitle, "awaySettledReason": awaySettledReason,
		"awayProxyQuiet": awayProxyQuiet, "awayNoGrant": awayNoGrant, "awayPlanUserOnly": awayPlanUserOnly, "awayNoAdvice": awayNoAdvice,
		"awayUnsure": awayUnsure("删除"), "awayEscalatedBody": awayEscalatedBody("问题", "理由"), "awayEscalatedReason": awayEscalatedReason("理由"),
		"awayAdvicePass": awayAdvice(true, "理由"), "awayAdviceReturn": awayAdvice(false, "理由"),
		"settledText": settledText(c), "settledText0": settledText(awayCounts{}),
		"awayPlanApproved": awayPlanApproved, "awayPlanRejected": awayPlanRejected,
		"awayStagePassed": awayStagePassed(2), "awayStageReturned": awayStageReturned(2),
		"userStatusZhPass": userStatusZh(store.ReqReview, store.ReqDone), "userStatusZhBack": userStatusZh(store.ReqReview, store.ReqInProgress),
		"userStatusZhRelease": userStatusZh(store.ReqDraft, store.ReqReady), "userStatusZhOther": userStatusZh(store.ReqReady, store.ReqBlocked),
		"holdHard": holdHard("验收只由用户决策"), "holdScope": holdScope("高风险", false, "中风险"), "holdScopeUnmarked": holdScope("", true, "低风险"),
		"holdEnded": holdEnded("G-3"), "handoffHow": handoffHow("甲", false), "handoffHowHanded": handoffHow("甲", true),
		"shareLinkInvalid": shareLinkInvalid, "shareLinkRevoked": shareLinkRevoked, "shareRevokedText": shareRevokedText, "shareExpiredText": shareExpiredText,
		"sharePasswordNeeded": sharePasswordNeeded, "sharePasswordWrong": sharePasswordWrong, "shareNotIncluded": shareNotIncluded, "shareReadOnly": shareReadOnly, "sharePasswordLocked": sharePasswordLocked(15),
		"ogPromoTitle": ogPromoTitle, "ogPromoDesc": ogPromoDesc, "ogSiteName": ogSiteName,
		"ogShareDesc": ogShareDesc(5, 2, "11-06 10:30"), "ogShareDesc0": ogShareDesc(0, 0, ""),
		"secretDenyPending": secretDenyPending, "secretDenyRevoked": secretDenyRevoked, "secretDenyExpired": secretDenyExpired, "secretDenyProject": secretDenyProject,
		"secretDenyCommand": secretDenyCommand, "secretDenyDisk": secretDenyDisk, "secretDenyUnreadable": secretDenyUnreadable,
		"shareCommentEmpty": shareCommentEmpty, "shareCommentTarget": shareCommentTarget, "shareCommentTooMany": shareCommentTooMany, "shareCommentPickReq": shareCommentPickReq, "shareCommentTooLong": shareCommentTooLong(2000),
	} {
		out[k] = v
	}
	return out
}
