package store

import (
	"context"
	"strings"
	"time"

	"github.com/NekroAI/kitestring/internal/protocol"
)

// ProcStop 是一次停止后台进程的留痕（R-209）。
type ProcStop struct {
	ID         string    `json:"id"`
	DeviceID   string    `json:"device_id"`
	DeviceName string    `json:"device_name"`
	ProcID     string    `json:"proc_id" doc:"进程号@启动时间"`
	Pid        int       `json:"pid"`
	Cmd        string    `json:"cmd"`
	Project    string    `json:"project"`
	SessionKey string    `json:"session_key"`
	State      string    `json:"state" doc:"停止时的状态：orphan 孤儿 / detached 已脱离"`
	By         string    `json:"by" doc:"谁停的：user = 你（网页）"`
	Signal     string    `json:"signal" doc:"SIGTERM / SIGKILL；没发信号时为空"`
	Result     string    `json:"result" enum:"exited,running,killed,failed,refused,gone"`
	Detail     string    `json:"detail"`
	At         time.Time `json:"at"`
}

// AddProcStop 记下一次停止。
func (s *Store) AddProcStop(ctx context.Context, p ProcStop) (ProcStop, error) {
	if p.ID == "" {
		p.ID = protocol.NewID()
	}
	if p.At.IsZero() {
		p.At = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO proc_stops(id, device_id, device_name, proc_id, pid, cmd, project, session_key, state, by, signal, result, detail, at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.DeviceID, p.DeviceName, p.ProcID, p.Pid, p.Cmd, p.Project, p.SessionKey, p.State, p.By, p.Signal, p.Result, p.Detail, ts(p.At))
	return p, err
}

// ProcStopsSince 列出 since 之后的停止记录（新的在前）。
func (s *Store) ProcStopsSince(ctx context.Context, since time.Time) ([]ProcStop, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, device_id, device_name, proc_id, pid, cmd, project, session_key, state, by, signal, result, detail, at
		FROM proc_stops WHERE at >= ? ORDER BY at DESC LIMIT 50`, ts(since))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ProcStop{}
	for rows.Next() {
		var p ProcStop
		var at string
		if err := rows.Scan(&p.ID, &p.DeviceID, &p.DeviceName, &p.ProcID, &p.Pid, &p.Cmd, &p.Project, &p.SessionKey, &p.State, &p.By, &p.Signal, &p.Result, &p.Detail, &at); err != nil {
			return nil, err
		}
		p.At = parseTS(at)
		out = append(out, p)
	}
	return out, rows.Err()
}

// ProjectForPath 按设备上登记过的项目目录找 cwd 属于哪个项目（取最长的前缀），找不到返回空。
func (s *Store) ProjectForPath(ctx context.Context, deviceID, cwd string) (id, name string) {
	if cwd == "" {
		return "", ""
	}
	rows, err := s.db.QueryContext(ctx, `SELECT pp.path, p.id, p.name FROM project_paths pp JOIN projects p ON p.id = pp.project_id WHERE pp.device_id = ?`, deviceID)
	if err != nil {
		return "", ""
	}
	defer rows.Close()
	best := -1
	for rows.Next() {
		var path, pid, pname string
		if rows.Scan(&path, &pid, &pname) != nil {
			continue
		}
		if (cwd == path || strings.HasPrefix(cwd, strings.TrimSuffix(path, "/")+"/")) && len(path) > best {
			best, id, name = len(path), pid, pname
		}
	}
	return id, name
}
