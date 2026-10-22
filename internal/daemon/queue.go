package daemon

import (
	"database/sql"
	"encoding/json"
	"fmt"

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
		CREATE TABLE IF NOT EXISTS delivered (decision_id TEXT NOT NULL, session_key TEXT NOT NULL, time TEXT NOT NULL, PRIMARY KEY (decision_id, session_key));`)
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
