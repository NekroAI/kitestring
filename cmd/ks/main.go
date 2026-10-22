// ks 是 Kitestring 的单二进制入口，按子命令承担 hub / daemon / hook / mcp 等角色（见 docs/decisions.md D12）。
package main

import (
	"fmt"
	"os"

	"github.com/NekroAI/kitestring/internal/version"
)

const usage = `用法：ks <命令> [参数]

命令：
  version              打印版本
  probe-hook <tool> <event>   M1 临时探针：记录 hook 输入并回显注入标记（验证完即删除）
  probe-mcp <tool>            M1 临时探针：最小 stdio MCP server（验证完即删除）
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "version":
		fmt.Printf("ks %s (%s)\n", version.Version, version.Commit)
	case "probe-hook":
		os.Exit(runProbeHook(os.Args[2:]))
	case "probe-mcp":
		os.Exit(runProbeMCP(os.Args[2:]))
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
}
