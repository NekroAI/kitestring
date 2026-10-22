// Package summary 通过 OpenAI 兼容的 Chat Completions 接口生成一句话进展摘要（D7）。
// Kitestring 自身不使用 Claude / ChatGPT 订阅额度（D13），只调用用户单独配置的接口。
package summary

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

// Config 是摘要模型配置。
type Config struct {
	BaseURL string `json:"base_url"` // 例：https://api.deepseek.com/v1
	APIKey  string `json:"api_key"`
	Model   string `json:"model"`
}

// Enabled 判断配置是否完整。
func (c Config) Enabled() bool { return c.BaseURL != "" && c.Model != "" }

const systemPrompt = `你是开发进度速记员。根据用户给 AI 编程助手的最后一条指令和助手的最后一条回复，用一句中文概括这个会话当前的进展，不超过 40 个字。只输出这句话，不要引号、前缀或解释。`

// Generate 生成摘要。prompt 与 reply 由调用方预先截断。
func Generate(ctx context.Context, cfg Config, prompt, reply string) (string, error) {
	if !cfg.Enabled() {
		return "", errors.New("摘要模型未配置")
	}
	body, _ := json.Marshal(map[string]any{
		"model": cfg.Model,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": fmt.Sprintf("最后一条指令：\n%s\n\n助手最后回复：\n%s", prompt, reply)},
		},
		"max_tokens":  120,
		"temperature": 0.2,
	})
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	url := strings.TrimSuffix(cfg.BaseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("摘要接口返回 %d：%s", resp.StatusCode, Truncate(string(raw), 200))
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("解析摘要响应失败：%w", err)
	}
	if len(out.Choices) == 0 {
		return "", errors.New("摘要接口未返回内容")
	}
	text := strings.TrimSpace(out.Choices[0].Message.Content)
	text = strings.Trim(text, "\"“”「」")
	return Truncate(strings.ReplaceAll(text, "\n", " "), 80), nil
}

// Truncate 按字符截断字符串，超长时追加省略号。
func Truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "…"
}
