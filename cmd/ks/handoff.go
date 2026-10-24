package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

const handoffUsage = `用法：ks handoff --doing 在做的 --next 下一步 [--waiting 等用户的] [--rules 约定和规矩] [--notes 注意事项]
      ks handoff --file 交接.json|交接.md|-   （JSON 键 doing / waiting / rules / next / notes；markdown 用「## 在做的」「## 等用户的」「## 约定和规矩」「## 下一步」「## 注意事项」分栏）
示例：ks handoff --doing "R-12 做到联调，分支 line3" --next "跑全量检查后交验收" --notes "别停 5199 的 pnpm dev"`

// handoffFields：交接的五栏，和 MCP 的 handoff_write 一致（R-224）。
type handoffFields struct {
	Doing, Waiting, Rules, Next, Notes string
}

func (f *handoffFields) set(name, v string) {
	switch name {
	case "doing":
		f.Doing = v
	case "waiting":
		f.Waiting = v
	case "rules":
		f.Rules = v
	case "next":
		f.Next = v
	case "notes":
		f.Notes = v
	}
}

// handoffHeading：把 markdown 标题 / JSON 键认成栏目名，认不出返回空串。
func handoffHeading(s string) string {
	s = strings.ToLower(strings.Trim(strings.TrimSpace(s), "#*：: "))
	for name, keys := range map[string][]string{
		"doing":   {"doing", "在做"},
		"waiting": {"waiting", "等用户"},
		"rules":   {"rules", "约定", "规矩"},
		"next":    {"next", "下一步"},
		"notes":   {"notes", "注意"},
	} {
		for _, k := range keys {
			if strings.HasPrefix(s, k) {
				return name
			}
		}
	}
	return ""
}

// parseHandoffFile：JSON（以 { 开头）或 markdown 分栏。
func parseHandoffFile(raw []byte) (handoffFields, error) {
	var f handoffFields
	text := strings.TrimSpace(string(raw))
	if strings.HasPrefix(text, "{") {
		m := map[string]any{}
		if err := json.Unmarshal([]byte(text), &m); err != nil {
			return f, fmt.Errorf("交接 JSON 格式不对：%w", err)
		}
		for k, v := range m {
			if name := handoffHeading(k); name != "" {
				f.set(name, strings.TrimSpace(fmt.Sprint(v)))
			}
		}
		return f, nil
	}
	cur := ""
	buf := map[string][]string{}
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			cur = handoffHeading(line)
			continue
		}
		if cur != "" {
			buf[cur] = append(buf[cur], line)
		}
	}
	for name, lines := range buf {
		f.set(name, strings.TrimSpace(strings.Join(lines, "\n")))
	}
	return f, nil
}

// runHandoff：ks handoff——没有 handoff_write 工具（MCP 是旧版）时的兜底，写法和拒收条件与它一致（由 Hub 判定）。
func runHandoff(args []string) error {
	fs := flag.NewFlagSet("handoff", flag.ContinueOnError)
	session := fs.String("session", "", "会话键（tool:session_id）；默认自动识别当前对话")
	file := fs.String("file", "", "交接文件：JSON 或 markdown，- 表示从标准输入读")
	var f handoffFields
	fs.StringVar(&f.Doing, "doing", "", "在做的：需求编号、做到哪、分支 / worktree")
	fs.StringVar(&f.Waiting, "waiting", "", "等用户的：待答决策、待验收")
	fs.StringVar(&f.Rules, "rules", "", "约定和规矩")
	fs.StringVar(&f.Next, "next", "", "下一步：接任后先做什么")
	fs.StringVar(&f.Notes, "notes", "", "注意事项：坑、在跑的进程、别碰的东西")
	if err := parseAnywhere(fs, args); err != nil {
		return err
	}
	if *file != "" {
		var raw []byte
		var err error
		if *file == "-" {
			raw, err = io.ReadAll(os.Stdin)
		} else {
			raw, err = os.ReadFile(*file)
		}
		if err != nil {
			return err
		}
		ff, err := parseHandoffFile(raw)
		if err != nil {
			return err
		}
		// 命令行参数优先于文件
		for _, p := range [][2]*string{{&f.Doing, &ff.Doing}, {&f.Waiting, &ff.Waiting}, {&f.Rules, &ff.Rules}, {&f.Next, &ff.Next}, {&f.Notes, &ff.Notes}} {
			if *p[0] == "" {
				*p[0] = *p[1]
			}
		}
	}
	if strings.TrimSpace(f.Doing) == "" && strings.TrimSpace(f.Next) == "" {
		return fmt.Errorf("至少写上在做的（--doing）和下一步（--next）\n%s", handoffUsage)
	}
	key := currentSession(*session)
	if key == "" {
		return fmt.Errorf("认不出当前会话：请在 Claude Code / Codex 的对话里运行，或用 --session tool:id 指定")
	}
	return agentCall("POST", "/agent/handoff", map[string]any{"session_key": key, "doing": f.Doing, "waiting": f.Waiting, "rules": f.Rules, "next": f.Next, "notes": f.Notes})
}
