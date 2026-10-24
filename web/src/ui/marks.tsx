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

// StatusPill：“运行中 12 分钟”。待命时还有子 Agent 在后台跑，写成“待命 · 后台 1 个任务”（R-115）。
export function StatusPill({ state, since, now, compact, bg }: { state: string; since?: string; now: number; compact?: boolean; bg?: number }) {
  const s = stateStyle[state] ?? stateStyle.idle
  const show = !compact && !!since && state !== 'ended'
  const label = state === 'parked' && bg ? `${t.state[state]}${t.common.sep}${t.session.bgTasks(bg)}` : (t.state[state] ?? state)
  const pill = (
    <span className={cx('inline-flex shrink-0 items-center gap-1.5 rounded-full px-2.5 py-1 text-xs font-medium whitespace-nowrap transition-colors duration-500', s.pill)}>
      <StatusDot state={state} />
      {label}
      {show && <span className="font-normal opacity-70 tabular">{shortDuration(since, now)}</span>}
    </span>
  )
  return show ? <Tip content={t.stateSince(label, duration(since, now), absTime(since))}>{pill}</Tip> : pill
}

// presence：常驻身份（PM / 开发）的在线状态（R-84）。挂着 ks wait 为在线待命；在跑、等授权、在问你为工作中；
// 结束了一轮却没挂上 ks wait、已结束或失联为离线。其它身份不需要常驻，返回空。
type RoleSession = { role?: string; state: string; state_detail?: string; parked?: boolean; done_at?: string | null; last_active_at?: string }
// anyRole：离开期间小结等需要给 Adv 也标在线点时传 true（R-165）
export function presence(s: RoleSession, anyRole = false): '' | 'standby' | 'working' | 'offline' {
  if (!anyRole && s.role !== 'pm' && s.role !== 'dev') return ''
  if (s.state === 'ended' || s.state === 'lost') return 'offline'
  if (s.parked || s.state === 'parked') return 'standby'
  const ds = displayState(s)
  if (ds === 'running' || ds === 'background' || ds === 'waiting_approval' || ds === 'waiting_answer') return 'working'
  return 'offline'
}

export const presenceDot: Record<string, string> = { standby: 'bg-run', working: 'bg-accent', offline: 'bg-fg-3/50' }

// RoleTag：会话身份标（R-124，D-117 方案 B）：描边胶囊写英文短名，PM / Dev 前面一个在线点
// （在线待命绿、工作中蓝、离线灰）。描边与阶段、状态这类实底标签一眼分开；离线不写出来，悬停再说。
export function RoleTag({ role, presence, className }: { role: string; presence?: string; className?: string }) {
  if (!t.role[role]) return null
  return (
    <span className={cx('inline-flex shrink-0 items-center gap-1 rounded-full border border-line-strong px-2 text-[11px] leading-[18px] font-medium whitespace-nowrap', presence === 'offline' ? 'text-fg-3' : 'text-fg-2', className)}>
      {presence && <span className={cx('size-1.5 shrink-0 rounded-full', presenceDot[presence])} />}
      {t.role[role]}
    </span>
  )
}

// RoleBadge：会话卡片、面板上的身份标，悬停说明身份与在线状态。
export function RoleBadge({ s }: { s: RoleSession }) {
  const role = s.role
  if (!role || !t.role[role]) return null
  const p = presence(s)
  return (
    <Tip content={p ? `${t.roleTip[role]}${t.common.sep}${t.presence[p]}` : t.roleTip[role]}>
      <RoleTag role={role} presence={p || undefined} />
    </Tip>
  )
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

// PriorityTag：优先级文字小标（R-129）：P0～P3 直接写出来，悬停说明紧急 / 高 / 普通 / 低。
const priorityTag: Record<string, string> = { P0: 'bg-danger-soft text-danger', P1: 'bg-wait-soft text-wait', P2: 'bg-sunken text-fg-2', P3: 'text-fg-3 ring-1 ring-inset ring-line-strong' }
export function PriorityTag({ p, className }: { p: string; className?: string }) {
  return (
    <Tip content={`${p} · ${t.priority[p] ?? p}`}>
      <span className={cx('inline-flex shrink-0 rounded px-1 font-mono text-[11px] leading-[18px] font-semibold', priorityTag[p] ?? priorityTag.P2, className)} aria-label={t.priority[p] ?? p}>
        {p}
      </span>
    </Tip>
  )
}

export function PriorityDot({ p }: { p: string }) {
  const c = p === 'P0' ? 'bg-danger' : p === 'P1' ? 'bg-wait' : p === 'P2' ? 'bg-accent/60' : 'bg-line-strong'
  return <span className={cx('inline-block size-2 shrink-0 rounded-full', c)} aria-label={t.priority[p] ?? p} />
}
