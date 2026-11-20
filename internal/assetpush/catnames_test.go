package assetpush

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseCategories(t *testing.T) {
	// 旧写法：字符串数组
	keys, names, err := parseCategories(json.RawMessage(`["prop", "vehicle", "prop"]`))
	if err != nil || len(keys) != 2 || names != nil {
		t.Fatalf("字符串数组应照旧可用：%v %v %v", keys, names, err)
	}
	// 对象写法
	keys, names, err = parseCategories(json.RawMessage(`{"vehicle": {}, "prop": {"name": " 道具 ", "subtypes": {"crate": "箱子"}}}`))
	if err != nil || strings.Join(keys, ",") != "prop,vehicle" {
		t.Fatalf("对象写法：%v %v", keys, err)
	}
	if names["prop"].Name != "道具" || names["prop"].Subtypes["crate"] != "箱子" {
		t.Errorf("中文名没读出来：%+v", names)
	}
	if _, ok := names["vehicle"]; ok {
		t.Errorf("没写中文名的类别不该在表里：%+v", names)
	}
	// 没写 categories
	if keys, names, err = parseCategories(nil); err != nil || keys != nil || names != nil {
		t.Errorf("没写应为空：%v %v %v", keys, names, err)
	}
}

func TestParseCategoriesInvalid(t *testing.T) {
	long := strings.Repeat("字", MaxDisplayName+1)
	for name, raw := range map[string]string{
		"不是数组也不是对象":     `"prop"`,
		"类别不是对象":        `{"prop": "道具"}`,
		"name 不是字符串":    `{"prop": {"name": 3}}`,
		"name 换行":       `{"prop": {"name": "道\n具"}}`,
		"name 太长":       `{"prop": {"name": "` + long + `"}}`,
		"subtypes 不是对象": `{"prop": {"subtypes": ["箱子"]}}`,
		"细类名不是字符串":      `{"prop": {"subtypes": {"crate": 1}}}`,
		"细类名太长":         `{"prop": {"subtypes": {"crate": "` + long + `"}}}`,
		"不认识的字段":        `{"prop": {"title": "道具"}}`,
	} {
		_, _, err := parseCategories(json.RawMessage(raw))
		if err == nil {
			t.Errorf("%s 应报错：%s", name, raw)
		}
	}
}
