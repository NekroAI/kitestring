package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path"
	"strings"
	"time"

	"github.com/NekroAI/kitestring/internal/protocol"
)

// Workspace 是工作空间（如公司 / 个人），承载通知路由。
type Workspace struct {
	ID        string       `json:"id"`
	Name      string       `json:"name"`
	Notify    NotifyConfig `json:"notify"`
	CreatedAt time.Time    `json:"created_at"`
}

// NotifyConfig 是工作空间的通知渠道配置（D8）。
type NotifyConfig struct {
	Bark  *BarkConfig  `json:"bark,omitempty"`
	WeCom *WeComConfig `json:"wecom,omitempty"`
	// Events 为空表示使用默认事件集合。
	Events []string `json:"events,omitempty"`
}

// BarkConfig 是 Bark 推送配置。
type BarkConfig struct {
	Server string `json:"server"` // 例：https://api.day.app
	Key    string `json:"key"`
}

// WeComConfig 是企业微信群机器人配置。
type WeComConfig struct {
	Webhook string `json:"webhook"`
}

// Project 是按 git remote 归并的项目（D16）。
type Project struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Remote      string `json:"remote"`
	WorkspaceID string `json:"workspace_id,omitempty"`
	DocRoot     string `json:"doc_root"`
	// AutoDispatch：会话空闲时，把排给它的待开始需求整批送过去（R-66），默认开启
	AutoDispatch bool      `json:"auto_dispatch"`
	CreatedAt    time.Time `json:"created_at"`
}

// NormalizeRemote 把各种写法的 git remote 归一化为 host/owner/repo 小写形式。
func NormalizeRemote(remote string) string {
	r := strings.TrimSpace(remote)
	if r == "" {
		return ""
	}
	r = strings.TrimSuffix(r, "/")
	r = strings.TrimSuffix(r, ".git")
	for _, p := range []string{"https://", "http://", "ssh://", "git://"} {
		r = strings.TrimPrefix(r, p)
	}
	if i := strings.Index(r, "@"); i >= 0 && i < strings.IndexAny(r+":/", ":/") {
		r = r[i+1:]
	}
	// scp 形式 host:owner/repo
	if i := strings.Index(r, ":"); i >= 0 && !strings.Contains(r[:i], "/") {
		rest := r[i+1:]
		// 去掉端口号形式 host:22/owner/repo
		if j := strings.Index(rest, "/"); j > 0 && isDigits(rest[:j]) {
			rest = rest[j+1:]
		}
		r = r[:i] + "/" + rest
	}
	return strings.ToLower(r)
}

func isDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return s != ""
}

// ResolveProject 根据设备、目录与 git 信息找到或创建项目，返回项目 ID。
func (s *Store) ResolveProject(ctx context.Context, deviceID, cwd string, git *protocol.Git) (string, error) {
	key := cwd
	remote := ""
	if git != nil && git.Available {
		if git.Root != "" {
			key = git.Root
		}
		remote = NormalizeRemote(git.Remote)
	}
	if key == "" {
		return "", nil
	}
	var pid, pidRemote string
	err := s.db.QueryRowContext(ctx, `SELECT p.id, p.remote FROM project_paths pp JOIN projects p ON p.id = pp.project_id WHERE pp.device_id = ? AND pp.path = ?`, deviceID, key).Scan(&pid, &pidRemote)
	if err == nil {
		if remote == "" || pidRemote == remote {
			return pid, nil
		}
		// 该路径首次建档时还没有 remote（或 remote 变了），按 D16 重新归并。
		return pid, s.tx(ctx, func(t *sql.Tx) error {
			var other string
			err := t.QueryRowContext(ctx, `SELECT id FROM projects WHERE remote = ?`, remote).Scan(&other)
			switch {
			case errors.Is(err, sql.ErrNoRows):
				if pidRemote == "" {
					_, err = t.ExecContext(ctx, `UPDATE projects SET remote = ?, name = ? WHERE id = ?`, remote, path.Base(remote), pid)
					return err
				}
				other = protocol.NewID()
				if _, err := t.ExecContext(ctx, `INSERT INTO projects(id, name, remote, created_at) VALUES(?, ?, ?, ?)`, other, path.Base(remote), remote, now()); err != nil {
					return err
				}
			case err != nil:
				return err
			}
			if _, err := t.ExecContext(ctx, `UPDATE project_paths SET project_id = ? WHERE device_id = ? AND path = ?`, other, deviceID, key); err != nil {
				return err
			}
			_, err = t.ExecContext(ctx, `UPDATE sessions SET project_id = ? WHERE project_id = ? AND device_id = ? AND cwd LIKE ?`, other, pid, deviceID, key+"%")
			pid = other
			return err
		})
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	err = s.tx(ctx, func(t *sql.Tx) error {
		if remote != "" {
			err := t.QueryRowContext(ctx, `SELECT id FROM projects WHERE remote = ?`, remote).Scan(&pid)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}
		if pid == "" {
			pid = protocol.NewID()
			name := path.Base(key)
			if remote != "" {
				name = path.Base(remote)
			}
			if _, err := t.ExecContext(ctx, `INSERT INTO projects(id, name, remote, created_at) VALUES(?, ?, ?, ?)`, pid, name, remote, now()); err != nil {
				return err
			}
		}
		_, err := t.ExecContext(ctx, `INSERT INTO project_paths(device_id, path, project_id) VALUES(?, ?, ?)`, deviceID, key, pid)
		return err
	})
	return pid, err
}

// ListProjects 列出全部项目。
func (s *Store) ListProjects(ctx context.Context) ([]Project, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, remote, COALESCE(workspace_id, ''), doc_root, auto_dispatch, created_at FROM projects ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Project{}
	for rows.Next() {
		var p Project
		var created string
		if err := rows.Scan(&p.ID, &p.Name, &p.Remote, &p.WorkspaceID, &p.DocRoot, &p.AutoDispatch, &created); err != nil {
			return nil, err
		}
		p.CreatedAt = parseTS(created)
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetProject 按 ID 读取项目。
func (s *Store) GetProject(ctx context.Context, id string) (Project, error) {
	var p Project
	var created string
	err := s.db.QueryRowContext(ctx, `SELECT id, name, remote, COALESCE(workspace_id, ''), doc_root, auto_dispatch, created_at FROM projects WHERE id = ?`, id).
		Scan(&p.ID, &p.Name, &p.Remote, &p.WorkspaceID, &p.DocRoot, &p.AutoDispatch, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	p.CreatedAt = parseTS(created)
	return p, err
}

// UpdateProject 修改项目名称、所属工作空间与方案文档根路径。
func (s *Store) UpdateProject(ctx context.Context, p Project) error {
	res, err := s.db.ExecContext(ctx, `UPDATE projects SET name = ?, workspace_id = ?, doc_root = ?, auto_dispatch = ? WHERE id = ?`, p.Name, nullStr(p.WorkspaceID), p.DocRoot, p.AutoDispatch, p.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ListWorkspaces 列出全部工作空间。
func (s *Store) ListWorkspaces(ctx context.Context) ([]Workspace, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, notify, created_at FROM workspaces ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Workspace{}
	for rows.Next() {
		var w Workspace
		var notify, created string
		if err := rows.Scan(&w.ID, &w.Name, &notify, &created); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(notify), &w.Notify)
		w.CreatedAt = parseTS(created)
		out = append(out, w)
	}
	return out, rows.Err()
}

// SaveWorkspace 新建（ID 为空）或更新工作空间。
func (s *Store) SaveWorkspace(ctx context.Context, w Workspace) (Workspace, error) {
	notify, _ := json.Marshal(w.Notify)
	if w.ID == "" {
		w.ID = protocol.NewID()
		w.CreatedAt = time.Now()
		_, err := s.db.ExecContext(ctx, `INSERT INTO workspaces(id, name, notify, created_at) VALUES(?, ?, ?, ?)`, w.ID, w.Name, string(notify), ts(w.CreatedAt))
		return w, err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE workspaces SET name = ?, notify = ? WHERE id = ?`, w.Name, string(notify), w.ID)
	if err != nil {
		return w, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return w, ErrNotFound
	}
	return w, nil
}

// DeleteWorkspace 删除工作空间，其下项目变为未归属。
func (s *Store) DeleteWorkspace(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM workspaces WHERE id = ?`, id)
	return err
}

// WorkspaceForProject 返回项目所属工作空间；未归属时返回 ErrNotFound。
func (s *Store) WorkspaceForProject(ctx context.Context, projectID string) (Workspace, error) {
	var w Workspace
	var notify, created string
	err := s.db.QueryRowContext(ctx, `SELECT w.id, w.name, w.notify, w.created_at FROM workspaces w JOIN projects p ON p.workspace_id = w.id WHERE p.id = ?`, projectID).
		Scan(&w.ID, &w.Name, &notify, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return w, ErrNotFound
	}
	_ = json.Unmarshal([]byte(notify), &w.Notify)
	w.CreatedAt = parseTS(created)
	return w, err
}
