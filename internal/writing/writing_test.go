package writing

import (
	"strings"
	"testing"
)

func TestCommentAndMessageHint(t *testing.T) {
	if CommentHint(strings.Repeat("字", CommentMax)) != "" || MessageHint(strings.Repeat("字", MessageMax)) != "" {
		t.Fatal("没超标不该提示")
	}
	if h := CommentHint(strings.Repeat("字", CommentMax+1)); !strings.Contains(h, "601 字") || !strings.Contains(h, "已保存") {
		t.Fatalf("留言超标提示不对：%s", h)
	}
	if h := MessageHint(strings.Repeat("字", MessageMax+1)); !strings.Contains(h, "121 字") || !strings.Contains(h, "细节写到 ks req comment") || !strings.Contains(h, "编号加一句话") {
		t.Fatalf("消息超标提示不对：%s", h)
	}
}

func TestFirstSentence(t *testing.T) {
	cases := map[string]string{
		"登录页支持记住我。单测 12 个通过":        "登录页支持记住我",
		"**结论：** 列表不再闪烁\n- 标准 1 通过": "列表不再闪烁",
		"## 结论：按钮变大了！细节…":           "按钮变大了",
		"  只有一句没有句号  ":              "只有一句没有句号",
	}
	for in, want := range cases {
		if got := FirstSentence(in); got != want {
			t.Errorf("FirstSentence(%q) = %q，应为 %q", in, got, want)
		}
	}
}

func TestEvidenceHint(t *testing.T) {
	if h := EvidenceHint("登录页支持记住我。\n1. 勾选后重开仍登录：通过（图 1）"); h != "" {
		t.Fatalf("合规证据不应有提示：%s", h)
	}
	long := strings.Repeat("长", ConclusionMax+1) + "。后面"
	if h := EvidenceHint(long); !strings.Contains(h, "第一句结论") || strings.Contains(h, "全文") {
		t.Fatalf("结论超长应只提示结论：%s", h)
	}
	if h := EvidenceHint(strings.Repeat("长", ConclusionMax)); h != "" {
		t.Fatalf("结论正好 %d 字不应提示：%s", ConclusionMax, h)
	}
	body := "结论很短。\n" + strings.Repeat("字", EvidenceMax)
	if h := EvidenceHint(body); !strings.Contains(h, "全文") || strings.Contains(h, "第一句结论") || !strings.HasPrefix(h, "已保存") {
		t.Fatalf("全文超长应提示压缩全文：%s", h)
	}
}

func TestDecisionHint(t *testing.T) {
	if h := DecisionHint("首页用卡片还是列表？", "结论：推荐卡片。"); h != "" {
		t.Fatalf("合规决策不应有提示：%s", h)
	}
	if h := DecisionHint(strings.Repeat("问", QuestionMax+1), ""); !strings.Contains(h, "问题") {
		t.Fatalf("问题超长应提示：%s", h)
	}
	if h := DecisionHint("要不要做？", strings.Repeat("说", DecisionContextMax+1)); !strings.Contains(h, "说明") || strings.Contains(h, "问题 ") {
		t.Fatalf("说明超长应只提示说明：%s", h)
	}
	if h := DecisionHint("要不要做？", strings.Repeat("说", DecisionContextMax)); h != "" {
		t.Fatalf("说明正好 %d 字不应提示：%s", DecisionContextMax, h)
	}
}
