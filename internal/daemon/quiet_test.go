package daemon

import (
	"strings"
	"testing"

	"github.com/NekroAI/kitestring/internal/protocol"
)

// R-227：只告知的团队消息（Quiet）不算“有新消息”，不会结束 ks wait；要动手的照常；
// 下一轮开始（用户发消息）或因别的消息唤醒时，只告知的单列一段一并附上，送达后不再补发。
func TestQuietTellDoesNotWakeButIsAttached(t *testing.T) {
	d := newQDaemon(t, t.TempDir())
	defer d.q.close()
	key := "claude:s1"
	d.decisions = []protocol.Decision{
		{ID: "1", Kind: "tell", SessionKey: key, Answer: "已部署 v1.2，daemon 已重启", Quiet: true},
	}
	d.mu.Lock()
	woken := d.hasFreshLocked(key)
	d.mu.Unlock()
	if woken {
		t.Fatal("只告知的消息不应唤醒 ks wait")
	}
	d.mu.Lock()
	d.decisions = append(d.decisions, protocol.Decision{ID: "2", Kind: "tell", SessionKey: key, Answer: "请裁定：分页 20 还是 50"})
	woken = d.hasFreshLocked(key)
	d.mu.Unlock()
	if !woken {
		t.Fatal("要动手的消息应唤醒 ks wait")
	}
	ev := protocol.Event{Name: "UserPromptSubmit", Tool: "claude", SessionID: "s1", TurnID: "t1"}
	text := d.injection(ev)
	i, j := strings.Index(text, "请裁定"), strings.Index(text, "已部署 v1.2")
	if i < 0 || j < i || !strings.Contains(text, "只告知") {
		t.Fatalf("唤醒时只告知的应单列在要动手的之后一并附上：%s", text)
	}
	if again := d.injection(protocol.Event{Name: "UserPromptSubmit", Tool: "claude", SessionID: "s1", TurnID: "t2"}); strings.Contains(again, "已部署") {
		t.Fatalf("送达过的不应补发：%s", again)
	}
}
