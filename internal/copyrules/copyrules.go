// Package copyrules 读 docs/copywriting-rules.json（界面文案规则，R-274），给后端测试检查显示在网页上的标签用。
// 前端 web/src/lib/copy.test.ts 读同一份文件；规则说明见 docs/copywriting.md 第 6 节。
// 只在测试里用：文件按源码位置找，不打进程序。
package copyrules

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
)

// Rules 是 JSON 里后端用得到的部分。
type Rules struct {
	Forbidden []struct {
		Word string `json:"word"`
		Use  string `json:"use"`
	} `json:"forbidden"`
	StatusSets map[string][]string `json:"statusSets"`
	Allow      []struct {
		Key    string   `json:"key"`
		Rules  []string `json:"rules"`
		Except []string `json:"except"`
		Reason string   `json:"reason"`
	} `json:"allow"`
}

// Path 是规则文件的位置（仓库根下的 docs/copywriting-rules.json）。
func Path() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "docs", "copywriting-rules.json")
}

// Load 读规则文件。
func Load() (*Rules, error) {
	b, err := os.ReadFile(Path())
	if err != nil {
		return nil, err
	}
	var r Rules
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("解析 %s：%w", Path(), err)
	}
	return &r, nil
}

// ForbiddenIn 返回 s 里出现的禁用词，每条写成「旧词 → 标准词」。
func (r *Rules) ForbiddenIn(s string) []string {
	var out []string
	for _, f := range r.Forbidden {
		t := s
		if strings.Contains(f.Use, f.Word) { // 标准词本身含禁用词时（「指派给」含「派给」）先去掉标准词再查
			t = strings.ReplaceAll(s, f.Use, "")
		}
		if strings.Contains(t, f.Word) {
			out = append(out, "「"+f.Word+"」→「"+f.Use+"」")
		}
	}
	return out
}

// CheckLabels 检查一组状态标签：每个值都在 sets 指定的状态集合里，且不含禁用词。exact 为真时还要求集合里每个词都用到了。
// 返回全部问题，没有问题时为空。
func (r *Rules) CheckLabels(name string, labels map[string]string, exact bool, sets ...string) []string {
	ok := map[string]bool{}
	for _, s := range sets {
		words, found := r.StatusSets[s]
		if !found {
			return []string{fmt.Sprintf("%s：statusSets 里没有 %s", name, s)}
		}
		for _, w := range words {
			ok[w] = true
		}
	}
	var bad []string
	used := map[string]bool{}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := labels[k]
		used[v] = true
		if !ok[v] {
			bad = append(bad, fmt.Sprintf("%s[%s]：「%s」不在 %s 里", name, k, v, strings.Join(sets, " / ")))
		}
		if f := r.ForbiddenIn(v); len(f) > 0 {
			bad = append(bad, fmt.Sprintf("%s[%s]：%s", name, k, strings.Join(f, "，")))
		}
	}
	if exact {
		for w := range ok {
			if !used[w] {
				bad = append(bad, fmt.Sprintf("%s：缺少「%s」", name, w))
			}
		}
	}
	sort.Strings(bad)
	return bad
}

var (
	itRe      = regexp.MustCompile(`(^|[^其])它`)
	spacingRe = regexp.MustCompile(`\p{Han}[A-Za-z0-9]|[A-Za-z0-9]\p{Han}`)
	abbrevRe  = regexp.MustCompile(`\d+\s?[smhd]\s?前`)
	clauseRe  = regexp.MustCompile(`[，。；]`)
)

// Check 按 copywriting.md 第 6 节检查一段显示在网页上的文字（与前端 copy.test.ts 的 RULES 相同，按钮长度除外），
// 返回全部问题，没有问题时为空。
func (r *Rules) Check(s string) []string {
	var bad []string
	if f := r.ForbiddenIn(s); len(f) > 0 {
		bad = append(bad, "禁用词 "+strings.Join(f, "，"))
	}
	if itRe.MatchString(s) {
		bad = append(bad, "用了「它」，写会话名或省略")
	}
	rs := []rune(s)
	for i, c := range rs {
		if c != '·' {
			continue
		}
		if i == 0 || i == len(rs)-1 || rs[i-1] != ' ' || rs[i+1] != ' ' {
			bad = append(bad, "「·」两侧要各留一个半角空格")
			break
		}
		lo, hi := max(0, i-8), min(len(rs), i+9)
		if clauseRe.MatchString(string(rs[lo:i])) || clauseRe.MatchString(string(rs[i+1:hi])) {
			bad = append(bad, "「·」连接了句子，改用逗号或句号")
			break
		}
	}
	if strings.ContainsAny(s, "“”\"") {
		bad = append(bad, "引号用「」")
	}
	if strings.Contains(s, "……") || strings.Contains(s, "...") {
		bad = append(bad, "省略号只用单个「…」，列举用「等」")
	}
	if strings.ContainsAny(s, "！!") {
		bad = append(bad, "不用感叹号")
	}
	if m := spacingRe.FindString(s); m != "" {
		bad = append(bad, "中英文 / 数字之间要留空格：「"+m+"」")
	}
	if abbrevRe.MatchString(s) {
		bad = append(bad, "相对时间用中文单位：「5 分钟前」")
	}
	return bad
}
