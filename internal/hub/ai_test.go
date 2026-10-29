package hub

import (
	"testing"

	"github.com/NekroAI/kitestring/internal/store"
)

// 决策短标题只做缩写（R-238 / R-241）：用字大多出自原问题才用，改写成别的意思的丢掉、界面显示原问题。
func TestTitleKeepsMeaning(t *testing.T) {
	q := "评审不做成固定关卡，改成「交给评审了就先等它」，行吗？"
	cases := []struct {
		title string
		ok    bool
	}{
		{"评审延迟后是否直接进验收", false}, // D-714 的真实例子：意思变了
		{"评审改成交给了就先等它", true},
		{"评审不做固定关卡", true},
		{"", false},
	}
	for _, c := range cases {
		if got := store.TitleKeepsMeaning(c.title, q); got != c.ok {
			t.Errorf("%q：应为 %v，实际 %v", c.title, c.ok, got)
		}
	}
	if !store.TitleKeepsMeaning("R-237 上线吗", "R-237 做完了，要不要上线？") {
		t.Error("编号与字母数字按字算，出自原问题的应保留")
	}
}
