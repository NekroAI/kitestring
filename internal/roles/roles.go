// Package roles 定义会话身份（R-82、R-190）：主助理（Aide）、顾问（Adv）、评审（Rev）、主调度（PM）、开发（Dev），
// 以及注入给 Agent 的身份提示词和身份别称。
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
	Aide     = "aide"
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
	//go:embed aide.md
	aidePrompt string
)

// labels：英文简写（R-124、D-118、R-190）；内部标识 pm / dev / advisor / reviewer / aide 不变。
var labels = map[string]string{PM: "PM", Dev: "Dev", Advisor: "Adv", Reviewer: "Rev", Aide: "Aide"}

// cnames：中文名（R-190）：PM 的中文名是「主调度」，「主」字头 = 每个项目只有一个。
var cnames = map[string]string{PM: "主调度", Dev: "开发", Advisor: "顾问", Reviewer: "评审", Aide: "主助理"}

// aliases：用户对 Agent 说的别称（R-190），ks role 也认得它们。只写别称，标识、英文简写、中文名自动算进去。
var aliases = map[string][]string{
	Aide:     {"助理", "参谋", "领航", "特助", "总助", "assistant"},
	Advisor:  {"advisor", "adviser"},
	Reviewer: {"审查", "审查员", "reviewer", "review"},
	PM:       {"调度", "项目经理", "统筹", "manager"},
	Dev:      {"程序员", "developer"},
}

// All 是全部身份，按展示顺序：替你判断（主助理、顾问、评审）在前，替你干活（主调度、开发）在后。
var All = []string{Aide, Advisor, Reviewer, PM, Dev}

// Valid 判断身份是否合法；空串表示无身份。
func Valid(role string) bool { return role == "" || labels[role] != "" }

// Label 返回身份的英文简写（PM / Dev / Adv / Rev / Aide）。
func Label(role string) string { return labels[role] }

// Name 返回身份的中文名（主调度 / 开发 / 顾问 / 评审 / 主助理）。
func Name(role string) string { return cnames[role] }

// Display 返回给 Agent 看的称呼「中文名（英文简写）」，如「主调度（PM）」；无身份时为空。
func Display(role string) string {
	if labels[role] == "" {
		return ""
	}
	return cnames[role] + "（" + labels[role] + "）"
}

// Aliases 返回身份的别称（不含标识、英文简写、中文名）。
func Aliases(role string) []string { return aliases[role] }

// Unique 判断该身份是否每个项目只有一个（主调度、主助理）：新会话接任时，旧的自动交出身份并收到通知（R-82、R-190）。
func Unique(role string) bool { return role == PM || role == Aide }

// Resident 判断该身份是否要求常驻在线（挂着 ks wait，R-83）。
func Resident(role string) bool { return role == PM || role == Dev || role == Aide }

// Resolve 把用户 / Agent 给的身份说法换成标识：标识本身、英文简写、中文名、别称都认（不分大小写），
// 也认带动作的说法，如「接管 PM」「当参谋」「做主助理」。none / 无 / 空表示清除身份，返回 ("", true)；认不出返回 ("", false)。
func Resolve(s string) (string, bool) {
	k := norm(s)
	switch k {
	case "", "none", "无", "清除", "无身份":
		return "", true
	}
	for _, try := range []string{k, trimVerb(k)} {
		for _, role := range All {
			if try == role || try == norm(labels[role]) || try == norm(cnames[role]) {
				return role, true
			}
			for _, a := range aliases[role] {
				if try == norm(a) {
					return role, true
				}
			}
		}
	}
	return "", false
}

func norm(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), ""))
}

// trimVerb 去掉「接管 / 当 / 做 / 担任 / 改当……的」这类动作前缀和「会话 / 身份」后缀。
func trimVerb(k string) string {
	for _, p := range []string{"接管", "担任", "改当", "改做", "来当", "来做", "当", "做", "是", "act as ", "be "} {
		if strings.HasPrefix(k, norm(p)) {
			k = strings.TrimPrefix(k, norm(p))
			break
		}
	}
	for _, p := range []string{"会话", "身份", "角色"} {
		k = strings.TrimSuffix(k, p)
	}
	return strings.TrimPrefix(k, "本项目的")
}

// duties：每个身份一句话职责，写进身份抬头和 MCP 说明（R-190）。
var duties = map[string]string{
	Aide:     "替你判断，每项目唯一、常驻：看验收证据写建议、裁定子任务的设计取舍、授权窗口内代答、写离开期间小结、放行草稿",
	Advisor:  "替你判断，可多个、不常驻：只答问题、做设计（出方案、效果图、记草稿）",
	Reviewer: "替你判断：只做审查，意见挂在需求上；任意工具（含 Codex）的会话都能担任",
	PM:       "替你干活，每项目唯一、常驻：排期、派子任务、合并部署、写简报",
	Dev:      "替你干活，用户批准才开：实现主调度派的需求",
}

// Duty 返回身份的一句话职责。
func Duty(role string) string { return duties[role] }

// Team 是全体身份的职责与别称，几行文字，写进身份抬头和 MCP 说明；子代理统一叫「子任务」（R-190）。
func Team() string {
	var b strings.Builder
	for _, role := range All {
		fmt.Fprintf(&b, "- %s——%s；别称：%s（ks role %s）\n", Display(role), duties[role], strings.Join(append([]string{cnames[role], labels[role]}, aliases[role]...), "、"), role)
	}
	b.WriteString("- 子任务：子任务、子代理、subagent——主调度 / 主助理派出的一次性后台子代理，不是身份，不用 ks role。\n")
	b.WriteString("用户说「PM」「接管 PM」「让 PM 去做」都指主调度；说「参谋」「助理」指主助理。")
	return b.String()
}

// AliasLine 是一行别称说明，写进身份抬头和 MCP 说明：「主助理（Aide）：主助理、助理、参谋……」。
func AliasLine(role string) string {
	names := append([]string{cnames[role], labels[role]}, aliases[role]...)
	return Display(role) + "：" + strings.Join(names, "、")
}

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
	case Aide:
		return strings.TrimSpace(aidePrompt)
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
	return fmt.Sprintf("[Kitestring] 你的会话身份：%s（用户可在 Kitestring 上看到；改身份用 ks role）。\n%s\n团队里的身份与别称（用户这样叫时指的是谁）：\n%s\n", Display(role), strings.TrimSpace(body), Team())
}

// Prompt 返回注入给 Agent 的默认身份提示词；无身份时为空。用户改写过的版本由 Hub 下发（R-98）。
func Prompt(role string) string { return Compose(role, "") }

// TakeoverPrompt：ks takeover 启动新会话时带的接任提示（R-224）。新会话按它声明身份，纸鸢在 ks role 的输出里附上上一任的交接。
func TakeoverPrompt(role string) string {
	return "连接到纸鸢，接任本项目的" + Display(role) + "：先运行 ks role " + role +
		"。输出里会附上上一任写的交接（在做的、等用户的、约定和规矩、下一步、注意事项）；读完先回我一句确认（已接任、接手了哪几件事、下一步），再按交接继续。"
}
