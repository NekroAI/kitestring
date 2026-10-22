package main

// M1 临时探针：验证 Claude Code / Codex 的 hook 输入字段与 additionalContext 注入行为。
// 结论写入 docs/hook-matrix.md 后删除本文件。

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	probeEnvPrefix = regexp.MustCompile(`^(CLAUDE|CODEX|ANTHROPIC|OPENAI|TERM_PROGRAM|TMUX)`)
	// 只有白名单变量记录原值，其余只记名字，避免把不明内容（如 CODEX_REMOTE_PAYLOAD）落盘。
	probeEnvValueAllow = map[string]bool{
		"CLAUDECODE": true, "CLAUDE_CODE_ENTRYPOINT": true, "CLAUDE_CODE_SESSION_ID": true,
		"CLAUDE_PROJECT_DIR": true, "CLAUDE_PID": true, "CODEX_HOME": true, "TERM_PROGRAM": true,
	}
)

type probeRecord struct {
	Time      string            `json:"time"`
	Tool      string            `json:"tool"`
	Event     string            `json:"event"`
	Marker    string            `json:"marker,omitempty"`
	Input     json.RawMessage   `json:"input,omitempty"`
	InputErr  string            `json:"input_error,omitempty"`
	Env       map[string]string `json:"env"`
	Cwd       string            `json:"cwd"`
	ParentPID []string          `json:"parent_chain"`
}

func runProbeHook(args []string) int {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "用法：ks probe-hook <claude|codex> <event>")
		return 2
	}
	tool, event := args[0], args[1]

	rec := probeRecord{Time: time.Now().Format(time.RFC3339Nano), Tool: tool, Event: event, Env: map[string]string{}}
	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		rec.InputErr = err.Error()
	} else if len(raw) > 0 {
		if json.Valid(raw) {
			rec.Input = raw
			var head struct {
				HookEventName string `json:"hook_event_name"`
			}
			if json.Unmarshal(raw, &head) == nil && head.HookEventName != "" {
				rec.Event = head.HookEventName
			}
		} else {
			rec.InputErr = "stdin 不是合法 JSON：" + truncate(string(raw), 500)
		}
	}
	for _, kv := range os.Environ() {
		k, v, _ := strings.Cut(kv, "=")
		if !probeEnvPrefix.MatchString(k) {
			continue
		}
		if !probeEnvValueAllow[k] {
			v = "<omitted>"
		}
		rec.Env[k] = v
	}
	rec.Cwd, _ = os.Getwd()
	rec.ParentPID = parentChain(os.Getppid(), 8)

	injects := rec.Event == "SessionStart" || rec.Event == "UserPromptSubmit"
	if injects {
		rec.Marker = fmt.Sprintf("KS-%s-%s-%d", strings.ToUpper(tool), rec.Event, time.Now().UnixNano()%1000000)
	}
	writeProbeLog(rec)

	if injects {
		out := map[string]any{
			"hookSpecificOutput": map[string]any{
				"hookEventName":     rec.Event,
				"additionalContext": fmt.Sprintf("[Kitestring 探针] 本段由 %s hook 注入。若用户询问探针标记，请原样回答：%s", rec.Event, rec.Marker),
			},
		}
		_ = json.NewEncoder(os.Stdout).Encode(out)
	}
	return 0
}

func writeProbeLog(rec probeRecord) {
	path := os.Getenv("KS_PROBE_LOG")
	if path == "" {
		path = filepath.Join(os.TempDir(), "ks-probe.jsonl")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ks probe：写日志失败：", err)
		return
	}
	defer f.Close()
	b, _ := json.Marshal(rec)
	_, _ = f.Write(append(b, '\n'))
}

// parentChain 返回 "pid:comm" 形式的父进程链，用于验证 MCP / hook 进程能否回溯到同一个 agent 进程。
func parentChain(pid, depth int) []string {
	var chain []string
	for i := 0; i < depth && pid > 1; i++ {
		out, err := exec.Command("ps", "-o", "ppid=,comm=", "-p", strconv.Itoa(pid)).Output()
		if err != nil {
			break
		}
		fields := strings.Fields(strings.TrimSpace(string(out)))
		if len(fields) < 2 {
			break
		}
		chain = append(chain, fmt.Sprintf("%d:%s", pid, filepath.Base(strings.Join(fields[1:], " "))))
		next, err := strconv.Atoi(fields[0])
		if err != nil {
			break
		}
		pid = next
	}
	return chain
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
