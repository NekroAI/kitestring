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

