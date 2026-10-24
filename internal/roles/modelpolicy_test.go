package roles

import (
	"regexp"
	"strings"
	"testing"
)

// R-228：三档策略只描述按难度的倾向，不写死模型名；只给会派子任务的身份；默认均衡；写明派活要标档位和理由。
func TestPolicyNote(t *testing.T) {
	if NormPolicy("") != PolicyBalanced || NormPolicy("乱写") != PolicyBalanced {
		t.Fatal("空和不认识的应按均衡")
	}
	if !ValidPolicy("") || !ValidPolicy("economy") || ValidPolicy("乱写") {
		t.Fatal("档位校验不对")
	}
	names := regexp.MustCompile(`(?i)claude|opus|sonnet|haiku|gpt|codex|gemini|fable|deepseek`)
	for _, role := range []string{PM, Aide, Dev} {
		for _, p := range Policies {
			n := PolicyNote(role, p)
			if !strings.Contains(n, PolicyName(p)) || !strings.Contains(n, "模型：<档位> — <一句理由>") || !strings.Contains(n, "这一项忽略") {
				t.Fatalf("%s/%s 应写明档位名、派活标档位和理由、无子代理能力时忽略：%s", role, p, n)
			}
			if m := names.FindString(n); m != "" {
				t.Fatalf("%s/%s 的策略不应写死模型名或工具名（%q）：%s", role, p, m, n)
			}
		}
	}
	if !strings.Contains(PolicyNote(PM, ""), "均衡") {
		t.Fatal("没设置时默认均衡")
	}
	if PolicyNote(Advisor, PolicyQuality) != "" || PolicyNote(Reviewer, PolicyEconomy) != "" || PolicyNote("", PolicyEconomy) != "" {
		t.Fatal("顾问、评审、无身份不派子任务，不带这一项")
	}
	if PolicyNote(PM, PolicyEconomy) == PolicyNote(PM, PolicyQuality) {
		t.Fatal("不同档位的倾向应不同")
	}
}
