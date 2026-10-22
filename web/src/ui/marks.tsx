import { cx, duration } from '../lib/util'
import { t } from '../i18n'

// 会话状态的视觉映射：颜色、是否醒目、是否有呼吸动画。
export const stateStyle: Record<string, { dot: string; pill: string; ring?: string; live?: boolean }> = {
  running: { dot: 'bg-run', pill: 'bg-run-soft text-run', live: true },
  waiting_approval: { dot: 'bg-wait', pill: 'bg-wait-soft text-wait', ring: 'ring-1 ring-wait/50 border-wait/40' },
  waiting_reply: { dot: 'bg-reply', pill: 'bg-reply-soft text-reply', ring: 'ring-1 ring-reply/40 border-reply/40' },
  idle: { dot: 'bg-idle', pill: 'bg-idle-soft text-idle' },
  ended: { dot: 'bg-fg-3/60', pill: 'bg-sunken text-fg-3' },
  lost: { dot: 'bg-danger', pill: 'bg-danger-soft text-danger' },
}

export function StatusDot({ state, className }: { state: string; className?: string }) {
  const s = stateStyle[state] ?? stateStyle.idle
  return (
    <span className={cx('relative inline-flex size-2 shrink-0', className)}>
      {s.live && <span className={cx('absolute inset-0 animate-ping rounded-full opacity-60', s.dot)} />}
      <span className={cx('relative inline-flex size-2 rounded-full', s.dot)} />
    </span>
  )
}

// StatusPill：“运行中 12 分钟”。
export function StatusPill({ state, since, now, compact }: { state: string; since?: string; now: number; compact?: boolean }) {
  const s = stateStyle[state] ?? stateStyle.idle
  const label = t.state[state] ?? state
  const dur = !compact && since && state !== 'ended' ? duration(since, now) : ''
  return (
    <span className={cx('inline-flex items-center gap-1.5 rounded-full px-2.5 py-1 text-xs font-medium whitespace-nowrap', s.pill)}>
      <StatusDot state={state} />
      {label}
      {dur && <span className="font-normal opacity-75 tabular">{dur}</span>}
    </span>
  )
}

// ToolMark：Claude / Codex 的识别标记。
export function ToolMark({ tool, size = 'md' }: { tool: string; size?: 'sm' | 'md' }) {
  const box = size === 'sm' ? 'size-5 rounded-md' : 'size-7 rounded-lg'
  if (tool === 'claude') {
    return (
      <span className={cx('inline-flex shrink-0 items-center justify-center bg-[#d97757] text-white', box)} title={t.tool.claude}>
        <svg viewBox="0 0 24 24" className={size === 'sm' ? 'size-3' : 'size-4'} fill="currentColor" aria-hidden>
          <path d="M12 2.5l1.6 6.1 5.6-2.9-3.5 5.2 6 1.1-6 1.1 3.5 5.2-5.6-2.9L12 21.5l-1.6-6.1-5.6 2.9 3.5-5.2-6-1.1 6-1.1-3.5-5.2 5.6 2.9z" />
        </svg>
      </span>
    )
  }
  return (
    <span className={cx('inline-flex shrink-0 items-center justify-center bg-fg text-bg', box)} title={t.tool.codex}>
      <svg viewBox="0 0 24 24" className={size === 'sm' ? 'size-3' : 'size-4'} fill="none" stroke="currentColor" strokeWidth="2.4" strokeLinecap="round" strokeLinejoin="round" aria-hidden>
        <path d="M7 8l4 4-4 4M13 16h4" />
      </svg>
    </span>
  )
}

// Sparkline：最近一小时活跃度（12 个 5 分钟柱）。
export function Sparkline({ data, state, className }: { data: number[]; state: string; className?: string }) {
  const max = Math.max(1, ...data)
  const color = state === 'running' ? 'bg-run' : state === 'waiting_approval' ? 'bg-wait' : state === 'waiting_reply' ? 'bg-reply' : 'bg-fg-3'
  return (
    <div className={cx('flex h-5 items-end gap-[2px]', className)} title={t.sessions.activity} aria-label={t.sessions.activity}>
      {data.map((v, i) => (
        <span
          key={i}
          className={cx('w-[3px] rounded-full transition-[height] duration-500', v ? color : 'bg-line', v && i < data.length - 1 && 'opacity-60')}
          style={{ height: v ? `${Math.max(18, (v / max) * 100)}%` : '12%' }}
        />
      ))}
    </div>
  )
}

export function PriorityDot({ p }: { p: string }) {
  const c = p === 'P0' ? 'bg-danger' : p === 'P1' ? 'bg-wait' : p === 'P2' ? 'bg-accent/60' : 'bg-line-strong'
  return <span className={cx('inline-block size-2 shrink-0 rounded-full', c)} title={t.priority[p] ?? p} />
}
