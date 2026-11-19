package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"log/slog"
	"runtime"
	"strings"
	"sync/atomic"
	"time"
)

// 慢操作日志（R-374）：记录占用数据库连接超过阈值的操作——一条语句从开始到结果读完（rows.Close），
// 一个事务从 Begin 到 Commit / Rollback——写明耗时、语句开头和调用点，便于查「谁占住了连接、别的请求在排队」。
// 做法是包一层驱动：只多记一次时间和调用栈的 PC，没超过阈值不解析、不写日志。

type slowLog struct {
	threshold atomic.Int64 // 纳秒；0 = 关闭
	log       atomic.Pointer[slog.Logger]
}

func (l *slowLog) on() bool { return l != nil && l.threshold.Load() > 0 }

// done 在操作结束时调用：超过阈值就写一条日志。
func (l *slowLog) done(kind, query string, start time.Time, pcs []uintptr) {
	if !l.on() {
		return
	}
	d := time.Since(start)
	if d < time.Duration(l.threshold.Load()) {
		return
	}
	lg := l.log.Load()
	if lg == nil {
		return
	}
	lg.Warn("数据库操作慢", "kind", kind, "ms", d.Milliseconds(), "sql", shortSQL(query), "caller", callerOf(pcs))
}

// SetSlowLog 设置慢操作阈值（0 关闭）和写日志的 logger。
func (s *Store) SetSlowLog(threshold time.Duration, log *slog.Logger) {
	if s.slow == nil {
		return
	}
	s.slow.threshold.Store(int64(threshold))
	s.slow.log.Store(log)
}

// DBStats 是连接池的统计（等连接的次数和总时长等），给慢请求日志附带。
func (s *Store) DBStats() sql.DBStats { return s.db.Stats() }

func shortSQL(q string) string {
	q = strings.Join(strings.Fields(q), " ")
	if r := []rune(q); len(r) > 160 {
		return string(r[:160]) + "…"
	}
	return q
}

// pcsHere 记下调用栈（只存 PC，便宜）；超过阈值时才由 callerOf 解析。
func (l *slowLog) pcsHere() []uintptr {
	if !l.on() {
		return nil
	}
	pcs := make([]uintptr, 24)
	n := runtime.Callers(3, pcs)
	return pcs[:n]
}

// callerOf 取栈里本项目的前三个函数（跳过本文件与 database/sql），形如 store.(*Store).X ← hub.(*Hub).Y。
func callerOf(pcs []uintptr) string {
	if len(pcs) == 0 {
		return ""
	}
	frames := runtime.CallersFrames(pcs)
	var out []string
	for {
		f, more := frames.Next()
		if strings.Contains(f.Function, "kitestring/internal/") && !strings.HasSuffix(f.File, "slowlog.go") {
			name := f.Function[strings.LastIndex(f.Function, "/")+1:]
			out = append(out, name)
			if len(out) == 3 {
				break
			}
		}
		if !more {
			break
		}
	}
	return strings.Join(out, " ← ")
}

// ---------- 驱动包装 ----------

type timedConnector struct {
	dsn  string
	drv  driver.Driver
	slow *slowLog
}

func (c *timedConnector) Connect(context.Context) (driver.Conn, error) {
	inner, err := c.drv.Open(c.dsn)
	if err != nil {
		return nil, err
	}
	return &timedConn{Conn: inner, slow: c.slow}, nil
}

func (c *timedConnector) Driver() driver.Driver { return c.drv }

// timedConn 包住驱动的连接；接口都转给里面的连接（modernc 的连接全都实现了）。
type timedConn struct {
	driver.Conn
	slow *slowLog
}

var (
	_ driver.ConnBeginTx        = (*timedConn)(nil)
	_ driver.ConnPrepareContext = (*timedConn)(nil)
	_ driver.ExecerContext      = (*timedConn)(nil)
	_ driver.QueryerContext     = (*timedConn)(nil)
	_ driver.Pinger             = (*timedConn)(nil)
	_ driver.SessionResetter    = (*timedConn)(nil)
	_ driver.Validator          = (*timedConn)(nil)
)

func (c *timedConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	start, pcs := time.Now(), c.slow.pcsHere()
	tx, err := c.Conn.(driver.ConnBeginTx).BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &timedTx{Tx: tx, slow: c.slow, start: start, pcs: pcs}, nil
}

func (c *timedConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	return c.Conn.(driver.ConnPrepareContext).PrepareContext(ctx, query)
}

func (c *timedConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	start, pcs := time.Now(), c.slow.pcsHere()
	r, err := c.Conn.(driver.ExecerContext).ExecContext(ctx, query, args)
	c.slow.done("exec", query, start, pcs)
	return r, err
}

func (c *timedConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	start, pcs := time.Now(), c.slow.pcsHere()
	r, err := c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
	if err != nil {
		c.slow.done("query", query, start, pcs)
		return nil, err
	}
	return &timedRows{Rows: r, slow: c.slow, query: query, start: start, pcs: pcs}, nil
}

func (c *timedConn) Ping(ctx context.Context) error {
	if p, ok := c.Conn.(driver.Pinger); ok {
		return p.Ping(ctx)
	}
	return nil
}

func (c *timedConn) ResetSession(ctx context.Context) error {
	if r, ok := c.Conn.(driver.SessionResetter); ok {
		return r.ResetSession(ctx)
	}
	return nil
}

func (c *timedConn) IsValid() bool {
	if v, ok := c.Conn.(driver.Validator); ok {
		return v.IsValid()
	}
	return true
}

type timedTx struct {
	driver.Tx
	slow  *slowLog
	start time.Time
	pcs   []uintptr
}

func (t *timedTx) Commit() error {
	err := t.Tx.Commit()
	t.slow.done("tx", "BEGIN … COMMIT", t.start, t.pcs)
	return err
}

func (t *timedTx) Rollback() error {
	err := t.Tx.Rollback()
	t.slow.done("tx", "BEGIN … ROLLBACK", t.start, t.pcs)
	return err
}

// timedRows 从查询开始计到 Close：结果没读完之前连接一直被占着。
type timedRows struct {
	driver.Rows
	slow   *slowLog
	query  string
	start  time.Time
	pcs    []uintptr
	closed bool
}

func (r *timedRows) Close() error {
	err := r.Rows.Close()
	if !r.closed {
		r.closed = true
		r.slow.done("query", r.query, r.start, r.pcs)
	}
	return err
}
