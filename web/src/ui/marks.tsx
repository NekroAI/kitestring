import { absTime, cx, duration, shortDuration } from '../lib/util'
import { t } from '../i18n'
import { Tip } from './base'
import { ClaudeGlyph, CodexGlyph } from './brand'

// 会话状态的视觉映射。waiting_answer 是 waiting_reply 的细分：Agent 正在向你提问或请你确认计划。
export const stateStyle: Record<string, { dot: string; pill: string; ring?: string; live?: boolean; slow?: boolean }> = {
  running: { dot: 'bg-run', pill: 'bg-run-soft text-run', live: true },
  background: { dot: 'bg-accent', pill: 'bg-accent-soft text-accent', live: true, slow: true },
  waiting_approval: { dot: 'bg-wait', pill: 'bg-wait-soft text-wait', ring: 'ring-1 ring-wait/45 border-wait/40' },
  waiting_answer: { dot: 'bg-reply', pill: 'bg-reply-soft text-reply', ring: 'ring-1 ring-reply/45 border-reply/40' },
  waiting_reply: { dot: 'bg-reply', pill: 'bg-reply-soft text-reply', ring: 'ring-1 ring-reply/30 border-reply/30' },
  idle: { dot: 'bg-idle', pill: 'bg-idle-soft text-idle' },
  ended: { dot: 'bg-fg-3/60', pill: 'bg-sunken text-fg-3' },
  done: { dot: 'bg-run', pill: 'bg-run-soft text-run' },
  parked: { dot: 'bg-accent', pill: 'bg-accent-soft text-accent', live: true, slow: true },
  lost: { dot: 'bg-danger', pill: 'bg-danger-soft text-danger' },
}

// displayState 把后端状态映射成界面状态（区分“等你回复”和“等你回答问题”）。
export function displayState(s: { state: string; state_detail?: string; done_at?: string | null; last_active_at?: string }): string {
  // 你手动标为已完成（或把卡在“运行中”的旧对话收起）：之后它真的又有动静，才按实际状态显示
  if (s.done_at && s.state !== 'ended' && (!s.last_active_at || s.last_active_at <= s.done_at || (s.state !== 'running' && s.state !== 'background'))) return 'done'
  if (s.state === 'waiting_reply' && /^(AskUserQuestion|ExitPlanMode|request_user_input)\b/.test(s.state_detail ?? '')) return 'waiting_answer'
  return s.state
}

// StatusDot：一只小纸鸢（菱形）。运行中轻轻摆动，等你授权或回答时一拽一拽。
export function StatusDot({ state, className }: { state: string; className?: string }) {
  const s = stateStyle[state] ?? stateStyle.idle
  const motion = s.live ? (s.slow ? 'kite-sway kite-slow' : 'kite-sway') : state === 'waiting_approval' || state === 'waiting_answer' ? 'kite-tug' : ''
  return (
    <span className={cx('inline-flex size-2 shrink-0 items-center justify-center', className)}>
      <span className={cx('inline-block size-[7px] rotate-45 rounded-[1.5px] transition-colors duration-500', s.dot, motion)} />
    </span>
  )
}

// StatusPill：“运行中 12 分钟”。
export function StatusPill({ state, since, now, compact }: { state: string; since?: string; now: number; compact?: boolean }) {
  const s = stateStyle[state] ?? stateStyle.idle
  const show = !compact && !!since && state !== 'ended'
  const pill = (
    <span className={cx('inline-flex shrink-0 items-center gap-1.5 rounded-full px-2.5 py-1 text-xs font-medium whitespace-nowrap transition-colors duration-500', s.pill)}>
      <StatusDot state={state} />
      {t.state[state] ?? state}
      {show && <span className="font-normal opacity-70 tabular">{shortDuration(since, now)}</span>}
    </span>
  )
  return show ? <Tip content={t.stateSince(t.state[state] ?? state, duration(since, now), absTime(since))}>{pill}</Tip> : pill
}

const phaseStyle: Record<string, string> = {
  planning: 'text-accent',
  implementing: 'text-run',
  debugging: 'text-wait',
  testing: 'text-reply',
  done: 'text-run',
  blocked: 'text-danger',
}

export function PhaseBadge({ phase }: { phase: string }) {
  if (!t.phase[phase]) return null
  return <span className={cx('shrink-0 rounded-md bg-sunken px-1.5 py-0.5 text-[11px] font-medium', phaseStyle[phase])}>{t.phase[phase]}</span>
}

// ToolMark：Claude Code / Codex 的品牌标记，放在统一的柔和底板上。
export function ToolMark({ tool, size = 'md' }: { tool: string; size?: 'sm' | 'md' | 'lg' }) {
  const box = size === 'sm' ? 'size-5 rounded-md' : size === 'lg' ? 'size-9 rounded-xl' : 'size-8 rounded-[10px]'
  const glyph = size === 'sm' ? 'size-3.5' : size === 'lg' ? 'size-6' : 'size-5'
  return (
    <Tip content={t.tool[tool] ?? tool}>
      <span className={cx('inline-flex shrink-0 items-center justify-center border border-line bg-surface-2', box)} aria-label={t.tool[tool] ?? tool}>
        {tool === 'claude' ? <ClaudeGlyph className={glyph} /> : <CodexGlyph className={cx(glyph, 'text-fg')} />}
      </span>
    </Tip>
  )
}

// Sparkline：最近一小时活跃度（12 个 5 分钟柱）。
export function Sparkline({ data, state, className }: { data: number[]; state: string; className?: string }) {
  const max = Math.max(1, ...data)
  const color = state === 'running' ? 'bg-run' : state === 'background' || state === 'parked' ? 'bg-accent' : state === 'waiting_approval' ? 'bg-wait' : state.startsWith('waiting') ? 'bg-reply' : 'bg-fg-3'
  return (
    <Tip content={t.session.activity}>
      <div className={cx('flex h-5 shrink-0 items-end gap-[2px]', className)} aria-label={t.session.activity}>
        {data.map((v, i) => (
          <span key={i} className={cx('w-[3px] rounded-full transition-[height] duration-500', v ? color : 'bg-line', v && i < data.length - 1 && 'opacity-60')} style={{ height: v ? `${Math.max(20, (v / max) * 100)}%` : '14%' }} />
        ))}
      </div>
    </Tip>
  )
}

export function PriorityDot({ p }: { p: string }) {
  const c = p === 'P0' ? 'bg-danger' : p === 'P1' ? 'bg-wait' : p === 'P2' ? 'bg-accent/60' : 'bg-line-strong'
  return <span className={cx('inline-block size-2 shrink-0 rounded-full', c)} aria-label={t.priority[p] ?? p} />
}
