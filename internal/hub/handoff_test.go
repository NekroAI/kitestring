package hub

import (
	"strings"
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/store"
)

// R-224：交接通知按身份写——主调度先停派新活、有子任务时写明要等；其它身份不提子任务。
func TestHandoffAsk(t *testing.T) {
	pm := handoffAsk("pm", "纸鸢", 2)
	for _, want := range []string{"先停派新活", "还有 2 个子任务在跑", "handoff_write", "ks takeover pm", "不用再挂 ks wait"} {
		if !strings.Contains(pm, want) {
			t.Fatalf("主调度的交接通知缺 %q：%s", want, pm)
		}
	}
	if aide := handoffAsk("aide", "纸鸢", 3); strings.Contains(aide, "子任务") || !strings.Contains(aide, "ks takeover aide") {
		t.Fatalf("主助理的交接通知不该提子任务：%s", aide)
	}
}

// 注入的交接只列写了的栏目，并要求新会话回一句确认。
func TestHandoffText(t *testing.T) {
	at := time.Date(2026, 10, 25, 9, 30, 0, 0, time.Local)
	got := handoffText(store.Handoff{FromTitle: "撤销遮罩验收", Doing: "R-1 做到一半", Next: "交验收", WrittenAt: &at}, "pm")
	for _, want := range []string{"上一任主调度（PM）「撤销遮罩验收」", "10-25 09:30", "### 在做的\nR-1 做到一半", "### 下一步\n交验收", "先回用户一句确认"} {
		if !strings.Contains(got, want) {
			t.Fatalf("交接内容缺 %q：\n%s", want, got)
		}
	}
	if strings.Contains(got, "等用户的") || strings.Contains(got, "注意事项") {
		t.Fatalf("没写的栏目不该出现：\n%s", got)
	}
}

// R-224 / R-225：建议压缩阈值默认 30 万，超出范围收回到上下限。
func TestCompactTokens(t *testing.T) {
	for in, want := range map[int]int{0: 300_000, -1: 300_000, 10: 50_000, 450_000: 450_000, 9_000_000: 2_000_000} {
		if got := (Settings{CompactTokens: in}).compactTokens(); got != want {
			t.Fatalf("compactTokens(%d) = %d，期望 %d", in, got, want)
		}
	}
}

// 没有 handoff_write 工具（MCP 是旧版）时，交接通知写明改用 ks handoff 并附用法示例。
func TestHandoffAskCLIFallback(t *testing.T) {
	for _, role := range []string{"pm", "aide"} {
		got := handoffAsk(role, "纸鸢", 0)
		for _, want := range []string{"没有 handoff_write 工具", "ks handoff --doing", "--next"} {
			if !strings.Contains(got, want) {
				t.Fatalf("%s 的交接通知缺 %q：%s", role, want, got)
			}
		}
	}
}
