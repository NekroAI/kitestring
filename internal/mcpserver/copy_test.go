package mcpserver

import (
	"testing"

	"github.com/NekroAI/kitestring/internal/copyrules"
)

// 给会话看的状态名与界面同一套术语（docs/copywriting-rules.json，R-274）。
func TestStatusLabelsFollowCopyRules(t *testing.T) {
	r, err := copyrules.Load()
	if err != nil {
		t.Fatal(err)
	}
	var bad []string
	bad = append(bad, r.CheckLabels("presenceLabel", presenceLabel, false, "presence")...)
	bad = append(bad, r.CheckLabels("reqStatusZh", reqStatusZh, true, "req")...)
	for _, b := range bad {
		t.Error(b)
	}
}
