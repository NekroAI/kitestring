// Package store 是 Hub 的 SQLite 持久层。只用标准 SQL 与 database/sql，便于将来切换 Postgres（D12）。
package store

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schema string

// ErrNotFound 表示记录不存在。
var ErrNotFound = errors.New("记录不存在")

// Store 封装数据库连接。
type Store struct {
	db *sql.DB
	// OnRequestVoided：改状态申请因需求状态被改动而自动作废后调用（R-188），hub 用它把结果送回申请方。
	OnRequestVoided func(ids []string)
	// OnWaitSettled：需求变为已完成、或决策被答复后调用（R-269），参数是它的编号（R-x / D-x）；hub 用它解除在等它的受阻需求。
	OnWaitSettled func(code string)
}

// Open 打开（必要时创建）数据库并应用结构。
func Open(path string) (*Store, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// SQLite 单写者：一个连接即可避免锁竞争。
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("应用数据库结构失败：%w", err)
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("数据库迁移失败：%w", err)
	}
	if err := backfillLanes(db); err != nil { // 泳道时间线（R-205）：旧库从事件记录补出历史分钟
		db.Close()
		return nil, fmt.Errorf("补录泳道时间线失败：%w", err)
	}
	return &Store{db: db}, nil
}

// addedColumns 是建表之后新增的列：旧数据库按需补齐（CREATE TABLE IF NOT EXISTS 不会改已有表）。
var addedColumns = []struct{ table, column, ddl string }{
	{"sessions", "state_since", "TEXT NOT NULL DEFAULT ''"},
	{"sessions", "turns", "INTEGER NOT NULL DEFAULT 0"},
	{"sessions", "turn_tools", "INTEGER NOT NULL DEFAULT 0"},
	{"sessions", "last_tool", "TEXT NOT NULL DEFAULT ''"},
	{"sessions", "last_tool_at", "TEXT NOT NULL DEFAULT ''"},
	{"sessions", "subagents", "INTEGER NOT NULL DEFAULT 0"},
	{"sessions", "perm_mode", "TEXT NOT NULL DEFAULT ''"},
	{"sessions", "compactions", "INTEGER NOT NULL DEFAULT 0"},
	{"sessions", "worktree", "INTEGER NOT NULL DEFAULT 0"},
	{"sessions", "title", "TEXT NOT NULL DEFAULT ''"},
	{"sessions", "phase", "TEXT NOT NULL DEFAULT ''"},
	{"sessions", "digest_done", "TEXT NOT NULL DEFAULT ''"},
	{"sessions", "digest_ask", "TEXT NOT NULL DEFAULT ''"},
	{"sessions", "instance", "TEXT NOT NULL DEFAULT ''"},
	{"sessions", "prev_instance", "TEXT NOT NULL DEFAULT ''"},
	{"sessions", "done_at", "TEXT NOT NULL DEFAULT ''"},
	{"sessions", "parked", "INTEGER NOT NULL DEFAULT 0"},
	{"decisions", "option_files", "TEXT NOT NULL DEFAULT '[]'"},
	{"requirements", "kind", "TEXT NOT NULL DEFAULT ''"},
	{"decisions", "approval", "INTEGER NOT NULL DEFAULT 0"},
	{"decisions", "context_files", "TEXT NOT NULL DEFAULT '[]'"},
	{"decisions", "short_title", "TEXT NOT NULL DEFAULT ''"},
	{"decisions", "kind", "TEXT NOT NULL DEFAULT 'decision'"},
	{"decisions", "answer_files", "TEXT NOT NULL DEFAULT '[]'"},
	{"requirements", "files", "TEXT NOT NULL DEFAULT '[]'"},
	{"evidence", "files", "TEXT NOT NULL DEFAULT '[]'"},
	{"requirements", "stage_id", "TEXT REFERENCES plan_stages(id) ON DELETE SET NULL"},
	{"decisions", "plan_id", "TEXT REFERENCES plans(id) ON DELETE SET NULL"},
	{"requirements", "offered_to", "TEXT NOT NULL DEFAULT ''"},
	{"requirements", "offered_at", "TEXT NOT NULL DEFAULT ''"},
	{"projects", "auto_dispatch", "INTEGER NOT NULL DEFAULT 1"},
	{"projects", "hidden", "INTEGER NOT NULL DEFAULT 0"},
	{"sessions", "tools_stale", "TEXT NOT NULL DEFAULT ''"},
	{"sessions", "context_tokens", "INTEGER NOT NULL DEFAULT 0"},
	{"sessions", "context_max", "INTEGER NOT NULL DEFAULT 0"},
	{"sessions", "parked_until", "TEXT NOT NULL DEFAULT ''"},
	{"sessions", "role", "TEXT NOT NULL DEFAULT ''"},
	{"decisions", "assigned_by", "TEXT NOT NULL DEFAULT ''"},
	{"projects", "need_devs", "INTEGER NOT NULL DEFAULT 0"},
	{"projects", "need_reason", "TEXT NOT NULL DEFAULT ''"},
	{"projects", "need_at", "TEXT NOT NULL DEFAULT ''"},
	{"sessions", "last_reply", "TEXT NOT NULL DEFAULT ''"},
	{"sessions", "last_reply_at", "TEXT NOT NULL DEFAULT ''"},
	{"evidence", "payload", "TEXT NOT NULL DEFAULT ''"},
	{"decisions", "blocks", "TEXT NOT NULL DEFAULT ''"},
	{"plan_stages", "summary_blocks", "TEXT NOT NULL DEFAULT ''"},
	{"decisions", "points", "TEXT NOT NULL DEFAULT ''"},
	{"decisions", "summary", "TEXT NOT NULL DEFAULT ''"},
	{"projects", "brief", "TEXT NOT NULL DEFAULT ''"},
	{"projects", "brief_by", "TEXT NOT NULL DEFAULT ''"},
	{"projects", "brief_at", "TEXT NOT NULL DEFAULT ''"},
	{"decisions", "choices", "TEXT NOT NULL DEFAULT ''"},
	{"devices", "deleted", "INTEGER NOT NULL DEFAULT 0"},
	{"sessions", "reply_ask", "TEXT NOT NULL DEFAULT ''"},
	{"decisions", "close_reason", "TEXT NOT NULL DEFAULT ''"},
	{"decisions", "replaced_by", "TEXT NOT NULL DEFAULT ''"},
	{"projects", "agent_release", "INTEGER NOT NULL DEFAULT 0"},
	{"projects", "commit_on_accept", "INTEGER NOT NULL DEFAULT 0"},
	{"projects", "model_policy", "TEXT NOT NULL DEFAULT ''"},
	{"sessions", "wait_on", "TEXT NOT NULL DEFAULT ''"},
	{"sessions", "tasks", "TEXT NOT NULL DEFAULT '[]'"},
	{"decisions", "risk", "TEXT NOT NULL DEFAULT ''"},
	{"decisions", "proxy_by", "TEXT NOT NULL DEFAULT ''"},
	{"decisions", "proxy_reason", "TEXT NOT NULL DEFAULT ''"},
	{"decisions", "proxy_risk", "TEXT NOT NULL DEFAULT ''"},
	{"decisions", "proxy_answer", "TEXT NOT NULL DEFAULT ''"},
	{"decisions", "proxy_at", "TEXT NOT NULL DEFAULT ''"},
	{"decisions", "overridden_at", "TEXT NOT NULL DEFAULT ''"},
	{"decisions", "from_decision", "TEXT NOT NULL DEFAULT ''"},
	{"decisions", "overturns", "INTEGER NOT NULL DEFAULT 0"},
	{"requirements", "from_decision", "TEXT NOT NULL DEFAULT ''"},
	{"decisions", "answer_reason", "TEXT NOT NULL DEFAULT ''"},
	{"status_changes", "reason_kind", "TEXT NOT NULL DEFAULT ''"},
	{"status_changes", "reason", "TEXT NOT NULL DEFAULT ''"},
	{"plans", "returned", "INTEGER NOT NULL DEFAULT 0"},
	{"decisions", "request_kind", "TEXT NOT NULL DEFAULT ''"},
	{"decisions", "request_count", "INTEGER NOT NULL DEFAULT 0"},
	{"decisions", "expires_at", "TEXT NOT NULL DEFAULT ''"},
	{"decisions", "timed_out", "INTEGER NOT NULL DEFAULT 0"},
	{"sessions", "background", "INTEGER NOT NULL DEFAULT 0"},
	{"decisions", "request_from", "TEXT NOT NULL DEFAULT ''"},
	{"decisions", "request_to", "TEXT NOT NULL DEFAULT ''"},
	{"decisions", "request_voided", "INTEGER NOT NULL DEFAULT 0"},
	{"evidence", "void_reason", "TEXT NOT NULL DEFAULT ''"},
	{"evidence", "voided_by", "TEXT NOT NULL DEFAULT ''"},
	{"evidence", "voided_role", "TEXT NOT NULL DEFAULT ''"},
	{"evidence", "voided_at", "TEXT NOT NULL DEFAULT ''"},
	// 会话标题快照（R-195）：动作发生时把会话当时的标题记下来，时间线显示当时的标题；旧数据为空
	{"status_changes", "session_title", "TEXT NOT NULL DEFAULT ''"},
	{"evidence", "session_title", "TEXT NOT NULL DEFAULT ''"},
	{"evidence", "role", "TEXT NOT NULL DEFAULT ''"},
	{"decisions", "by_title", "TEXT NOT NULL DEFAULT ''"},
	{"review_advice", "by_title", "TEXT NOT NULL DEFAULT ''"},
	{"review_advice", "by_role", "TEXT NOT NULL DEFAULT ''"},
	// 团队消息按身份寻址（R-191）：发消息时发送方的身份、发自哪个子任务（agent_id）、回复的是哪条团队消息（决策 ID）
	{"decisions", "from_role", "TEXT NOT NULL DEFAULT ''"},
	{"decisions", "from_agent", "TEXT NOT NULL DEFAULT ''"},
	{"decisions", "reply_to", "TEXT NOT NULL DEFAULT ''"},
	// 交接交出身份（R-224）：被新会话接任后不再挂 ks wait、不再被唤醒；重新声明身份时清除
	{"sessions", "retired_at", "TEXT NOT NULL DEFAULT ''"},
	// 待命超时（R-229）：开发会话待命超过项目设置的时长、手上没有排给它的需求时记下；指派 / 点名给它的消息 / 你给它发消息时清除
	{"sessions", "idle_at", "TEXT NOT NULL DEFAULT ''"},
	{"projects", "dev_idle_minutes", "INTEGER NOT NULL DEFAULT 0"},
	// 空等下线（R-272）：常驻会话连续空等的起点；累计超过项目设置的时长后写交接下线（offline_at），新会话接任时清除
	{"sessions", "idle_wait_since", "TEXT NOT NULL DEFAULT ''"},
	{"sessions", "offline_at", "TEXT NOT NULL DEFAULT ''"},
	{"projects", "idle_offline_minutes", "INTEGER NOT NULL DEFAULT 0"},
	{"projects", "wait_max_minutes", "INTEGER NOT NULL DEFAULT 0"},
	// 状态只认明确记录（R-239）：等待类型、批准 / 拒绝、推荐选项存成字段，老数据补列时一次性回填
	{"sessions", "wait_kind", "TEXT NOT NULL DEFAULT ''"},
	// Claude 配置来源（R-240）：非默认 CLAUDE_CONFIG_DIR 的显示名、目录和启动命令
	{"sessions", "profile", "TEXT NOT NULL DEFAULT ''"},
	{"sessions", "profile_dir", "TEXT NOT NULL DEFAULT ''"},
	{"sessions", "launcher", "TEXT NOT NULL DEFAULT ''"},
	{"decisions", "verdict", "TEXT NOT NULL DEFAULT ''"},
	{"decisions", "rec_option", "INTEGER NOT NULL DEFAULT -1"},
	// 顶部简报的一句话结论和要先看的几条（R-212）
	{"morning_notes", "headline", "TEXT NOT NULL DEFAULT ''"},
	{"morning_notes", "look", "TEXT NOT NULL DEFAULT ''"},
	{"projects", "brief_headline", "TEXT NOT NULL DEFAULT ''"},
	{"projects", "brief_look", "TEXT NOT NULL DEFAULT ''"},
	// 暂时离开（R-210）：决策上标的硬规则类别；授权窗口属于哪次离开
	{"decisions", "hard", "TEXT NOT NULL DEFAULT ''"},
	{"grants", "away_id", "TEXT NOT NULL DEFAULT ''"},
	// 受阻指向（R-243）：受阻的需求在等哪条需求 / 决策（R-x / D-x）、为什么；离开受阻时清空
	{"requirements", "blocked_on", "TEXT NOT NULL DEFAULT ''"},
	{"requirements", "blocked_reason", "TEXT NOT NULL DEFAULT ''"},
	// 进行中停滞（R-243）：进行中的需求多久没动静、也没有会话在跑，就进「待处理」；0 按默认 2 小时
	{"projects", "stall_minutes", "INTEGER NOT NULL DEFAULT 0"},
	// 会话通信（R-249）：团队消息「只告知」改用明确的列（老数据按标题回填）；一次广播拆成每个收件人一行，用 broadcast_id 归组
	{"decisions", "fyi", "INTEGER NOT NULL DEFAULT 0"},
	{"decisions", "broadcast_id", "TEXT NOT NULL DEFAULT ''"},
	// 交接三栏（R-245）：做到一半的事沿用 doing；口头约定、注意事项是新的两栏。旧的 waiting / rules / next / notes 仍保留，老交接照样显示
	{"handoffs", "agreements", "TEXT NOT NULL DEFAULT ''"},
	{"handoffs", "caveats", "TEXT NOT NULL DEFAULT ''"},
	// 指派后未开始处理（R-278）：送达后多久没开始处理就再提醒一次、再过同样时长进「待处理」；0 按默认 10 分钟。
	// 需求上记下送达时间、提醒时间、进「待处理」的时间；状态一变或重新指派就清空
	{"projects", "start_nudge_minutes", "INTEGER NOT NULL DEFAULT 0"},
	{"requirements", "offer_delivered_at", "TEXT NOT NULL DEFAULT ''"},
	{"requirements", "offer_nudged_at", "TEXT NOT NULL DEFAULT ''"},
	{"requirements", "offer_overdue_at", "TEXT NOT NULL DEFAULT ''"},
	// 访客留言（R-281）：所有者放入需求的访客留言，记下来源（访客、分享、针对的对象、改写前的原话、附言），JSON
	{"decisions", "guest", "TEXT NOT NULL DEFAULT ''"},
	// 可交互原型（R-197）：证据、决策、决策选项挂的原型快照（JSON）
	{"evidence", "prototypes", "TEXT NOT NULL DEFAULT ''"},
	{"decisions", "prototypes", "TEXT NOT NULL DEFAULT ''"},
	{"decisions", "option_prototypes", "TEXT NOT NULL DEFAULT ''"},
}

// columnFills：列刚补上时执行一次的回填（R-239），之后不再跑——老数据从文字里推一次，新数据由写入方明确给出。
var columnFills = map[string]func(*sql.DB) error{
	"sessions.wait_kind":   fillWaitKind,
	"decisions.verdict":    fillVerdict,
	"decisions.rec_option": fillRecOption,
	"decisions.fyi":        fillTellFYI,
}

// sessionSnap 读会话现在的标题与身份，写记录时作为快照存下（R-195）。会话不存在时都为空。
func sessionSnap(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, key string) (title, role string) {
	if key == "" {
		return "", ""
	}
	_ = q.QueryRowContext(ctx, `SELECT title, role FROM sessions WHERE key = ?`, key).Scan(&title, &role)
	return title, role
}

func migrate(db *sql.DB) error {
	for _, c := range addedColumns {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, c.table, c.column).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			if _, err := db.Exec(fmt.Sprintf(`ALTER TABLE %s ADD COLUMN %s %s`, c.table, c.column, c.ddl)); err != nil {
				return err
			}
			if fill := columnFills[c.table+"."+c.column]; fill != nil {
				if err := fill(db); err != nil {
					return fmt.Errorf("回填 %s.%s：%w", c.table, c.column, err)
				}
			}
		}
	}
	return nil
}

// Close 关闭数据库。
func (s *Store) Close() error { return s.db.Close() }

// DB 暴露底层连接，供测试与维护命令使用。
func (s *Store) DB() *sql.DB { return s.db }

func now() string { return ts(time.Now()) }

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func parseTS(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}

func parseNullTS(s sql.NullString) *time.Time {
	if !s.Valid || s.String == "" {
		return nil
	}
	t := parseTS(s.String)
	return &t
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// tx 在事务中执行 fn。
func (s *Store) tx(ctx context.Context, fn func(*sql.Tx) error) error {
	t, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(t); err != nil {
		_ = t.Rollback()
		return err
	}
	return t.Commit()
}

// GetSetting 读取设置项，不存在时返回空串。
func (s *Store) GetSetting(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

// SetSetting 写入设置项。
func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO settings(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// CreateWebSession 记录 Web 登录会话。
func (s *Store) CreateWebSession(ctx context.Context, tokenHash string, ttl time.Duration) error {
	n := time.Now()
	_, err := s.db.ExecContext(ctx, `INSERT INTO web_sessions(token_hash, created_at, expires_at) VALUES(?, ?, ?)`, tokenHash, ts(n), ts(n.Add(ttl)))
	return err
}

// ValidWebSession 判断 Web 会话是否有效。
func (s *Store) ValidWebSession(ctx context.Context, tokenHash string) (bool, error) {
	var exp string
	err := s.db.QueryRowContext(ctx, `SELECT expires_at FROM web_sessions WHERE token_hash = ?`, tokenHash).Scan(&exp)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return time.Now().Before(parseTS(exp)), nil
}

// DeleteWebSession 注销 Web 会话；allSessions 为真时清空全部（改密码时使用）。
func (s *Store) DeleteWebSession(ctx context.Context, tokenHash string, allSessions bool) error {
	if allSessions {
		_, err := s.db.ExecContext(ctx, `DELETE FROM web_sessions`)
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM web_sessions WHERE token_hash = ?`, tokenHash)
	return err
}
