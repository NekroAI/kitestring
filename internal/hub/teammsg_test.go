package hub

import (
	"strings"
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/store"
)

// R-249 P3：送达状态只从记录推——消息行已标送达 → delivered；收件会话已结束 / 失联 / 标完成 → offline；
// 其余 → pending_wake，原因来自 wakeAbility（马上叫醒 / 这一轮结束后 / Codex 等用户下次发消息），只告知的写明不叫醒。
func TestDeliveryOf(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	later := now.Add(20 * time.Minute)
	got := now.Add(-time.Minute)
	queued := store.Decision{Code: "D-1", SessionKey: "k", Status: store.DecAnswered}
	fyi := store.Decision{Code: "D-2", SessionKey: "k", Status: store.DecAnswered, FYI: true}
	done := store.Decision{Code: "D-3", SessionKey: "k", Status: store.DecDelivered, DeliveredAt: &got}
	cases := []struct {
		name   string
		d      store.Decision
		x      store.Session
		ok     bool
		status string
		why    string
	}{
		{"已送达", done, store.Session{Tool: "claude", State: store.StateEnded}, true, DeliveredSt, "已送达（"},
		{"Claude 挂后台 wait：马上叫醒", queued, store.Session{Tool: "claude", State: store.StateParked, Parked: true}, true, PendingWakeSt, "马上叫醒（挂着后台 ks wait）"},
		{"Codex 前台 wait：马上叫醒", queued, store.Session{Tool: "codex", Client: "cli", State: store.StateParked, Parked: true, ParkedUntil: &later}, true, PendingWakeSt, "马上叫醒（挂着前台 ks wait"},
		{"Codex 没挂 wait：等用户下次发消息", queued, store.Session{Tool: "codex", Client: "app-server", State: store.StateWaitReply}, true, PendingWakeSt, "Codex 不能后台叫醒，等用户下次发消息"},
		{"在跑：这一轮结束后", queued, store.Session{Tool: "claude", State: store.StateRunning}, true, PendingWakeSt, "这一轮结束后收到"},
		{"只告知 + 待命：不叫醒", fyi, store.Session{Tool: "claude", State: store.StateParked, Parked: true}, true, PendingWakeSt, "只告知、不叫醒它（它挂着后台 ks wait）"},
		{"只告知 + Codex 没挂 wait", fyi, store.Session{Tool: "codex", State: store.StateWaitReply}, true, PendingWakeSt, "只告知、不叫醒：Codex 不能后台叫醒"},
		{"对方已结束", queued, store.Session{Tool: "claude", State: store.StateEnded}, true, OfflineSt, "会话已结束，离线"},
		{"对方失联", queued, store.Session{Tool: "codex", State: store.StateLost}, true, OfflineSt, "失联"},
		{"用户标完成", queued, store.Session{Tool: "claude", State: store.StateWaitReply, DoneAt: &got}, true, OfflineSt, "标为已完成"},
		{"会话不存在", queued, store.Session{}, false, OfflineSt, "不存在"},
		{"已关闭", store.Decision{Code: "D-4", SessionKey: "k", Status: store.DecClosed}, store.Session{Tool: "claude", State: store.StateParked, Parked: true}, true, OfflineSt, "已关闭"},
	}
	for _, c := range cases {
		e := deliveryOf(c.d, c.x, c.ok, now)
		if e.Status != c.status || !strings.Contains(e.Reason, c.why) || e.StatusLabel != deliveryLabel[c.status] || e.Code != c.d.Code {
			t.Errorf("%s：%+v，应为 %s 且原因含「%s」", c.name, e, c.status, c.why)
		}
	}
	if e := deliveryOf(queued, store.Session{Tool: "codex", Client: "cli", State: store.StateWaitReply}, true, now); e.Tool != "Codex 终端" {
		t.Errorf("工具应写显示名：%+v", e)
	}
	line := deliveryLine(deliveryOf(queued, store.Session{Key: "k", Tool: "codex", Client: "app-server", Role: "advisor", State: store.StateWaitReply}, true, now))
	if line != "- D-1 → 顾问 k（Codex App）：待送达——Codex 不能后台叫醒，等用户下次发消息" {
		t.Errorf("收件人一行：%s", line)
	}
}

// 广播的身份过滤：逗号 / 顿号分隔，认中文名和别称，none 表示没身份的会话；认不出报错。
func TestParseRoleFilter(t *testing.T) {
	f, err := parseRoleFilter("dev，评审、none")
	if err != nil || !f["dev"] || !f["reviewer"] || !f[""] || len(f) != 3 {
		t.Fatalf("解析不对：%v %v", f, err)
	}
	if f, err := parseRoleFilter(" "); err != nil || len(f) != 0 {
		t.Fatalf("空的不过滤：%v %v", f, err)
	}
	if _, err := parseRoleFilter("dev,老板"); err == nil {
		t.Fatal("认不出的身份应报错")
	}
}

// 查已发消息时取正文：按会话键定位，标题里的冒号不影响。
func TestSentText(t *testing.T) {
	for in, want := range map[string]string{
		"主调度 「接入：PM」（claude:a） 留言：R-1 好了":                      "R-1 好了",
		"主调度 「接入：PM」（claude:a） 广播：已部署":                         "已部署",
		"会话 「x」（claude:a） 留言（原本发给主助理；本项目现在没有主助理，请你处理或问用户）：问一下": "问一下",
		"主调度 「x」（claude:a） 回复 D-3：好":                           "好",
	} {
		if got := sentText(in, "claude:a"); got != want {
			t.Errorf("%s → %q，应为 %q", in, got, want)
		}
	}
}
