package main

import (
	"context"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/NekroAI/kitestring/internal/client"
	"github.com/NekroAI/kitestring/internal/roles"
)

// runRole：ks role <身份>——声明当前会话的身份（R-82、R-190）。标识、中文名、英文简写和别称都认：
// ks role aide / 主助理 / 参谋、ks role pm / 主调度 / "接管 PM"；旧标识 pm / dev / advisor / reviewer 不变。成功后打印身份提示词，Agent 立即按它工作；
// 会话恢复或压缩上下文后，daemon 会通过 hook 再补发一次。
func runRole(args []string) error {
	fs := flag.NewFlagSet("role", flag.ContinueOnError)
	session := fs.String("session", "", "会话键（tool:session_id）；默认自动识别当前对话")
	if err := parseAnywhere(fs, args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("用法：ks role <%s|none>（也认中文名和别称，如 主助理、参谋、主调度）", strings.Join(roles.All, "|"))
	}
	role, ok := roles.Resolve(strings.Join(fs.Args(), " "))
	if !ok {
		var b strings.Builder
		for _, r := range roles.All {
			b.WriteString("\n  ks role " + r + "  " + roles.AliasLine(r))
		}
		return fmt.Errorf("未知身份 %q，可选（后面是也认的叫法）：%s\n清除身份用 none", strings.Join(fs.Args(), " "), b.String())
	}
	key := currentSession(*session)
	if key == "" {
		return fmt.Errorf("认不出当前会话：请在 Claude Code / Codex 的对话里运行，或用 --session tool:id 指定")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c := client.New(20 * time.Second)
	var out struct {
		Label   string   `json:"label"`
		Prompt  string   `json:"prompt"`
		Demoted []string `json:"demoted"`
		Handoff bool     `json:"handoff"`
	}
	if err := c.Do(ctx, "POST", "/agent/sessions/role", map[string]any{"session_key": key, "role": role}, &out); err != nil {
		return err
	}
	if role == "" {
		fmt.Println("[Kitestring] 已清除本会话的身份，回到普通会话。")
		return nil
	}
	if out.Handoff {
		fmt.Printf("[Kitestring] 已接任%s：上一任写的交接附在下面，上一任已自动卸任。\n", roles.Display(role))
	} else if len(out.Demoted) > 0 {
		fmt.Printf("[Kitestring] 已接任%s：原来的%s会话（%s）降为普通会话，并已收到通知。\n", roles.Display(role), roles.Name(role), strings.Join(out.Demoted, "、"))
	}
	fmt.Print(out.Prompt)
	return nil
}

// currentSession 识别 ks 命令所在的对话：--session 优先；其次按父进程找到 Agent 进程、取它最近一次 hook 的会话
// （恢复过的 Claude 会话里环境变量可能已过期；Codex 没有会话环境变量）；最后用 Claude 的环境变量兜底。
func currentSession(flagVal string) string {
	if flagVal != "" {
		return flagVal
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var res struct {
		SessionKey string `json:"session_key"`
	}
	if err := client.New(5*time.Second).Do(ctx, "GET", "/session?"+url.Values{"ppid": {strconv.Itoa(os.Getppid())}}.Encode(), nil, &res); err == nil && res.SessionKey != "" {
		return res.SessionKey
	}
	return cliSession("")
}

// currentTeller 识别 ks team tell 所在的对话和发消息的子任务（R-191）：按父进程找到会话，
// 再取这个会话最近一条 ks team tell 命令是哪个子任务发的（主线程发的为空）。认不出时退回 currentSession。
func currentTeller() (key, agent string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var res struct {
		SessionKey string `json:"session_key"`
		AgentID    string `json:"agent_id"`
	}
	if err := client.New(5*time.Second).Do(ctx, "GET", "/session?"+url.Values{"ppid": {strconv.Itoa(os.Getppid())}, "tell": {"1"}}.Encode(), nil, &res); err == nil && res.SessionKey != "" {
		return res.SessionKey, res.AgentID
	}
	return cliSession(""), ""
}

// runBrief：ks brief <简报>——PM 写给用户的简报，显示在工作台顶部（R-113）；ks brief "" 清除。
func runBrief(args []string) error {
	fs := flag.NewFlagSet("brief", flag.ContinueOnError)
	session := fs.String("session", "", "会话键（tool:session_id）；默认自动识别当前对话")
	if err := parseAnywhere(fs, args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("用法：ks brief <简报：一段话，先结论，提到需求写编号>（传空字符串清除）")
	}
	key := currentSession(*session)
	if key == "" {
		return fmt.Errorf("认不出当前会话：请在 Claude Code / Codex 的对话里运行，或用 --session tool:id 指定")
	}
	return agentCall("POST", "/agent/brief", map[string]any{"session_key": key, "text": strings.Join(fs.Args(), " ")})
}

// runMorning：主助理写离开期间小结（R-165）；ks morning 是旧名，保留别名。
//
//	ks away <总述>                          写总述（传空字符串清除）
//	ks away advise <R-x> <pass|return|clear> <理由>   给待验收的需求写验收建议
func runMorning(args []string) error {
	fs := flag.NewFlagSet("morning", flag.ContinueOnError)
	session := fs.String("session", "", "会话键（tool:session_id）；默认自动识别当前对话")
	if err := parseAnywhere(fs, args); err != nil {
		return err
	}
	usage := fmt.Errorf("用法：ks away <总述：一段话，先结论，提到需求写编号>（传空字符串清除）\n      ks away advise <R-x> <pass|return|clear> <理由>")
	if fs.NArg() < 1 {
		return usage
	}
	key := currentSession(*session)
	if key == "" {
		return fmt.Errorf("认不出当前会话：请在 Claude Code / Codex 的对话里运行，或用 --session tool:id 指定")
	}
	body := map[string]any{"session_key": key}
	if a := fs.Args(); a[0] == "advise" {
		if len(a) < 3 {
			return usage
		}
		body["advice"] = []map[string]any{{"requirement": a[1], "verdict": a[2], "reason": strings.Join(a[3:], " ")}}
	} else {
		body["text"] = strings.Join(a, " ")
	}
	return agentCall("POST", "/agent/morning", body)
}

// runTeam：ks team——列出本项目的 PM 与开发会话（R-86）。
func runTeam(args []string) error {
	fs := flag.NewFlagSet("team", flag.ContinueOnError)
	session := fs.String("session", "", "会话键（tool:session_id）；默认自动识别当前对话")
	reason := fs.String("reason", "", "recommend / need：为什么要开 Dev 会话（为什么要并行、为什么子代理不合适）")
	work := fs.String("work", "", "recommend：开了做什么（需求编号）")
	duration := fs.String("duration", "", "recommend：预计用多久")
	minutes := fs.Int("minutes", 0, "recommend：批准时限（分钟），默认用设置里的（30）")
	req := fs.String("req", "", "tell：相关需求编号（如 R-12）")
	to := fs.String("to", "", "tell：收件身份 aide（主助理）| pm（主调度，默认）| reviewer（评审）")
	reply := fs.String("reply", "", "tell：回复收到的团队消息（填它的编号 D-x），回复会送回发消息的一方")
	if err := parseAnywhere(fs, args); err != nil {
		return err
	}
	tell := fs.NArg() > 0 && fs.Arg(0) == "tell"
	key, agent := *session, ""
	if tell && key == "" {
		key, agent = currentTeller()
	} else {
		key = currentSession(key)
	}
	if key == "" {
		return fmt.Errorf("认不出当前会话：请在 Claude Code / Codex 的对话里运行，或用 --session tool:id 指定")
	}
	if fs.NArg() > 0 && fs.Arg(0) == "recommend" {
		// ks team recommend <数量> --reason ... [--work ...] [--duration ...] [--minutes N]：推荐开 Dev 会话，用户限时批准（R-169）
		n, err := strconv.Atoi(fs.Arg(1))
		if fs.NArg() < 2 || err != nil || n < 1 || *reason == "" {
			return fmt.Errorf("用法：ks team recommend <数量> --reason <为什么要开> [--work <做什么>] [--duration <预计多久>] [--minutes <批准时限>]")
		}
		body := map[string]any{"session_key": key, "count": n, "reason": *reason}
		if *work != "" {
			body["work"] = *work
		}
		if *duration != "" {
			body["duration"] = *duration
		}
		if *minutes > 0 {
			body["minutes"] = *minutes
		}
		return agentCall("POST", "/agent/team/recommend", body)
	}
	if fs.NArg() > 0 && fs.Arg(0) == "need" {
		// ks team need <数量> --reason ...：旧名，等同 recommend（R-87 → R-169）；数量 0 撤回推荐
		n, err := strconv.Atoi(fs.Arg(1))
		if fs.NArg() < 2 || err != nil || (n > 0 && *reason == "") {
			return fmt.Errorf("用法：ks team need <数量> --reason <为什么要开>（旧名，等同 ks team recommend；数量填 0 撤回推荐）")
		}
		return agentCall("POST", "/agent/team/need", map[string]any{"session_key": key, "count": n, "reason": *reason})
	}
	if tell {
		// ks team tell <文字> [--to aide|pm|reviewer] [--reply D-x] [--req R-x]：按身份给本项目的团队成员发消息，
		// 或回复收到的团队消息；只送给 Agent、不进用户收件箱（R-135 / R-191）
		text := strings.TrimSpace(strings.Join(fs.Args()[1:], " "))
		if text == "" {
			return fmt.Errorf("用法：ks team tell <要说的话> [--to aide|pm|reviewer] [--req R-x]\n      ks team tell --reply D-x <回复>")
		}
		body := map[string]any{"session_key": key, "text": text}
		for k, v := range map[string]string{"requirement": *req, "to": *to, "reply_to": *reply, "agent_id": agent} {
			if v != "" {
				body[k] = v
			}
		}
		return agentCall("POST", "/agent/team/tell", body)
	}
	return agentCall("GET", "/agent/team?"+url.Values{"session_key": {key}}.Encode(), nil)
}
