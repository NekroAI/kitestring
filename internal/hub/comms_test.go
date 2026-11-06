package hub

import (
	"strings"
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/store"
)

func TestCommsText(t *testing.T) {
	label, text := sysText(store.Decision{Answer: "你关联的需求有变化（status · 用户改需求状态）：用户把 R-267「泳道子任务行显示实际用的模型名（如 Opus 5.5），名字里不再带「· 最强模…」从待验收改为已完成。 只是告诉你，不是派给你的活；需要时自己看，不必回复。"})
	if label != "需求状态变更" {
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

	// R-277 之后的写法：结尾「只是告知」、指派的需求、提出决策、项目设置变更拆分照常
	for _, c := range []struct{ in, label, text string }{
		{"你关联的需求有变化（status · 用户改需求状态）：用户把 R-12「标题」从待验收改为已完成。 只是告知，不是指派；需要时自己查看，不必回复。", "需求状态变更", "用户把 R-12 从待验收改为已完成。"},
		{"这条需求是你指派的（review · 交验收）：R-12「标题」已提交验收（claude:19189640-d44a）：做完了；达标 4 / 5。等用户验收，不必回复；需要细节用 req_get。 只是告知，不是指派；不必回复。", "待验收", "R-12 已提交验收：做完了；达标 4 / 5。"},
		{"你订阅的事件（decision · 有人提决策）：主调度「x」提出了决策 D-3「要不要拆」，等待用户答复。 只是告知，不是指派；需要时自己查看，不必回复。", "新决策", "主调度「x」提出了决策 D-3「要不要拆」，等待用户答复。"},
		{"你订阅的事件（settings · 用户改项目设置）：用户把项目 纸鸢 的「单次等待上限」从 30 分钟改为 55 分钟。之后 ks wait 每次最多等这么久。 只是告知，不是指派；需要时自己查看，不必回复。", "项目设置变更", "用户把项目 纸鸢 的「单次等待上限」从 30 分钟改为 55 分钟。之后 ks wait 每次最多等这么久。"},
	} {
		label, text := sysText(store.Decision{Answer: c.in})
		if got := shrinkReqs(keyParen.ReplaceAllString(text, "")); label != c.label || got != c.text {
			t.Errorf("sysText(%q) = %q %q", c.in, label, got)
		}
	}

	// 会话发的：去掉发送方前缀和开头的需求标题
	body := sentText("PM 「设计通讯」（claude:a） 留言：R-249「纸鸢做跨 harness 的会话通信：查在线会话、…」已合并，开始做通讯页", "claude:a")
	if got := stripReq(body, "R-249"); got != "已合并，开始做通讯页" {
		t.Fatalf("stripReq = %q", got)
	}
}

// D-1171：会话详情里送进来的消息显示成「类别 + 一句话」，不露会话键、事件代号和写给会话的操作提示。
func TestInboxText(t *testing.T) {
	cases := []struct {
		name        string
		a           store.Act
		label, text string
	}{
		{"订阅事件", store.Act{Via: store.KindTell, Req: "R-274", Text: "你订阅的事件（review · 交验收）：R-274「界面文案规范定稿」已交验收（claude:06f4e9db-0825-4b52-aae0-9d37430e68f6）：文案规范已定稿；达标 5/5。等用户验收，不必回复；需要细节用 req_get。"},
			"待验收", "已交验收：文案规范已定稿；达标 5/5。"},
		{"订阅需求", store.Act{Via: store.KindTell, Req: "R-273", Text: "你订阅的需求有变动（只告知）：R-273「会话详情页重新设计」状态从待开始改为进行中（主调度 「方案待批」（claude:06f4e9db-0825-4b52-aae0-9d37430e68f6））。细节见 ks req get R-273；不想再收：ks req unwatch R-273。"},
			"订阅提醒", "状态从待开始改为进行中（主调度 「方案待批」）。"},
		{"会话发的", store.Act{Via: store.KindTell, Peer: "部署", PeerRole: "pm", Req: "R-273", Text: "PM 「会话详情页开发中」（claude:06f4e9db-0825-4b52-aae0-9d37430e68f6） 的子任务「实现 R-273」（abd1a8825cdca7608） 留言：R-273「会话详情页重新设计」R-273 两个取舍，我先按倾向做。有异议回复。"},
			"", "R-273 两个取舍，我先按倾向做。"},
		{"答复原样", store.Act{Via: store.KindDecision, Text: "批准"}, "", "批准"},
	}
	for _, c := range cases {
		label, text := inboxText(c.a)
		if label != c.label || text != c.text {
			t.Errorf("%s：得到 %q %q，应为 %q %q", c.name, label, text, c.label, c.text)
		}
		if strings.Contains(text, "claude:") || strings.Contains(text, "review ·") {
			t.Errorf("%s：还露着内部代号：%q", c.name, text)
		}
	}
}

// R-288：导航上的待送达数按项目分开，点数字进条数最多的项目；发给已结束会话的不算。
func TestCommsPendingByProject(t *testing.T) {
	e := newShareEnv(t)
	d, err := e.st.CreateDevice(e.ctx, "mac2", "h2")
	if err != nil {
		t.Fatal(err)
	}
	pa, err := e.st.ResolveProject(e.ctx, d.ID, "/repo/alpha", nil)
	if err != nil {
		t.Fatal(err)
	}
	pb, err := e.st.ResolveProject(e.ctx, d.ID, "/repo/beta", nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, x := range [][3]string{{"claude:a1", pa, "parked"}, {"claude:b1", pb, "parked"}, {"claude:b2", pb, "ended"}} {
		if _, err := e.st.DB().Exec(`INSERT INTO sessions(key, tool, session_id, device_id, project_id, state, cwd, started_at, last_active_at) VALUES(?, 'claude', ?, ?, ?, ?, '/repo', ?, ?)`, x[0], x[0], d.ID, x[1], x[2], now, now); err != nil {
			t.Fatal(err)
		}
	}
	tell := func(to, pid string) {
		if _, err := e.st.CreateDecision(e.ctx, store.Decision{Kind: store.KindTell, SessionKey: to, ProjectID: pid, Question: "q", Answer: "hi", AssignedBy: e.pm}); err != nil {
			t.Fatal(err)
		}
	}
	tell("claude:a1", pa)
	tell("claude:b1", pb)
	tell("claude:b1", pb)
	tell("claude:b2", pb) // 收件会话已结束：无法送达，不算
	// R-294：只告知、按设计攒着的不算进导航黄标
	if _, err := e.st.CreateDecision(e.ctx, store.Decision{Kind: store.KindTell, SessionKey: "claude:a1", ProjectID: pa, Question: "q", Answer: "fyi", AssignedBy: e.pm, FYI: true}); err != nil {
		t.Fatal(err)
	}
	var out struct {
		Count    int
		Projects []commsPendingProj
	}
	e.do(e.owner, "GET", "/api/comms/pending", nil, &out, 200)
	if out.Count != 3 || len(out.Projects) != 2 {
		t.Fatalf("count=%d projects=%+v", out.Count, out.Projects)
	}
	if out.Projects[0].ProjectID != pb || out.Projects[0].Count != 2 || out.Projects[0].Name != "beta" || out.Projects[1].Count != 1 {
		t.Fatalf("按项目分开不对：%+v", out.Projects)
	}
}
