package main

import (
	"fmt"
	"strings"

	"github.com/NekroAI/kitestring/internal/assetpush"
	"github.com/NekroAI/kitestring/internal/roles"
)

// runHelp：ks help [服务]（R-359）。不带参数打印总帮助和服务清单；带服务名只打印这项服务的用法——
// 轻接入的会话常驻的只有几行短说明，用到哪项服务时再用它取（每项 roles.ServiceMax 字以内）。
func runHelp(args []string) error {
	if len(args) == 0 {
		fmt.Println(usage)
		fmt.Print("\n按需取用的服务说明（轻接入用）：\n" + roles.ServiceIndex())
		return nil
	}
	// 子页（R-407）：ks help asset compose
	if len(args) == 2 {
		if page, ok := helpPage(args[0], args[1]); ok {
			fmt.Print(page)
			return nil
		}
	}
	s, ok := roles.FindService(strings.Join(args, " "))
	if !ok {
		return fmt.Errorf("没有服务 %q，可选：\n%s", strings.Join(args, " "), roles.ServiceIndex())
	}
	fmt.Println(s.Text)
	return nil
}

// helpPages 是服务下的子页：细节放子页，服务主页只写一行指过去，控制常驻和按需取用的字数。
var helpPages = map[string]map[string]string{
	"asset": {"compose": assetpush.ComposeHelp},
}

func helpPage(service, page string) (string, bool) {
	s, ok := roles.FindService(service)
	if !ok {
		return "", false
	}
	text, ok := helpPages[s.Name][strings.ToLower(strings.TrimSpace(page))]
	return text, ok
}
