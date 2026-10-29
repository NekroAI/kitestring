package store

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/NekroAI/kitestring/internal/protocol"
)

// token 用量（R-206）：daemon 上传按分钟的累计值，这里覆盖写入；小时级由同一小时的分钟重新求和。
// 新算 = In + CW + Out；命中率 = CR / (CR + In + CW)。

// LaneTok 是一个会话（agent 为空）或子任务、一个模型在时间范围内有用量的格子，按时间升序，几个数组一一对应。
type LaneTok struct {
	Key     string  `json:"k"`
	Agent   string  `json:"a"`
	Model   string  `json:"model"`
	Minutes []int64 `json:"m" doc:"格子起点，Unix 分钟；按小时取时为整点"`
	In      []int64 `json:"in" doc:"没命中缓存的输入"`
	CW      []int64 `json:"cw" doc:"写缓存（两个档位合计）"`
	CW1h    []int64 `json:"cw1h" doc:"其中写进 1 小时档的"`
	CR      []int64 `json:"cr" doc:"读缓存"`
	Out     []int64 `json:"out" doc:"输出"`
	N       []int   `json:"n" doc:"回复次数"`
}

// TokVals 是一格的用量。
type TokVals struct {
	In   int64 `json:"in"`
	CW   int64 `json:"cw"`
	CW1h int64 `json:"cw1h"`
	CR   int64 `json:"cr"`
	Out  int64 `json:"out"`
	N    int   `json:"n"`
}

// LaneTokMinute 是实时推送的一格用量：这一分钟和它所在小时现在的值（绝对值，网页直接替换）。
type LaneTokMinute struct {
	Key    string  `json:"k"`
	Agent  string  `json:"a"`
	Model  string  `json:"model"`
	Minute int64   `json:"m"`
	Tok    TokVals `json:"t"`
	Hour   TokVals `json:"h"`
}

// LaneCold 是一次缓存重建（冷启动）。
type LaneCold struct {
	Key       string `json:"k"`
	Agent     string `json:"a"`
	Model     string `json:"model"`
	At        int64  `json:"at" doc:"Unix 毫秒"`
	Tokens    int64  `json:"tokens" doc:"这次重算的 token 数"`
	Cause     string `json:"cause" enum:"idle,compact,model,start,other" doc:"idle 闲置超过缓存档位；compact 上下文压缩；model 换模型；start 第一次回复；other 看不出"`
	IdleMin   int    `json:"idle_min" doc:"距上一次回复多少分钟"`
	PrevModel string `json:"prev_model,omitempty" doc:"换模型时，之前的模型"`
}

// ApplyUsage 存下一批用量（覆盖同一格），重算涉及的小时，返回要推给网页的格子与冷启动。
// 太早（分钟级已压缩掉）的格子不收，免得只用剩下的分钟把小时级算少。
func (s *Store) ApplyUsage(ctx context.Context, us []protocol.UsageMinute, cs []protocol.ColdStart, now time.Time) ([]LaneTokMinute, []LaneCold, error) {
	cut := unixMinute(now.Add(-LaneKeep)) + 60
	var mins []LaneTokMinute
	var colds []LaneCold
	err := s.tx(ctx, func(tx *sql.Tx) error {
		type hk struct {
			k, a, model string
			h           int64
		}
		hours := map[hk]bool{}
		for _, u := range us {
			if u.Key == "" || u.Minute < cut {
				continue
			}
			if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO lane_tokens(session_key, agent_id, model, minute, inp, cw, cw1h, cr, out, n) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				u.Key, u.Agent, u.Model, u.Minute, u.Input, u.CacheW, u.CacheW1h, u.CacheR, u.Output, u.Requests); err != nil {
				return err
			}
			hours[hk{u.Key, u.Agent, u.Model, u.Minute / 60 * 60}] = true
			mins = append(mins, LaneTokMinute{Key: u.Key, Agent: u.Agent, Model: u.Model, Minute: u.Minute,
				Tok: TokVals{In: u.Input, CW: u.CacheW, CW1h: u.CacheW1h, CR: u.CacheR, Out: u.Output, N: u.Requests}})
		}
		hv := map[hk]TokVals{}
		for h := range hours {
			var v TokVals
			if err := tx.QueryRowContext(ctx, `INSERT OR REPLACE INTO lane_token_hours(session_key, agent_id, model, minute, inp, cw, cw1h, cr, out, n)
				SELECT ?, ?, ?, ?, SUM(inp), SUM(cw), SUM(cw1h), SUM(cr), SUM(out), SUM(n) FROM lane_tokens
				WHERE session_key = ? AND agent_id = ? AND model = ? AND minute >= ? AND minute < ?
				RETURNING inp, cw, cw1h, cr, out, n`,
				h.k, h.a, h.model, h.h, h.k, h.a, h.model, h.h, h.h+60).Scan(&v.In, &v.CW, &v.CW1h, &v.CR, &v.Out, &v.N); err != nil {
				return err
			}
			hv[h] = v
		}
		for i := range mins {
			m := &mins[i]
			m.Hour = hv[hk{m.Key, m.Agent, m.Model, m.Minute / 60 * 60}]
		}
		for _, c := range cs {
			if c.Key == "" || c.At/60000 < cut {
				continue
			}
			res, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO lane_colds(session_key, agent_id, at_ms, model, tokens, cause, idle_min, prev_model) VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
				c.Key, c.Agent, c.At, c.Model, c.Tokens, c.Cause, c.IdleMin, c.PrevModel)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n > 0 {
				colds = append(colds, LaneCold{Key: c.Key, Agent: c.Agent, Model: c.Model, At: c.At, Tokens: c.Tokens, Cause: c.Cause, IdleMin: c.IdleMin, PrevModel: c.PrevModel})
			}
		}
		return nil
	})
	return mins, colds, err
}

// laneTokens 读 [from, to) 内的用量（hourly 读小时级）与冷启动。
func (s *Store) laneTokens(ctx context.Context, from, to int64, hourly bool) ([]LaneTok, []LaneCold, error) {
	table := "lane_tokens"
	if hourly {
		table = "lane_token_hours"
	}
	out := []LaneTok{}
	rows, err := s.db.QueryContext(ctx, `SELECT session_key, agent_id, model, minute, inp, cw, cw1h, cr, out, n FROM `+table+`
		WHERE minute >= ? AND minute < ? ORDER BY session_key, agent_id, model, minute`, from, to)
	if err != nil {
		return nil, nil, err
	}
	var cur *LaneTok
	for rows.Next() {
		var k, a, model string
		var m int64
		var v TokVals
		if err := rows.Scan(&k, &a, &model, &m, &v.In, &v.CW, &v.CW1h, &v.CR, &v.Out, &v.N); err != nil {
			rows.Close()
			return nil, nil, err
		}
		if cur == nil || cur.Key != k || cur.Agent != a || cur.Model != model {
			out = append(out, LaneTok{Key: k, Agent: a, Model: model})
			cur = &out[len(out)-1]
		}
		cur.Minutes = append(cur.Minutes, m)
		cur.In = append(cur.In, v.In)
		cur.CW = append(cur.CW, v.CW)
		cur.CW1h = append(cur.CW1h, v.CW1h)
		cur.CR = append(cur.CR, v.CR)
		cur.Out = append(cur.Out, v.Out)
		cur.N = append(cur.N, v.N)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	colds := []LaneCold{}
	crows, err := s.db.QueryContext(ctx, `SELECT session_key, agent_id, model, at_ms, tokens, cause, idle_min, prev_model FROM lane_colds WHERE at_ms >= ? AND at_ms < ? ORDER BY at_ms`, from*60000, to*60000)
	if err != nil {
		return nil, nil, err
	}
	defer crows.Close()
	for crows.Next() {
		var c LaneCold
		if err := crows.Scan(&c.Key, &c.Agent, &c.Model, &c.At, &c.Tokens, &c.Cause, &c.IdleMin, &c.PrevModel); err != nil {
			return nil, nil, err
		}
		colds = append(colds, c)
	}
	return out, colds, crows.Err()
}

// TokenModels 返回近 30 天有用量的模型（设置里按它列出可填价格的模型），按名字排。
func (s *Store) TokenModels(ctx context.Context, now time.Time) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT model FROM lane_token_hours WHERE minute >= ? AND model <> '' ORDER BY model`, unixMinute(now.Add(-LaneKeep)))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// 缓存余温（R-207）：每个会话最近一次请求的时刻、缓存档位和上下文大小。剩余时间由网页按「档位 − 距最后一次请求」估算，
// 接口不给过期时间，所以都是「约」。只看主会话（子任务有各自的缓存，不影响主会话下一次回复）。

// warmKeep：往回找多久的用量；更早的会话不给余温（停了一周以上，说冷不冷都没有意义）。
const warmKeep = 7 * 24 * time.Hour

// LaneWarm 是一个会话最近一次请求的缓存状态。
type LaneWarm struct {
	Key   string `json:"k"`
	Model string `json:"model"`
	At    int64  `json:"at" doc:"最近一次请求，Unix 毫秒（只精确到分钟）"`
	TTL   int    `json:"ttl" doc:"缓存档位（秒）：3600 / 300 来自用量里写缓存的档位；0 拿不到（没写过缓存，或来源不提供档位）"`
	Ctx   int64  `json:"ctx" doc:"那一分钟平均每次请求的输入总量（读缓存 + 写缓存 + 未命中），下次冷启动要重算的量"`
}

// laneWarm 读近 7 天每个会话最近一次请求的缓存状态。档位只认 Claude Code 对话记录里的写缓存分档（1 小时档有写入记 3600，
// 只有 5 分钟档记 300）；Codex 等来源不提供，记 0，由设置里手填的保留时长补。
func (s *Store) laneWarm(ctx context.Context, now time.Time) ([]LaneWarm, error) {
	from := unixMinute(now.Add(-warmKeep))
	out := []LaneWarm{}
	idx := map[string]int{}
	rows, err := s.db.QueryContext(ctx, `SELECT t.session_key, t.model, t.minute, t.inp, t.cw, t.cr, t.n FROM lane_tokens t
		JOIN (SELECT session_key k, MAX(minute) m FROM lane_tokens WHERE agent_id = '' AND n > 0 AND minute >= ? GROUP BY session_key) l ON t.session_key = l.k AND t.minute = l.m
		WHERE t.agent_id = '' AND t.n > 0`, from)
	if err != nil {
		return nil, err
	}
	nowMs := now.UnixMilli()
	for rows.Next() {
		var k, model string
		var m, in, cw, cr int64
		var n int
		if err := rows.Scan(&k, &model, &m, &in, &cw, &cr, &n); err != nil {
			rows.Close()
			return nil, err
		}
		ctxTok := (in + cw + cr) / int64(n)
		if i, ok := idx[k]; ok { // 同一分钟用了几个模型：取输入更大的
			if ctxTok > out[i].Ctx {
				out[i].Ctx, out[i].Model = ctxTok, model
			}
			continue
		}
		at := m*60000 + 59999
		if at > nowMs {
			at = nowMs
		}
		idx[k] = len(out)
		out = append(out, LaneWarm{Key: k, Model: model, At: at, Ctx: ctxTok})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// 档位：最近一次写过缓存的那一分钟，有 1 小时档的写入就是 1 小时档，否则 5 分钟档
	trows, err := s.db.QueryContext(ctx, `SELECT t.session_key, SUM(t.cw1h) FROM lane_tokens t
		JOIN (SELECT session_key k, MAX(minute) m FROM lane_tokens WHERE agent_id = '' AND cw > 0 AND minute >= ? GROUP BY session_key) l ON t.session_key = l.k AND t.minute = l.m
		WHERE t.agent_id = '' GROUP BY t.session_key`, from)
	if err != nil {
		return nil, err
	}
	defer trows.Close()
	for trows.Next() {
		var k string
		var h1 int64
		if err := trows.Scan(&k, &h1); err != nil {
			return nil, err
		}
		i, ok := idx[k]
		if !ok || !strings.HasPrefix(k, protocol.ToolClaude+":") {
			continue
		}
		out[i].TTL = 300
		if h1 > 0 {
			out[i].TTL = 3600
		}
	}
	return out, trows.Err()
}

// TokenTools 返回近 30 天有用量的来源（会话键冒号前的部分，如 claude / codex），按名字排。设置里按它列出可手填缓存保留时长的来源。
func (s *Store) TokenTools(ctx context.Context, now time.Time) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT substr(session_key, 1, instr(session_key, ':') - 1) AS tool FROM lane_token_hours WHERE minute >= ? AND instr(session_key, ':') > 1 ORDER BY tool`, unixMinute(now.Add(-LaneKeep)))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
