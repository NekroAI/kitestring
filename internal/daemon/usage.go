package daemon

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/NekroAI/kitestring/internal/protocol"
)

// token 用量（R-206）：本机后台增量读对话记录里的 usage，按分钟汇总后只把数字发给 Hub。
//
//   - 读哪些：hook 事件带来的对话记录（Claude Code 主会话 <id>.jsonl、Codex rollout-*.jsonl），
//     加上 Claude Code 主会话旁边的 <id>/subagents/agent-*.jsonl（子任务）。记进 daemon.db 的 usage_files，重启后接着读。
//   - 怎么读：每个文件记着读到的字节位置，只读新增；只读到最后一个完整的行；每轮每个文件最多读 usageChunk，
//     每 usageEvery 一轮，文件大小没变就只 stat 一次（不打开）。
//   - 去重：同一条回复在 Claude Code 记录里按内容块出现多次（message.id 相同、usage 相同），只记第一次；
//     Codex 有 token_usage_record 时按 response_id，没有时按 token_count 的累计总数（没变就是重复）。
//   - 汇总：每分钟、每个模型一行，记没命中缓存的输入、写缓存（和其中 1 小时档的）、读缓存、输出、回复次数；
//     新算 = 输入 + 写缓存 + 输出，读缓存只算命中率。读到的位置和汇总在同一个事务里写，重启不重复计。
//   - 冷启动：单次写缓存（Codex 是未命中缓存的输入）超过 coldThreshold，且这次读缓存不到上一次输入总量的一半
//     （前面的对话没命中、整段重算；只是新加了一大段内容、前面照样命中的不算）记一次，起因按上一次回复推：
//     中间有上下文压缩 / 换了模型 / 闲置超过缓存档位（5 分钟或 1 小时）/ 文件里第一次回复。
//   - 发送：有变化的分钟带版本号，连着 Hub 时成批发（绝对值，Hub 覆盖），Hub 回批次号后才标为已发；没回就下次重发。

const (
	usageEvery    = 15 * time.Second
	usageChunk    = 4 << 20 // 每轮每个文件最多读这么多字节
	usageBatch    = 500     // 每批最多发这么多分钟
	coldThreshold = 15000
	usageKeep     = 30 * 24 * time.Hour // 更早的记录不读（Hub 只留 30 天的分钟）
	usageSeenKeep = 64                  // 每个文件记着最近这么多条回复编号（同一回复的重复行是挨着的）
	usageSubIdle  = 2 * time.Hour       // 主会话记录这么久没变，就不再列它的子任务目录
	usageAckWait  = 30 * time.Second    // 发出后这么久没回，下一轮重发
)

const (
	usageClaude = "claude"
	usageCodex  = "codex"
)

// usageFileState 是每个文件除位置以外要记着的东西（推冷启动起因、去重）。
type usageFileState struct {
	LastAt     int64    `json:"last_at,omitempty"` // 上一次回复，Unix 毫秒
	LastModel  string   `json:"last_model,omitempty"`
	LastTTL    int      `json:"last_ttl,omitempty"` // 上一次写缓存用的档位（秒）
	LastCtx    int64    `json:"last_ctx,omitempty"` // 上一次回复的输入总量（读缓存 + 写缓存 + 未命中），看这次的读缓存是不是接着上一次
	Compacted  bool     `json:"compacted,omitempty"`
	Seen       []string `json:"seen,omitempty"`
	CodexTotal int64    `json:"codex_total,omitempty"`
	CodexRec   bool     `json:"codex_rec,omitempty"` // 见过 token_usage_record，之后不看 token_count
	Model      string   `json:"model,omitempty"`     // Codex：turn_context 里的当前模型
}

type usageFile struct {
	path, key, agent, kind string
	offset                 int64
	state                  usageFileState
	size                   int64 // 上一轮看到的大小（只在内存里）
}

type usageScanner struct {
	db  *sql.DB
	now func() time.Time

	mu      sync.Mutex
	files   map[string]*usageFile
	mains   map[string]time.Time // Claude 主会话记录 → 最近一次看到它变化（据此决定还列不列子任务目录）
	batches map[string][]usageSent
	seq     int64
	sentAt  time.Time
	// onLine：Claude Code 记录的每一行也给后台命令（R-208）看一眼（结束通知、后台任务编号）
	onLine func(key, agent string, line []byte, cut time.Time)
}

type usageSent struct {
	key, agent, model string
	minute            int64
	ver               int64
	cold              bool
	at                int64
}

func openUsage(db *sql.DB) (*usageScanner, error) {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS usage_files (path TEXT PRIMARY KEY, session_key TEXT NOT NULL, agent_id TEXT NOT NULL DEFAULT '', kind TEXT NOT NULL,
			offset INTEGER NOT NULL DEFAULT 0, state TEXT NOT NULL DEFAULT '{}', seen_at INTEGER NOT NULL DEFAULT 0);
		CREATE TABLE IF NOT EXISTS usage_minutes (session_key TEXT NOT NULL, agent_id TEXT NOT NULL, model TEXT NOT NULL, minute INTEGER NOT NULL,
			inp INTEGER NOT NULL DEFAULT 0, cw INTEGER NOT NULL DEFAULT 0, cw1h INTEGER NOT NULL DEFAULT 0, cr INTEGER NOT NULL DEFAULT 0,
			out INTEGER NOT NULL DEFAULT 0, n INTEGER NOT NULL DEFAULT 0, ver INTEGER NOT NULL DEFAULT 1, sent INTEGER NOT NULL DEFAULT 0, touched INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (session_key, agent_id, model, minute));
		CREATE TABLE IF NOT EXISTS usage_colds (session_key TEXT NOT NULL, agent_id TEXT NOT NULL, at_ms INTEGER NOT NULL, model TEXT NOT NULL DEFAULT '',
			tokens INTEGER NOT NULL, cause TEXT NOT NULL, idle_min INTEGER NOT NULL DEFAULT 0, prev_model TEXT NOT NULL DEFAULT '', sent INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (session_key, agent_id, at_ms));`)
	if err != nil {
		return nil, err
	}
	u := &usageScanner{db: db, now: time.Now, files: map[string]*usageFile{}, mains: map[string]time.Time{}, batches: map[string][]usageSent{}}
	rows, err := db.Query(`SELECT path, session_key, agent_id, kind, offset, state FROM usage_files`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		f := &usageFile{}
		var st string
		if err := rows.Scan(&f.path, &f.key, &f.agent, &f.kind, &f.offset, &st); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(st), &f.state)
		f.size = -1
		u.files[f.path] = f
		if f.kind == usageClaude && f.agent == "" {
			u.mains[f.path] = time.Time{}
		}
	}
	return u, rows.Err()
}

// track 记下一个会话的对话记录（hook 事件带来的 transcript_path）。子任务的记录由主会话的目录推出来。
func (u *usageScanner) track(key, path string) {
	if path == "" || key == "" || !strings.HasSuffix(path, ".jsonl") || strings.Contains(path, "/subagents/") {
		return
	}
	kind := usageClaude
	if strings.HasPrefix(key, protocol.ToolCodex+":") {
		kind = usageCodex
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if _, ok := u.files[path]; ok {
		return
	}
	u.addLocked(&usageFile{path: path, key: key, kind: kind, size: -1})
	if kind == usageClaude {
		u.mains[path] = u.now()
	}
}

func (u *usageScanner) addLocked(f *usageFile) {
	u.files[f.path] = f
	st, _ := json.Marshal(f.state)
	_, _ = u.db.Exec(`INSERT OR IGNORE INTO usage_files(path, session_key, agent_id, kind, offset, state, seen_at) VALUES(?, ?, ?, ?, 0, ?, ?)`,
		f.path, f.key, f.agent, f.kind, string(st), u.now().Unix())
}

// subagents 列出主会话旁边的子任务记录，新出现的加进来。
func (u *usageScanner) subagents() {
	u.mu.Lock()
	now := u.now()
	var mains []*usageFile
	for p, seen := range u.mains {
		if f := u.files[p]; f != nil && (seen.IsZero() || now.Sub(seen) < usageSubIdle) {
			mains = append(mains, f)
		}
	}
	u.mu.Unlock()
	for _, m := range mains {
		dir := filepath.Join(strings.TrimSuffix(m.path, ".jsonl"), "subagents")
		ents, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		u.mu.Lock()
		for _, e := range ents {
			n := e.Name()
			if e.IsDir() || !strings.HasPrefix(n, "agent-") || !strings.HasSuffix(n, ".jsonl") {
				continue
			}
			p := filepath.Join(dir, n)
			if _, ok := u.files[p]; ok {
				continue
			}
			u.addLocked(&usageFile{path: p, key: m.key, agent: strings.TrimSuffix(strings.TrimPrefix(n, "agent-"), ".jsonl"), kind: usageClaude, size: -1})
		}
		u.mu.Unlock()
	}
}

// scan 读一轮：所有文件的新增部分（每个最多 usageChunk）。返回这一轮写进汇总的回复数。
func (u *usageScanner) scan() int {
	u.subagents()
	u.mu.Lock()
	files := make([]*usageFile, 0, len(u.files))
	for _, f := range u.files {
		files = append(files, f)
	}
	u.mu.Unlock()
	sort.Slice(files, func(i, j int) bool { return files[i].path < files[j].path })
	total := 0
	for _, f := range files {
		n, err := u.scanFile(f)
		if err != nil && !os.IsNotExist(err) {
			continue
		}
		total += n
	}
	u.prune()
	return total
}

type usageAgg struct {
	minutes map[[4]string]*protocol.UsageMinute
	colds   []protocol.ColdStart
	n       int
}

func (a *usageAgg) add(key, agent, model string, at time.Time, in, cw, cw1h, cr, out int64) {
	m := at.Unix() / 60
	k := [4]string{key, agent, model, fmt.Sprint(m)}
	r := a.minutes[k]
	if r == nil {
		r = &protocol.UsageMinute{Key: key, Agent: agent, Model: model, Minute: m}
		a.minutes[k] = r
	}
	r.Input += in
	r.CacheW += cw
	r.CacheW1h += cw1h
	r.CacheR += cr
	r.Output += out
	r.Requests++
	a.n++
}

func (u *usageScanner) scanFile(f *usageFile) (int, error) {
	st, err := os.Stat(f.path)
	if err != nil {
		return 0, err
	}
	size := st.Size()
	if size == f.size && size == f.offset {
		return 0, nil
	}
	if f.size >= 0 && size != f.size && f.agent == "" && f.kind == usageClaude {
		u.mu.Lock()
		u.mains[f.path] = u.now()
		u.mu.Unlock()
	}
	f.size = size
	if size < f.offset {
		// 文件变短了（被截断或换了文件）：从现在的末尾接着读，不回头重算
		f.offset = size
		return 0, u.saveFile(nil, f)
	}
	if size == f.offset {
		return 0, nil
	}
	fh, err := os.Open(f.path)
	if err != nil {
		return 0, err
	}
	defer fh.Close()
	if _, err := fh.Seek(f.offset, io.SeekStart); err != nil {
		return 0, err
	}
	r := bufio.NewReaderSize(fh, 64<<10)
	agg := &usageAgg{minutes: map[[4]string]*protocol.UsageMinute{}}
	off := f.offset
	cut := u.now().Add(-usageKeep)
	shellCut := u.now().Add(-shellKeep)
	for off-f.offset < usageChunk {
		line, err := r.ReadBytes('\n')
		if err != nil {
			break // 没有换行的最后一段还在写，下一轮再读
		}
		off += int64(len(line))
		if f.kind == usageCodex {
			u.codexLine(f, line, cut, agg)
		} else {
			u.claudeLine(f, line, cut, agg)
			if u.onLine != nil {
				u.onLine(f.key, f.agent, line, shellCut)
			}
		}
	}
	if off == f.offset {
		return 0, nil
	}
	f.offset = off
	return agg.n, u.saveFile(agg, f)
}

var (
	markUsage   = []byte(`"usage"`)
	markCompact = []byte(`"compact_boundary"`)
)

type claudeLine struct {
	Type        string    `json:"type"`
	Subtype     string    `json:"subtype"`
	Timestamp   time.Time `json:"timestamp"`
	IsSidechain bool      `json:"isSidechain"`
	Message     *struct {
		ID    string `json:"id"`
		Model string `json:"model"`
		Usage *struct {
			Input         int64 `json:"input_tokens"`
			CacheCreation int64 `json:"cache_creation_input_tokens"`
			CacheRead     int64 `json:"cache_read_input_tokens"`
			Output        int64 `json:"output_tokens"`
			Tiers         *struct {
				M5 int64 `json:"ephemeral_5m_input_tokens"`
				H1 int64 `json:"ephemeral_1h_input_tokens"`
			} `json:"cache_creation"`
		} `json:"usage"`
	} `json:"message"`
}

func (u *usageScanner) claudeLine(f *usageFile, line []byte, cut time.Time, agg *usageAgg) {
	usage, compact := bytes.Contains(line, markUsage), bytes.Contains(line, markCompact)
	if !usage && !compact {
		return
	}
	var l claudeLine
	if json.Unmarshal(line, &l) != nil {
		return
	}
	if l.Type == "system" && l.Subtype == "compact_boundary" {
		f.state.Compacted = true
		return
	}
	m := l.Message
	if l.Type != "assistant" || m == nil || m.Usage == nil || m.ID == "" || l.Timestamp.IsZero() || m.Model == "<synthetic>" {
		return
	}
	if f.agent == "" && l.IsSidechain {
		return // 老版本把子任务也写在主会话记录里：分不清是哪个子任务，不算
	}
	if f.seen(m.ID) {
		return
	}
	us := m.Usage
	var h1 int64
	ttl := f.state.LastTTL
	if us.Tiers != nil {
		h1 = us.Tiers.H1
		switch {
		case us.Tiers.H1 > 0:
			ttl = 3600
		case us.Tiers.M5 > 0:
			ttl = 300
		}
	} else if us.CacheCreation > 0 {
		ttl = 300
	}
	u.note(f, l.Timestamp, m.Model, us.CacheCreation, us.CacheRead, us.Input+us.CacheCreation+us.CacheRead, ttl, cut, agg)
	if l.Timestamp.Before(cut) {
		return
	}
	agg.add(f.key, f.agent, m.Model, l.Timestamp, us.Input, us.CacheCreation, h1, us.CacheRead, us.Output)
}

// seen 记下回复编号，已见过的返回真。
func (f *usageFile) seen(id string) bool {
	for _, s := range f.state.Seen {
		if s == id {
			return true
		}
	}
	f.state.Seen = append(f.state.Seen, id)
	if len(f.state.Seen) > usageSeenKeep {
		f.state.Seen = f.state.Seen[len(f.state.Seen)-usageSeenKeep:]
	}
	return false
}

// note 判断这次回复是不是冷启动，并记下推下一次起因要用的状态。
func (u *usageScanner) note(f *usageFile, at time.Time, model string, rebuild, read, ctx int64, ttl int, cut time.Time, agg *usageAgg) {
	s := &f.state
	if rebuild > coldThreshold && read*2 < s.LastCtx+1 && !at.Before(cut) {
		c := protocol.ColdStart{Key: f.key, Agent: f.agent, Model: model, At: at.UnixMilli(), Tokens: rebuild}
		window := s.LastTTL
		if window == 0 {
			window = 300
		}
		gap := time.Duration(0)
		if s.LastAt > 0 {
			gap = at.Sub(time.UnixMilli(s.LastAt))
			c.IdleMin = int(gap / time.Minute)
		}
		switch {
		case s.LastAt == 0:
			c.Cause = protocol.ColdFirst
		case s.Compacted:
			c.Cause = protocol.ColdCompact
		case s.LastModel != "" && model != "" && s.LastModel != model:
			c.Cause, c.PrevModel = protocol.ColdModel, s.LastModel
		case gap > time.Duration(window)*time.Second:
			c.Cause = protocol.ColdIdle
		default:
			c.Cause = protocol.ColdOther
		}
		agg.colds = append(agg.colds, c)
	}
	s.LastAt, s.Compacted, s.LastTTL, s.LastCtx = at.UnixMilli(), false, ttl, ctx
	if model != "" {
		s.LastModel = model
	}
}

var (
	markTokenCount = []byte(`"token_count"`)
	markTokenRec   = []byte(`"token_usage_record"`)
	markTurnCtx    = []byte(`"turn_context"`)
	markCompacted  = []byte(`compacted"`)
)

type codexUsage struct {
	Input  int64 `json:"input_tokens"`
	Cached int64 `json:"cached_input_tokens"`
	Write  int64 `json:"cache_write_input_tokens"`
	Output int64 `json:"output_tokens"`
	Total  int64 `json:"total_tokens"`
}

type codexLine struct {
	Timestamp time.Time `json:"timestamp"`
	Type      string    `json:"type"`
	Payload   struct {
		Type       string      `json:"type"`
		Model      string      `json:"model"`
		ResponseID string      `json:"response_id"`
		Usage      *codexUsage `json:"usage"`
		Info       *struct {
			Total codexUsage `json:"total_token_usage"`
			Last  codexUsage `json:"last_token_usage"`
		} `json:"info"`
	} `json:"payload"`
}

func (u *usageScanner) codexLine(f *usageFile, line []byte, cut time.Time, agg *usageAgg) {
	tc, rec, ctx, cmp := bytes.Contains(line, markTokenCount), bytes.Contains(line, markTokenRec), bytes.Contains(line, markTurnCtx), bytes.Contains(line, markCompacted)
	if !tc && !rec && !ctx && !cmp {
		return
	}
	var l codexLine
	if json.Unmarshal(line, &l) != nil {
		return
	}
	p := &l.Payload
	var us codexUsage
	switch {
	case l.Type == "turn_context":
		if p.Model != "" {
			f.state.Model = p.Model
		}
		return
	case l.Type == "compacted" || p.Type == "context_compacted":
		f.state.Compacted = true
		return
	case l.Type == "token_usage_record" && p.Usage != nil:
		f.state.CodexRec = true
		if p.ResponseID != "" && f.seen(p.ResponseID) {
			return
		}
		us = *p.Usage
	case l.Type == "event_msg" && p.Type == "token_count" && p.Info != nil && !f.state.CodexRec:
		if p.Info.Total.Total == f.state.CodexTotal {
			return // 同一次回复的重复通知
		}
		f.state.CodexTotal = p.Info.Total.Total
		us = p.Info.Last
	default:
		return
	}
	if l.Timestamp.IsZero() {
		return
	}
	// Codex 的 input_tokens 含命中缓存的部分（和写缓存）
	in := max(us.Input-us.Cached-us.Write, 0)
	rebuild := us.Write
	if rebuild == 0 {
		rebuild = in
	}
	model := f.state.Model
	u.note(f, l.Timestamp, model, rebuild, us.Cached, max(us.Input, us.Cached+us.Write), 300, cut, agg)
	if l.Timestamp.Before(cut) {
		return
	}
	agg.add(f.key, f.agent, model, l.Timestamp, in, us.Write, 0, us.Cached, us.Output)
}

// saveFile 把这一段的汇总和读到的位置写在同一个事务里（重启从这里续读，不重复计）。
func (u *usageScanner) saveFile(agg *usageAgg, f *usageFile) error {
	tx, err := u.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	now := u.now().Unix()
	if agg != nil {
		for _, m := range agg.minutes {
			if _, err := tx.Exec(`INSERT INTO usage_minutes(session_key, agent_id, model, minute, inp, cw, cw1h, cr, out, n, ver, sent, touched) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, 0, ?)
				ON CONFLICT(session_key, agent_id, model, minute) DO UPDATE SET inp = inp + excluded.inp, cw = cw + excluded.cw, cw1h = cw1h + excluded.cw1h,
					cr = cr + excluded.cr, out = out + excluded.out, n = n + excluded.n, ver = ver + 1, touched = excluded.touched`,
				m.Key, m.Agent, m.Model, m.Minute, m.Input, m.CacheW, m.CacheW1h, m.CacheR, m.Output, m.Requests, now); err != nil {
				return err
			}
		}
		for _, c := range agg.colds {
			if _, err := tx.Exec(`INSERT OR IGNORE INTO usage_colds(session_key, agent_id, at_ms, model, tokens, cause, idle_min, prev_model) VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
				c.Key, c.Agent, c.At, c.Model, c.Tokens, c.Cause, c.IdleMin, c.PrevModel); err != nil {
				return err
			}
		}
	}
	st, _ := json.Marshal(f.state)
	if _, err := tx.Exec(`UPDATE usage_files SET offset = ?, state = ?, seen_at = ? WHERE path = ?`, f.offset, string(st), now, f.path); err != nil {
		return err
	}
	return tx.Commit()
}

// prune 清掉两天前、已发出、一小时没动过的分钟和冷启动，以及三十天没变化的文件记录。
func (u *usageScanner) prune() {
	now := u.now()
	old := now.Add(-48*time.Hour).Unix() / 60
	_, _ = u.db.Exec(`DELETE FROM usage_minutes WHERE minute < ? AND sent >= ver AND touched < ?`, old, now.Add(-time.Hour).Unix())
	_, _ = u.db.Exec(`DELETE FROM usage_colds WHERE at_ms < ? AND sent = 1`, old*60000)
	gone := now.Add(-usageKeep).Unix()
	rows, err := u.db.Query(`SELECT path FROM usage_files WHERE seen_at < ?`, gone)
	if err != nil {
		return
	}
	var paths []string
	for rows.Next() {
		var p string
		if rows.Scan(&p) == nil {
			paths = append(paths, p)
		}
	}
	rows.Close()
	u.mu.Lock()
	defer u.mu.Unlock()
	for _, p := range paths {
		if st, err := os.Stat(p); err == nil && now.Sub(st.ModTime()) < usageKeep {
			_, _ = u.db.Exec(`UPDATE usage_files SET seen_at = ? WHERE path = ?`, now.Unix(), p)
			continue
		}
		_, _ = u.db.Exec(`DELETE FROM usage_files WHERE path = ?`, p)
		delete(u.files, p)
		delete(u.mains, p)
	}
}

// pending 取一批还没发出（或发出后又变了）的分钟和冷启动，组成一条消息；没有要发的返回 false。
// 上一批发出后 usageAckWait 内没收到回执就不再取（等回执或超时重发）。
func (u *usageScanner) pending() (protocol.Envelope, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	now := u.now()
	if len(u.batches) > 0 && now.Sub(u.sentAt) < usageAckWait {
		return protocol.Envelope{}, false
	}
	u.batches = map[string][]usageSent{}
	env := protocol.Envelope{Type: protocol.MsgUsage, Usage: []protocol.UsageMinute{}, Colds: []protocol.ColdStart{}}
	var sent []usageSent
	rows, err := u.db.Query(`SELECT session_key, agent_id, model, minute, inp, cw, cw1h, cr, out, n, ver FROM usage_minutes WHERE sent < ver ORDER BY minute LIMIT ?`, usageBatch)
	if err != nil {
		return env, false
	}
	for rows.Next() {
		var m protocol.UsageMinute
		var ver int64
		if rows.Scan(&m.Key, &m.Agent, &m.Model, &m.Minute, &m.Input, &m.CacheW, &m.CacheW1h, &m.CacheR, &m.Output, &m.Requests, &ver) == nil {
			env.Usage = append(env.Usage, m)
			sent = append(sent, usageSent{key: m.Key, agent: m.Agent, model: m.Model, minute: m.Minute, ver: ver})
		}
	}
	rows.Close()
	crows, err := u.db.Query(`SELECT session_key, agent_id, at_ms, model, tokens, cause, idle_min, prev_model FROM usage_colds WHERE sent = 0 ORDER BY at_ms LIMIT ?`, usageBatch)
	if err != nil {
		return env, false
	}
	for crows.Next() {
		var c protocol.ColdStart
		if crows.Scan(&c.Key, &c.Agent, &c.At, &c.Model, &c.Tokens, &c.Cause, &c.IdleMin, &c.PrevModel) == nil {
			env.Colds = append(env.Colds, c)
			sent = append(sent, usageSent{key: c.Key, agent: c.Agent, cold: true, at: c.At})
		}
	}
	crows.Close()
	if len(sent) == 0 {
		return env, false
	}
	u.seq++
	env.Batch = fmt.Sprintf("u%d-%d", now.UnixNano(), u.seq)
	u.batches[env.Batch] = sent
	u.sentAt = now
	return env, true
}

// acked 把 Hub 已存下的批次标为已发（发出后又变了的分钟版本号更大，仍算没发）。
func (u *usageScanner) acked(ids []string) {
	u.mu.Lock()
	var all []usageSent
	for _, id := range ids {
		all = append(all, u.batches[id]...)
		delete(u.batches, id)
	}
	u.mu.Unlock()
	if len(all) == 0 {
		return
	}
	tx, err := u.db.Begin()
	if err != nil {
		return
	}
	defer tx.Rollback() //nolint:errcheck
	for _, s := range all {
		if s.cold {
			_, _ = tx.Exec(`UPDATE usage_colds SET sent = 1 WHERE session_key = ? AND agent_id = ? AND at_ms = ?`, s.key, s.agent, s.at)
		} else {
			_, _ = tx.Exec(`UPDATE usage_minutes SET sent = ? WHERE session_key = ? AND agent_id = ? AND model = ? AND minute = ? AND sent < ?`, s.ver, s.key, s.agent, s.model, s.minute, s.ver)
		}
	}
	_ = tx.Commit()
}

// reset 断线重连后忘掉没回执的批次，马上可以重发。
func (u *usageScanner) reset() {
	u.mu.Lock()
	u.batches = map[string][]usageSent{}
	u.mu.Unlock()
}

// record 记一次外部上报的用量（DSH 插件直接给数字，没有 transcript 可读）。
// 与转录扫描写同一张表：按分钟累加，版本号 +1、sent 置 0，下一轮 pending 会把它带给 Hub。
func (u *usageScanner) record(key, agent, model string, at time.Time, in, cw, cr, out, requests int64) error {
	if key == "" || model == "" {
		return nil
	}
	if at.IsZero() {
		at = u.now()
	}
	if requests <= 0 {
		requests = 1
	}
	minute := at.Unix() / 60
	_, err := u.db.Exec(`INSERT INTO usage_minutes(session_key, agent_id, model, minute, inp, cw, cw1h, cr, out, n, ver, sent, touched) VALUES(?, ?, ?, ?, ?, ?, 0, ?, ?, ?, 1, 0, ?)
		ON CONFLICT(session_key, agent_id, model, minute) DO UPDATE SET inp = inp + excluded.inp, cw = cw + excluded.cw,
			cr = cr + excluded.cr, out = out + excluded.out, n = n + excluded.n, ver = ver + 1, sent = 0, touched = excluded.touched`,
		key, agent, model, minute, max(in, 0), max(cw, 0), max(cr, 0), max(out, 0), requests, u.now().Unix())
	return err
}

// watchUsage 每 usageEvery 读一轮对话记录，连着 Hub 时把变化发出去。
func (d *Daemon) watchUsage(ctx context.Context) {
	t := time.NewTicker(usageEvery)
	defer t.Stop()
	for {
		d.usage.scan()
		d.sendUsage()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (d *Daemon) sendUsage() {
	d.mu.Lock()
	connected := d.connected
	d.mu.Unlock()
	if !connected {
		return
	}
	if env, ok := d.usage.pending(); ok {
		d.sendDirect(env)
	}
}
