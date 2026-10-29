package daemon

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/NekroAI/kitestring/internal/protocol"
)

func newQDaemon(t *testing.T, dir string) *Daemon {
	q, err := openQueue(filepath.Join(dir, "daemon.db"))
	if err != nil {
		t.Fatal(err)
	}
	return &Daemon{q: q, tasks: newTaskState(), instances: map[string]string{}, dshInst: map[string]map[string]bool{}, seeded: map[string][]string{}, latest: map[string]string{}, staleWarn: map[string]bool{}, kick: make(chan struct{}, 1), log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

// R-203：PreToolUse(Agent) 与 SubagentStart 之间 daemon 重启，名字仍能配上。
func TestSubagentNameSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	d1 := newQDaemon(t, dir)
	pre := protocol.Event{Name: "PreToolUse", ToolName: "Agent", ToolUseID: "tu1"}
	d1.noteTaskEvent("claude:s1", map[string]any{"tool_input": map[string]any{"description": "审查 daemon 重启", "subagent_type": "general-purpose"}}, &pre)
	d1.q.close() // 部署重启

	d2 := newQDaemon(t, dir)
	defer d2.q.close()
	start := protocol.Event{Name: "SubagentStart", AgentType: "general-purpose", AgentID: "a1"}
	d2.noteTaskEvent("claude:s1", map[string]any{}, &start)
	if start.ToolBrief != "审查 daemon 重启" {
		t.Fatalf("重启后名字丢了：%q", start.ToolBrief)
	}
	// 配过一次就消耗掉
	again := protocol.Event{Name: "SubagentStart", AgentType: "general-purpose", AgentID: "a2"}
	d2.noteTaskEvent("claude:s1", map[string]any{}, &again)
	if again.ToolBrief != "" {
		t.Fatalf("同一条描述不该配两次：%q", again.ToolBrief)
	}
}

// 配不上时取子代理记录里的第一句提示词。
func TestSubagentNameFallbackPrompt(t *testing.T) {
	dir := t.TempDir()
	tp := filepath.Join(dir, "sess.jsonl")
	sub := filepath.Join(dir, "sess", "subagents")
	os.MkdirAll(sub, 0o755)
	os.WriteFile(filepath.Join(sub, "agent-a9.jsonl"), []byte(`{"type":"user","message":{"role":"user","content":"Read-only audit of the web UI。然后汇报"}}`+"\n"), 0o644)
	if got := subagentPromptName(tp, "a9"); got != "Read-only audit of the web UI" {
		t.Fatalf("得到 %q", got)
	}
	if subagentPromptName(tp, "nope") != "" {
		t.Fatal("没有记录应为空")
	}
	d := newQDaemon(t, dir)
	defer d.q.close()
	start := protocol.Event{Name: "SubagentStart", AgentID: "a9"}
	d.noteTaskEvent("claude:s1", map[string]any{"transcript_path": tp}, &start)
	if start.ToolBrief != "Read-only audit of the web UI" {
		t.Fatalf("兜底名字：%q", start.ToolBrief)
	}
}
