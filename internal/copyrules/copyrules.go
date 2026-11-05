// Package copyrules 读 docs/copywriting-rules.json（界面文案规则，R-274），给后端测试检查显示在网页上的标签用。
// 前端 web/src/lib/copy.test.ts 读同一份文件；规则说明见 docs/copywriting.md 第 6 节。
// 只在测试里用：文件按源码位置找，不打进程序。
package copyrules

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
