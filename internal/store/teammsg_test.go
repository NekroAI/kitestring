package store

import (
	"context"
	"path/filepath"
	"testing"
)

// R-249 P3：团队消息的「只告知」存在 fyi 列，一次广播拆成每个收件人一行、用 broadcast_id 归组；
// 老数据按标题 QuietTellQuestion 回填 fyi；SentTells / BroadcastTells 按发送方、按广播查。
func TestTellFYIAndBroadcast(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	dv, err := st.CreateDevice(ctx, "mac", "h1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`INSERT INTO sessions(key, tool, session_id, device_id, state, started_at, last_active_at, idle_at) VALUES('claude:a', 'claude', 'a', ?, 'parked', '', '', 'x'), ('claude:b', 'claude', 'b', ?, 'parked', '', '', 'x')`, dv.ID, dv.ID); err != nil {
		t.Fatal(err)
	}

	quiet, err := st.CreateDecision(ctx, Decision{Kind: KindTell, SessionKey: "claude:a", Question: "团队消息", Answer: "已部署", AssignedBy: "claude:pm", FYI: true})
	if err != nil {
		t.Fatal(err)
	}
	if !quiet.FYI || quiet.BroadcastID != "" {
		t.Fatalf("只告知应存进 fyi 列：%+v", quiet)
	}
	var idle string
	_ = st.DB().QueryRow(`SELECT idle_at FROM sessions WHERE key = 'claude:a'`).Scan(&idle)
	if idle == "" {
		t.Fatal("只告知不应清除待命超时")
	}
	loud, err := st.CreateDecision(ctx, Decision{Kind: KindTell, SessionKey: "claude:b", Question: "团队消息", Answer: "请裁定", AssignedBy: "claude:pm"})
	if err != nil {
		t.Fatal(err)
	}
	_ = st.DB().QueryRow(`SELECT idle_at FROM sessions WHERE key = 'claude:b'`).Scan(&idle)
	if loud.FYI || idle != "" {
		t.Fatalf("要动手的消息 fyi 为假并清除待命超时：%+v idle=%q", loud, idle)
	}
	// fyi 只对团队消息有意义：别的 kind 带了也不存
	note, err := st.CreateDecision(ctx, Decision{Kind: KindNote, Question: "x", Answer: "留言", FYI: true})
	if err != nil || note.FYI {
		t.Fatalf("非团队消息不记 fyi：%+v %v", note, err)
	}

	var bc []Decision
	for _, to := range []string{"claude:a", "claude:b"} {
		d, err := st.CreateDecision(ctx, Decision{Kind: KindTell, SessionKey: to, Question: "团队消息", Answer: "广播", AssignedBy: "claude:pm", BroadcastID: "B1", FYI: true})
		if err != nil {
			t.Fatal(err)
		}
		bc = append(bc, d)
	}
	got, err := st.BroadcastTells(ctx, "B1")
	if err != nil || len(got) != 2 || got[0].Code != bc[0].Code || got[1].SessionKey != "claude:b" || !got[1].FYI {
		t.Fatalf("同一次广播应按编号列出两行：%+v %v", got, err)
	}
	if none, _ := st.BroadcastTells(ctx, ""); len(none) != 0 {
		t.Fatal("空 broadcast_id 不应查出东西")
	}
	sent, err := st.SentTells(ctx, "claude:pm", 10)
	if err != nil || len(sent) != 4 || sent[0].Code != bc[1].Code || sent[3].Code != quiet.Code {
		t.Fatalf("SentTells 应按发送方列出、最近的在前：%+v %v", sent, err)
	}
	if other, _ := st.SentTells(ctx, "claude:a", 10); len(other) != 0 {
		t.Fatal("别人发的不应列出")
	}

	// 老数据：只告知只靠标题区分，补列时按标题回填
	if _, err := st.DB().Exec(`UPDATE decisions SET fyi = 0`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`UPDATE decisions SET question = ? WHERE id = ?`, QuietTellQuestion, quiet.ID); err != nil {
		t.Fatal(err)
	}
	if err := fillTellFYI(st.DB()); err != nil {
		t.Fatal(err)
	}
	q, _ := st.GetDecision(ctx, quiet.Code)
	l, _ := st.GetDecision(ctx, loud.Code)
	if !q.FYI || l.FYI {
		t.Fatalf("按标题回填：%v %v", q.FYI, l.FYI)
	}
}
