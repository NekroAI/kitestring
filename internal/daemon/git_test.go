package daemon

import "testing"

// 提交采集（R-156）：只留标题写了需求编号或是 revert 的提交，按时间从旧到新。
func TestParseCommits(t *testing.T) {
	rec := func(sha, subject, body string) string {
		return sha + "\x1f2026-10-24T01:02:03+08:00\x1f" + subject + "\x1f" + body + "\x1e\n"
	}
	out := rec("c3", `Revert "feat: x"`, "This reverts commit ABCDEF1234567.\n") +
		rec("c2", "chore: 无关", "") +
		rec("c1", "feat: 加按钮（R-12）", "细节") +
		rec("c0", "feat: 版本号 HR-3 不算", "")
	cs := parseCommits(out)
	if len(cs) != 2 || cs[0].SHA != "c1" || cs[1].SHA != "c3" || cs[1].Reverts != "abcdef1234567" || cs[0].At.IsZero() {
		t.Fatalf("解析结果不对：%+v", cs)
	}
}
