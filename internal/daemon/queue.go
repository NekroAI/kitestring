package daemon

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	_ "modernc.org/sqlite"

	"github.com/NekroAI/kitestring/internal/protocol"
)

// queue 是 daemon 的本地持久队列：断网时缓存事件与送达回执，恢复后按顺序补传（至少一次，Hub 按 ID 幂等）。
type queue struct {
	db *sql.DB
}

func openQueue(path string) (*queue, error) {
	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(3000)", path))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS outbox (id TEXT PRIMARY KEY, kind TEXT NOT NULL, payload TEXT NOT NULL);
		CREATE TABLE IF NOT EXISTS delivered (decision_id TEXT NOT NULL, session_key TEXT NOT NULL, time TEXT NOT NULL, PRIMARY KEY (decision_id, session_key));
		CREATE TABLE IF NOT EXISTS agent_calls (seq INTEGER PRIMARY KEY AUTOINCREMENT, session_key TEXT NOT NULL, typ TEXT NOT NULL, descr TEXT NOT NULL, at TEXT NOT NULL);
		-- 会话开出来的后台进程（R-209）：pid + 启动时间认一个进程；agent 是开它的 Agent 进程（“进程号@启动时间”，启动时间不知道时只有进程号）；
		-- source：shell = 扫进程表时在会话的后台 shell 下面看到的，env = 命令环境里的 CLAUDE_PID 认出来的。
		-- ended_at / exit_code 留给 R-208（后台命令结束时间与退出码）；现在 ended_at 只记“发现它不在了”的时间。stop_*：停止记录
		CREATE TABLE IF NOT EXISTS bg_procs (
			pid INTEGER NOT NULL, start TEXT NOT NULL, session_key TEXT NOT NULL DEFAULT '', agent TEXT NOT NULL DEFAULT '',
			cwd TEXT NOT NULL DEFAULT '', cmd TEXT NOT NULL DEFAULT '', source TEXT NOT NULL DEFAULT '',
			first_seen TEXT NOT NULL, ended_at TEXT, exit_code INTEGER,
			stop_at TEXT, stop_signal TEXT NOT NULL DEFAULT '', stop_result TEXT NOT NULL DEFAULT '',
			PRIMARY KEY (pid, start));`)
	if err != nil {
		db.Close()
		return nil, err
	}
	return &queue{db: db}, nil
}

func (q *queue) close() error { return q.db.Close() }

func (q *queue) pushEvent(ev protocol.Event) error {
	b, _ := json.Marshal(ev)
	_, err := q.db.Exec(`INSERT OR IGNORE INTO outbox(id, kind, payload) VALUES(?, 'event', ?)`, ev.ID, string(b))
	return err
}

func (q *queue) pushDelivery(d protocol.Delivery) error {
	b, _ := json.Marshal(d)
	_, err := q.db.Exec(`INSERT OR IGNORE INTO outbox(id, kind, payload) VALUES(?, 'delivered', ?)`, "delivered:"+d.DecisionID, string(b))
	if err != nil {
		return err
	}
	_, err = q.db.Exec(`INSERT OR IGNORE INTO delivered(decision_id, session_key, time) VALUES(?, ?, ?)`, d.DecisionID, d.SessionKey, d.Time.Format("2006-01-02T15:04:05.999999999Z07:00"))
	return err
}

// pending 返回待发送的一批消息（按 ID 排序；事件 ID 为 ULID，天然按时间排序）。
func (q *queue) pending(limit int) (events []protocol.Event, deliveries []protocol.Delivery, err error) {
	rows, err := q.db.Query(`SELECT kind, payload FROM outbox ORDER BY id LIMIT ?`, limit)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var kind, payload string
		if err := rows.Scan(&kind, &payload); err != nil {
			return nil, nil, err
		}
		switch kind {
		case "event":
			var ev protocol.Event
			if json.Unmarshal([]byte(payload), &ev) == nil {
				events = append(events, ev)
			}
		case "delivered":
			var d protocol.Delivery
			if json.Unmarshal([]byte(payload), &d) == nil {
				deliveries = append(deliveries, d)
			}
		}
	}
	return events, deliveries, rows.Err()
}

func (q *queue) ack(ids []string) error {
	for _, id := range ids {
		if _, err := q.db.Exec(`DELETE FROM outbox WHERE id = ?`, id); err != nil {
			return err
		}
	}
	return nil
}

func (q *queue) size() int {
	var n int
	_ = q.db.QueryRow(`SELECT COUNT(*) FROM outbox`).Scan(&n)
	return n
}

// wasDelivered 判断本机是否已向该会话注入过某决策（Hub 回执到达前防止重复注入）。
func (q *queue) wasDelivered(decisionID, sessionKey string) bool {
	var n int
	_ = q.db.QueryRow(`SELECT COUNT(*) FROM delivered WHERE decision_id = ? AND session_key = ?`, decisionID, sessionKey).Scan(&n)
	return n > 0
}

// forgetDelivered 清除本机注入记录（Hub 重新置为待接收时，例如用户改答或手动重发）。
func (q *queue) forgetDelivered(decisionID string) {
	_, _ = q.db.Exec(`DELETE FROM delivered WHERE decision_id = ?`, decisionID)
}

// 待配对的子代理调用（R-203）：PreToolUse(Agent) 记下 description，等 SubagentStart 配对；落盘后 daemon 重启也不丢。
type agentCallRow struct {
	seq int64
	agentCall
}

func (q *queue) agentCallsAdd(key string, c agentCall) {
	_, _ = q.db.Exec(`INSERT INTO agent_calls(session_key, typ, descr, at) VALUES(?, ?, ?, ?)`, key, c.typ, c.desc, c.at.UTC().Format(time.RFC3339Nano))
}

func (q *queue) agentCallsLoad(key string) []agentCallRow {
	rows, err := q.db.Query(`SELECT seq, typ, descr, at FROM agent_calls WHERE session_key = ? ORDER BY seq`, key)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []agentCallRow
	for rows.Next() {
		var r agentCallRow
		var at string
		if rows.Scan(&r.seq, &r.typ, &r.desc, &at) == nil {
			r.at, _ = time.Parse(time.RFC3339Nano, at)
			out = append(out, r)
		}
	}
	return out
}

func (q *queue) agentCallDel(seq int64) {
	_, _ = q.db.Exec(`DELETE FROM agent_calls WHERE seq = ?`, seq)
}

func (q *queue) agentCallsClear(key string) {
	_, _ = q.db.Exec(`DELETE FROM agent_calls WHERE session_key = ?`, key)
}

// procRec 是 daemon.db 里记下的一个会话后台进程（R-209）。
type procRec struct {
	pid                         int
	start, key, agent, cwd, cmd string
	source                      string
}

func (r procRec) id() string { return procID(r.pid, r.start) }

func procID(pid int, start string) string { return fmt.Sprint(pid) + "@" + start }

// procsAdd 记下新见到的后台进程（已记过的不动）。
func (q *queue) procsAdd(now time.Time, rs []procRec) {
	for _, r := range rs {
		_, _ = q.db.Exec(`INSERT OR IGNORE INTO bg_procs(pid, start, session_key, agent, cwd, cmd, source, first_seen) VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
			r.pid, r.start, r.key, r.agent, r.cwd, r.cmd, r.source, now.UTC().Format(time.RFC3339))
	}
}

// procsLive 返回还没发现结束的记录，键是“进程号@启动时间”。
func (q *queue) procsLive() map[string]procRec {
	rows, err := q.db.Query(`SELECT pid, start, session_key, agent, cwd, cmd, source FROM bg_procs WHERE ended_at IS NULL`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := map[string]procRec{}
	for rows.Next() {
		var r procRec
		if rows.Scan(&r.pid, &r.start, &r.key, &r.agent, &r.cwd, &r.cmd, &r.source) == nil {
			out[r.id()] = r
		}
	}
	return out
}

// procsEnded 标记这些进程已不在（R-208 之前只有大致时间、没有退出码），并清掉一周前结束的记录。
func (q *queue) procsEnded(now time.Time, ids []procRec) {
	at := now.UTC().Format(time.RFC3339)
	for _, r := range ids {
		_, _ = q.db.Exec(`UPDATE bg_procs SET ended_at = ? WHERE pid = ? AND start = ? AND ended_at IS NULL`, at, r.pid, r.start)
	}
	_, _ = q.db.Exec(`DELETE FROM bg_procs WHERE ended_at IS NOT NULL AND ended_at < ?`, now.Add(-7*24*time.Hour).UTC().Format(time.RFC3339))
}

// procStopped 把一次停止的信号与结果写进记录。
func (q *queue) procStopped(now time.Time, pid int, start, signal, result string) {
	_, _ = q.db.Exec(`UPDATE bg_procs SET stop_at = ?, stop_signal = ?, stop_result = ? WHERE pid = ? AND start = ?`, now.UTC().Format(time.RFC3339), signal, result, pid, start)
}
