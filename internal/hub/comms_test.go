package hub

import (
	"testing"

	"github.com/NekroAI/kitestring/internal/store"
)

func TestCommsText(t *testing.T) {
	label, text := sysText(store.Decision{Answer: "你关联的需求有变化（status · 用户改需求状态）：用户把 R-267「泳道子任务行显示实际用的模型名（如 Opus 5.5），名字里不再带「· 最强模…」从待验收改为已完成。 只是告诉你，不是派给你的活；需要时自己看，不必回复。"})
	if label != "用户改需求状态" {
		t.Fatalf("label = %q", label)
	}
	if got := shrinkReqs(keyParen.ReplaceAllString(text, "")); got != "用户把 R-267 从待验收改为已完成。" {
		t.Fatalf("text = %q", got)
	}

	_, text = sysText(store.Decision{Answer: "你订阅的事件（review · 交验收）：R-204「进程已经退出的会话不再显示「运行中」：工作台按进程是否还在判断会话状态」已交验收（claude:19189640-d44a-4b7f-a4cf-d0d47bdb18b4）：重启 daemon 后，已退出进程的会话也会被结束；达标 4/5。等用户验收，不必回复；需要细节用 req_get。"})
	if got := shrinkReqs(keyParen.ReplaceAllString(text, "")); got != "R-204 已交验收：重启 daemon 后，已退出进程的会话也会被结束；达标 4/5。" {
		t.Fatalf("text = %q", got)
	}

	label, text = sysText(store.Decision{Question: watchTellQ, Answer: "你订阅的需求有变动（只告知）：R-12「标题」状态从进行中改为待验收（主调度）。细节见 ks req get R-12；不想再收：ks req unwatch R-12。"})
	if label != "订阅提醒" || shrinkReqs(text) != "R-12 状态从进行中改为待验收（主调度）。" {
		t.Fatalf("watch = %q %q", label, shrinkReqs(text))
	}

	// 会话发的：去掉发送方前缀和开头的需求标题
	body := sentText("PM 「设计通讯」（claude:a） 留言：R-249「纸鸢做跨 harness 的会话通信：查在线会话、…」已合并，开始做通讯页", "claude:a")
	if got := stripReq(body, "R-249"); got != "已合并，开始做通讯页" {
		t.Fatalf("stripReq = %q", got)
	}
}
