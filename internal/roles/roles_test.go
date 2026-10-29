package roles

import (
	"strings"
	"testing"
)

// 别称识别（R-190）：标识、英文简写、中文名、别称和「接管 / 当 / 做」的说法都映射到标识。
func TestResolve(t *testing.T) {
	for in, want := range map[string]string{
		"aide": "aide", "Aide": "aide", "主助理": "aide", "参谋": "aide", "当参谋": "aide", "做主助理": "aide", "助理": "aide", "领航": "aide", "特助": "aide", "总助": "aide",
		"advisor": "advisor", "Adv": "advisor", "顾问": "advisor", "当顾问": "advisor",
		"reviewer": "reviewer", "Rev": "reviewer", "评审": "reviewer", "当审查员": "reviewer",
		"pm": "pm", "PM": "pm", "接管 PM": "pm", "接管PM": "pm", "主调度": "pm", "调度": "pm", "项目经理": "pm", "统筹": "pm",
		"dev": "dev", "Dev": "dev", "开发": "dev", "做开发": "dev", "程序员": "dev",
		"none": "", "": "",
	} {
		got, ok := Resolve(in)
		if !ok || got != want {
			t.Errorf("Resolve(%q) = %q, %v；want %q", in, got, ok, want)
		}
	}
	for _, bad := range []string{"厨师", "subagent", "子任务"} {
		if _, ok := Resolve(bad); ok {
			t.Errorf("Resolve(%q) 应认不出", bad)
		}
	}
	if !Unique(Aide) || !Unique(PM) || Unique(Advisor) || !Resident(Aide) || Resident(Advisor) {
		t.Error("主助理、主调度每项目唯一；主助理常驻、顾问不常驻")
	}
}

// R-229：主调度提示词写明小验证与二次退回复盘；主调度、开发写明待命超时。
func TestPMPromptRework(t *testing.T) {
	pm := Prompt(PM)
	for _, want := range []string{"小验证子任务", "这条路行不行", "附证据", "验证通过再派全套实现", "第二次被退回", "先停下复盘做法", "不要接着在原做法上修补", "待命超时 · 建议收尾"} {
		if !strings.Contains(pm, want) {
			t.Fatalf("主调度提示词缺 %q", want)
		}
	}
	if dev := Prompt(Dev); !strings.Contains(dev, "待命超时") || !strings.Contains(dev, "不再给你送订阅事件") {
		t.Fatal("开发提示词应写明待命超时")
	}
}

// R-243：做事的身份都写明「原型 / 方案做完当场提决策」「等别的需求就设受阻并指向它」。
func TestPromptWaitOn(t *testing.T) {
	for _, r := range []string{PM, Dev, Aide, Advisor} {
		p := Prompt(r)
		for _, want := range []string{"做完原型 / 方案，当场用 decision_raise", "设为 blocked 并用 waiting_on 指向它", "不要一直标着进行中干等"} {
			if !strings.Contains(p, want) {
				t.Fatalf("%s 提示词缺 %q", r, want)
			}
		}
	}
}
