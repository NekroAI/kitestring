package hub

import (
	"reflect"
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/protocol"
	"github.com/NekroAI/kitestring/internal/store"
)

// R-245：一轮提交的合入判定。补丁相同的 cherry-pick 由 daemon 认出；合入时解决过冲突的 cherry-pick（补丁对不上），
// 按 main 上关联到同一需求、标题相同、更晚的那份算已合入；标题不同、或 main 上那份更早的不算。
func TestRoundPlaces(t *testing.T) {
	at := func(d int) time.Time { return time.Date(2026, 10, d, 10, 0, 0, 0, time.UTC) }
	cs := []store.ChainCommit{
		{Link: "cf8c6860000", At: at(29)}, // 分支上的提交
		{Link: "aaaaaaa0000", At: at(29)}, // 分支上另一个提交，main 上没有对应的
		{Link: "05df2540000", At: at(31)}, // 合入 main 时解决过冲突的 cherry-pick
		{Link: "bbbbbbb0000", At: at(30)}, // 本机找不到
	}
	place := map[string]protocol.CommitPlace{
		"cf8c6860000": {Subject: "等你的事都进「要你处理」（R-243）", Main: protocol.PlaceNo, Deploy: protocol.PlaceNo},
		"aaaaaaa0000": {Subject: "另一处改动（R-243）", Main: protocol.PlaceNo, Deploy: protocol.PlaceNo},
		"05df2540000": {Subject: "等你的事都进「要你处理」（R-243）", Main: protocol.PlaceAncestor, Deploy: protocol.PlaceAncestor},
		"bbbbbbb0000": {Main: protocol.PlaceUnknown, Deploy: protocol.PlaceUnknown},
	}
	notMain, missing, notDep := roundPlaces(cs, place)
	if !reflect.DeepEqual(notMain, []string{"aaaaaaa"}) || !reflect.DeepEqual(missing, []string{"bbbbbbb"}) || !reflect.DeepEqual(notDep, []string{"aaaaaaa"}) {
		t.Fatalf("notMain=%v missing=%v notDep=%v", notMain, missing, notDep)
	}
	// main 上同标题的那份更早：不能拿来顶后面的提交
	cs[2].At = at(28)
	if notMain, _, _ := roundPlaces(cs, place); !reflect.DeepEqual(notMain, []string{"cf8c686", "aaaaaaa"}) {
		t.Fatalf("更早的同标题提交不算：%v", notMain)
	}
	// daemon 认出 cherry-pick -x 的来源记录（picked）：算已合入、已部署
	if notMain, _, notDep := roundPlaces(cs[1:2], map[string]protocol.CommitPlace{"aaaaaaa0000": {Main: protocol.PlacePicked, Deploy: protocol.PlacePicked}}); len(notMain)+len(notDep) != 0 {
		t.Fatalf("picked 应算已合入、已部署：%v %v", notMain, notDep)
	}
	// 已合入 main 但部署的版本里还没有
	place["05df2540000"] = protocol.CommitPlace{Subject: place["05df2540000"].Subject, Main: protocol.PlacePatch, Deploy: protocol.PlaceNo}
	if notMain, _, notDep := roundPlaces(cs[2:3], place); len(notMain) != 0 || !reflect.DeepEqual(notDep, []string{"05df254"}) {
		t.Fatalf("合入没部署：%v %v", notMain, notDep)
	}
}
