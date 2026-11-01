package main

import (
	"context"
	"flag"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/NekroAI/kitestring/internal/client"
)

const arrangeUsage = `用法：ks arrange add <安排> [--until 2h|1d|2026-11-02|18:00]   记一条当前安排（用户口头交代的临时安排）
      ks arrange list [--all]                                    列出本项目生效中的安排（--all 连已到期、已撤销的）
      ks arrange revoke N-x                                      撤销一条
例：ks arrange add "额度紧，做完 R-12、R-13 就停" --until 6h；ks arrange add "这次可以 push"`

// arrangeRow 是 Hub 返回的一条安排（只取打印用的字段）。
type arrangeRow struct {
	Code      string     `json:"code"`
	Text      string     `json:"text"`
	BySession string     `json:"by_session"`
	ByTitle   string     `json:"by_title"`
	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt *time.Time `json:"expires_at"`
	Status    string     `json:"status"`
}

func (a arrangeRow) line() string {
	who := "用户"
	if a.BySession != "" {
		who = a.ByTitle
		if who == "" {
			who = a.BySession
		}
	}
	until := "撤销前一直有效"
	if a.ExpiresAt != nil {
		until = "至 " + a.ExpiresAt.Local().Format("01-02 15:04")
	}
	state := map[string]string{"active": "", "expired": "［已到期］", "revoked": "［已撤销］"}[a.Status]
	return fmt.Sprintf("%s %s「%s」——%s记于 %s，%s", a.Code, state, a.Text, who, a.CreatedAt.Local().Format("01-02 15:04"), until)
}

// runArrange：ks arrange——当前安排（R-245），与 MCP 的 arrange_add / arrange_list / arrange_revoke 一一对应。
func runArrange(args []string) error {
	fs := flag.NewFlagSet("arrange", flag.ContinueOnError)
	session := fs.String("session", "", "会话键（tool:session_id）；默认自动识别当前对话")
	until := fs.String("until", "", "add：到期时间——时长（30m、2h、1d）或日期（2026-11-02、2026-11-02 18:00、18:00）；不填表示撤销前一直有效")
	all := fs.Bool("all", false, "list：连已到期、已撤销的也列出")
	if err := parseAnywhere(fs, args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("%s", arrangeUsage)
	}
	key := currentSession(*session)
	if key == "" {
		return fmt.Errorf("认不出当前会话：请在 Claude Code / Codex 的对话里运行，或用 --session tool:id 指定")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c := client.New(20 * time.Second)
	var one struct {
		Arrangement arrangeRow `json:"arrangement"`
		Message     string     `json:"message"`
	}
	switch fs.Arg(0) {
	case "add":
		text := strings.TrimSpace(strings.Join(fs.Args()[1:], " "))
		if text == "" {
			return fmt.Errorf("要写安排的内容\n%s", arrangeUsage)
		}
		if err := c.Do(ctx, "POST", "/agent/arrangements", map[string]any{"session_key": key, "text": text, "until": *until}, &one); err != nil {
			return err
		}
		fmt.Println(one.Arrangement.line())
		fmt.Println(one.Message)
	case "list", "ls":
		var out struct {
			Arrangements []arrangeRow `json:"arrangements"`
		}
		q := url.Values{"session_key": {key}}
		if *all {
			q.Set("all", "true")
		}
		if err := c.Do(ctx, "GET", "/agent/arrangements?"+q.Encode(), nil, &out); err != nil {
			return err
		}
		if len(out.Arrangements) == 0 {
			fmt.Println("本项目现在没有生效中的安排。")
			return nil
		}
		for _, a := range out.Arrangements {
			fmt.Println(a.line())
		}
	case "revoke", "rm":
		if fs.NArg() < 2 {
			return fmt.Errorf("要写撤销哪条（N-x）\n%s", arrangeUsage)
		}
		if err := c.Do(ctx, "POST", "/agent/arrangements/"+url.PathEscape(fs.Arg(1))+"/revoke", map[string]any{"session_key": key}, &one); err != nil {
			return err
		}
		fmt.Println(one.Message)
	default:
		return fmt.Errorf("不认识的子命令 %q\n%s", fs.Arg(0), arrangeUsage)
	}
	return nil
}
