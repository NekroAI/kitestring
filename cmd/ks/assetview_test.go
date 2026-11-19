package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/protocol"
	"github.com/NekroAI/kitestring/internal/store"
)

// 1000 件素材的模拟数据（rollcall-squad 一类的项目：8 个类别、带多个批次和预览图）。
func fakeAssets(n int) ([]store.Asset, store.AssetFacets) {
	cats := []string{"weapon", "character", "vehicle", "prop", "env", "ui", "vfx", "audio"}
	f := store.AssetFacets{Counts: map[string]int{}, CategoryCounts: map[string]int{}}
	var out []store.Asset
	for i := 0; i < n; i++ {
		cat := cats[i%len(cats)]
		st := store.AssetStatuses[i%len(store.AssetStatuses)]
		var previews []store.AssetPreview
		for j := 0; j < 4; j++ {
			previews = append(previews, store.AssetPreview{View: []string{"front", "side", "back", "top"}[j], FileRef: store.FileRef{ID: fmt.Sprintf("f%d-%d", i, j), Name: fmt.Sprintf("preview-%d.png", j)}})
		}
		v := &store.AssetVersion{Version: 3, Previews: previews, Triangles: 12000, Meta: json.RawMessage(`{"lod":2,"notes":"` + strings.Repeat("长文字", 40) + `"}`)}
		out = append(out, store.Asset{Code: fmt.Sprintf("A-%d", i+1), UID: fmt.Sprintf("core:%s.item_%04d", cat, i), Category: cat, Status: st, Version: 3,
			Batches: []string{"G11e1", "G12"}, Latest: v, CreatedAt: time.Now()})
		f.Counts[st]++
		f.CategoryCounts[cat]++
	}
	return out, f
}

// R-391 验收 2：1000 件素材下，list 默认文字不超过 4KB、--json 默认不超过 20KB，summary 不超过 1KB；--all / --full 能取全部。
func TestAssetOutputBounded(t *testing.T) {
	all, facets := fakeAssets(1000)
	page := all[:assetListLimit] // Hub 按 limit 只给一页

	txt := listText(1000, 0, page, facets, true)
	if len(txt) > 4096 {
		t.Fatalf("list 默认文字 %d 字节，超过 4KB", len(txt))
	}
	if !strings.Contains(txt, "符合条件 1000 件") || !strings.Contains(txt, "--offset") {
		t.Fatalf("应给总数和翻页提示：\n%s", txt)
	}
	// 一行写不下时少列几条，仍不超限，并告诉从哪里翻页
	long := make([]store.Asset, len(page))
	copy(long, page)
	for i := range long {
		long[i].UID = "core:" + strings.Repeat("very_long_name_", 6) + fmt.Sprint(i)
	}
	if lt := listText(1000, 0, long, facets, true); len(lt) > 4096 || !strings.Contains(lt, "--offset") {
		t.Fatalf("长 id 下仍应不超过 4KB（%d）并给翻页提示", len(lt))
	}

	t.Logf("list 文字 %d 字节", len(txt))
	b, _ := json.MarshalIndent(compactList(1000, 0, page, facets), "", "  ")
	t.Logf("--json 默认 %d 字节", len(b))
	if len(b) > 20*1024 {
		t.Fatalf("--json 默认 %d 字节，超过 20KB", len(b))
	}
	if strings.Contains(string(b), "lod") || strings.Contains(string(b), "preview-0.png") {
		t.Fatal("--json 默认不应带完整内容")
	}
	if !strings.Contains(string(b), `"previews": 4`) || !strings.Contains(string(b), `"total": 1000`) {
		t.Fatalf("--json 默认应带预览图个数和总数：%s", b[:300])
	}

	// summary
	sum := protocol.AssetSummary{Project: "rollcall-squad", Total: 1000, Statuses: facets.Counts, Categories: facets.CategoryCounts}
	for i := 0; i < 60; i++ {
		sum.Batches = append(sum.Batches, fmt.Sprintf("G%02d", i))
	}
	t.Logf("summary %d 字节", len(summaryText(sum)))
	if st := summaryText(sum); len(st) > 1024 {
		t.Fatalf("summary %d 字节，超过 1KB：\n%s", len(st), st)
	}
	// 类别很多时也不增长
	for i := 0; i < 300; i++ {
		sum.Categories[fmt.Sprintf("cat_%d", i)] = i + 1
	}
	t.Logf("summary %d 字节", len(summaryText(sum)))
	if st := summaryText(sum); len(st) > 1024 {
		t.Fatalf("类别很多时 summary %d 字节，超过 1KB", len(st))
	}

	// --all：不限条数，全部列出；--full：完整内容
	if at := listText(1000, 0, all, facets, false); strings.Count(at, "\n") < 1000 || strings.Contains(at, "--offset") {
		t.Fatalf("--all 应列出全部 1000 件，不再提示翻页")
	}
	if fb, _ := json.Marshal(all); !strings.Contains(string(fb), "preview-0.png") {
		t.Fatal("--full 应给完整内容")
	}
}

// R-391 验收 3：get 默认只含最新版本（新的在前）和最近 3 条审核意见，--history 才有历史。
func TestAssetTrimDetail(t *testing.T) {
	d := store.AssetDetail{Versions: []store.AssetVersion{{Version: 4}, {Version: 3}, {Version: 2}, {Version: 1}}, Reviews: make([]store.AssetReview, 7)}
	got := trimDetail(d)
	if len(got.Versions) != 1 || got.Versions[0].Version != 4 || len(got.Reviews) != 3 {
		t.Fatalf("默认只留最新版本和最近 3 条意见：%d 版 %d 条", len(got.Versions), len(got.Reviews))
	}
	if len(d.Versions) != 4 || len(d.Reviews) != 7 {
		t.Fatal("不应改动原数据")
	}
}
