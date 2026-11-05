package store

import (
	"context"
	"time"
)

// 空等下线（R-272）：Claude 的提示缓存 1 小时有效，ks wait 单次等待不超过 55 分钟，醒来时缓存还在；
// 常驻会话连续空等（只是 ks wait 超时续等，期间没有要动手的事、你也没发消息）累计超过项目设置的时长后，
// 下一次醒来时纸鸢叫它写交接、下线，不再无限续等。你回来后在项目目录开新会话接任。

const (
	// WaitMaxDefault / WaitMaxCap：Claude Code 的 ks wait 单次最长等待的默认值和上限。
	WaitMaxDefault = 55 * time.Minute
	WaitMaxCap     = 55 * time.Minute
	// IdleOfflineDefault：常驻会话空等累计多久后写交接下线。
	IdleOfflineDefault = 4 * time.Hour
)

// WaitMax 是项目设置的 ks wait 单次最长等待；没设时为默认 55 分钟，不超过 55 分钟。
func (p Project) WaitMax() time.Duration {
	if p.WaitMaxMinutes > 0 {
		return min(time.Duration(p.WaitMaxMinutes)*time.Minute, WaitMaxCap)
	}
	return WaitMaxDefault
}

// IdleOfflineLimit 是项目设置的空等下线时长；没设时为默认 4 小时。
func (p Project) IdleOfflineLimit() time.Duration {
	if p.IdleOfflineMinutes > 0 {
		return time.Duration(p.IdleOfflineMinutes) * time.Minute
	}
	return IdleOfflineDefault
}

// MarkIdleWait 在会话还没有空等起点时记下 at（一段连续空等的开始）；已有时不动。
func (s *Store) MarkIdleWait(ctx context.Context, key string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sessions SET idle_wait_since = ? WHERE key = ? AND idle_wait_since = ''`, ts(at.UTC()), key)
	return err
}

// ResetIdleWait 清除空等起点（有要动手的事、你发了消息、它真的干了活）；at 不为零时从 at 重新算起。
func (s *Store) ResetIdleWait(ctx context.Context, key string, at time.Time) error {
	v := ""
	if !at.IsZero() {
		v = ts(at.UTC())
	}
	_, err := s.db.ExecContext(ctx, `UPDATE sessions SET idle_wait_since = ? WHERE key = ?`, v, key)
	return err
}

// SetOffline 记下 / 清除会话空等下线（R-272）；清除时空等起点一并清掉。返回状态是否有变化。
func (s *Store) SetOffline(ctx context.Context, key string, at *time.Time) (bool, error) {
	q, args := `UPDATE sessions SET offline_at = ? WHERE key = ? AND offline_at = ''`, []any{"", key}
	if at != nil {
		args[0] = ts(at.UTC())
	} else {
		q = `UPDATE sessions SET offline_at = '', idle_wait_since = '' WHERE key = ? AND offline_at <> ''`
		args = []any{key}
	}
	res, err := s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// MoveUndelivered 把 from 名下还没送达的点名消息（团队消息、答复、留言、派活通知等），连同它提的、还等用户答复的决策，
// 改送给 to（R-272）：下线的会话被新会话接任时，下线期间积压的事项转给接任的会话，用户之后的答复也送到新会话。
// 身份通知（kind = role，如「用户请你交接」）留给原会话。返回转了几条。
func (s *Store) MoveUndelivered(ctx context.Context, from, to string) (int, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE decisions SET session_key = ? WHERE session_key = ? AND status IN (?, ?) AND kind <> ?`, to, from, DecAnswered, DecPending, KindRole)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}
