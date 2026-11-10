package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/NekroAI/kitestring/internal/roles"
)

// runTakeover：ks takeover <身份>——在当前目录（项目目录）启动一个新的 claude（或 --tool codex）会话并带上接任提示（R-224）。
// 新会话按提示运行 ks role <身份>，纸鸢在输出里附上上一任写好的交接，上一任自动交出身份。
func runTakeover(args []string) error {
	fs := flag.NewFlagSet("takeover", flag.ContinueOnError)
	tool := fs.String("tool", "claude", "用哪个工具开新会话：claude | codex")
	dry := fs.Bool("print", false, "只打印要运行的命令，不启动")
	if err := parseAnywhere(fs, args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("用法：ks takeover <身份> [--tool claude|codex] [--print]（身份如 pm、aide、主调度、主助理）")
	}
	role, ok := roles.Resolve(strings.Join(fs.Args(), " "))
	if !ok || role == "" {
		return fmt.Errorf("未知身份 %q：%s", strings.Join(fs.Args(), " "), roleUsageCLI())
	}
	argv, err := takeoverArgv(*tool, role)
	if err != nil {
		return err
	}
	if *dry {
		fmt.Println(shellJoin(argv))
		return nil
	}
	bin, err := exec.LookPath(argv[0])
	if err != nil {
		return fmt.Errorf("找不到 %s：先安装，或用 --tool 换一个工具", argv[0])
	}
	fmt.Fprintf(os.Stderr, "[Kitestring] 在 %s 启动新的 %s 会话，接任%s。\n", mustWd(), argv[0], roles.Display(role))
	return execReplace(bin, argv)
}

// takeoverArgv：启动新会话的命令行（第一个参数是首条消息）。
func takeoverArgv(tool, role string) ([]string, error) {
	switch tool {
	case "claude", "codex":
		return []string{tool, roles.TakeoverPrompt(role)}, nil
	}
	return nil, fmt.Errorf("--tool 只能是 claude 或 codex")
}

func roleUsageCLI() string {
	var b []string
	for _, r := range roles.All {
		b = append(b, r+"（"+roles.Name(r)+"）")
	}
	return "可选 " + strings.Join(b, "、")
}

func mustWd() string {
	wd, _ := os.Getwd()
	return wd
}

// shellJoin 把参数拼成能直接粘贴到终端的一行。
func shellJoin(argv []string) string {
	out := make([]string, len(argv))
	for i, a := range argv {
		if strings.ContainsAny(a, " '\"$`\\!*?;&|<>()（）：，。") {
			a = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		}
		out[i] = a
	}
	return strings.Join(out, " ")
}
