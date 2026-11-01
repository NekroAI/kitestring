package store

import (
	"context"
	"time"

	"github.com/NekroAI/kitestring/internal/protocol"
)

// 在线会话列表（R-249）：每个会话的状态与所用工具，只从会话记录推（原则第 11 条），不看对话文字。
// 和 Presence() 分开：Presence 只管常驻身份（工人池、自动派活依赖它，不改）；Availability 管所有会话。

// Availability 的取值。
const (
	AvailStandby = "standby" // 待命：挂着 ks wait（Claude 后台、Codex 前台）或 DSH 插件在待命
	AvailBusy    = "busy"    // 忙：这一轮正在跑（含后台任务在跑、等用户授权、用提问工具在问用户）
	AvailOnline  = "online"  // 在线：一轮结束、没挂 ks wait（等用户下次发消息）
	AvailOffline = "offline" // 离线：会话已结束或失联
)

// Availability 是会话现在的状态（待命 / 忙 / 在线 / 离线），用当前时间判断 Codex 前台 ks wait 是否已到期。
func (x Session) Availability() string { return x.AvailabilityAt(time.Now()) }

// AvailabilityAt 同 Availability，时间由调用方给（测试、同一批里统一时刻用）。
// 规则：
//   - 已结束 / 失联 → offline；
//   - 正在跑、后台任务在跑、等授权、用提问工具在问 → busy（Claude 即使还挂着后台 ks wait 也先算忙：这一轮结束后才收到）；
//   - 挂着 ks wait / DSH 插件待命 → standby；Codex 的前台 ks wait 过了 ParkedUntil 不再算待命（记录没来得及收回），算 online；
//   - 其余（一轮结束在等用户、刚开始）→ online。
func (x Session) AvailabilityAt(now time.Time) string {
	switch {
	case x.State == StateEnded || x.State == StateLost:
		return AvailOffline
	case x.State == StateRunning || x.State == StateBackground || x.State == StateWaitApproval:
		return AvailBusy
	case x.State == StateWaitReply && x.WaitKind == WaitQuestion:
		return AvailBusy
	case x.Parked || x.State == StateParked:
		if x.CodexWaitExpired(now) {
			return AvailOnline
		}
		return AvailStandby
	}
	return AvailOnline
}

// CodexWaitExpired：Codex 会话的前台 ks wait 记录还挂着，但已过了 ParkedUntil。
func (x Session) CodexWaitExpired(now time.Time) bool {
	return x.Tool == protocol.ToolCodex && x.Parked && x.ParkedUntil != nil && !now.Before(*x.ParkedUntil)
}

// Harness 是会话所用工具的显示名：Claude Code、配了启动命令的写启动命令（如 claude-x）、
// Codex 终端 / Codex App / Codex exec、DSH。只看 tool、launcher、client 三个字段。
func (x Session) Harness() string {
	switch x.Tool {
	case protocol.ToolClaude:
		if x.Launcher != "" {
			return x.Launcher
		}
		return "Claude Code"
	case protocol.ToolCodex:
		switch x.Client {
		case "app-server", "desktop":
			return "Codex App"
		case "exec":
			return "Codex exec"
		case "cli":
			return "Codex 终端"
		}
		return "Codex"
	case protocol.ToolDSH:
		return "DSH"
	}
	return x.Tool
}

// SessionRoster 列出会话（在线会话列表用，R-249）：projectID 为空时列所有项目；withEnded 为假时不列已结束的（失联的照列，显示离线）。
// 不含桌面应用自己的后台任务（R-179）。按最近活跃排序，最多 limit 个（≤0 不限），另返回符合条件的总数。
// 只补关联的需求，不补工具动作、活跃度等工作台字段。
func (s *Store) SessionRoster(ctx context.Context, projectID string, withEnded bool, limit int) ([]Session, int, error) {
	where := ` WHERE s.background = 0`
	args := []any{}
	if projectID != "" {
		where += ` AND s.project_id = ?`
		args = append(args, projectID)
	}
	if !withEnded {
		where += ` AND s.state <> ?`
		args = append(args, StateEnded)
	}
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions s`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	q := sessionSelect + where + ` ORDER BY s.last_active_at DESC`
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, 0, err
	}
	out := []Session{}
	for rows.Next() {
		x, err := scanSession(rows)
		if err != nil {
			rows.Close()
			return nil, 0, err
		}
		out = append(out, x)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, 0, err
	}
	for i := range out {
		if err := s.fillReqRefs(ctx, &out[i]); err != nil {
			return nil, 0, err
		}
	}
	return out, total, nil
}
