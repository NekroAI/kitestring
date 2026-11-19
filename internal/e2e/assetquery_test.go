package e2e

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/client"
	"github.com/NekroAI/kitestring/internal/hub"
	"github.com/NekroAI/kitestring/internal/protocol"
	"github.com/NekroAI/kitestring/internal/store"
)

// R-391：ks asset list / summary / get 看哪个项目——--project 认项目名（以前只认项目 ID，填名字查不到）、
// 没写时按当前目录的 git remote 找项目（以前只看会话所在项目，会话在别的目录启动就查到空）、再退回会话所在项目；
// 列表带总数和分页，summary 只有统计。
func TestAssetQueryProject(t *testing.T) {
	e := setup(t)
	key := assetSession(t, e, "asset-q")
	if _, err := pushAssets(t, copyFixture(t), key, "S1"); err != nil {
		t.Fatal(err)
	}
	sess := e.session(key)
	proj, err := e.store.GetProject(t.Context(), sess.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	agent := func(path string, out any) error {
		return client.New(20*time.Second).Do(context.Background(), "GET", path, nil, out)
	}
	list := func(q url.Values) hub.AssetListView {
		var out hub.AssetListView
		if err := agent("/agent/assets?"+q.Encode(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	all := list(url.Values{"project": {sess.ProjectID}})
	if all.Total < 3 || len(all.Assets) != all.Total {
		t.Fatalf("按项目 ID：%d / %d", len(all.Assets), all.Total)
	}
	// --project 填项目名
	if byName := list(url.Values{"project": {proj.Name}}); byName.Total != all.Total {
		t.Fatalf("按项目名应查到同样的 %d 件：%d", all.Total, byName.Total)
	}
	if err := agent("/agent/assets?project=no-such-project", &hub.AssetListView{}); err == nil || !strings.Contains(err.Error(), "no-such-project") {
		t.Fatalf("项目名不存在应报错：%v", err)
	}
	// 没写 --project、也没有会话：按当前目录的 remote 找项目
	if proj.Remote != "" {
		if byDir := list(url.Values{"cwd_remote": {proj.Remote}}); byDir.Total != all.Total {
			t.Fatalf("按目录的 remote 应查到 %d 件：%d", all.Total, byDir.Total)
		}
	}
	// 会话所在项目是兜底：目录的 remote 对不上任何项目时退回会话所在项目
	if bySession := list(url.Values{"session_key": {key}, "cwd_remote": {"https://example.invalid/none/none.git"}}); bySession.Total != all.Total {
		t.Fatalf("目录对不上项目时退回会话所在项目：%d", bySession.Total)
	}
	// 分页：总数不变，只返回一页
	page := list(url.Values{"project": {proj.Name}, "limit": {"2"}, "offset": {"1"}})
	if page.Total != all.Total || len(page.Assets) != 2 || page.Assets[0].Code != all.Assets[1].Code {
		t.Fatalf("分页：%d / %d，首条 %v", len(page.Assets), page.Total, page.Assets)
	}
	// summary：统计
	var sum protocol.AssetSummary
	if err := agent("/agent/assets-summary?project="+url.QueryEscape(proj.Name), &sum); err != nil {
		t.Fatal(err)
	}
	if sum.Total != all.Total || sum.Project != proj.Name || sum.Statuses[store.AssetPending] != all.Total || len(sum.Batches) != 1 {
		t.Fatalf("summary：%+v", sum)
	}
	// get 也认项目名
	var d store.AssetDetail
	if err := agent("/agent/assets/"+all.Assets[0].Code+"?project="+url.QueryEscape(proj.Name), &d); err != nil {
		t.Fatal(err)
	}
	if d.Code != all.Assets[0].Code {
		t.Fatalf("get：%+v", d.Asset)
	}
}

// R-391：一次审多件返工，同一会话下一轮只收到一条合并的消息（一个标题行 + 逐件意见）。
func TestAssetReviewMerged(t *testing.T) {
	e := setup(t)
	hub.SetAssetReviewDelay(0)
	key := assetSession(t, e, "asset-merge")
	if _, err := pushAssets(t, copyFixture(t), key, "S1"); err != nil {
		t.Fatal(err)
	}
	var list hub.AssetListView
	e.call("GET", "/api/assets?batch=S1", nil, &list)
	if len(list.Assets) < 3 {
		t.Fatalf("样例里应有 3 件以上：%d", len(list.Assets))
	}
	for i, a := range list.Assets {
		var out reviewOut
		e.call("POST", "/api/assets/"+a.Code+"/review", map[string]any{"verdict": "rework", "comment": "意见" + string(rune('A'+i))}, &out)
	}
	cwd, _ := os.Getwd()
	var got string
	e.waitFor("意见送达", func() bool {
		got = hook(t, "claude", map[string]any{"session_id": "asset-merge", "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "继续", "prompt_id": "p1"})
		return strings.Contains(got, "意见A")
	})
	if n := strings.Count(got, "用户在资源库审核了以下"); n != 1 {
		t.Fatalf("应只有一条合并的消息，实际 %d 条：%s", n, got)
	}
	if !strings.Contains(got, "审核了以下 "+string(rune('0'+len(list.Assets)))+" 件素材") {
		t.Fatalf("标题应写明件数：%s", got)
	}
	for i := range list.Assets {
		if !strings.Contains(got, "意见"+string(rune('A'+i))) {
			t.Fatalf("缺第 %d 条意见：%s", i+1, got)
		}
	}
}
