package store

import (
	"context"
	"database/sql"
	"errors"
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

// ProcIgnore 是一次忽略孤儿进程（R-209）。UndoneAt / EndedAt 都为空时有效。
type ProcIgnore struct {
	ID         string     `json:"id"`
	DeviceID   string     `json:"device_id"`
	DeviceName string     `json:"device_name"`
	ProcID     string     `json:"proc_id" doc:"进程号@启动时间"`
	Pid        int        `json:"pid"`
	Cmd        string     `json:"cmd"`
	Project    string     `json:"project"`
	SessionKey string     `json:"session_key"`
	By         string     `json:"by" doc:"谁忽略的：user = 你（网页）"`
	At         time.Time  `json:"at"`
	UndoneBy   string     `json:"undone_by,omitempty"`
	UndoneAt   *time.Time `json:"undone_at,omitempty"`
	EndedAt    *time.Time `json:"ended_at,omitempty" doc:"进程退出、记录失效的时间"`
}

const ignoreCols = `id, device_id, device_name, proc_id, pid, cmd, project, session_key, by, at, undone_by, undone_at, ended_at`

func scanIgnore(row interface{ Scan(...any) error }) (ProcIgnore, error) {
	var p ProcIgnore
	var at string
	var undone, ended sql.NullString
	err := row.Scan(&p.ID, &p.DeviceID, &p.DeviceName, &p.ProcID, &p.Pid, &p.Cmd, &p.Project, &p.SessionKey, &p.By, &at, &p.UndoneBy, &undone, &ended)
	p.At = parseTS(at)
	p.UndoneAt, p.EndedAt = parseNullTS(undone), parseNullTS(ended)
	return p, err
}

// AddProcIgnore 记下一次忽略；同一进程已在忽略中时返回原记录。
func (s *Store) AddProcIgnore(ctx context.Context, p ProcIgnore) (ProcIgnore, error) {
	if cur, err := scanIgnore(s.db.QueryRowContext(ctx, `SELECT `+ignoreCols+` FROM proc_ignores WHERE device_id = ? AND proc_id = ? AND undone_at IS NULL AND ended_at IS NULL`, p.DeviceID, p.ProcID)); err == nil {
		return cur, nil
	}
	p.ID = protocol.NewID()
	if p.At.IsZero() {
		p.At = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO proc_ignores(id, device_id, device_name, proc_id, pid, cmd, project, session_key, by, at) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.DeviceID, p.DeviceName, p.ProcID, p.Pid, p.Cmd, p.Project, p.SessionKey, p.By, ts(p.At))
	return p, err
}

// UndoProcIgnore 取消忽略（留着原行，记下谁、何时取消）；没有有效的忽略时返回 ErrNotFound。
func (s *Store) UndoProcIgnore(ctx context.Context, deviceID, procID, by string) (ProcIgnore, error) {
	cur, err := scanIgnore(s.db.QueryRowContext(ctx, `SELECT `+ignoreCols+` FROM proc_ignores WHERE device_id = ? AND proc_id = ? AND undone_at IS NULL AND ended_at IS NULL`, deviceID, procID))
	if errors.Is(err, sql.ErrNoRows) {
		return cur, ErrNotFound
	}
	if err != nil {
		return cur, err
	}
	at := time.Now().UTC()
	if _, err := s.db.ExecContext(ctx, `UPDATE proc_ignores SET undone_by = ?, undone_at = ? WHERE id = ?`, by, ts(at), cur.ID); err != nil {
		return cur, err
	}
	cur.UndoneBy, cur.UndoneAt = by, &at
	return cur, nil
}

// ActiveProcIgnores 返回有效的忽略，键是 设备 ID + "|" + 进程号@启动时间。
func (s *Store) ActiveProcIgnores(ctx context.Context) (map[string]ProcIgnore, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+ignoreCols+` FROM proc_ignores WHERE undone_at IS NULL AND ended_at IS NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]ProcIgnore{}
	for rows.Next() {
		p, err := scanIgnore(rows)
		if err != nil {
			return nil, err
		}
		out[p.DeviceID+"|"+p.ProcID] = p
	}
	return out, rows.Err()
}

// EndProcIgnores：设备上报的列表里已经没有的进程（退出了），它的忽略自然失效。
func (s *Store) EndProcIgnores(ctx context.Context, deviceID string, live map[string]bool) error {
	rows, err := s.db.QueryContext(ctx, `SELECT id, proc_id FROM proc_ignores WHERE device_id = ? AND undone_at IS NULL AND ended_at IS NULL`, deviceID)
	if err != nil {
		return err
	}
	var gone []string
	for rows.Next() {
		var id, proc string
		if rows.Scan(&id, &proc) == nil && !live[proc] {
			gone = append(gone, id)
		}
	}
	rows.Close()
	for _, id := range gone {
		if _, err := s.db.ExecContext(ctx, `UPDATE proc_ignores SET ended_at = ? WHERE id = ?`, now(), id); err != nil {
			return err
		}
	}
	return nil
}
