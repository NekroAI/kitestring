package roles

import "strings"

// 轻接入的按需说明（R-359）：常驻的只有 lite.md 那几行（LiteMax 字以内），每项服务的用法用到时才由 ks help <服务> 取（ServiceMax 字以内）。

const (
	// LiteMax：轻接入常驻文字的上限（字）。
	LiteMax = 600
	// ServiceMax：每项服务详细说明的上限（字）。
	ServiceMax = 500
)

// Service 是一项可按需取用说明的服务。
type Service struct {
	Name    string   // ks help 后面写的名字
	Aliases []string // 也认的叫法
	Title   string   // 一句话：做什么
	Text    string
}

// Services 是 ks help <服务> 认的全部服务，按展示顺序。
var Services = []Service{
	{Name: "decision", Aliases: []string{"决策", "decisions"}, Title: "要用户定的事提到纸鸢上", Text: `决策（ks decision raise，MCP 工具 decision_raise）：要用户定的事提到纸鸢上，用户在网页或手机上答复。
- 一次只问一件事：--question 一句话（60 字以内），--option 选项（可重复），--recommend 推荐及理由，--context 背景（400 字以内）。
- 只要用户点头：加 --approval（批准 / 拒绝，可附言，按附言调整）。
- 有关联需求加 --req R-x；方案图用 --image。
- 提错了或要修订：ks decision withdraw D-x --reason 原因，不要并排再提一条。
- 提完挂一轮 ks wait，答复送到时自动继续；没等到的，用户下次发消息时会带上。`},
	{Name: "secret", Aliases: []string{"凭据", "secrets", "token"}, Title: "申请和取用第三方凭据", Text: `凭据（MCP 工具 secret_request / secret_list / secret_amend）：
- 申请：ks secret request <凭据名> --purpose 用途 --field '环境变量名|显示名|获取方式' [--plain 不敏感的变量名] [--allow '命令 *'] [--days 7]。获取方式按服务写清步骤；用户在纸鸢上填值并授权后结果送到你这里。
- 取用：ks secret run S-x -- <命令>，值只作为这条命令的环境变量出现；命令里写编号 S-x，ks secret list 看能用的。
- 被拒（命令不在范围、到期、没授权写盘）或要换 Token：ks secret amend S-x --reason 理由（--add-allow、--replace 等）。
- 不要打印、回显值，不要让用户在终端里输入或粘贴凭据。完整用法：ks secret help。`},
	{Name: "asset", Aliases: []string{"资源库", "素材", "assets"}, Title: "把素材、音效、字体推进资源库给用户审核", Text: `资源库（ks asset push，MCP 工具 asset_push）：用户在网页上审核。
- ks asset push asset.json [--dry-run]：以带命名空间的 id 为主键，写法见 ks asset help；有错整批不推（--skip-invalid 跳过）。
- 重推同一 id 记新版本；未连 Hub 时排队。已在用的旧素材加 --baseline（用户批准整批通过）。
- 具名变体各是一条（id@变体名）；旧条目迁新 id 用 ks asset supersede --map。音效、字体、SVG 同样推。
- 中文名可选：asset.json 的 display.name，本来有才填，别编。
- 查询：先 ks asset summary，再 list --status / --category / --id 'core:weapon.*' / --q 关键字（id 或中文名）；单件 get <id|A-x>，不整表拉 --json。
- 网页授权本会话后可用 ks asset review 批量审（--dry-run 预检，--undo 撤销）。`},
	{Name: "ask", Aliases: []string{"求助", "aide", "主助理"}, Title: "设计取舍问主助理", Text: `求助主助理：设计取舍、拿不准的判断，用 ks team tell --to aide <问题，写清选项和倾向>（MCP 工具 team_tell），问完不等，先做不受影响的部分；答复送回本会话，挂着 ks wait 时立刻唤醒。
- 消息只放一两句（120 字以内）；细节多时先写进需求留言（ks req comment R-x）。
- 本项目没有主助理时退回给主调度；都没有时用 ks decision raise 问用户。
- ks team sessions 看本项目有哪些会话在线。`},
	{Name: "wait", Aliases: []string{"等待", "ks wait"}, Title: "每轮结束挂一轮 ks wait", Text: `等待（轻接入）：每轮结束前挂一轮 ks wait，只被发给你的事唤醒：你提的决策被答复、发给你的团队消息、用户在工作台上直接发给你的话。
- Claude Code：用 Bash 工具在后台运行 ks wait（run_in_background，工具超时设得比 55 分钟长），然后结束本轮；它结束时你会被唤醒。
- Codex：在前台运行 ks wait（最多等 30 分钟，shell 工具超时设成 35 分钟以上），运行期间一直等它结束，不要中断、不要重复运行。
- 醒来后按输出继续做；超时就用一两句话说明做到哪，然后结束本轮，不再挂 ks wait。
- 不订阅别的会话的事件，不用常驻，也不用写交接。`},
	{Name: "role", Aliases: []string{"身份", "roles", "lite", "轻接入"}, Title: "轻接入与完整身份之间切换", Text: `身份：ks role <身份> 切换，输出提示词，照它工作。
- 轻接入：ks role lite（也认 轻接入、轻接入纸鸢、轻量），不占唯一位，可多个会话同时轻接入。
- 完整身份：ks role aide（主助理）、advisor（顾问）、reviewer（评审）、pm（主调度）、dev（开发）；担任后按身份提示词走需求、验收证据、团队消息等流程。
- ks role none 清除，回到普通会话。`},
}

// FindService 按名字或叫法找服务（不分大小写）。
func FindService(name string) (Service, bool) {
	k := norm(name)
	for _, s := range Services {
		if k == s.Name {
			return s, true
		}
		for _, a := range s.Aliases {
			if k == norm(a) {
				return s, true
			}
		}
	}
	return Service{}, false
}

// ServiceIndex 是 ks help 列出的服务清单，一项一行。
func ServiceIndex() string {
	var b strings.Builder
	for _, s := range Services {
		b.WriteString("  ks help " + s.Name + "  " + s.Title + "（也认：" + strings.Join(s.Aliases, "、") + "）\n")
	}
	return b.String()
}
