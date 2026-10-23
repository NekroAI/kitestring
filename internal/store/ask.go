package store

import (
	"strings"
	"unicode/utf8"
)

// 「在等你回复」（R-117）：会话一轮结束时的最后回复如果是在问用户，记下回复的最后一段，
// 工作台据此把没有正式问题的会话放进「要你处理」，ks wait 也据此决定提醒的轻重。
//
// 判断只看最后一段（Agent 问问题几乎都放在结尾）：去掉 Markdown 记号后以问号结尾，
// 或者含有明显在征求用户意见的说法（要不要、是否、你看、请确认……）。只是汇报进度、说“继续待命”的不算。

const askTailMax = 400 // 摘录最多保留的字数

var askCues = []string{
	"要不要", "是否要", "是否需要", "是否可以", "要我", "需要我", "你看", "您看", "你决定", "你来定", "请确认", "确认一下", "可以吗", "行吗", "好吗", "对吗",
	"你想", "你希望", "选哪", "哪一个", "哪种", "请回复", "请告诉我", "请你", "等你拍板", "等你决定", "等你确认", "等你回复",
	"should i", "do you want", "would you", "shall i", "let me know", "which one", "please confirm",
}

// AskTail：text 是在问用户时返回它的最后一段（最多 400 字，从段首截起时前面加“…”），否则返回空。
func AskTail(text string) string {
	paras := splitParas(text)
	if len(paras) == 0 {
		return ""
	}
	last := paras[len(paras)-1]
	if !asks(last) {
		return ""
	}
	// 最后一段很短（如“要提交吗？”）时带上前一段，让人看得懂在问什么
	out := last
	if utf8.RuneCountInString(last) < 60 && len(paras) > 1 {
		out = paras[len(paras)-2] + "\n\n" + last
	}
	if r := []rune(out); len(r) > askTailMax {
		out = "…" + strings.TrimSpace(string(r[len(r)-askTailMax:]))
	}
	return out
}

func splitParas(text string) []string {
	var out []string
	for _, p := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n\n") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func asks(p string) bool {
	plain := strings.TrimRight(p, " \t\n*_`~>)）】」』\"'。.!！")
	if strings.HasSuffix(plain, "?") || strings.HasSuffix(plain, "？") {
		return true
	}
	// 段内任何一句以问号结尾（“要提交吗？我先挂着等你。”）
	if strings.Contains(p, "？") || strings.Contains(p, "? ") || strings.Contains(p, "?\n") {
		return true
	}
	low := strings.ToLower(p)
	for _, c := range askCues {
		if strings.Contains(low, c) {
			return true
		}
	}
	return false
}
