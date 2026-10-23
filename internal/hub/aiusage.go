package hub

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NekroAI/kitestring/internal/store"
	"github.com/NekroAI/kitestring/internal/summary"
)

// 模型调用用量（R-120）：hub 自己调模型的五个用途各自计量，设置页按功能看本日 / 本周。
// 只统计 hub 自己的调用（次数、失败、接口返回的 token），不涉及用户对话内容；只从上线起统计。

const (
	aiTurn          = "turn"           // 每轮小结（一次调用同时出摘要、标题、等你回复要点）
	aiTitle         = "title"          // 会话标题（会话第一条消息时）
	aiDecisionTitle = "decision_title" // 决策短标题
	aiRecap         = "recap"          // 时段回顾
	aiTest          = "test"           // 设置页「测试」
)

var aiFeatures = []string{aiTurn, aiTitle, aiDecisionTitle, aiRecap, aiTest}

// aiCtx 让 ctx 下的模型调用都记一条计量；用 WithoutCancel 写库，调用超时或取消时失败的那条也记得上。
func (h *Hub) aiCtx(ctx context.Context, feature string) context.Context {
	return summary.WithRecorder(ctx, feature, func(c summary.Call) {
		err := h.store.RecordAICall(context.WithoutCancel(ctx), store.AICall{
			Feature: c.Feature, Model: c.Model, OK: c.OK, PromptTokens: c.PromptTokens, CompletionTokens: c.CompletionTokens,
			HasUsage: c.HasUsage, DurationMS: c.Duration.Milliseconds(),
		})
		if err != nil {
			h.log.Warn("记录模型调用用量失败", "err", err)
		}
	})
}

// AIFeatureUsage 是一个功能的本日 / 本周用量。
type AIFeatureUsage struct {
	ID    string        `json:"id" enum:"turn,title,decision_title,recap,test" doc:"用途：turn 每轮小结 / title 会话标题 / decision_title 决策短标题 / recap 时段回顾 / test 设置页测试"`
	Today store.AIUsage `json:"today" doc:"本日：从今天 0 点（Hub 所在时区）起"`
	Week  store.AIUsage `json:"week" doc:"本周：从本周一 0 点起"`
}

// AIUsageReport 是设置页要的整份用量。
type AIUsageReport struct {
	Features  []AIFeatureUsage `json:"features" doc:"五个用途各一行，没调用过的为 0"`
	Since     *time.Time       `json:"since,omitempty" doc:"最早一条记录的时间；用量只从上线起统计，没有记录时为空"`
	DayStart  time.Time        `json:"day_start"`
	WeekStart time.Time        `json:"week_start"`
}

type aiUsageOut struct{ Body AIUsageReport }

// weekStart 返回 t 所在周（周一起）的 0 点。
func weekStart(t time.Time) time.Time {
	d := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
	return d.AddDate(0, 0, -((int(d.Weekday()) + 6) % 7))
}

func (h *Hub) aiUsage(ctx context.Context, now time.Time) (*aiUsageOut, error) {
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	week := weekStart(now)
	today, err := h.store.AIUsageSince(ctx, day)
	if err != nil {
		return nil, err
	}
	wk, err := h.store.AIUsageSince(ctx, week)
	if err != nil {
		return nil, err
	}
	out := &aiUsageOut{}
	out.Body.DayStart, out.Body.WeekStart = day, week
	for _, f := range aiFeatures {
		out.Body.Features = append(out.Body.Features, AIFeatureUsage{ID: f, Today: today[f], Week: wk[f]})
	}
	if at, err := h.store.AIUsageStart(ctx); err == nil && !at.IsZero() {
		out.Body.Since = &at
	}
	return out, nil
}

func (h *Hub) registerAIUsageWeb(web huma.API) {
	huma.Register(web, huma.Operation{OperationID: "get-ai-usage", Method: http.MethodGet, Path: "/api/ai/usage", Tags: []string{"settings"}, Summary: "Hub 自己的模型调用用量：按功能统计本日 / 本周的次数、失败与 token（R-120）"},
		func(ctx context.Context, _ *struct{}) (*aiUsageOut, error) {
			out, err := h.aiUsage(ctx, time.Now())
			return out, apiErr(err)
		})
}
