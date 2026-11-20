package main

import (
	"strings"
	"testing"

	"github.com/NekroAI/kitestring/internal/protocol"
)

// R-411：summary 的类别统计带上中文名（英文 key 仍在，--category 填 key）；没写中文名的照旧。
func TestSummaryCategoryNames(t *testing.T) {
	sum := protocol.AssetSummary{Project: "rollcall-squad", Total: 18, Statuses: map[string]int{"pending": 18}, Categories: map[string]int{"prop": 15, "vehicle": 3},
		CategoryNames: map[string]protocol.AssetCategoryName{"prop": {Name: "道具"}}}
	got := summaryText(sum)
	if !strings.Contains(got, "道具（prop） 15") || !strings.Contains(got, "vehicle 3") {
		t.Errorf("类别统计应带中文名、没写的照旧：\n%s", got)
	}
	sum.CategoryNames = nil
	if got := summaryText(sum); !strings.Contains(got, "prop 15") || strings.Contains(got, "（prop）") {
		t.Errorf("没有中文名时应照旧：\n%s", got)
	}
}
