package assetpush

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// SupersedePair 是 ks asset supersede 的一条取代关系（R-380）：Old 被 New 取代，New 为空表示取消取代。
type SupersedePair struct {
	Old string `json:"old"`
	New string `json:"new,omitempty"`
}

// SupersedeMapHelp 是 --map 对照表的写法（ks asset help 打印）。
const SupersedeMapHelp = `ks asset supersede --map 对照表的写法（文件路径，或 - 从标准输入读）：
  每行「旧 新」，用空格、制表符、逗号或 → 分隔，# 开头是注释：
    G11e1/carbine   core:weapon.carbine
    A-12            core:weapon.carbine@desert
  或 JSON：{"G11e1/carbine": "core:weapon.carbine"}，或 [{"old": "A-12", "new": "core:weapon.carbine"}]。
  旧条目写编号 A-x、素材 id，或旧的按批次推送的「批次/文件名」（ks asset list 里的写法）；新的写素材 id 或编号。
  有一条出错（找不到、跨项目、自己取代自己、形成循环）时一条都不改；--dry-run 只校验。--clear 旧条目... 取消取代。`

// ParseSupersedeMap 解析 --map 的内容。
func ParseSupersedeMap(text string) ([]SupersedePair, error) {
	t := strings.TrimSpace(text)
	if t == "" {
		return nil, errors.New("对照表是空的")
	}
	switch t[0] {
	case '{':
		var m map[string]string
		if err := json.Unmarshal([]byte(t), &m); err != nil {
			return nil, fmt.Errorf("对照表不是有效的 JSON 对象：%w", err)
		}
		// JSON 对象没有顺序，按文本里出现的先后排，报告和原文对得上
		dec := json.NewDecoder(strings.NewReader(t))
		var out []SupersedePair
		if _, err := dec.Token(); err != nil {
			return nil, err
		}
		for dec.More() {
			k, err := dec.Token()
			if err != nil {
				return nil, err
			}
			var v string
			if err := dec.Decode(&v); err != nil {
				return nil, err
			}
			out = append(out, SupersedePair{Old: strings.TrimSpace(k.(string)), New: strings.TrimSpace(v)})
		}
		return checkPairs(out)
	case '[':
		var out []SupersedePair
		if err := json.Unmarshal([]byte(t), &out); err != nil {
			return nil, fmt.Errorf("对照表不是有效的 JSON 数组（[{\"old\": ..., \"new\": ...}]）：%w", err)
		}
		return checkPairs(out)
	}
	var out []SupersedePair
	for i, line := range strings.Split(t, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.FieldsFunc(line, func(r rune) bool { return r == ' ' || r == '\t' || r == ',' || r == '→' })
		if len(f) != 2 {
			return nil, fmt.Errorf("对照表第 %d 行要写成「旧 新」：%s", i+1, line)
		}
		out = append(out, SupersedePair{Old: f[0], New: f[1]})
	}
	return checkPairs(out)
}

func checkPairs(ps []SupersedePair) ([]SupersedePair, error) {
	if len(ps) == 0 {
		return nil, errors.New("对照表里没有取代关系")
	}
	for i, p := range ps {
		if strings.TrimSpace(p.Old) == "" || strings.TrimSpace(p.New) == "" {
			return nil, fmt.Errorf("对照表第 %d 条的旧条目和新素材都要写", i+1)
		}
	}
	return ps, nil
}
