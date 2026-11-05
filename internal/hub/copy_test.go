package hub

import (
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
