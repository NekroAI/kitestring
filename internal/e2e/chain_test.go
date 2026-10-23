package e2e

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/client"
	"github.com/NekroAI/kitestring/internal/store"
)

// 决策后果链（R-154）：新建需求、提新决策时可声明来源决策；从一条决策能看到它产生的需求（声明的、来源里写了编号的）与提交，
// 以及取代（撤回时的 replaced_by）、推翻 / 接着的关系；什么都没有的旧决策返回空链。
func TestDecisionChain(t *testing.T) {
	e := setup(t)
	cwd, _ := os.Getwd()
	c := client.New(10 * time.Second)
	ctx := context.Background()
	hook(t, "claude", map[string]any{"session_id": "dev", "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "上线", "prompt_id": "p0"})
	e.waitFor("会话入库", func() bool { return e.session("claude:dev").ProjectID != "" })

	raise := func(body map[string]any) (store.Decision, error) {
		body["session_key"] = "claude:dev"
		var d store.Decision
		err := c.Do(ctx, "POST", "/agent/decisions", body, &d)
		return d, err
	}
	mustRaise := func(body map[string]any) store.Decision {
		t.Helper()
		d, err := raise(body)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	createReq := func(body map[string]any) (store.Requirement, error) {
		body["session_key"] = "claude:dev"
		var r store.Requirement
		err := c.Do(ctx, "POST", "/agent/requirements", body, &r)
		return r, err
	}
	chain := func(code string) store.DecisionChain {
		var ch store.DecisionChain
		e.call("GET", "/api/decisions/"+code+"/chain", nil, &ch)
		return ch
	}

	old := mustRaise(map[string]any{"question": "旧决策：要不要加暗色模式", "options": []string{"要", "不要"}})
	if ch := chain(old.Code); ch.From != nil || ch.ReplacedBy != nil || len(ch.Followups)+len(ch.Replaces)+len(ch.Requirements) != 0 {
		t.Fatalf("没有关系的旧决策应是空链：%+v", ch)
	}

	a := mustRaise(map[string]any{"question": "会话收进折叠区还是作为主体", "options": []string{"A 主体", "B 折叠"}})
	e.call("POST", "/api/decisions/answers", map[string]any{"answers": []map[string]any{{"ref": a.Code, "answer": "B 折叠"}}}, nil)

	// 声明来源决策的需求、来源里写了编号的需求；编号只是前缀（D-x0）的不算
	r1, err := createReq(map[string]any{"title": "会话折叠区", "from_decision": strings.ToLower(a.Code)})
	if err != nil || r1.FromDecision != a.Code {
		t.Fatalf("新建需求应记下来源决策 %s：%v %q", a.Code, err, r1.FromDecision)
	}
	r2, _ := createReq(map[string]any{"title": "折叠区计数", "source": a.Code + " 第 2 项"})
	r3, _ := createReq(map[string]any{"title": "无关需求", "source": a.Code + "0 第 1 项"})
	if _, err := createReq(map[string]any{"title": "乱写来源", "from_decision": "D-9999"}); err == nil || !strings.Contains(err.Error(), "from_decision") {
		t.Fatalf("来源决策不存在应被拒：%v", err)
	}
	if err := c.Do(ctx, "POST", "/agent/requirements/"+r1.Code+"/evidence", map[string]any{"session_key": "claude:dev", "kind": "commit", "link": "abc1234", "summary": "feat: 会话折叠区\n细节"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.Do(ctx, "POST", "/agent/requirements/"+r1.Code+"/evidence", map[string]any{"session_key": "claude:dev", "kind": "test", "summary": "测试通过"}, nil); err != nil {
		t.Fatal(err)
	}

	// 推翻、接着；overturns 必须带 from_decision；来源决策要存在
	if _, err := raise(map[string]any{"question": "改回主体", "overturns": true}); err == nil || !strings.Contains(err.Error(), "from_decision") {
		t.Fatalf("推翻却没写推翻谁应被拒：%v", err)
	}
	if _, err := raise(map[string]any{"question": "x", "from_decision": "D-9999"}); err == nil {
		t.Fatal("来源决策不存在应被拒")
	}
	b := mustRaise(map[string]any{"question": "会话改回主体", "from_decision": a.Code, "overturns": true})
	cdec := mustRaise(map[string]any{"question": "折叠区默认收起几条", "from_decision": a.Code})
	if !b.Overturns || b.FromDecision != a.Code || cdec.Overturns || cdec.FromDecision != a.Code {
		t.Fatalf("决策应记下来源与是否推翻：%+v %+v", b, cdec)
	}
	// 取代：撤回时指向新决策
	dd := mustRaise(map[string]any{"question": "要不要改回主体（初稿）"})
	if err := c.Do(ctx, "POST", "/agent/decisions/"+dd.Code+"/withdraw", map[string]any{"session_key": "claude:dev", "reason": "问法不清", "replaced_by": b.Code}, nil); err != nil {
		t.Fatal(err)
	}

	ch := chain(a.Code)
	if len(ch.Requirements) != 2 || ch.Requirements[0].Code != r1.Code || ch.Requirements[0].Relation != "from" || ch.Requirements[1].Code != r2.Code || ch.Requirements[1].Relation != "source" {
		t.Fatalf("%s 产生的需求应是 %s（声明）和 %s（来源），不含 %s：%+v", a.Code, r1.Code, r2.Code, r3.Code, ch.Requirements)
	}
	if cm := ch.Requirements[0].Commits; len(cm) != 1 || cm[0].Link != "abc1234" || cm[0].Summary != "feat: 会话折叠区" {
		t.Fatalf("需求上的提交应进后果链（只取 commit 类、首行）：%+v", cm)
	}
	if len(ch.Followups) != 2 || ch.Followups[0].Code != b.Code || !ch.Followups[0].Overturns || ch.Followups[1].Code != cdec.Code || ch.Followups[1].Overturns {
		t.Fatalf("%s 应被 %s 推翻、由 %s 接着：%+v", a.Code, b.Code, cdec.Code, ch.Followups)
	}
	chb := chain(b.Code)
	if chb.From == nil || chb.From.Code != a.Code || !chb.From.Overturns || len(chb.Replaces) != 1 || chb.Replaces[0].Code != dd.Code {
		t.Fatalf("%s 应推翻了 %s、取代了 %s：%+v", b.Code, a.Code, dd.Code, chb)
	}
	if chd := chain(dd.Code); chd.ReplacedBy == nil || chd.ReplacedBy.Code != b.Code {
		t.Fatalf("%s 应被 %s 取代：%+v", dd.Code, b.Code, chd.ReplacedBy)
	}
	// 决策关联的需求算「针对」
	q := mustRaise(map[string]any{"question": "折叠区放左边还是右边", "requirement": r1.Code})
	if chq := chain(q.Code); len(chq.Requirements) != 1 || chq.Requirements[0].Relation != "about" {
		t.Fatalf("决策关联的需求应标为针对：%+v", chq.Requirements)
	}
	// Agent 也能读（复盘用）
	var ach store.DecisionChain
	if err := c.Do(ctx, "GET", "/agent/decisions/"+a.Code+"/chain", nil, &ach); err != nil || len(ach.Followups) != 2 {
		t.Fatalf("Agent 读后果链：%v %+v", err, ach)
	}
}
