package copyrules

import "testing"

// 规则文件能读、状态集合齐全、豁免都写了理由（R-274）。豁免是否失效由前端 copy.test.ts 检查（它遍历全部界面文案）。
func TestRulesFile(t *testing.T) {
	r, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"session", "sessionMark", "presence", "roster", "device", "req", "decision", "delivery"} {
		if len(r.StatusSets[s]) == 0 {
			t.Errorf("statusSets 缺少 %s", s)
		}
	}
	if len(r.Forbidden) == 0 {
		t.Error("forbidden 为空")
	}
	for _, a := range r.Allow {
		if a.Key == "" || a.Reason == "" {
			t.Errorf("豁免必须写 key 和 reason：%+v", a)
		}
	}
	if got := r.ForbiddenIn("有 3 件要你处理，2 个在跑"); len(got) != 2 {
		t.Errorf("禁用词检查不对：%v", got)
	}
	// 标准词含禁用词（「指派给」含「派给」）时不误报，单独的「派给」照常拦
	if got := r.ForbiddenIn("已指派给「x」"); len(got) != 0 {
		t.Errorf("「指派给」不应算禁用词：%v", got)
	}
	if got := r.ForbiddenIn("派给「x」"); len(got) != 1 {
		t.Errorf("「派给」应算禁用词：%v", got)
	}
	if bad := r.CheckLabels("x", map[string]string{"a": "受阻", "b": "阻塞"}, false, "req"); len(bad) != 2 {
		t.Errorf("状态标签检查不对：%v", bad)
	}
	// 通用检查与前端 copy.test.ts 的规则一致（R-277）
	for s, n := range map[string]int{
		"待验收 R-12":        0,
		"「x」上的 Codex：摘要。": 0,
		"它在跑":             2, // 「它」+ 禁用词
		"待验收 · R-12":      0,
		"评审未完成 · 标题，再说":   1, // 「·」连句
		"5m 前":            1,
		"已交付3条":           1,
		"“引号”":            1,
		"好！":              1,
		"等等……":            1,
		"其它":              0,
	} {
		if got := r.Check(s); len(got) != n {
			t.Errorf("Check(%q) = %v，应有 %d 处问题", s, got, n)
		}
	}
}

func TestAddressProblems(t *testing.T) {
	r, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"https://mycorp.com/a", "a.b.cn", "http://nas.myhome.top:8080", "8.8.8.8", "121.5.6.7:3000", "https://cdn.foo.io"} {
		if len(r.AddressProblems(s)) == 0 {
			t.Errorf("应拦下：%s", s)
		}
	}
	for _, s := range []string{"https://example.com", "https://cdn.example.com/x", "a.example.org", "ks.example", "http://localhost:7000", "127.0.0.1", "192.0.2.10", "198.51.100.7", "203.0.113.9", "192.168.1.5", "zh.ts", "R-1.2", "第 1.2 版"} {
		if got := r.AddressProblems(s); len(got) != 0 {
			t.Errorf("应放行：%s，却报 %v", s, got)
		}
	}
}
