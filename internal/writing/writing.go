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
