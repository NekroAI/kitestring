package main

import (
	"fmt"
	"net/url"
	"os/exec"
	"sort"
	"strings"

	"github.com/NekroAI/kitestring/internal/protocol"
	"github.com/NekroAI/kitestring/internal/store"
)

// R-391：资源库查询给 AI 看，输出不能随素材数量无限增长。
const (
	assetListLimit  = 50   // ks asset list 默认最多列几条
	assetTextBudget = 3600 // 默认（有条数限制）时文字输出的字节上限，写不下就少列几条、提示用 --offset 翻页
	assetTopN       = 8    // 统计里类别、批次最多列几个
	assetKeepReview = 3    // ks asset get 默认带最近几条审核意见
)

// cwdRemote 是当前目录的 git remote（没有就空）：交给 Hub 找当前目录所在的项目。
func cwdRemote() string {
	out, err := exec.Command("git", "remote").Output()
	if err != nil {
		return ""
	}
	names := strings.Fields(string(out))
	if len(names) == 0 {
		return ""
	}
	name := names[0]
	for _, n := range names {
		if n == "origin" {
			name = n
		}
	}
	u, err := exec.Command("git", "remote", "get-url", name).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(u))
}

// assetQuery 拼查询参数：给定的非空项 + 会话键 + 当前目录的 remote（没写 --project 时 Hub 用它找项目）。
func assetQuery(kv map[string]string, session string) url.Values {
	q := url.Values{}
	for k, v := range kv {
		if v != "" {
			q.Set(k, v)
		}
	}
	if k := cliSession(session); k != "" {
		q.Set("session_key", k)
	}
	if kv["project"] == "" {
		if r := cwdRemote(); r != "" {
			q.Set("cwd_remote", r)
		}
	}
	return q
}

func statusCounts(m map[string]int) string {
	var parts []string
	for _, st := range store.AssetStatuses {
		if n := m[st]; n > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", assetStatusLabel[st], n))
		}
	}
	if len(parts) == 0 {
		return "没有素材"
	}
	return strings.Join(parts, " · ")
}

// topCounts 按数量从多到少列前 n 个，其余合并成「另 N 类」。
func topCounts(m map[string]int, n int, unit string) string {
	type kv struct {
		k string
		n int
	}
	var all []kv
	for k, v := range m {
		all = append(all, kv{k, v})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].n != all[j].n {
			return all[i].n > all[j].n
		}
		return all[i].k < all[j].k
	})
	var parts []string
	for i, e := range all {
		if i == n {
			parts = append(parts, fmt.Sprintf("另 %d %s", len(all)-n, unit))
			break
		}
		parts = append(parts, fmt.Sprintf("%s %d", e.k, e.n))
	}
	return strings.Join(parts, "、")
}

func summaryText(s protocol.AssetSummary) string {
	var b strings.Builder
	name := "全部项目"
	if s.Project != "" {
		name = "项目「" + s.Project + "」"
	}
	fmt.Fprintf(&b, "%s资源库共 %d 件：%s\n", name, s.Total, statusCounts(s.Statuses))
	if len(s.Categories) > 0 {
		fmt.Fprintf(&b, "类别（%d 个）：%s\n", len(s.Categories), topCounts(s.Categories, assetTopN, "类"))
	}
	if len(s.Batches) > 0 {
		shown := s.Batches
		more := ""
		if len(shown) > assetTopN {
			shown, more = shown[:assetTopN], fmt.Sprintf("，另 %d 个", len(s.Batches)-assetTopN)
		}
		fmt.Fprintf(&b, "批次（%d 个）：%s%s\n", len(s.Batches), strings.Join(shown, "、"), more)
	}
	b.WriteString("要看具体素材：ks asset list --status / --category / --batch / --id 前缀* / --q 关键字（素材 id 或中文名）筛选；单件 ks asset get <素材 id>\n")
	return b.String()
}

// shortLine 是列表里的一行：编号、素材 id、版本、状态、类别（批次和面数在 get 里看）。
func shortLine(a store.Asset) string {
	name := a.Batch + "/" + a.Key
	if a.UID != "" {
		name = a.UID
	}
	s := fmt.Sprintf("%s %s v%d %s", a.Code, name, a.Version, assetStatusLabel[a.Status])
	if a.Latest != nil && a.Latest.Name != "" {
		s = fmt.Sprintf("%s %s「%s」v%d %s", a.Code, name, a.Latest.Name, a.Version, assetStatusLabel[a.Status])
	}
	if a.Category != "" {
		s += " " + a.Category
	}
	return s + linkNote(a)
}

// linkNote：变体、取代关系（R-380），接在一行素材后面。
func linkNote(a store.Asset) string {
	s := ""
	if a.Parent != nil {
		s += " · " + a.Parent.Code + " 的变体 " + a.Variant
	}
	if r := a.SupersededBy; r != nil {
		s += " · 已被 " + refName(r, "") + " 取代"
	}
	return s
}

// listText 列出一页素材：开头是总数和统计；limited 为真时整体不超过 assetTextBudget 字节。
func listText(total, offset int, assets []store.Asset, f store.AssetFacets, limited bool) string {
	if total == 0 {
		return "资源库里没有符合条件的素材\n"
	}
	var head strings.Builder
	fmt.Fprintf(&head, "符合条件 %d 件；所在范围：%s\n", total, statusCounts(f.Counts))
	if len(f.CategoryCounts) > 0 {
		fmt.Fprintf(&head, "类别：%s\n", topCounts(f.CategoryCounts, assetTopN, "类"))
	}
	var b strings.Builder
	b.WriteString(head.String())
	shown := 0
	for _, a := range assets {
		line := shortLine(a) + "\n"
		if limited && b.Len()+len(line) > assetTextBudget {
			break
		}
		b.WriteString(line)
		shown++
	}
	if rest := total - offset - shown; rest > 0 {
		fmt.Fprintf(&b, "（已列第 %d–%d 件，还有 %d 件：--offset %d 翻页，或加筛选缩小范围，--all 取全部）\n", offset+1, offset+shown, rest, offset+shown)
	}
	return b.String()
}

type compactAsset struct {
	Code     string   `json:"code"`
	ID       string   `json:"id"`
	Name     string   `json:"name,omitempty" doc:"中文名（R-400）"`
	Category string   `json:"category,omitempty"`
	Status   string   `json:"status"`
	Version  int      `json:"version"`
	Batches  []string `json:"batches,omitempty"`
	Previews int      `json:"previews"`
	Variant  string   `json:"variant,omitempty" doc:"变体名（R-380）"`
	Parent   string   `json:"parent,omitempty" doc:"变体所属素材的编号"`
	// SupersededBy：取代它的素材（素材 id，旧条目写编号）
	SupersededBy string `json:"superseded_by,omitempty"`
}

// compactList 是 --json 默认输出：精简字段 + 统计，--full 才给完整内容。
func compactList(total, offset int, assets []store.Asset, f store.AssetFacets) map[string]any {
	items := make([]compactAsset, 0, len(assets))
	for _, a := range assets {
		c := compactAsset{Code: a.Code, ID: a.Key, Category: a.Category, Status: a.Status, Version: a.Version, Batches: a.Batches}
		if a.UID != "" {
			c.ID = a.UID
		} else {
			c.ID = a.Batch + "/" + a.Key
		}
		if a.Latest != nil {
			c.Previews = len(a.Latest.Previews)
			c.Name = a.Latest.Name
		}
		c.Variant = a.Variant
		if a.Parent != nil {
			c.Parent = a.Parent.Code
		}
		if r := a.SupersededBy; r != nil {
			c.SupersededBy = r.UID
			if c.SupersededBy == "" {
				c.SupersededBy = r.Code
			}
		}
		items = append(items, c)
	}
	return map[string]any{
		"total": total, "offset": offset, "shown": len(items),
		"statuses": f.Counts, "categories": f.CategoryCounts,
		"assets": items,
		"note":   "精简字段；完整内容加 --full，单件详情用 ks asset get <id>",
	}
}

// trimDetail：get 默认只留最新版本（Hub 返回时新的在前）和最近几条审核意见。
func trimDetail(d store.AssetDetail) store.AssetDetail {
	if len(d.Versions) > 1 {
		d.Versions = d.Versions[:1]
	}
	if len(d.Reviews) > assetKeepReview {
		d.Reviews = d.Reviews[:assetKeepReview]
	}
	return d
}
