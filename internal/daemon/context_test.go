package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

func TestContextUsage(t *testing.T) {
	dir := t.TempDir()
	claude := filepath.Join(dir, "c.jsonl")
	os.WriteFile(claude, []byte(`{"message":{"usage":{"input_tokens":5,"cache_creation_input_tokens":100,"cache_read_input_tokens":1000,"output_tokens":9}}}
{"message":{"usage":{"input_tokens":2,"cache_creation_input_tokens":2755,"cache_read_input_tokens":427837,"output_tokens":1079}}}
`), 0o600)
	if used, win := contextUsage(claude); used != 430594 || win != 0 {
		t.Fatalf("Claude 应取最后一条 usage：%d %d", used, win)
	}
	codex := filepath.Join(dir, "x.jsonl")
	os.WriteFile(codex, []byte(`{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":900},"last_token_usage":{"input_tokens":48000,"output_tokens":10},"model_context_window":272000}}}
`), 0o600)
	if used, win := contextUsage(codex); used != 48000 || win != 272000 {
		t.Fatalf("Codex 应取 last_token_usage 与窗口：%d %d", used, win)
	}
	if used, _ := contextUsage(filepath.Join(dir, "none")); used != 0 {
		t.Fatal("文件不存在时为 0")
	}
}
