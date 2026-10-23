// Package roles 定义会话身份（R-82）：PM、Dev（开发）、Adv（顾问）、Rev（审查员），以及注入给 Agent 的身份提示词。
package roles

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
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

// DefaultBody 返回内置默认模板正文（编译进 ks）；无身份时为空。
func DefaultBody(role string) string {
	switch role {
	case PM:
		return strings.TrimSpace(pmPrompt)
	case Dev:
		return strings.TrimSpace(devPrompt)
	case Advisor:
		return strings.TrimSpace(advisorPrompt)
	case Reviewer:
		return strings.TrimSpace(reviewerPrompt)
	}
	return ""
}

// BodyHash 是默认模板正文的指纹，用来判断“默认模板是否在用户改写之后又更新了”（R-98）。
func BodyHash(body string) string {
	h := sha256.Sum256([]byte(strings.TrimSpace(body)))
	return hex.EncodeToString(h[:8])
}

// Compose 把正文拼上固定的身份抬头（抬头不可改写）；body 为空时用默认模板。
func Compose(role, body string) string {
	if labels[role] == "" {
		return ""
	}
	if strings.TrimSpace(body) == "" {
		body = DefaultBody(role)
	}
	return fmt.Sprintf("[Kitestring] 你的会话身份：%s（%s，用户可在 Kitestring 上看到；改身份用 ks role）。\n%s", labels[role], cnames[role], strings.TrimSpace(body)+"\n")
}

// Prompt 返回注入给 Agent 的默认身份提示词；无身份时为空。用户改写过的版本由 Hub 下发（R-98）。
func Prompt(role string) string { return Compose(role, "") }
