// Package roles 定义会话身份（R-82）：PM、Dev（开发）、Adv（顾问）、Rev（审查员），以及注入给 Agent 的身份提示词。
package roles

import (
	_ "embed"
	"fmt"
	"strings"
)

const (
	PM       = "pm"
	Dev      = "dev"
	Advisor  = "advisor"
	Reviewer = "reviewer"
)

var (
	//go:embed pm.md
	pmPrompt string
	//go:embed dev.md
	devPrompt string
	//go:embed advisor.md
	advisorPrompt string
	//go:embed reviewer.md
	reviewerPrompt string
)

// 显示名统一用英文短名（R-124、D-118）；内部标识 pm / dev / advisor / reviewer 不变。
var labels = map[string]string{PM: "PM", Dev: "Dev", Advisor: "Adv", Reviewer: "Rev"}

// cnames：英文短名对应的中文叫法，写进身份提示词，用户说“做开发 / 当顾问”时 Agent 也对得上。
var cnames = map[string]string{PM: "项目经理", Dev: "开发", Advisor: "顾问", Reviewer: "审查员"}

// All 是全部身份，按展示顺序。
var All = []string{PM, Dev, Advisor, Reviewer}

// Valid 判断身份是否合法；空串表示无身份。
func Valid(role string) bool { return role == "" || labels[role] != "" }

// Label 返回身份的显示名。
func Label(role string) string { return labels[role] }

// Resident 判断该身份是否要求常驻在线（挂着 ks wait，R-83）。
func Resident(role string) bool { return role == PM || role == Dev }

// Prompt 返回注入给 Agent 的身份提示词；无身份时为空。
func Prompt(role string) string {
	var body string
	switch role {
	case PM:
		body = pmPrompt
	case Dev:
		body = devPrompt
	case Advisor:
		body = advisorPrompt
	case Reviewer:
		body = reviewerPrompt
	default:
		return ""
	}
	return fmt.Sprintf("[Kitestring] 你的会话身份：%s（%s，用户可在 Kitestring 上看到；改身份用 ks role）。\n%s", labels[role], cnames[role], strings.TrimSpace(body)+"\n")
}
