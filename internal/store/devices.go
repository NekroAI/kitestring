package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/NekroAI/kitestring/internal/protocol"
)

// Device 是一台接入的设备。
type Device struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Hostname  string     `json:"hostname"`
	Platform  string     `json:"platform"`
	Version   string     `json:"version"`
	Revoked   bool       `json:"revoked"`
	Online    bool       `json:"online"`
	LastSeen  *time.Time `json:"last_seen,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
}

const deviceCols = `id, name, hostname, platform, version, revoked, online, last_seen, created_at`

func scanDevice(row interface{ Scan(...any) error }) (Device, error) {
	var d Device
	var lastSeen sql.NullString
	var created string
	err := row.Scan(&d.ID, &d.Name, &d.Hostname, &d.Platform, &d.Version, &d.Revoked, &d.Online, &lastSeen, &created)
	d.LastSeen = parseNullTS(lastSeen)
	d.CreatedAt = parseTS(created)
	return d, err
}

// CreateDevice 新建设备，tokenHash 为设备 token 的哈希。
func (s *Store) CreateDevice(ctx context.Context, name, tokenHash string) (Device, error) {
	id := protocol.NewID()
	_, err := s.db.ExecContext(ctx, `INSERT INTO devices(id, name, token_hash, created_at) VALUES(?, ?, ?, ?)`, id, name, tokenHash, now())
	if err != nil {
		return Device{}, err
	}
	return s.GetDevice(ctx, id)
}

// GetDevice 按 ID 读取设备。
func (s *Store) GetDevice(ctx context.Context, id string) (Device, error) {
	d, err := scanDevice(s.db.QueryRowContext(ctx, `SELECT `+deviceCols+` FROM devices WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return d, ErrNotFound
	}
	return d, err
}

// DeviceByToken 按 token 哈希读取未吊销的设备。
func (s *Store) DeviceByToken(ctx context.Context, tokenHash string) (Device, error) {
	d, err := scanDevice(s.db.QueryRowContext(ctx, `SELECT `+deviceCols+` FROM devices WHERE token_hash = ? AND revoked = 0`, tokenHash))
	if errors.Is(err, sql.ErrNoRows) {
		return d, ErrNotFound
	}
	return d, err
}

// ListDevices 列出全部设备。
func (s *Store) ListDevices(ctx context.Context) ([]Device, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+deviceCols+` FROM devices ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Device{}
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// RevokeDevice 吊销设备 token。
func (s *Store) RevokeDevice(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE devices SET revoked = 1, online = 0 WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetDeviceOnline 更新设备在线状态；hello 非空时同时更新设备信息。
func (s *Store) SetDeviceOnline(ctx context.Context, id string, online bool, hello *protocol.Hello) error {
	if hello != nil {
		_, err := s.db.ExecContext(ctx, `UPDATE devices SET online = ?, last_seen = ?, hostname = ?, platform = ?, version = ? WHERE id = ?`,
			online, now(), hello.Hostname, hello.Platform, hello.Version, id)
		return err
	}
	_, err := s.db.ExecContext(ctx, `UPDATE devices SET online = ?, last_seen = ? WHERE id = ?`, online, now(), id)
	return err
}

// ResetOnline 在 Hub 启动时把所有设备置为离线。
func (s *Store) ResetOnline(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `UPDATE devices SET online = 0`)
	return err
}
