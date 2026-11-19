package store

import (
	"context"
	"database/sql"
	"strings"
)

// 读写分离（R-374）：SQLite 同一时刻只能有一个写者，但 WAL 模式下读不挡写、写不挡读。
// 原来全部操作挤在一个连接上，一个慢查询（如需求列表）占着连接时，事件写入、别的页面请求都在后面排队，
// 线上同一接口 1～28 秒，daemon 的长连接也因此断线。
// 现在：写（ExecContext、事务、写语句）走唯一的写连接，仍然串行；读走一组只读连接（query_only），可以并发。
// 调用方照旧写 s.db.XxxContext，由这里按语句分流。
type dbx struct {
	w *sql.DB // 写连接，只有 1 个
	r *sql.DB // 只读连接池
}

// readConns 是只读连接数。
const readConns = 4

func (d *dbx) ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error) {
	return d.w.ExecContext(ctx, q, args...)
}

func (d *dbx) BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error) {
	return d.w.BeginTx(ctx, opts)
}

func (d *dbx) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	return d.pick(q).QueryContext(ctx, q, args...)
}

func (d *dbx) QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row {
	return d.pick(q).QueryRowContext(ctx, q, args...)
}

func (d *dbx) Close() error {
	err := d.r.Close()
	if e := d.w.Close(); e != nil {
		err = e
	}
	return err
}

// Stats 合计两组连接的排队次数和时长。
func (d *dbx) Stats() sql.DBStats {
	a, b := d.w.Stats(), d.r.Stats()
	a.OpenConnections += b.OpenConnections
	a.InUse += b.InUse
	a.Idle += b.Idle
	a.WaitCount += b.WaitCount
	a.WaitDuration += b.WaitDuration
	return a
}

// pick：带 INSERT / UPDATE / DELETE 等写操作的查询（如 … RETURNING）走写连接，其余走只读连接。
func (d *dbx) pick(q string) *sql.DB {
	if writeSQL(q) {
		return d.w
	}
	return d.r
}

var writeWords = []string{"INSERT", "UPDATE", "DELETE", "REPLACE", "CREATE", "DROP", "ALTER", "PRAGMA", "VACUUM", "BEGIN", "ANALYZE", "REINDEX"}

// writeSQL 判断语句会不会写库：看第一个关键字；WITH 开头的再看里面有没有写操作。宁可误判成写（只是少了并发），不能漏判。
func writeSQL(q string) bool {
	f := strings.Fields(strings.ToUpper(q))
	if len(f) == 0 {
		return false
	}
	first := strings.TrimLeft(f[0], "(")
	for _, w := range writeWords {
		if first == w {
			return true
		}
	}
	if first == "WITH" {
		for _, t := range f[1:] {
			switch strings.Trim(t, "(),") {
			case "INSERT", "UPDATE", "DELETE", "REPLACE":
				return true
			}
		}
	}
	return false
}
