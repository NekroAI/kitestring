package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/NekroAI/kitestring/internal/protocol"
)

// FileRef 是附件的引用（图片），随决策答复、修改意见、需求描述、证据一起保存。
type FileRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Mime string `json:"mime"`
	Size int64  `json:"size"`
}

// CreateFile 登记一个已写入磁盘的附件。
func (s *Store) CreateFile(ctx context.Context, name, mime, sha string, size int64, uploader string) (FileRef, error) {
	f := FileRef{ID: protocol.NewID(), Name: name, Mime: mime, Size: size}
	_, err := s.db.ExecContext(ctx, `INSERT INTO files(id, name, mime, size, sha256, uploader, created_at) VALUES(?, ?, ?, ?, ?, ?, ?)`, f.ID, name, mime, size, sha, uploader, ts(time.Now()))
	return f, err
}

// GetFile 读取附件信息。
func (s *Store) GetFile(ctx context.Context, id string) (FileRef, error) {
	var f FileRef
	err := s.db.QueryRowContext(ctx, `SELECT id, name, mime, size FROM files WHERE id = ?`, id).Scan(&f.ID, &f.Name, &f.Mime, &f.Size)
	if errors.Is(err, sql.ErrNoRows) {
		return f, ErrNotFound
	}
	return f, err
}

// ResolveFiles 把附件 ID 列表解析成引用，忽略不存在的 ID。
func (s *Store) ResolveFiles(ctx context.Context, ids []string) ([]FileRef, error) {
	out := []FileRef{}
	seen := map[string]bool{}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		f, err := s.GetFile(ctx, id)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, nil
}

func filesJSON(fs []FileRef) string {
	if fs == nil {
		fs = []FileRef{}
	}
	b, _ := json.Marshal(fs)
	return string(b)
}

func parseFiles(s string) []FileRef {
	out := []FileRef{}
	_ = json.Unmarshal([]byte(s), &out)
	return out
}
