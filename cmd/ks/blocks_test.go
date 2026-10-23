package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --blocks 接受 JSON 数组、单个块对象或 JSON 文件路径；格式不对时说清楚（R-97）。
func TestAttachBlocks(t *testing.T) {
	body := map[string]any{}
	raw, err := attachBlocks(body, `[{"type":"flow","mermaid":"graph TD; A-->B"},{"type":"checklist","items":[]}]`)
	if err != nil || len(raw) != 2 || !rawHasChecklist(raw) {
		t.Fatalf("JSON 数组：%v %v", raw, err)
	}
	if raw, _ := attachBlocks(map[string]any{}, `{"type":"table","columns":["a"],"rows":[["1"]]}`); len(raw) != 1 {
		t.Fatal("单个对象应当作一个块")
	}
	f := filepath.Join(t.TempDir(), "b.json")
	os.WriteFile(f, []byte(`[{"type":"metrics","items":[{"label":"x","value":1}]}]`), 0o600)
	if raw, err := attachBlocks(map[string]any{}, f); err != nil || len(raw) != 1 || rawHasChecklist(raw) {
		t.Fatalf("文件路径：%v %v", raw, err)
	}
	if _, err := attachBlocks(map[string]any{}, `[{"type":`); err == nil || !strings.Contains(err.Error(), "JSON 格式不对") {
		t.Fatalf("坏 JSON 应报错：%v", err)
	}
	if _, err := attachBlocks(map[string]any{}, "/no/such.json"); err == nil || !strings.Contains(err.Error(), "--blocks") {
		t.Fatalf("读不到文件应报错：%v", err)
	}
}
