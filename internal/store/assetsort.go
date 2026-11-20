package store

import (
	"sort"
	"strings"
	"unicode"
)

// R-410：资源库排序。
const (
	SortRecent = "recent" // 最近更新在前：看最新版本的推送时间，变体有新版本也算素材本身更新；只改审核状态不算
	SortName   = "name"   // 名称自然排序：有中文名用中文名，否则素材 id（旧条目用 key）
	SortSeq    = "seq"    // 推送先后：素材编号
)

// assetPushedAt：最新版本的推送时间。不用 assets.updated_at：它在审核改状态、合并批次时也会动。
func assetPushedAt(a Asset) int64 {
	if a.Latest != nil && !a.Latest.CreatedAt.IsZero() {
		return a.Latest.CreatedAt.UnixNano()
	}
	return a.CreatedAt.UnixNano()
}

// AssetSortName 是名称排序用的名字：中文名，没有写素材 id，旧条目写 key。
func AssetSortName(a Asset) string {
	if a.Latest != nil && a.Latest.Name != "" {
		return a.Latest.Name
	}
	if a.UID != "" {
		return a.UID
	}
	return a.Key
}

func assetSeqOf(a Asset) int { return assetSeq(a.Code) }

// SortAssets 就地排序。recent：新的在前（变体的新版本算到素材本身上）；name：自然排序；seq：编号。
func SortAssets(as []Asset, by string) {
	switch by {
	case SortRecent:
		at := make(map[string]int64, len(as))
		for _, a := range as {
			id := a.ID
			if a.Parent != nil {
				id = a.Parent.ID
			}
			at[id] = max(at[id], assetPushedAt(a))
		}
		stamp := func(a Asset) int64 {
			id := a.ID
			if a.Parent != nil {
				id = a.Parent.ID
			}
			return max(assetPushedAt(a), at[id])
		}
		sort.SliceStable(as, func(i, j int) bool {
			if x, y := stamp(as[i]), stamp(as[j]); x != y {
				return x > y
			}
			return assetSeqOf(as[i]) > assetSeqOf(as[j])
		})
	case SortName:
		sort.SliceStable(as, func(i, j int) bool {
			if c := NaturalCompare(AssetSortName(as[i]), AssetSortName(as[j])); c != 0 {
				return c < 0
			}
			return assetSeqOf(as[i]) < assetSeqOf(as[j])
		})
	case SortSeq:
		sort.SliceStable(as, func(i, j int) bool { return assetSeqOf(as[i]) < assetSeqOf(as[j]) })
	}
}

// NaturalCompare 自然排序：连续数字按数值比，其余不分大小写按字符比。
func NaturalCompare(a, b string) int {
	ra, rb := []rune(strings.ToLower(a)), []rune(strings.ToLower(b))
	i, j := 0, 0
	for i < len(ra) && j < len(rb) {
		if unicode.IsDigit(ra[i]) && unicode.IsDigit(rb[j]) {
			si, sj := i, j
			for i < len(ra) && unicode.IsDigit(ra[i]) {
				i++
			}
			for j < len(rb) && unicode.IsDigit(rb[j]) {
				j++
			}
			na, nb := strings.TrimLeft(string(ra[si:i]), "0"), strings.TrimLeft(string(rb[sj:j]), "0")
			if len(na) != len(nb) {
				return len(na) - len(nb)
			}
			if c := strings.Compare(na, nb); c != 0 {
				return c
			}
			continue
		}
		if ra[i] != rb[j] {
			if ra[i] < rb[j] {
				return -1
			}
			return 1
		}
		i++
		j++
	}
	return (len(ra) - i) - (len(rb) - j)
}
