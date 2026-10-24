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

// runRole：ks role <身份>——声明当前会话的身份（R-82）。成功后打印身份提示词，Agent 立即按它工作；
// 会话恢复或压缩上下文后，daemon 会通过 hook 再补发一次。
func runRole(args []string) error {
	fs := flag.NewFlagSet("role", flag.ContinueOnError)
	session := fs.String("session", "", "会话键（tool:session_id）；默认自动识别当前对话")
	if err := parseAnywhere(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("用法：ks role <%s|none>", strings.Join(roles.All, "|"))
	}
	role := strings.ToLower(fs.Arg(0))
	if role == "none" {
		role = ""
	}
	if !roles.Valid(role) {
		return fmt.Errorf("未知身份 %q：可选 %s，清除身份用 none", fs.Arg(0), strings.Join(roles.All, "、"))
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
	}
	if err := c.Do(ctx, "POST", "/agent/sessions/role", map[string]any{"session_key": key, "role": role}, &out); err != nil {
		return err
	}
	if role == "" {
		fmt.Println("[Kitestring] 已清除本会话的身份，回到普通会话。")
		return nil
	}
	if len(out.Demoted) > 0 {
		fmt.Printf("[Kitestring] 已接管 PM：原来的 PM 会话（%s）降为普通会话，并已收到通知。\n", strings.Join(out.Demoted, "、"))
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

// runMorning：Adv 写离开期间小结（R-165）；ks morning 是旧名，保留别名。
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
	reason := fs.String("reason", "", "need：为什么需要 Dev 会话")
	req := fs.String("req", "", "tell：相关需求编号（如 R-12）")
	if err := parseAnywhere(fs, args); err != nil {
		return err
	}
	key := currentSession(*session)
	if key == "" {
		return fmt.Errorf("认不出当前会话：请在 Claude Code / Codex 的对话里运行，或用 --session tool:id 指定")
	}
	if fs.NArg() > 0 && fs.Arg(0) == "need" {
		// ks team need <数量> --reason ...：提醒用户开开发会话（R-87）
		n, err := strconv.Atoi(fs.Arg(1))
		if fs.NArg() < 2 || err != nil || (n > 0 && *reason == "") {
			return fmt.Errorf("用法：ks team need <数量> --reason <为什么需要>（数量填 0 撤销提醒）")
		}
		return agentCall("POST", "/agent/team/need", map[string]any{"session_key": key, "count": n, "reason": *reason})
	}
	if fs.NArg() > 0 && fs.Arg(0) == "tell" {
		// ks team tell <文字> [--req R-x]：给本项目的 PM 留言，只送给 PM、不进用户收件箱（R-135）
		text := strings.TrimSpace(strings.Join(fs.Args()[1:], " "))
		if text == "" {
			return fmt.Errorf("用法：ks team tell <要告诉 PM 的话> [--req R-x]")
		}
		body := map[string]any{"session_key": key, "text": text}
		if *req != "" {
			body["requirement"] = *req
		}
		return agentCall("POST", "/agent/team/tell", body)
	}
	return agentCall("GET", "/agent/team?"+url.Values{"session_key": {key}}.Encode(), nil)
}
