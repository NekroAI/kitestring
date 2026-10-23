package e2e

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/hub"
	"github.com/NekroAI/kitestring/internal/store"
)

// 模型调用用量（R-120）：hub 自己调模型的各用途按功能计量——次数、失败、接口返回的 token，没返回 usage 的单独标注；
// 本日 / 本周分开统计；上线前没有记录时全是 0、since 为空；记录里不含提示词与输出。
func TestAIUsage(t *testing.T) {
	e := setup(t)
	var mode atomic.Value // "usage" | "nousage" | "fail"
	mode.Store("usage")
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch mode.Load() {
		case "fail":
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		case "usage":
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": "ok"}}}, "usage": map[string]any{"prompt_tokens": 100, "completion_tokens": 20}})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": "## 概览\n推进了登录页。"}}}})
		}
	}))
	t.Cleanup(model.Close)
	e.call("PUT", "/api/settings", map[string]any{"public_url": "", "summary_base_url": model.URL + "/v1", "summary_model": "m", "notify_default": map[string]any{}, "ai": map[string]bool{"recap": true}}, nil)

	get := func() hub.AIUsageReport {
		var out hub.AIUsageReport
		e.call("GET", "/api/ai/usage", nil, &out)
		return out
	}
	byID := func(o hub.AIUsageReport, id string) hub.AIFeatureUsage {
		for _, f := range o.Features {
			if f.ID == id {
				return f
			}
		}
		t.Fatalf("没有用途 %s", id)
		return hub.AIFeatureUsage{}
	}

	// 上线前没有记录：五个用途都在、全是 0，since 为空
	o := get()
	if len(o.Features) != 5 || o.Since != nil {
		t.Fatalf("没有记录时应列出五个用途、since 为空：%+v", o)
	}
	for _, f := range o.Features {
		if f.Today != (store.AIUsage{}) || f.Week != (store.AIUsage{}) {
			t.Fatalf("没有记录时用量应为 0：%+v", f)
		}
	}

	// 设置页测试：一次带 usage、一次失败；回顾：一次没 usage
	e.call("POST", "/api/settings/test-summary", nil, nil)
	mode.Store("fail")
	if code, _ := e.try("POST", "/api/settings/test-summary", nil); code != http.StatusBadGateway {
		t.Fatalf("模型失败应返回 502：%d", code)
	}
	mode.Store("nousage")
	e.call("POST", "/api/requirements", map[string]any{"title": "回顾要有活动记录"}, nil)
	now := time.Now()
	e.call("POST", "/api/recap", map[string]any{"from": now.Add(-time.Hour), "to": now.Add(time.Minute)}, nil)

	o = get()
	test, recap := byID(o, "test"), byID(o, "recap")
	if want := (store.AIUsage{Calls: 2, Failed: 1, PromptTokens: 100, CompletionTokens: 20}); test.Today != want || test.Week != want {
		t.Fatalf("测试用途应是 2 次、失败 1、100/20 token：%+v", test)
	}
	if want := (store.AIUsage{Calls: 1, NoUsage: 1}); recap.Today != want {
		t.Fatalf("没返回 usage 的调用应单独标注、token 不计入：%+v", recap.Today)
	}
	if o.Since == nil || time.Since(*o.Since) > time.Minute {
		t.Fatalf("since 应是最早一条记录的时间：%v", o.Since)
	}

	// 本日 / 本周：今天 0 点之前、本周一之后的调用只算本周；35 天前的记录被清掉、不计入
	day, week := o.DayStart, o.WeekStart
	if yesterday := day.Add(-time.Hour); !yesterday.Before(week) {
		if err := e.store.RecordAICall(t.Context(), store.AICall{Feature: "test", OK: true, PromptTokens: 7, CompletionTokens: 3, HasUsage: true, At: yesterday}); err != nil {
			t.Fatal(err)
		}
		o = get()
		if f := byID(o, "test"); f.Today.PromptTokens != 100 || f.Week.PromptTokens != 107 || f.Week.Calls != 3 {
			t.Fatalf("昨天的调用应只算进本周：%+v", f)
		}
	}
	if err := e.store.RecordAICall(t.Context(), store.AICall{Feature: "test", OK: true, At: now.Add(-40 * 24 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := e.store.RecordAICall(t.Context(), store.AICall{Feature: "test", OK: true, At: now}); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := e.store.DB().QueryRow(`SELECT COUNT(*) FROM ai_calls WHERE at < ?`, now.Add(-36*24*time.Hour).UTC().Format(time.RFC3339Nano)).Scan(&n); err != nil || n != 0 {
		t.Fatalf("35 天前的记录应被清掉：%d %v", n, err)
	}
	// 只存计量：表里没有存提示词或输出的列
	rows, err := e.store.DB().Query(`SELECT name FROM pragma_table_info('ai_calls')`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var c string
		_ = rows.Scan(&c)
		if strings.Contains(c, "prompt") && !strings.HasSuffix(c, "_tok") || c == "content" || c == "output" {
			t.Fatalf("计量表不应存对话内容：%s", c)
		}
	}
}
