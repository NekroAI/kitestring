package store

import (
	"context"
	"time"

	"github.com/NekroAI/kitestring/internal/protocol"
)

// PushSub 是一个浏览器的系统推送订阅（R-49）。
type PushSub struct {
	ID        string     `json:"id"`
	Endpoint  string     `json:"endpoint"`
	P256dh    string     `json:"-"`
	Auth      string     `json:"-"`
	Name      string     `json:"name"`
	Origin    string     `json:"-"`
	CreatedAt time.Time  `json:"created_at"`
	LastOKAt  *time.Time `json:"last_ok_at,omitempty"`
}

// SavePushSub 新建或更新订阅（同一 endpoint 只留一条）。
func (s *Store) SavePushSub(ctx context.Context, p PushSub) (PushSub, error) {
	if p.ID == "" {
		p.ID = protocol.NewID()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO push_subs(id, endpoint, p256dh, auth, name, origin, created_at) VALUES(?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(endpoint) DO UPDATE SET p256dh = excluded.p256dh, auth = excluded.auth, name = excluded.name, origin = excluded.origin`,
		p.ID, p.Endpoint, p.P256dh, p.Auth, p.Name, p.Origin, now())
	if err != nil {
		return p, err
	}
	err = s.db.QueryRowContext(ctx, `SELECT id FROM push_subs WHERE endpoint = ?`, p.Endpoint).Scan(&p.ID)
	return p, err
}

// ListPushSubs 列出全部订阅。
func (s *Store) ListPushSubs(ctx context.Context) ([]PushSub, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, endpoint, p256dh, auth, name, origin, created_at, last_ok_at FROM push_subs ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PushSub{}
	for rows.Next() {
		var p PushSub
		var created, ok string
		if err := rows.Scan(&p.ID, &p.Endpoint, &p.P256dh, &p.Auth, &p.Name, &p.Origin, &created, &ok); err != nil {
			return nil, err
		}
		p.CreatedAt = parseTS(created)
		if ok != "" {
			t := parseTS(ok)
			p.LastOKAt = &t
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// DeletePushSub 按 ID 或 endpoint 删除订阅。
func (s *Store) DeletePushSub(ctx context.Context, idOrEndpoint string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM push_subs WHERE id = ? OR endpoint = ?`, idOrEndpoint, idOrEndpoint)
	return err
}

// TouchPushSub 记下最近一次推送成功的时间。
func (s *Store) TouchPushSub(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE push_subs SET last_ok_at = ? WHERE id = ?`, now(), id)
	return err
}
