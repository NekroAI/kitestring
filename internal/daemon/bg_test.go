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
