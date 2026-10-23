package daemon

import (
	"os"
	"regexp"
	"strconv"
)

// 上下文占用（R-60）：一轮结束时从会话记录尾部只匹配 token 用量字段（不读对话内容），
// 估出这个会话现在的上下文大小，网页据此提示“偏重”、交出需求时优先推荐轻量会话。

var (
	// Claude：assistant 消息的 usage；上下文 ≈ 输入 + 缓存写入 + 缓存读取
	claudeUsageRe = regexp.MustCompile(`"usage":\{"input_tokens":(\d+),"cache_creation_input_tokens":(\d+),"cache_read_input_tokens":(\d+)`)
	// Codex：token_count 事件的 last_token_usage.input_tokens 与 model_context_window
	codexUsageRe  = regexp.MustCompile(`"last_token_usage":\{"input_tokens":(\d+)`)
	codexWindowRe = regexp.MustCompile(`"model_context_window":(\d+)`)
)

func readTail(path string, n int64) []byte {
	if path == "" {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil
	}
	off := max(st.Size()-n, 0)
	buf := make([]byte, st.Size()-off)
	if _, err := f.ReadAt(buf, off); err != nil && len(buf) == 0 {
		return nil
	}
	return buf
}

// contextUsage 返回会话当前的上下文 token 数与上下文窗口（不知道时为 0）。
func contextUsage(path string) (used, window int) {
	buf := readTail(path, 256<<10)
	if len(buf) == 0 {
		return 0, 0
	}
	if m := claudeUsageRe.FindAllSubmatch(buf, -1); len(m) > 0 {
		last := m[len(m)-1]
		for _, g := range last[1:] {
			n, _ := strconv.Atoi(string(g))
			used += n
		}
		return used, 0
	}
	if m := codexUsageRe.FindAllSubmatch(buf, -1); len(m) > 0 {
		used, _ = strconv.Atoi(string(m[len(m)-1][1]))
		if w := codexWindowRe.FindAllSubmatch(buf, -1); len(w) > 0 {
			window, _ = strconv.Atoi(string(w[len(w)-1][1]))
		}
	}
	return used, window
}
