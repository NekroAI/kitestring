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
