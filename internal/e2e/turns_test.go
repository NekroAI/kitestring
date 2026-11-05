package e2e

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/client"
	"github.com/NekroAI/kitestring/internal/store"
)

type turnsBody struct {
	Turns []struct {
		ID        string `json:"id"`
		Source    string `json:"source"`
		Prompt    string `json:"prompt"`
		PromptID  string `json:"prompt_id"`
		ReplyID   string `json:"reply_id"`
		Reply     string `json:"reply"`
		ReplyKind string `json:"reply_kind"`
		Items     []struct {
			ID   string `json:"id"`
			Kind string `json:"kind"`
			Req  string `json:"req"`
			Dec  string `json:"dec"`
			To   string `json:"to"`
			Via  string `json:"via"`
		} `json:"items"`
		Inbox []struct {
			ID       string `json:"id"`
			Web      bool   `json:"web"`
			Text     string `json:"text"`
			Delivery *struct {
				Status string `json:"status"`
			} `json:"delivery"`
		} `json:"inbox"`
		Tools struct {
			N     int `json:"n"`
			Names []struct {
				Name string `json:"name"`
				N    int    `json:"n"`
			} `json:"names"`
		} `json:"tools"`
	} `json:"turns"`
	More    bool   `json:"more"`
	Before  string `json:"before"`
	Objects []struct {
		Code    string   `json:"code"`
		Kind    string   `json:"kind"`
		Pending bool     `json:"pending"`
		Items   []string `json:"items"`
	} `json:"objects"`
	Pending []struct {
		ID       string `json:"id"`
		Text     string `json:"text"`
		Delivery struct {
			Status  string `json:"status"`
			Command string `json:"command"`
		} `json:"delivery"`
	} `json:"pending"`
}

// R-273：会话按轮的时间线——每轮的提问、工具汇总、完整回复；纸鸢动作只来自明确记录（状态变化、提决策）；
// 对象索引带相关条目、待答决策在最前；往上分页；网页发的话返回送达状态（待命 woke、下线 offline 附接任命令），送进某一轮后标「网页」。
func TestSessionTurns(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	sid, key := "turns-1", "claude:turns-1"
	turns := func(q string) turnsBody {
		var b turnsBody
		e.call("GET", "/api/sessions/"+url.PathEscape(key)+"/turns"+q, nil, &b)
		return b
	}
	c := client.New(30 * time.Second)

	// 第一轮：你在终端说的话 → 两次工具 → 回复
	hook(t, "claude", map[string]any{"session_id": sid, "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "先看 R-1"})
	e.waitFor("会话入库", func() bool { return e.session(key).Key != "" })
	var req struct {
		Code string `json:"code"`
	}
	e.call("POST", "/api/requirements", map[string]any{"title": "按轮时间线", "status": "ready"}, &req)
	if err := c.Do(context.Background(), "POST", "/agent/sessions/"+url.PathEscape(key)+"/link", map[string]any{"requirement": req.Code}, nil); err != nil {
		t.Fatal(err)
	}
	hook(t, "claude", map[string]any{"session_id": sid, "cwd": cwd, "hook_event_name": "PreToolUse", "tool_name": "Bash", "tool_input": map[string]any{"command": "go test ./..."}})
	hook(t, "claude", map[string]any{"session_id": sid, "cwd": cwd, "hook_event_name": "PreToolUse", "tool_name": "Read", "tool_input": map[string]any{"file_path": "/x/a.go"}})
	var dec struct {
		Code string `json:"code"`
	}
	if err := c.Do(context.Background(), "POST", "/agent/decisions", map[string]any{"session_key": key, "question": "选哪套", "options": []string{"A", "B"}, "requirement": req.Code}, &dec); err != nil {
		t.Fatal(err)
	}
	reply := "## 做完了\n\n" + strings.Repeat("长", 3000) + " 见 " + req.Code
	hook(t, "claude", map[string]any{"session_id": sid, "cwd": cwd, "hook_event_name": "Stop", "last_assistant_message": reply})
	e.waitFor("一轮结束", func() bool { return len(turns("").Turns) == 1 && turns("").Turns[0].Reply != "" })

	b := turns("")
	tn := b.Turns[0]
	if tn.Source != "terminal" || tn.Prompt != "先看 R-1" || tn.ReplyKind != "full" || tn.Reply != reply {
		t.Fatalf("第一轮不对：%+v", tn)
	}
	if tn.Tools.N != 2 || len(tn.Tools.Names) != 2 {
		t.Fatalf("工具汇总不对：%+v", tn.Tools)
	}
	kinds := map[string]bool{}
	for _, it := range tn.Items {
		kinds[it.Kind] = true
		if it.Kind == store.ActStatus && (it.Req != req.Code || it.Via != "link") {
			t.Fatalf("开始做的记录不对：%+v", it)
		}
	}
	if !kinds[store.ActStatus] || !kinds[store.ActRaise] {
		t.Fatalf("纸鸢动作应有开始做和提决策：%+v", tn.Items)
	}
	// 对象索引：待答决策在最前；需求带回复里提到它的那一轮
	if len(b.Objects) < 2 || b.Objects[0].Code != dec.Code || !b.Objects[0].Pending {
		t.Fatalf("待答决策应排最前：%+v", b.Objects)
	}
	for _, o := range b.Objects {
		if o.Code == req.Code && !contains(o.Items, "r:"+tn.ReplyID) {
			t.Fatalf("需求的相关条目应包含提到它的回复：%+v", o)
		}
	}

	// 网页发的话：待命时 woke；送进下一轮后标「网页」
	go func() { _ = c.Do(context.Background(), "GET", "/wait?session_key="+key+"&timeout=5s", nil, nil) }()
	e.waitFor("挂起", func() bool { return e.session(key).Parked })
	var sent struct {
		ID       string `json:"id"`
		Delivery struct {
			Status string `json:"status"`
		} `json:"delivery"`
	}
	e.call("POST", "/api/sessions/"+url.PathEscape(key)+"/reply", map[string]any{"comment": "合进 main"}, &sent)
	if sent.ID == "" || sent.Delivery.Status != "woke" {
		t.Fatalf("待命时发的话应为已叫醒：%+v", sent)
	}
	e.waitFor("消息送达", func() bool {
		d, err := e.store.GetDecision(context.Background(), sent.ID)
		return err == nil && d.Status == store.DecDelivered
	})
	hook(t, "claude", map[string]any{"session_id": sid, "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "<task-notification>ks wait 结束</task-notification>"})
	e.waitFor("第二轮", func() bool { return len(turns("").Turns) == 2 })
	t2 := turns("").Turns[1]
	if t2.Source != "web" || len(t2.Inbox) != 1 || !t2.Inbox[0].Web || t2.Inbox[0].Text != "合进 main" || t2.Inbox[0].Delivery == nil || t2.Inbox[0].Delivery.Status != "woke" {
		t.Fatalf("网页发的话应送进第二轮、标网页、它在处理：%+v", t2)
	}
	hook(t, "claude", map[string]any{"session_id": sid, "cwd": cwd, "hook_event_name": "Stop", "last_assistant_message": "好"})
	e.waitFor("第二轮结束", func() bool { return turns("").Turns[1].Reply == "好" })
	if d := turns("").Turns[1].Inbox[0].Delivery; d == nil || d.Status != "delivered" {
		t.Fatalf("这一轮结束后应为已送达：%+v", d)
	}

	// 分页：limit=1 先给最新一轮，before 取更早的
	p1 := turns("?limit=1")
	if len(p1.Turns) != 1 || !p1.More || p1.Before == "" || p1.Turns[0].Reply != "好" {
		t.Fatalf("第一页不对：%+v", p1)
	}
	p2 := turns("?limit=1&before=" + url.QueryEscape(p1.Before))
	if len(p2.Turns) != 1 || p2.More || p2.Turns[0].Prompt != "先看 R-1" {
		t.Fatalf("第二页不对：%+v", p2)
	}

	// 下线：发的话排队，附接任命令
	if _, err := e.store.DB().Exec(`UPDATE sessions SET role = 'pm', offline_at = ?, parked = 0 WHERE key = ?`, time.Now().UTC().Format(time.RFC3339Nano), key); err != nil {
		t.Fatal(err)
	}
	var off struct {
		Delivery struct {
			Status  string `json:"status"`
			Command string `json:"command"`
		} `json:"delivery"`
	}
	e.call("POST", "/api/sessions/"+url.PathEscape(key)+"/reply", map[string]any{"comment": "在吗"}, &off)
	if off.Delivery.Status != "offline" || off.Delivery.Command != "ks takeover pm" {
		t.Fatalf("下线时应提示接任：%+v", off)
	}
	b = turns("")
	if len(b.Pending) != 1 || b.Pending[0].Text != "在吗" || b.Pending[0].Delivery.Command != "ks takeover pm" {
		t.Fatalf("没送进任何一轮的网页消息应在 pending：%+v", b.Pending)
	}
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
