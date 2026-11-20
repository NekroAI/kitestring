package store

import (
	"testing"
	"time"
)

func TestSortAssets(t *testing.T) {
	mk := func(seq int, uid string, min int, name string) Asset {
		v := &AssetVersion{CreatedAt: time.Date(2026, 1, 1, 0, min, 0, 0, time.UTC), Name: name}
		return Asset{ID: uid, Code: AssetCode(seq), UID: uid, Key: uid, Latest: v, UpdatedAt: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)}
	}
	list := func() []Asset {
		a := mk(1, "core:w.a10", 5, "")
		b := mk(2, "core:w.a2", 1, "")
		c := mk(3, "core:w.b", 3, "卡宾枪")
		d := mk(4, "core:w.b~desert", 9, "")
		d.Parent = &AssetRef{ID: "core:w.b"} // 变体有新版本，算到 b 上
		return []Asset{a, b, c, d}
	}
	codes := func(as []Asset) string {
		s := ""
		for _, a := range as {
			s += a.Code + " "
		}
		return s
	}
	for _, tc := range []struct{ by, want string }{
		{SortRecent, "A-4 A-3 A-1 A-2 "},
		{SortName, "A-2 A-1 A-4 A-3 "}, // a2 < a10 自然排序；变体无中文名按 id；中文名排在字母后
		{SortSeq, "A-1 A-2 A-3 A-4 "},
	} {
		as := list()
		SortAssets(as, tc.by)
		if got := codes(as); got != tc.want {
			t.Errorf("%s：得到 %s，要 %s", tc.by, got, tc.want)
		}
	}
	// 只改审核状态（updated_at 变新）不影响最近更新
	as := list()
	as[1].UpdatedAt = time.Now()
	SortAssets(as, SortRecent)
	if codes(as) != "A-4 A-3 A-1 A-2 " {
		t.Errorf("改状态不应算更新：%s", codes(as))
	}
}
