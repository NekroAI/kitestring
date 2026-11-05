package hub

import (
	"sort"
	"strings"
	"testing"

	"github.com/NekroAI/kitestring/internal/copyrules"
)

// 后端生成、显示在网页上的状态标签按 docs/copywriting.md 的术语表（docs/copywriting-rules.json，与前端共用），R-274。
func TestStatusLabelsFollowCopyRules(t *testing.T) {
	r, err := copyrules.Load()
	if err != nil {
		t.Fatal(err)
	}
	var bad []string
	bad = append(bad, r.CheckLabels("deliveryLabel", deliveryLabel, true, "delivery")...)
	bad = append(bad, r.CheckLabels("availLabel", availLabel, true, "roster")...)
	bad = append(bad, r.CheckLabels("reqStatusZh", reqStatusZh, true, "req")...)
	for _, b := range bad {
		t.Error(b)
	}
}

// copy.go 里只给网页和推送看的文字逐条过一遍文案规则（与前端 copy.test.ts 同一份规则），R-277。
func TestCopyFollowsCopyRules(t *testing.T) {
	r, err := copyrules.Load()
	if err != nil {
		t.Fatal(err)
	}
	texts := copyTexts()
	if len(texts) < 60 {
		t.Fatalf("只取到 %d 条文字，copyTexts 可能漏了", len(texts))
	}
	keys := make([]string, 0, len(texts))
	for k := range texts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if bad := r.Check(texts[k]); len(bad) > 0 {
			t.Errorf("%s「%s」：%s", k, texts[k], strings.Join(bad, "；"))
		}
	}
}

// 推送标题按 copywriting.md 3.7：事件名 + 空格 + 编号，不用「·」连接。
func TestPushTitles(t *testing.T) {
	for got, want := range map[string]string{
		pushDecisionTitle("D-12"):                "新决策 D-12",
		pushReviewTitle("R-12"):                  "待验收 R-12",
		pushStageTitle("P-3", 2):                 "待验收 P-3 阶段 2",
		pushPlanTitle("P-3", false):              "待批准 P-3",
		pushSessionTitle(pushWaitReply, "纸鸢"):    "待回复「纸鸢」",
		pushDeviceLostTitle("MacBook"):           "设备离线「MacBook」",
		pushReviewBody("标题", 2, 3):               "评审通过 2 / 3：标题",
		pushSessionBody("Codex", "MacBook", " "): "「MacBook」上的 Codex",
	} {
		if got != want {
			t.Errorf("得到「%s」，应为「%s」", got, want)
		}
	}
}
