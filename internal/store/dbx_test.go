package store

import (
	"bytes"
	"context"
	"database/sql"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// R-374：一个操作长时间占着数据库（写事务没提交、读到一半的查询没关），别的读请求和写入不再排在它后面。
func TestReadsNotBlockedBySlowOperation(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	pid := testProject(t, s, "p")
	for i := 0; i < 3; i++ {
		if _, err := s.CreateRequirement(ctx, Requirement{ProjectID: pid, Title: "需求", Priority: "P2"}); err != nil {
			t.Fatal(err)
		}
	}

	// 1. 写事务占着写连接：读照常返回
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	within(t, "写事务未提交时读需求列表", func() error {
		_, err := s.ListRequirements(ctx, "", nil)
		return err
	})
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}

	// 2. 读到一半的查询占着一个读连接：别的读和写都照常
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM requirements`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	within(t, "慢查询没读完时读需求列表", func() error {
		_, err := s.ListRequirements(ctx, "", nil)
		return err
	})
	within(t, "慢查询没读完时写入", func() error {
		_, err := s.CreateRequirement(ctx, Requirement{ProjectID: pid, Title: "又一条", Priority: "P2"})
		return err
	})
	rows.Close()

	// 写完立刻能读到（读连接看到已提交的写入）
	list, err := s.ListRequirements(ctx, "", nil)
	if err != nil || len(list) != 4 {
		t.Fatalf("读不到刚写入的需求：%d %v", len(list), err)
	}
}

func within(t *testing.T, what string, f func() error) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- f() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("%s：%v", what, err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("%s：被占着的操作挡住了", what)
	}
}

// 只读连接拒绝写：每个新建的只读连接都带 query_only（写在 DSN 里，每次建连接都生效），
// 漏判成读的写语句会直接报错，不会悄悄和写连接抢锁。同时占住全部只读连接，逐个验证。
func TestReadConnsAreReadOnly(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	for i := 0; i < readConns; i++ {
		c, err := s.db.r.Conn(ctx) // 不归还：下一轮拿到的一定是新建的连接
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		var q int
		if err := c.QueryRowContext(ctx, `PRAGMA query_only`).Scan(&q); err != nil || q != 1 {
			t.Fatalf("第 %d 个只读连接 query_only = %d（%v）", i+1, q, err)
		}
		if _, err := c.ExecContext(ctx, `INSERT INTO settings(key, value) VALUES('x', 'y')`); err == nil {
			t.Fatalf("第 %d 个只读连接居然能写", i+1)
		}
	}
	if st := s.db.r.Stats(); st.OpenConnections != readConns {
		t.Fatalf("只读连接数 %d，应为 %d", st.OpenConnections, readConns)
	}
}

// 事务整段走写连接（含先读后写），不按语句分流：事务里读到的是自己刚写的。
func TestTxStaysOnWriter(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	err := s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO settings(key, value) VALUES('tx', '1')`); err != nil {
			return err
		}
		var v string
		if err := tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = 'tx'`).Scan(&v); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE settings SET value = ? WHERE key = 'tx'`, v+"2")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	var v string
	if err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = 'tx'`).Scan(&v); err != nil || v != "12" {
		t.Fatalf("事务结果不对：%q %v", v, err)
	}
}

func TestWriteSQL(t *testing.T) {
	for q, want := range map[string]bool{
		`SELECT * FROM events`:                               false,
		"  \n\tselect 1":                                     false,
		`WITH x AS (SELECT 1) SELECT * FROM x`:               false,
		`INSERT INTO a VALUES(1) RETURNING id`:               true,
		`update a set b = 1 returning b`:                     true,
		`DELETE FROM a RETURNING id`:                         true,
		`WITH x AS (SELECT 1) INSERT INTO a SELECT * FROM x`: true,
		`INSERT OR REPLACE INTO a SELECT 1 RETURNING a`:      true,
		`REPLACE INTO a VALUES(1)`:                           true,
		`PRAGMA wal_checkpoint`:                              true,
	} {
		if got := writeSQL(q); got != want {
			t.Errorf("writeSQL(%q) = %v，应为 %v", q, got, want)
		}
	}
}

// 慢操作日志：超过阈值的查询记下语句和调用点。
func TestSlowLog(t *testing.T) {
	s := openTest(t)
	var buf syncBuf
	lg := slog.New(slog.NewTextHandler(&buf, nil))
	s.SetSlowLog(time.Nanosecond, lg)
	if _, err := s.ListRequirements(context.Background(), "", nil); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "数据库操作慢") || !strings.Contains(out, "store.(*Store).ListRequirements") {
		t.Fatalf("慢操作日志没写语句或调用点：%s", out)
	}
	s.SetSlowLog(0, lg)
	buf.Reset()
	if _, err := s.ListRequirements(context.Background(), "", nil); err != nil {
		t.Fatal(err)
	}
	if buf.String() != "" {
		t.Fatalf("关掉后还在写：%s", buf.String())
	}
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (w *syncBuf) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

func (w *syncBuf) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.String()
}

func (w *syncBuf) Reset() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.b.Reset()
}
