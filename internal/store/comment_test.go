package store

import (
	"context"
	"path/filepath"
	"testing"
)

// 会话留言（R-249）：kind = comment，直接是已关闭（不送达），和用户留言一起进需求的 feedback（时间倒序），署名是留言的会话和身份。
func TestCreateComment(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	r, err := st.CreateRequirement(ctx, Requirement{Title: "留言测试", Status: ReqInProgress, Priority: "P2"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateComment(ctx, r, Comment{By: "claude:dev"}); err == nil {
		t.Fatal("空留言应当报错")
	}
	if _, err := st.CreateComment(ctx, r, Comment{Text: "没署名"}); err == nil {
		t.Fatal("没署名的留言应当报错")
	}
	note, err := st.CreateDecision(ctx, Decision{Kind: KindNote, RequirementID: r.ID, Question: r.Title, Answer: "用户留言"})
	if err != nil {
		t.Fatal(err)
	}
	c, err := st.CreateComment(ctx, r, Comment{By: "claude:dev", Role: "dev", Agent: "sub-1", Text: "  接口改成分页，细节见下  "})
	if err != nil {
		t.Fatal(err)
	}
	if c.Kind != KindComment || c.Status != DecClosed || c.AssignedBy != "claude:dev" || c.FromRole != "dev" || c.FromAgent != "sub-1" || c.Answer != "接口改成分页，细节见下" || c.RequirementCode != r.Code {
		t.Fatalf("留言记录不对：%+v", c)
	}
	full, err := st.GetRequirement(ctx, r.Code)
	if err != nil {
		t.Fatal(err)
	}
	if len(full.Feedback) != 2 || full.Feedback[0].Code != c.Code || full.Feedback[1].Code != note.Code {
		t.Fatalf("feedback 应当按时间倒序含会话留言和用户留言：%+v", full.Feedback)
	}
	// 不进用户待决策
	pend, err := st.ListDecisions(ctx, []string{DecPending, DecAnswered}, -1)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range pend {
		if d.Code == c.Code {
			t.Fatalf("会话留言不该是待送达 / 待决策：%+v", d)
		}
	}
}
