//go:build !windows

package e2e

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/client"
	"github.com/NekroAI/kitestring/internal/protocol"
	"github.com/NekroAI/kitestring/internal/store"
)

// fakeAgent 起一个名叫 claude / codex 的脚本进程，它在下面再起几个 shell 子进程（模拟后台命令），返回脚本进程。
func fakeAgent(t *testing.T, name string, shells ...string) *exec.Cmd {
	t.Helper()
	dir := t.TempDir()
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	for _, s := range shells {
		b.WriteString("/bin/sh -c '" + s + "' &\n")
	}
	b.WriteString("wait\n")
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(b.String()), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/sh", path)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
	})
	return cmd
}

// shellPid 找 agent 进程下命令行含 marker 的 shell 子进程。
func shellPid(t *testing.T, agent int, marker string) int {
	t.Helper()
	for i := 0; i < 50; i++ {
		out, _ := exec.Command("ps", "-A", "-ww", "-o", "pid=,ppid=,command=").Output()
		for _, line := range strings.Split(string(out), "\n") {
			f := strings.Fields(line)
			if len(f) >= 3 && f[1] == strconv.Itoa(agent) && strings.Contains(line, marker) {
				pid, _ := strconv.Atoi(f[0])
				return pid
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("没找到 %s 的 shell 子进程", marker)
	return 0
}

func tasksOf(s store.Session, kind string) []store.BgTask {
	var out []store.BgTask
	for _, t := range s.Tasks {
		if t.Kind == kind {
			out = append(out, t)
		}
	}
	return out
}

// R-162：会话下能看到在跑的后台 shell 与子代理。后台 shell 由 daemon 扫进程表得到：
// run_in_background 的命令用它的描述做标签，前台调用还在跑的不算，ks wait（待命）与 ks hook / mcp 不算；
// 进程结束后 10 秒内消失；Codex 同样可用。子代理按 agent_id 记，描述取父会话 Agent 调用，工具调用记在它名下、不进主会话。
func TestBackgroundTasks(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	c := client.New(5 * time.Second)
	ctx := context.Background()
	send := func(tool string, ppid int, sid, name string, extra map[string]any) {
		t.Helper()
		p := map[string]any{"session_id": sid, "cwd": cwd, "hook_event_name": name}
		for k, v := range extra {
			p[k] = v
		}
		var resp protocol.HookResponse
		if err := c.Do(ctx, "POST", "/hook", protocol.HookRequest{Tool: tool, Event: name, Payload: p, PPID: ppid}, &resp); err != nil {
			t.Fatal(err)
		}
	}

	bgCmd := "sleep 60; echo r162-bg"
	fgCmd := "sleep 61; echo r162-fg"
	agent := fakeAgent(t, "claude", bgCmd, fgCmd, "sleep 62; echo ks wait --timeout 2h", "sleep 63; echo ks mcp")
	pid := agent.Process.Pid
	bgPid := shellPid(t, pid, "r162-bg")
	shellPid(t, pid, "r162-fg")

	key := "claude:bg-1"
	send("claude", pid, "bg-1", "UserPromptSubmit", map[string]any{"prompt": "开工"})
	// 后台命令：PreToolUse 带 run_in_background，PostToolUse 立刻回来
	bgIn := map[string]any{"tool_name": "Bash", "tool_use_id": "tu-bg", "tool_input": map[string]any{"command": bgCmd, "description": "跑后台测试", "run_in_background": true}}
	send("claude", pid, "bg-1", "PreToolUse", bgIn)
	send("claude", pid, "bg-1", "PostToolUse", bgIn)
	// 前台命令还在跑：没有 PostToolUse
	send("claude", pid, "bg-1", "PreToolUse", map[string]any{"tool_name": "Bash", "tool_use_id": "tu-fg", "tool_input": map[string]any{"command": fgCmd}})

	e.waitFor("后台 shell 出现", func() bool { return len(tasksOf(e.session(key), "shell")) > 0 })
	time.Sleep(4 * time.Second) // 再扫一轮，确认不会多出别的
	shells := tasksOf(e.session(key), "shell")
	if len(shells) != 1 || shells[0].Label != "跑后台测试" || shells[0].StartedAt.IsZero() {
		t.Fatalf("应只有一个后台 shell，标签为描述：%+v", shells)
	}
	if time.Since(shells[0].StartedAt) > time.Minute {
		t.Fatalf("开始时间不对：%v", shells[0].StartedAt)
	}

	// 前台命令结束（PostToolUse）后进程还活着：变成后台（例如被转到后台）
	send("claude", pid, "bg-1", "PostToolUse", map[string]any{"tool_name": "Bash", "tool_use_id": "tu-fg"})
	e.waitFor("前台转后台", func() bool { return len(tasksOf(e.session(key), "shell")) == 2 })

	// 后台命令结束：10 秒内消失
	if err := syscall.Kill(bgPid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	gone := time.Now()
	e.waitFor("后台 shell 消失", func() bool {
		for _, x := range tasksOf(e.session(key), "shell") {
			if x.Label == "跑后台测试" {
				return false
			}
		}
		return true
	})
	if d := time.Since(gone); d > 10*time.Second {
		t.Fatalf("结束后 %v 才消失，超过 10 秒", d)
	}

	// 子代理：描述来自父会话的 Agent 调用；它的工具调用记在它名下，不进主会话的最近动作
	send("claude", pid, "bg-1", "PreToolUse", map[string]any{"tool_name": "Agent", "tool_use_id": "tu-ag", "tool_input": map[string]any{"description": "盘点批准类动作", "subagent_type": "Explore", "prompt": "不该上报的提示词"}})
	send("claude", pid, "bg-1", "SubagentStart", map[string]any{"agent_id": "ag-1", "agent_type": "Explore"})
	send("claude", pid, "bg-1", "PreToolUse", map[string]any{"agent_id": "ag-1", "agent_type": "Explore", "tool_name": "Read", "tool_input": map[string]any{"file_path": "/x/app/plans.tsx"}})
	e.waitFor("子代理出现", func() bool {
		a := tasksOf(e.session(key), "agent")
		return len(a) == 1 && a[0].LastAction != ""
	})
	s := e.session(key)
	a := tasksOf(s, "agent")[0]
	if a.ID != "ag-1" || a.Label != "盘点批准类动作" || a.AgentType != "Explore" || a.LastAction != "Read · plans.tsx" || len(a.Ticks) != 1 {
		t.Fatalf("子代理记录不对：%+v", a)
	}
	for _, r := range s.RecentTools {
		if strings.Contains(r.Label, "plans.tsx") {
			t.Fatalf("子代理的工具调用不应进主会话的最近动作：%+v", s.RecentTools)
		}
	}
	// 时间线刻度（R-170）：主会话自己的三次调用（两次 Bash、一次 Agent），子代理的 Read 不算
	if len(s.Ticks) != 3 {
		t.Fatalf("主会话时间线应有 3 个刻度（不含子代理）：%v", s.Ticks)
	}
	if strings.Contains(s.LastTool, "plans.tsx") {
		t.Fatalf("子代理的工具调用不应改主会话的最近动作：%s", s.LastTool)
	}
	send("claude", pid, "bg-1", "SubagentStop", map[string]any{"agent_id": "ag-1", "agent_type": "Explore"})
	e.waitFor("子代理结束后消失", func() bool { return len(tasksOf(e.session(key), "agent")) == 0 })

	// 会话结束：全部清空
	send("claude", pid, "bg-1", "SessionEnd", nil)
	e.waitFor("会话结束清空", func() bool { s := e.session(key); return s.State == store.StateEnded && len(s.Tasks) == 0 })

	// Codex：命令执行完一轮仍没退出的 shell 也是后台任务，标签取命令
	cx := fakeAgent(t, "codex", "sleep 64; echo r162-codex")
	shellPid(t, cx.Process.Pid, "r162-codex")
	cxIn := map[string]any{"tool_name": "Bash", "tool_use_id": "cx-1", "tool_input": map[string]any{"command": "sleep 64; echo r162-codex"}}
	send("codex", cx.Process.Pid, "cx-1", "UserPromptSubmit", map[string]any{"prompt": "开工"})
	send("codex", cx.Process.Pid, "cx-1", "PreToolUse", cxIn)
	time.Sleep(4 * time.Second)
	if n := len(tasksOf(e.session("codex:cx-1"), "shell")); n != 0 {
		t.Fatalf("前台命令还在跑时不应算后台：%d", n)
	}
	send("codex", cx.Process.Pid, "cx-1", "PostToolUse", cxIn)
	e.waitFor("Codex 后台 shell 出现", func() bool {
		sh := tasksOf(e.session("codex:cx-1"), "shell")
		return len(sh) == 1 && sh[0].Label == "sleep 64; echo r162-codex"
	})
}
