package roles

import "testing"

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
