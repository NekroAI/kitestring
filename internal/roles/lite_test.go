package roles

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// R-359：轻接入一句话进入；常驻文字与每项服务说明都有字数上限；它不是团队身份。
func TestLite(t *testing.T) {
	for _, in := range []string{"lite", "Lite", "轻接入", "轻接入纸鸢", "当轻接入", "轻量", "light"} {
		if got, ok := Resolve(in); !ok || got != Lite {
			t.Errorf("Resolve(%q) = %q, %v；want lite", in, got, ok)
		}
	}
	if Member(Lite) || Member("") || !Member(PM) || !Valid(Lite) || Resident(Lite) || Unique(Lite) {
		t.Fatal("轻接入合法，但不是团队身份、不常驻、不占唯一位")
	}
	for _, r := range All {
		if r == Lite {
			t.Fatal("轻接入不在身份列表 All 里")
		}
	}
	p := LitePrompt()
	if n := utf8.RuneCountInString(p); n > LiteMax {
		t.Fatalf("轻接入常驻文字 %d 字，超过上限 %d", n, LiteMax)
	}
	if Compose(Lite, "") != p || Prompt(Lite) != p {
		t.Fatal("轻接入的提示词就是那几行短说明")
	}
	for _, want := range []string{"ks decision raise", "ks secret request", "ks team tell --to aide", "ks wait", "ks help", "ks role pm"} {
		if !strings.Contains(p, want) {
			t.Fatalf("轻接入说明缺 %q", want)
		}
	}
	for _, bad := range []string{"req_create", "evidence_attach", "handoff_write"} {
		if strings.Contains(p, bad) {
			t.Fatalf("轻接入说明不应要求 %q", bad)
		}
	}
	// 说明里提到的每个 ks help <服务> 都取得到，每项不超过上限
	for _, s := range Services {
		if n := utf8.RuneCountInString(s.Text); n > ServiceMax {
			t.Errorf("ks help %s 有 %d 字，超过上限 %d", s.Name, n, ServiceMax)
		}
		if !strings.Contains(p, "ks help "+s.Name) {
			t.Errorf("轻接入说明没提到 ks help %s", s.Name)
		}
		for _, a := range append([]string{s.Name}, s.Aliases...) {
			if got, ok := FindService(a); !ok || got.Name != s.Name {
				t.Errorf("FindService(%q) 应找到 %s", a, s.Name)
			}
		}
	}
	if _, ok := FindService("厨师"); ok {
		t.Fatal("不认识的服务应找不到")
	}
}

// R-359：原来 MCP 说明里的流程规矩挪进身份提示词，五个身份都带上（改写过的正文也带），行为不变。
func TestCommonRulesInRolePrompts(t *testing.T) {
	rules := CommonRules()
	for _, want := range []string{"req_create 记下来", "session_link_req", "evidence_attach 附上证据", "decision_raise 提出", "team_tell", "secret_request", "plan_create", "ks wait 挂起等待", "订阅事件（--on）"} {
		if !strings.Contains(rules, want) {
			t.Fatalf("通用流程规矩缺 %q", want)
		}
	}
	for _, r := range All {
		if !strings.Contains(Prompt(r), rules) {
			t.Fatalf("%s 的身份提示词应带上通用流程规矩", r)
		}
		if !strings.Contains(Compose(r, "用户改写的正文"), rules) {
			t.Fatalf("%s 改写过正文后仍应带上通用流程规矩", r)
		}
	}
	if !strings.Contains(Team(), "ks role lite") {
		t.Fatal("团队表应说明轻接入")
	}
}
