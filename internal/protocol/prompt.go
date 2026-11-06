package protocol

import (
	"regexp"
	"strings"
)

var (
	// 由 agent 自动注入、并非用户输入的消息（后台任务通知、子 Agent 交回结果等）。
	autoPromptRe = regexp.MustCompile(`(?s)^\s*<(task-notification|agent-message|local-command-stdout|local-command-stderr|bash-stdout|bash-stderr)\b`)
	// 系统提示块，任何位置出现都去掉。
	systemBlockRe = regexp.MustCompile(`(?s)<(system-reminder|local-command-caveat)>.*?</(system-reminder|local-command-caveat)>`)
	cmdNameRe     = regexp.MustCompile(`(?s)<command-name>\s*(.*?)\s*</command-name>`)
	cmdArgsRe     = regexp.MustCompile(`(?s)<command-args>\s*(.*?)\s*</command-args>`)
	cmdTagsRe     = regexp.MustCompile(`(?s)<command-(message|name|args)>.*?</command-(message|name|args)>`)
	bashInputRe   = regexp.MustCompile(`(?s)<bash-input>\s*(.*?)\s*</bash-input>`)
)

// SourceAuto 标记由 agent 自动注入（非用户输入）的 UserPromptSubmit。
const SourceAuto = "auto"

// 自动注入消息的来源（R-287），记在 UserPromptSubmit 的 Event.Wake 上：
// WakeBg 后台命令结束、监视有输出；WakeAgent 子任务交回结果；WakeOutput 终端 ! 命令 / 斜杠命令的输出。
// 「纸鸢送达」「待命到时」不在这里判：ks wait 本身也是后台命令，要靠 KsWaitEnd 记录（Hub 拼轮时判）。
const (
	WakeBg     = "bg"
	WakeAgent  = "agent"
	WakeOutput = "output"
)

var (
	wakeSummaryRe = regexp.MustCompile(`(?s)<summary>\s*(.*?)\s*</summary>`)
	wakeQuotedRe  = regexp.MustCompile(`"([^"]*)"`)
)

// AutoKind 判断一条自动注入消息的来源，并取出一句说明（后台命令 / 监视 / 子任务的描述，截到 80 字）。
// 不是自动注入的消息返回空。
func AutoKind(p string) (kind, note string) {
	m := autoPromptRe.FindStringSubmatch(p)
	if m == nil {
		return "", ""
	}
	switch m[1] {
	case "agent-message":
		return WakeAgent, ""
	case "task-notification":
	default:
		return WakeOutput, ""
	}
	sum := ""
	if s := wakeSummaryRe.FindStringSubmatch(p); s != nil {
		sum = s[1]
	}
	if q := wakeQuotedRe.FindStringSubmatch(sum); q != nil {
		note = q[1]
		if r := []rune(note); len(r) > 80 {
			note = string(r[:80]) + "…"
		}
	}
	if strings.HasPrefix(sum, "Agent ") {
		return WakeAgent, note
	}
	return WakeBg, note
}

// SourceStandby 标在 DSH 插件的 Stop / Interrupt 上：插件开着待命，这一轮结束后马上挂 /wait，
// Hub 直接记成待命，不再先记“等你回复”再转待命（R-251）。
const SourceStandby = "standby"

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

// ambientPromptHead 是 Codex 桌面版「项目建议」后台任务第一条提示词的固定开头（R-179）。
// client 都是 app-server，和用户自己开的对话分不开，只能靠提示词；统一压成单个空格再比对，不怕换行 / 缩进差异。
const ambientPromptHead = "# Overview Generate 0 to 3 hyperpersonalized suggestions for what this user can do with Codex"

// IsBackgroundPrompt 判断一条会话的第一条提示词是不是桌面应用自己在后台发起的任务
// （目前只认 Codex 的「项目建议」）。用户自己的对话永远不会以这段固定文字开头。
func IsBackgroundPrompt(tool, prompt string) bool {
	if tool != ToolCodex {
		return false
	}
	return strings.HasPrefix(strings.Join(strings.Fields(prompt), " "), ambientPromptHead)
}
