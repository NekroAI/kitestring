package hub

import (
	"strings"

	"github.com/NekroAI/kitestring/internal/store"
)

// 硬规则（R-210）：这几类决策只由用户决策，主助理在授权窗口内也不能代答，接口层拒绝。
//   - 自动判定：批准方案 / 计划（approval）、选项或要决策的点带效果图（设计选型）。
//   - 提问方标注：decision_raise 的 hard 字段（下面 hardKinds），标了就不能代答、风险档按 high。
//   - 代答方核对：decision_answer 必须带 hard_check = none，表示主助理核对过不属于这几类；填了某一类就拒绝。
//   - 兜底（不拦截提问、只降级）：没标 hard，但问题或选项里出现 unsureWords 里的词，算「拿不准」——
//     主助理不能代答，离开期间照推（勿扰也推）。词表只在这里维护。

// hardKinds 是硬规则类别 → 中文名（拒绝时的说明里用）。
var hardKinds = map[string]string{
	"product":      "产品方向",
	"design":       "设计选型",
	"semantics":    "语义 / 接口变更",
	"permission":   "权限变更",
	"gate":         "门禁 / 评测",
	"irreversible": "不可逆或对外操作",
	"acceptance":   "验收",
}

// hardKindsDoc 是给 Agent 看的类别说明（MCP / ks 帮助 / API 描述共用）。
const hardKindsDoc = "product 产品方向（做不做、做成什么样、给谁用）；design 设计选型（界面 / 交互取哪套）；semantics 语义 / 接口变更（字段含义、对外接口、数据格式）；" +
	"permission 权限变更（谁能做什么、授权范围）；gate 门禁 / 评测（测试、评测、审批规则的口径）；irreversible 不可逆或对外操作（删除数据、发布、推送到远端、花钱、对外发消息）；acceptance 验收（是否通过）"

// unsureWords：问题或选项里出现这些词、又没标 hard 的决策算「拿不准」。
var unsureWords = []string{"产品方向", "语义", "接口", "权限", "门禁", "评测", "不可逆", "删除", "发布", "推送到远端", "git push", "花钱", "付费", "对外"}

// hardOf 返回 d 命中的硬规则（只看决策内容，不看状态和谁来答）；没命中时为空。
func hardOf(d store.Decision) string {
	if d.Approval {
		return "批准方案 / 计划只由用户操作"
	}
	if name, ok := hardKinds[d.Hard]; ok {
		return name + "只由用户决策"
	}
	for _, fs := range d.OptionFiles {
		if len(fs) > 0 {
			return "设计选型（选项带效果图）只由用户决策"
		}
	}
	for _, p := range d.Points {
		for _, it := range p.Items {
			if it.Image != nil {
				return "设计选型（要决策的点带效果图）只由用户决策"
			}
		}
	}
	return ""
}

// unsureOf 返回 d 的问题或选项里命中的第一个兜底词；没命中或已标 hard 时为空。
func unsureOf(d store.Decision) string {
	if d.Hard != "" {
		return ""
	}
	texts := append([]string{d.Question}, d.Options...)
	for _, p := range d.Points {
		texts = append(texts, p.Title)
		for _, it := range p.Items {
			texts = append(texts, it.Label)
		}
	}
	all := strings.ToLower(strings.Join(texts, "\n"))
	for _, w := range unsureWords {
		if strings.Contains(all, strings.ToLower(w)) {
			return w
		}
	}
	return ""
}
