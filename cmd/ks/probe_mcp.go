package main

// M1 临时探针：最小 stdio MCP server，验证 MCP 调用能否与 PreToolUse hook 对齐到同一会话（Q9）。
// 结论写入 docs/hook-matrix.md 后删除本文件。

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type probeDecisionArgs struct {
	Question string   `json:"question" jsonschema:"需要用户决策的问题"`
	Options  []string `json:"options,omitempty" jsonschema:"候选选项"`
}

type probeDecisionOut struct {
	DecisionID string `json:"decision_id"`
}

type probeMCPRecord struct {
	Time     string          `json:"time"`
	Tool     string          `json:"tool"`
	Args     json.RawMessage `json:"args"`
	Meta     any             `json:"meta,omitempty"`
	Client   any             `json:"client,omitempty"`
	EnvNames []string        `json:"env_names"`
	PPID     []string        `json:"parent_chain"`
}

func runProbeMCP(args []string) int {
	tool := "unknown"
	if len(args) > 0 {
		tool = args[0]
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "ksprobe", Version: "0.0.1"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "decision_raise", Description: "向用户提出一个需要决策的问题（Kitestring 探针）"},
		func(ctx context.Context, req *mcp.CallToolRequest, in probeDecisionArgs) (*mcp.CallToolResult, probeDecisionOut, error) {
			rec := probeMCPRecord{
				Time:     time.Now().Format(time.RFC3339Nano),
				Tool:     tool,
				Args:     req.Params.Arguments,
				PPID:     parentChain(os.Getppid(), 4),
				EnvNames: []string{},
			}
			if req.Params.Meta != nil {
				rec.Meta = req.Params.Meta
			}
			if p := req.Session.InitializeParams(); p != nil {
				rec.Client = p.ClientInfo
			}
			for _, kv := range os.Environ() {
				k, _, _ := strings.Cut(kv, "=")
				if probeEnvPrefix.MatchString(k) {
					rec.EnvNames = append(rec.EnvNames, k)
				}
			}
			writeProbeMCPLog(rec)
			return nil, probeDecisionOut{DecisionID: fmt.Sprintf("DEC-%d", time.Now().UnixNano()%100000)}, nil
		})
	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		fmt.Fprintln(os.Stderr, "ks probe-mcp：", err)
		return 1
	}
	return 0
}

func writeProbeMCPLog(rec probeMCPRecord) {
	path := os.Getenv("KS_PROBE_MCP_LOG")
	if path == "" {
		path = os.TempDir() + "/ks-probe-mcp.jsonl"
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	b, _ := json.Marshal(rec)
	_, _ = f.Write(append(b, '\n'))
}
