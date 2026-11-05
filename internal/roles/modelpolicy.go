package roles

import "strings"

// 子任务模型策略（R-228）：项目设置里的三档，写进主调度、主助理、开发的身份提示词。
// 只描述按任务难度的倾向，不写死任何模型名、不假设用的是哪家工具——各工具怎么对应由会话自己判断。
const (
	PolicyEconomy  = "economy"  // 经济
	PolicyBalanced = "balanced" // 均衡（默认）
	PolicyQuality  = "quality"  // 质量优先
)

// Policies 是全部档位，按从省到贵的顺序。
var Policies = []string{PolicyEconomy, PolicyBalanced, PolicyQuality}

var policyNames = map[string]string{PolicyEconomy: "经济", PolicyBalanced: "均衡", PolicyQuality: "质量优先"}

var policyLean = map[string]string{
	PolicyEconomy:  "尽量用便宜、快的小模型；只有新机制、跨模块或返工过的活才用最强的模型。",
	PolicyBalanced: "机械活（搜索、读文件、照方案改样式文案、单处 bug）用便宜、快的小模型；设计取舍和复杂实现用最强的模型。",
	PolicyQuality:  "实现和审查都用最强的模型；只有纯搜索、读文件这类不需要判断的活才用便宜的。",
}

// NormPolicy 规整档位：空或不认识的按均衡。
func NormPolicy(p string) string {
	if _, ok := policyNames[strings.TrimSpace(p)]; ok {
		return strings.TrimSpace(p)
	}
	return PolicyBalanced
}

// ValidPolicy：空串（沿用默认）或三档之一。
func ValidPolicy(p string) bool {
	_, ok := policyNames[p]
	return p == "" || ok
}

// PolicyName 返回档位中文名。
func PolicyName(p string) string { return policyNames[NormPolicy(p)] }

// usesPolicy：会派子任务的身份才带这一项；顾问、评审不派。
func usesPolicy(role string) bool { return role == PM || role == Aide || role == Dev }

// PolicyNote 是写进身份提示词的「子任务模型策略」一段；身份不派子任务时为空。
func PolicyNote(role, policy string) string {
	if !usesPolicy(role) {
		return ""
	}
	p := NormPolicy(policy)
	var b strings.Builder
	b.WriteString("[Kitestring] 子任务模型策略（项目设置）：本项目设为「" + policyNames[p] + "」。派子任务（子代理）时按任务难度选模型，倾向——" + policyLean[p] + "\n")
	b.WriteString("纸鸢只给倾向，不指定模型：你所用的工具有哪些模型档位，就在那里对应出相对的「便宜小模型」和「最强模型」，不照抄别家的模型名。")
	b.WriteString("派子任务时写明用了哪档、一句理由：在任务说明第一行写「模型：<档位> — <一句理由>」，事后好对账（泳道上子任务行会按用量记录显示实际用的模型，名字里不必再写档位）。")
	b.WriteString("你的工具没有子代理能力，或派子任务时不能指定模型，这一项忽略。\n")
	return b.String()
}
