package daemon

import (
	"encoding/json"
	"testing"
)

// 样本取自 Claude Code 2.1.285 的真实 Stop 输入（后台运行 sleep 时结束本轮）。
const stopWithBackground = `{"background_tasks":[{"id":"bri53u1it","type":"shell","status":"running","description":"Run sleep and echo in background","command":"sleep 20 && echo done"},{"id":"x2","type":"shell","status":"completed","command":"ls"}],"hook_event_name":"Stop"}`

func TestBackgroundTasks(t *testing.T) {
	var p map[string]any
	if err := json.Unmarshal([]byte(stopWithBackground), &p); err != nil {
		t.Fatal(err)
	}
	n, brief := backgroundTasks(p)
	if n != 1 || brief != "Run sleep and echo in background" {
		t.Fatalf("得到 %d / %q，期望只计运行中的 1 个", n, brief)
	}
	if n, _ := backgroundTasks(map[string]any{"background_tasks": []any{}}); n != 0 {
		t.Fatal("空列表应为 0")
	}
}

// R-162：从进程表认后台 shell。样本取自 Claude Code 2.1.285 后台运行命令时的真实进程命令行。
func TestShellProcLabel(t *testing.T) {
	claude := `/bin/zsh -c source /Users/x/.claude/shell-snapshots/snapshot-zsh-1.sh 2>/dev/null || true && setopt NO_EXTENDED_GLOB NO_BARE_GLOB_QUAL 2>/dev/null || true && eval 'sleep 45; echo '\''done'\''' < /dev/null && pwd -P >| /tmp/claude-1eb3-cwd`
	if !isShellProc(claude) || procLabel(claude) != "sleep 45; echo 'done'" {
		t.Fatalf("Claude Code 的 shell：%v %q", isShellProc(claude), procLabel(claude))
	}
	if !matchCall(claude, &shellCall{cmd: "sleep 45; echo 'done'"}) {
		t.Fatal("带单引号的命令应能对上")
	}
	codex := `/bin/zsh -lc pnpm dev --host`
	if !isShellProc(codex) || procLabel(codex) != "pnpm dev --host" {
		t.Fatalf("Codex 的 shell：%q", procLabel(codex))
	}
	for _, s := range []string{"node /x/server.js", "/usr/bin/python3 -c print(1)", "zsh"} {
		if isShellProc(s) {
			t.Fatalf("%q 不是 shell 命令", s)
		}
	}
	for _, s := range []string{"/bin/zsh -c eval 'ks wait --timeout 2h'", "/bin/sh -c /Users/x/.local/bin/ks hook claude Stop", "/bin/sh -c npx -y some-mcp-server"} {
		if !ignoreProcRe.MatchString(s) {
			t.Fatalf("%q 应排除", s)
		}
	}
	if parseEtime("1-02:03:04") != (26*3600+3*60+4)*1e9 || parseEtime("05:06") != 306*1e9 {
		t.Fatal("etime 解析不对")
	}
}
