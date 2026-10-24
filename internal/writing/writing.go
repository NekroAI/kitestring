// Package writing 是 Agent 交给用户读的文字的写作规范与字数提示（R-110）：
// 证据、决策超过阈值时照常保存，只在工具返回里附一句压缩提示。
package writing

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	// ConclusionMax 是证据第一句（结论）的字数上限。
	ConclusionMax = 40
	// EvidenceMax 是一条证据摘要的字数上限。
	EvidenceMax = 600
	// QuestionMax 是决策问题的字数上限：问题应当一句话。
	QuestionMax = 60
	// DecisionContextMax 是决策说明（context）的字数上限。
	DecisionContextMax = 400
)

// Count 按字计数（中英文都按字符算），忽略首尾空白。
func Count(s string) int { return utf8.RuneCountInString(strings.TrimSpace(s)) }

// FirstSentence 取证据的第一句：去掉开头的 Markdown 标记和“结论：”前缀，截到第一个句末标点或换行。
func FirstSentence(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimLeft(s, "#*->` \t")
	for _, p := range []string{"结论：", "结论:"} {
		s = strings.TrimPrefix(s, p)
	}
	s = strings.TrimLeft(s, "* \t")
	if i := strings.IndexAny(s, "。！？!?\n"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(strings.TrimRight(s, "*"))
}

// EvidenceHint 检查证据摘要；不超标时返回空串。
func EvidenceHint(summary string) string {
	var tips []string
	if n := Count(FirstSentence(summary)); n > ConclusionMax {
		tips = append(tips, fmt.Sprintf("第一句结论 %d 字，超过 %d 字：改成一句用户能看到的变化", n, ConclusionMax))
	}
	if n := Count(summary); n > EvidenceMax {
		tips = append(tips, fmt.Sprintf("全文 %d 字，超过 %d 字：只留逐条判定和依据，实现细节压到最后几行", n, EvidenceMax))
	}
	return hint(tips)
}

// DecisionHint 检查决策的问题和说明；不超标时返回空串。
func DecisionHint(question, context string) string {
	var tips []string
	if n := Count(question); n > QuestionMax {
		tips = append(tips, fmt.Sprintf("问题 %d 字，超过 %d 字：一句话只问一件事", n, QuestionMax))
	}
	if n := Count(context); n > DecisionContextMax {
		tips = append(tips, fmt.Sprintf("说明 %d 字，超过 %d 字：按“结论 / 依据 / 细节”压缩，要拍板的点拆成单独的决策", n, DecisionContextMax))
	}
	return hint(tips)
}

func hint(tips []string) string {
	if len(tips) == 0 {
		return ""
	}
	return "已保存。字数提示：" + strings.Join(tips, "；") + "。下次照此压缩，用户读得更快。"
}

// BlocksGuide 是表达块（R-97）的用法，MCP 工具说明、ks 帮助和身份提示词共用。
const BlocksGuide = "表达块由纸鸢统一渲染，不支持任意 HTML。每块是一个对象 {type, id?, title?, caption?, …}，图片字段填本机路径（会自动上传）。" +
	"checklist 验收清单（验收证据必填）：items [{criterion: 第几条验收标准, text: 这条标准, result: pass|fail|ask, why: 一句依据, see: 关联块的 id}]；" +
	"compare 前后对比（界面类需求默认用）：before、after 两张同尺寸截图，title 一句话标题，boxes 框出改后的图上改了哪里（网页默认显示改后加框，点一下切到改前）；" +
	"figure 截图 / 录屏：image，title 一句话标题，boxes 框出改动的地方，可被清单项的 see 引用；" +
	"boxes 是 [{x, y, w, h, label}]，坐标是图片原始尺寸的比例（0～1，左上角为原点；Playwright 用元素的 boundingBox 除以截图的 CSS 宽高），label 一句话说改了什么；越界或格式不对的框丢掉并提示。" +
	"flow 流程 / 状态图：mermaid 源码（flowchart、stateDiagram、sequenceDiagram）；" +
	"metrics 2～4 个指标：items [{label, value, unit?, note?, trend?: [数字]}]；" +
	"scope 改动范围：items [{module, count, note?}]，按模块汇总，不列文件；" +
	"options 选项（决策用）：items [{label, desc, recommended?, image?}]；" +
	"table 小表格：columns（最多 6 列）、rows。" +
	"能用块就不写长文：界面类给图或对比，流程类给流程图。图按顺序全部显示：重点看的放前面，补充、测试一类的放后面。写错的块不保存，返回里会说明原因，证据照常保存。"

// PayloadHint 检查结构化证据：结论字数、有没有清单。不超标时返回空串。
func PayloadHint(verdict string, hasChecklist bool) string {
	var tips []string
	if n := Count(verdict); n > ConclusionMax {
		tips = append(tips, fmt.Sprintf("verdict %d 字，超过 %d 字：改成一句用户能看到的变化", n, ConclusionMax))
	}
	if !hasChecklist {
		tips = append(tips, "没有 checklist 块：验收证据用它逐条对照验收标准给出判定，纸鸢会画成验收清单")
	}
	return hint(tips)
}
