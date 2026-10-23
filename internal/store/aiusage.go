package store

import (
	"context"
	"time"

	"github.com/NekroAI/kitestring/internal/protocol"
)

// 模型调用用量（R-120）：hub 自己的每次模型调用记一行计量，设置页按功能看本日 / 本周。
// 只记功能、模型、成败、接口返回的 token 和耗时；不含提示词与输出，也不读用户对话内容。

// AICall 是一次模型接口调用。
type AICall struct {
	Feature          string
	Model            string
	OK               bool
	PromptTokens     int
	CompletionTokens int
	HasUsage         bool
	DurationMS       int64
	At               time.Time
}

const aiCallKeep = 35 * 24 * time.Hour

// RecordAICall 记一次模型调用，顺手清掉 35 天前的记录。
func (s *Store) RecordAICall(ctx context.Context, c AICall) error {
	if c.At.IsZero() {
		c.At = time.Now()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO ai_calls(id, feature, model, ok, prompt_tok, compl_tok, has_usage, duration_ms, at) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		protocol.NewID(), c.Feature, c.Model, b2i(c.OK), c.PromptTokens, c.CompletionTokens, b2i(c.HasUsage), c.DurationMS, ts(c.At))
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `DELETE FROM ai_calls WHERE at < ?`, ts(c.At.Add(-aiCallKeep)))
	return err
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// AIUsage 是某个功能在一段时间内的用量。
type AIUsage struct {
	Calls            int `json:"calls" doc:"调用次数（含失败）"`
	Failed           int `json:"failed" doc:"失败次数：网络错误、接口非 2xx、响应解析失败"`
	PromptTokens     int `json:"prompt_tokens" doc:"输入 token，取自接口返回的 usage，只累计返回了 usage 的调用"`
	CompletionTokens int `json:"completion_tokens" doc:"输出 token，同上"`
	NoUsage          int `json:"no_usage" doc:"调用成功但接口没返回 usage 的次数；这些调用的 token 没有计入，不是 0"`
}

// AIUsageSince 按功能统计 since 之后的调用。
func (s *Store) AIUsageSince(ctx context.Context, since time.Time) (map[string]AIUsage, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT feature, COUNT(*), SUM(1 - ok), SUM(CASE WHEN has_usage = 1 THEN prompt_tok ELSE 0 END), SUM(CASE WHEN has_usage = 1 THEN compl_tok ELSE 0 END), SUM(CASE WHEN ok = 1 AND has_usage = 0 THEN 1 ELSE 0 END)
		FROM ai_calls WHERE at >= ? GROUP BY feature`, ts(since))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]AIUsage{}
	for rows.Next() {
		var f string
		var u AIUsage
		if err := rows.Scan(&f, &u.Calls, &u.Failed, &u.PromptTokens, &u.CompletionTokens, &u.NoUsage); err != nil {
			return nil, err
		}
		out[f] = u
	}
	return out, rows.Err()
}

// AIUsageStart 是最早一条计量记录的时间；还没有记录时为零值。
func (s *Store) AIUsageStart(ctx context.Context) (time.Time, error) {
	var at *string
	if err := s.db.QueryRowContext(ctx, `SELECT MIN(at) FROM ai_calls`).Scan(&at); err != nil || at == nil {
		return time.Time{}, err
	}
	return parseTS(*at), nil
}
