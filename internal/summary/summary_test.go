package summary

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func fake(t *testing.T, handler func(n int, body string) (int, string)) (Config, *int) {
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, 1<<16)
		l, _ := r.Body.Read(b)
		n++
		code, out := handler(n, string(b[:l]))
		w.WriteHeader(code)
		fmt.Fprint(w, out)
	}))
	t.Cleanup(srv.Close)
	return Config{BaseURL: srv.URL, Model: "m"}, &n
}

func reply(content, finish string) string {
	return fmt.Sprintf(`{"choices":[{"message":{"content":%q},"finish_reason":%q}]}`, content, finish)
}

func TestChatJSONRetriesEmptyOutput(t *testing.T) {
	cfg, n := fake(t, func(n int, body string) (int, string) {
		if n == 1 {
			return 200, reply("", "length")
		}
		if !strings.Contains(body, `"max_tokens":1200`) {
			t.Errorf("重试应翻倍 max_tokens: %s", body)
		}
		return 200, reply("```json\n{\"summary\":\"好\"}\n```", "stop")
	})
	var v struct{ Summary string }
	if err := ChatJSON(context.Background(), cfg, "s", "u", 600, &v); err != nil || v.Summary != "好" || *n != 2 {
		t.Fatalf("err=%v v=%+v n=%d", err, v, *n)
	}
}

func TestChatJSONFallsBackWithoutResponseFormat(t *testing.T) {
	cfg, _ := fake(t, func(n int, body string) (int, string) {
		if strings.Contains(body, "response_format") {
			return 400, `{"error":"unsupported response_format"}`
		}
		return 200, reply(`{"summary":"ok"}`, "stop")
	})
	var v struct{ Summary string }
	if err := ChatJSON(context.Background(), cfg, "s", "u", 600, &v); err != nil || v.Summary != "ok" {
		t.Fatalf("err=%v v=%+v", err, v)
	}
}

func TestChatJSONErrorCarriesRawOutput(t *testing.T) {
	cfg, n := fake(t, func(int, string) (int, string) { return 200, reply("抱歉，我无法输出", "stop") })
	var v struct{ Summary string }
	err := ChatJSON(context.Background(), cfg, "s", "u", 600, &v)
	if err == nil || !strings.Contains(err.Error(), "抱歉") || !strings.Contains(err.Error(), "stop") || *n != 2 {
		t.Fatalf("err=%v n=%d", err, *n)
	}
}

// 计量（R-120）：每次接口调用各记一条（ChatJSON 的重试和 response_format 回退各算一次）；
// token 只取接口返回的 usage，没返回就标明没有，不记成 0；没挂计量时不受影响。
func TestRecorderCountsEveryCall(t *testing.T) {
	cfg, _ := fake(t, func(n int, body string) (int, string) {
		switch n {
		case 1:
			return 400, `{"error":"unsupported response_format"}`
		case 2:
			return 200, `{"choices":[{"message":{"content":""},"finish_reason":"length"}],"usage":{"prompt_tokens":50,"completion_tokens":600}}`
		}
		return 200, reply(`{"summary":"ok"}`, "stop") // 没有 usage
	})
	var calls []Call
	ctx := WithRecorder(context.Background(), "turn", func(c Call) { calls = append(calls, c) })
	var v struct{ Summary string }
	if err := ChatJSON(ctx, cfg, "s", "u", 600, &v); err != nil || v.Summary != "ok" {
		t.Fatalf("err=%v v=%+v", err, v)
	}
	if len(calls) != 3 {
		t.Fatalf("应记 3 次调用（回退 400、空输出、成功）：%+v", calls)
	}
	if c := calls[0]; c.OK || c.HasUsage || c.Feature != "turn" || c.Model != "m" {
		t.Fatalf("400 应记为失败、无 usage：%+v", c)
	}
	if c := calls[1]; !c.OK || !c.HasUsage || c.PromptTokens != 50 || c.CompletionTokens != 600 {
		t.Fatalf("应取接口返回的 usage：%+v", c)
	}
	if c := calls[2]; !c.OK || c.HasUsage || c.PromptTokens != 0 {
		t.Fatalf("没返回 usage 要标明没有：%+v", c)
	}
	// 模型未配置时没有发出请求，不计量
	calls = nil
	if _, err := Chat(ctx, Config{}, "s", "u", 10); err == nil || len(calls) != 0 {
		t.Fatalf("未配置不应计量：%v %d", err, len(calls))
	}
}
