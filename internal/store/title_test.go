package store

import "testing"

// 标题开头的编号前缀去掉，正文里的编号保留（R-129）。
func TestCleanTitle(t *testing.T) {
	cases := map[string]string{
		"R-95-D 工作台改版：PM 简报": "工作台改版：PM 简报",
		"R-12：导出 CSV":        "导出 CSV",
		"P-3 阶段 2 收尾":        "阶段 2 收尾",
		"D-7、R-8 两件事":        "两件事",
		"修复 R-12 的导出":        "修复 R-12 的导出",
		"  导出 CSV  ":         "导出 CSV",
		"R-12":               "R-12",
		"RBAC 权限":            "RBAC 权限",
	}
	for in, want := range cases {
		if got := CleanTitle(in); got != want {
			t.Errorf("CleanTitle(%q) = %q，应为 %q", in, got, want)
		}
	}
}
