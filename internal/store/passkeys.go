package store

import (
	"context"
	"time"
)

// Passkey 是一个已登记的 passkey（R-7）。Credential 是 webauthn.Credential 的 JSON，只含公钥与计数器。
type Passkey struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Origin     string     `json:"origin"`
	RPID       string     `json:"-"`
	Credential string     `json:"-"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
}

func (s *Store) SavePasskey(ctx context.Context, p Passkey) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO passkeys(id, name, origin, rp_id, credential, created_at) VALUES(?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET credential = excluded.credential`, p.ID, p.Name, p.Origin, p.RPID, p.Credential, now())
	return err
}

func (s *Store) ListPasskeys(ctx context.Context) ([]Passkey, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, origin, rp_id, credential, created_at, last_used_at FROM passkeys ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Passkey{}
	for rows.Next() {
		var p Passkey
		var created, used string
		if err := rows.Scan(&p.ID, &p.Name, &p.Origin, &p.RPID, &p.Credential, &created, &used); err != nil {
			return nil, err
		}
		p.CreatedAt = parseTS(created)
		if used != "" {
			t := parseTS(used)
			p.LastUsedAt = &t
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// UsePasskey 登录成功后更新计数器等数据并记下使用时间。
func (s *Store) UsePasskey(ctx context.Context, id, credential string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE passkeys SET credential = ?, last_used_at = ? WHERE id = ?`, credential, now(), id)
	return err
}

func (s *Store) RenamePasskey(ctx context.Context, id, name string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE passkeys SET name = ? WHERE id = ?`, name, id)
	return err
}

func (s *Store) DeletePasskey(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM passkeys WHERE id = ?`, id)
	return err
}
