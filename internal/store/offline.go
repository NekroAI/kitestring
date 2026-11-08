package store

import (
	"context"
	"time"

	"github.com/NekroAI/kitestring/internal/protocol"
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
		q = `UPDATE sessions SET offline_at = '', stopped_at = '', idle_wait_since = '' WHERE key = ? AND (offline_at <> '' OR stopped_at <> '')`
		args = []any{key}
	}
	res, err := s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// MoveUndelivered 把 from 名下还没送达的点名消息（团队消息、答复、留言、指派通知等），连同它提的、还等用户答复的决策，
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

// CloseDupBroadcasts：接任时转过来的积压消息里，同一次广播（同一条身份变动通知）key 已经有一行的，
// 多出的这行关闭、不再送达（R-319：新会话接任前作为无身份会话已收到过同一条广播）。返回关闭了几行。
func (s *Store) CloseDupBroadcasts(ctx context.Context, key string) (int, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE decisions SET status = ?, close_reason = ? WHERE session_key = ? AND kind = ? AND broadcast_id <> '' AND status = ?
		AND EXISTS (SELECT 1 FROM decisions y WHERE y.session_key = decisions.session_key AND y.broadcast_id = decisions.broadcast_id AND y.seq < decisions.seq)`,
		DecClosed, "同一条广播已送给这个会话", key, KindTell, DecAnswered)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// StopQuestion 是停下提醒（R-291）的标题：kind = role 的纸鸢提醒，用标题区分，daemon 据此在 Stop 时送达。
const StopQuestion = protocol.StopQuestion

// SetStopped 记下用户让会话停下（R-291）：先是「停下中」（stopped_at），写完交接或结束本轮不再待命时再记已下线（SetOffline）。
// 已下线、已在停下中的不再重记。返回状态是否有变化。
func (s *Store) SetStopped(ctx context.Context, key string, at time.Time) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE sessions SET stopped_at = ? WHERE key = ? AND offline_at = '' AND stopped_at = ''`, ts(at.UTC()), key)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// StopNoticePending：会话有没有还没送达的停下提醒（R-291）。
func (s *Store) StopNoticePending(ctx context.Context, key string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM decisions WHERE session_key = ? AND kind = ? AND question = ? AND status = ?`, key, KindRole, StopQuestion, DecAnswered).Scan(&n)
	return n > 0, err
}

// StoppedOnDevice 列出设备上已停下的会话（R-291），按会话键：除停下提醒和停下中要用的答复外，点名给它们的消息先不下发。
func (s *Store) StoppedOnDevice(ctx context.Context, deviceID string) (map[string]Session, error) {
	list, err := s.querySessions(ctx, sessionSelect+` WHERE s.device_id = ? AND s.stopped_at <> ''`, deviceID)
	out := map[string]Session{}
	for _, x := range list {
		out[x.Key] = x
	}
	return out, err
}

// StoppableSessions 列出项目里还没结束、能停下的会话（R-291「全部停下」）：不含后台任务、已结束 / 失联、已归档、已交接、已下线、已在停下中的。
func (s *Store) StoppableSessions(ctx context.Context, projectID string) ([]Session, error) {
	return s.querySessions(ctx, sessionSelect+` WHERE s.project_id = ? AND s.background = 0 AND s.state NOT IN (?, ?) AND s.done_at = ''
		AND s.retired_at = '' AND s.offline_at = '' AND s.stopped_at = '' ORDER BY s.last_active_at DESC`, projectID, StateEnded, StateLost)
}

func (s *Store) querySessions(ctx context.Context, q string, args ...any) ([]Session, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Session{}
	for rows.Next() {
		x, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}
