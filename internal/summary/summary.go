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

// Generate 用固定提示词生成一句话摘要（用于设置页测试）。prompt 与 reply 由调用方预先截断。
func Generate(ctx context.Context, cfg Config, prompt, reply string) (string, error) {
	text, err := Chat(ctx, cfg, systemPrompt, fmt.Sprintf("最后一条指令：\n%s\n\n助手最后回复：\n%s", prompt, reply), 120)
	if err != nil {
		return "", err
	}
	return Clean(text, 80), nil
}

// Call 是一次模型接口调用的计量记录（R-120）：只有功能、模型、成败、接口返回的 token 用量和耗时，不含任何提示词或输出内容。
type Call struct {
	Feature          string
	Model            string
	OK               bool // 接口返回了 2xx 且响应能解析；网络错误、非 2xx、响应解析失败算失败
	PromptTokens     int
	CompletionTokens int
	HasUsage         bool // 接口响应里带了 usage；没带时两个 token 数为 0，不能当作「用了 0 个」
	Duration         time.Duration
}

type recorderKey struct{}
type recorder struct {
	feature string
	fn      func(Call)
}

// WithRecorder 让 ctx 下的每次接口调用（ChatJSON 的重试也各算一次）都向 fn 报一条计量记录，feature 标明用途。
func WithRecorder(ctx context.Context, feature string, fn func(Call)) context.Context {
	return context.WithValue(ctx, recorderKey{}, recorder{feature, fn})
}

// Chat 调用 OpenAI 兼容的 Chat Completions 接口，返回模型输出文本。
func Chat(ctx context.Context, cfg Config, system, user string, maxTokens int) (string, error) {
	text, _, err := chat(ctx, cfg, system, user, maxTokens, false)
	return text, err
}

// errJSONModeUnsupported 表示接口不认 response_format，需要去掉后重试。
var errJSONModeUnsupported = errors.New("接口不支持 response_format")

// ChatJSON 要求模型输出 JSON 对象并解析到 v（R-119）。
// 先带 response_format=json_object 调用（接口不支持就去掉重试）；输出里没有可解析的 JSON
// （常见原因是推理内容占满 max_tokens，正文为空或被截断）时，把 max_tokens 翻倍再试一次。
// 最终失败的错误里带结束原因和输出开头一段，便于排查。
func ChatJSON(ctx context.Context, cfg Config, system, user string, maxTokens int, v any) error {
	jsonMode := true
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		text, finish, err := chat(ctx, cfg, system, user, maxTokens, jsonMode)
		if errors.Is(err, errJSONModeUnsupported) {
			jsonMode = false
			text, finish, err = chat(ctx, cfg, system, user, maxTokens, false)
		}
		if err != nil {
			return err
		}
		if err = JSON(text, v); err == nil {
			return nil
		}
		lastErr = fmt.Errorf("%w（结束原因 %q，max_tokens %d，输出 %d 字，开头：%q）", err, finish, maxTokens, utf8.RuneCountInString(text), Truncate(text, 120))
		maxTokens *= 2
	}
	return lastErr
}

func chat(ctx context.Context, cfg Config, system, user string, maxTokens int, jsonMode bool) (text, finish string, err error) {
	if !cfg.Enabled() {
		return "", "", errors.New("模型未配置")
	}
	if rec, ok := ctx.Value(recorderKey{}).(recorder); ok && rec.fn != nil {
		start := time.Now()
		call := Call{Feature: rec.feature, Model: cfg.Model}
		defer func() {
			call.OK = err == nil
			call.Duration = time.Since(start)
			rec.fn(call)
		}()
		return chatCall(ctx, cfg, system, user, maxTokens, jsonMode, &call)
	}
	return chatCall(ctx, cfg, system, user, maxTokens, jsonMode, &Call{})
}

func chatCall(ctx context.Context, cfg Config, system, user string, maxTokens int, jsonMode bool, call *Call) (string, string, error) {
	payload := map[string]any{
		"model": cfg.Model,
		"messages": []map[string]string{
			{"role": "system", "content": system},
			{"role": "user", "content": user},
		},
		"max_tokens":  maxTokens,
		"temperature": 0.2,
	}
	if jsonMode {
		payload["response_format"] = map[string]string{"type": "json_object"}
	}
	body, _ := json.Marshal(payload)
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	url := strings.TrimSuffix(cfg.BaseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode/100 != 2 {
		if jsonMode && resp.StatusCode == http.StatusBadRequest && strings.Contains(string(raw), "response_format") {
			return "", "", errJSONModeUnsupported
		}
		return "", "", fmt.Errorf("模型接口返回 %d：%s", resp.StatusCode, Truncate(string(raw), 200))
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage *struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", "", fmt.Errorf("解析模型响应失败：%w", err)
	}
	if out.Usage != nil { // token 数只取接口返回的 usage，没有就标明没有，不估算
		call.HasUsage, call.PromptTokens, call.CompletionTokens = true, out.Usage.PromptTokens, out.Usage.CompletionTokens
	}
	if len(out.Choices) == 0 {
		return "", "", errors.New("模型未返回内容")
	}
	return strings.TrimSpace(out.Choices[0].Message.Content), out.Choices[0].FinishReason, nil
}

// JSON 从模型输出中提取第一个 JSON 对象并解析到 v（容忍代码块包裹与前后多余文字）。
func JSON(text string, v any) error {
	i, j := strings.Index(text, "{"), strings.LastIndex(text, "}")
	if i < 0 || j <= i {
		return errors.New("模型输出中没有 JSON")
	}
	return json.Unmarshal([]byte(text[i:j+1]), v)
}

// Clean 去掉引号与换行并截断，用于单行文本。
func Clean(text string, n int) string {
	text = strings.TrimSpace(text)
	text = strings.Trim(text, "\"“”「」『』'`")
	return Truncate(strings.Join(strings.Fields(text), " "), n)
}

// Truncate 按字符截断字符串，超长时追加省略号。
func Truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "…"
}
