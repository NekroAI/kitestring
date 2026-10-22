package protocol

import (
	"regexp"
	"strings"
)

var (
	// 由 agent 自动注入、并非用户输入的消息（后台任务通知等）。
	autoPromptRe = regexp.MustCompile(`(?s)^\s*<(task-notification|local-command-stdout|local-command-stderr|bash-stdout|bash-stderr)\b`)
	// 系统提示块，任何位置出现都去掉。
	systemBlockRe = regexp.MustCompile(`(?s)<(system-reminder|local-command-caveat)>.*?</(system-reminder|local-command-caveat)>`)
	cmdNameRe     = regexp.MustCompile(`(?s)<command-name>\s*(.*?)\s*</command-name>`)
	cmdArgsRe     = regexp.MustCompile(`(?s)<command-args>\s*(.*?)\s*</command-args>`)
	cmdTagsRe     = regexp.MustCompile(`(?s)<command-(message|name|args)>.*?</command-(message|name|args)>`)
	bashInputRe   = regexp.MustCompile(`(?s)<bash-input>\s*(.*?)\s*</bash-input>`)
)

// SourceAuto 标记由 agent 自动注入（非用户输入）的 UserPromptSubmit。
const SourceAuto = "auto"

// CleanPrompt 把 UserPromptSubmit 的原始文本整理成用户可读的形式。
// auto 为真表示这不是用户输入（如后台任务完成通知），不应当作“你最后说的话”。
func CleanPrompt(p string) (text string, auto bool) {
	if autoPromptRe.MatchString(p) {
		return "", true
	}
	p = systemBlockRe.ReplaceAllString(p, "")
	if m := bashInputRe.FindStringSubmatch(p); m != nil {
		return "! " + strings.TrimSpace(m[1]), false
	}
	if m := cmdNameRe.FindStringSubmatch(p); m != nil {
		name := strings.TrimSpace(m[1])
		if !strings.HasPrefix(name, "/") {
			name = "/" + name
		}
		args := ""
		if a := cmdArgsRe.FindStringSubmatch(p); a != nil {
			args = strings.TrimSpace(a[1])
		}
		rest := strings.TrimSpace(cmdTagsRe.ReplaceAllString(p, ""))
		out := strings.TrimSpace(name + " " + args)
		if rest != "" {
			out += " " + rest
		}
		return out, false
	}
	return strings.TrimSpace(p), false
}
