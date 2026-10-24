package store

import (
	"context"
	"database/sql"
	"strings"

	"github.com/NekroAI/kitestring/internal/protocol"
	"github.com/NekroAI/kitestring/internal/summary"
)

// 后台命令（R-208）：daemon 上传整条记录（类型、描述、命令、起止、结果、最后一行输出摘要），这里按会话 + ref 覆盖写入。

// lostSpan：结束时间不知道的命令（lost），时间线上只当它占开始后的这么久（毫秒），查询范围也按这个算。
const lostSpan = 10 * 60 * 1000

const shellCols = `session_key, ref, agent_id, tid, kind, label, cmd, start_ms, end_ms, status, exit_code, last_line, hot, hot_ms, rss_kb`

func clip(s string, n int) string { return summary.Truncate(strings.TrimSpace(s), n) }

// ApplyShells 存下一批后台命令（同一条覆盖），返回规整后的记录（推给网页）。
func (s *Store) ApplyShells(ctx context.Context, deviceID string, cs []protocol.ShellCmd) ([]protocol.ShellCmd, error) {
	var out []protocol.ShellCmd
	err := s.tx(ctx, func(tx *sql.Tx) error {
		for _, c := range cs {
			if c.Key == "" || c.Ref == "" || c.Start <= 0 {
				continue
			}
			c.Label, c.Cmd, c.Last = clip(c.Label, 80), clip(c.Cmd, 1000), clip(c.Last, 160)
			switch c.Kind {
			case protocol.ShellBuild, protocol.ShellDeploy, protocol.ShellService, protocol.ShellPoll:
			default:
				c.Kind = protocol.ShellOther
			}
			switch c.Status {
			case protocol.ShellRunning, protocol.ShellOK, protocol.ShellFailed, protocol.ShellStopped, protocol.ShellEnded, protocol.ShellLost:
			default:
				continue
			}
			if c.Hot != "cpu" && c.Hot != "mem" {
				c.Hot, c.HotAt, c.RSSKB = "", 0, 0
			}
			var end, hotAt any
			if c.End > 0 {
				end = c.End
			}
			if c.HotAt > 0 {
				hotAt = c.HotAt
			}
			if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO lane_shells(`+shellCols+`, device_id) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				c.Key, c.Ref, c.Agent, c.Task, c.Kind, c.Label, c.Cmd, c.Start, end, c.Status, c.Exit, c.Last, c.Hot, hotAt, c.RSSKB, deviceID); err != nil {
				return err
			}
			out = append(out, c)
		}
		return nil
	})
	return out, err
}

func scanShell(rows *sql.Rows) (protocol.ShellCmd, error) {
	var c protocol.ShellCmd
	var end, hotAt sql.NullInt64
	var exit sql.NullInt64
	err := rows.Scan(&c.Key, &c.Ref, &c.Agent, &c.Task, &c.Kind, &c.Label, &c.Cmd, &c.Start, &end, &c.Status, &exit, &c.Last, &c.Hot, &hotAt, &c.RSSKB)
	c.End, c.HotAt = end.Int64, hotAt.Int64
	if exit.Valid {
		v := int(exit.Int64)
		c.Exit = &v
	}
	return c, err
}

// laneShells 返回和 [fromMs, toMs) 有交集的后台命令（还在跑的一直算；不知道结束时间的按开始后 10 分钟算）。
func (s *Store) laneShells(ctx context.Context, fromMs, toMs int64) ([]protocol.ShellCmd, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+shellCols+` FROM lane_shells WHERE start_ms < ? AND (end_ms >= ? OR (end_ms IS NULL AND (status <> 'lost' OR start_ms >= ?)))
		ORDER BY start_ms`, toMs, fromMs, fromMs-lostSpan)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []protocol.ShellCmd{}
	for rows.Next() {
		c, err := scanShell(rows)
		if err != nil {
			return out, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
