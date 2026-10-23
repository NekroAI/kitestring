import { Send, Zap } from 'lucide-react'
import { toast } from 'sonner'
import type { Session } from '../api/client'
import { useSessions } from '../api/queries'
import { t } from '../i18n'
import { StatusDot, displayState } from '../ui/marks'
import { Menu, type MenuItem } from '../ui/overlay'
import { Button, Tip } from '../ui/base'
import { cx } from '../lib/util'
import type { CtxItem } from '../ui/contextMenu'
import { useActions } from './actions'
import { sessionTitle } from './SessionCard'

// 把需求交给某个会话开工（D-14）。选会话时按“能不能马上接手”分三组：
// 待命中的会立刻开工；其它活跃会话在你下次和它对话（或它进入待命）时收到；你收起的会话要你回终端唤醒。

export type TargetGroup = 'now' | 'later' | 'stale'

// 上下文占用（R-60）：知道窗口时按比例，不知道（Claude）时按绝对值——15 万起偏重，60 万起很重，建议开新会话接手。
// 圆环的填充比例：不知道窗口时按 100 万算（Claude Code 的长上下文模型）。
export type CtxLoad = { used: number; max: number; ratio: number; level: 'ok' | 'mid' | 'high'; label: string; short: string }
const fmtK = (n: number) => (n >= 1e6 ? `${(n / 1e6).toFixed(1)}M` : `${Math.round(n / 1000)}k`)
export function ctxLoad(s: Session): CtxLoad | null {
  const used = s.context_tokens ?? 0
  const max = s.context_max ?? 0
  if (!used) return null
  const level = max ? (used / max >= 0.8 ? 'high' : used / max >= 0.5 ? 'mid' : 'ok') : used >= 600_000 ? 'high' : used >= 150_000 ? 'mid' : 'ok'
  const ratio = Math.min(1, used / (max || 1_000_000))
  return { used, max, ratio, level, label: max ? `${fmtK(used)} / ${fmtK(max)}` : fmtK(used), short: max ? `${Math.round((used / max) * 100)}%` : fmtK(used) }
}

// CtxRing：小圆环，填充比例 = 当前对话占上下文窗口的比例；偏重变黄、很重变红。
export function CtxRing({ c, className }: { c: CtxLoad; className?: string }) {
  const r = 5.5
  const len = 2 * Math.PI * r
  return (
    <svg viewBox="0 0 14 14" className={cx('size-3.5 shrink-0 -rotate-90', className)} aria-hidden>
      <circle cx="7" cy="7" r={r} fill="none" stroke="currentColor" strokeOpacity={0.25} strokeWidth="2" />
      <circle cx="7" cy="7" r={r} fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeDasharray={`${Math.max(0.5, c.ratio * len)} ${len}`} />
    </svg>
  )
}

const ctxColor = (c: CtxLoad) => (c.level === 'high' ? 'text-danger' : c.level === 'mid' ? 'text-wait' : 'text-fg-3')
const ctxTip = (c: CtxLoad) => (c.level === 'high' ? t.session.ctxHeavyTip(c.label) : c.level === 'mid' ? t.session.ctxTip(c.label) : t.session.ctxOkTip(c.label))

// CtxMark：会话卡 / 列表里的上下文占用——圆环加简短数字，悬停看详情。
export function CtxMark({ s }: { s: Session }) {
  const c = s.state !== 'ended' && ctxLoad(s)
  if (!c) return null
  return (
    <Tip content={ctxTip(c)}>
      <span className={cx('inline-flex shrink-0 items-center gap-1 tabular', ctxColor(c))}>
        <CtxRing c={c} />
        {c.short}
      </span>
    </Tip>
  )
}

const heavy = (s: Session) => (ctxLoad(s)?.level === 'high' ? 2 : ctxLoad(s)?.level === 'mid' ? 1 : 0)

export function targetGroup(s: Session): TargetGroup {
  const ds = displayState(s)
  // 挂着 ks wait 的会话（哪怕本轮还没结束、仍显示运行中）收到就会立刻响应
  if (ds === 'parked' || (s.parked && ds !== 'done')) return 'now'
  if (ds === 'done') return 'stale'
  return 'later'
}

export function assignCandidates(sessions: Session[], projectId?: string | null): Session[] {
  const order: Record<TargetGroup, number> = { now: 0, later: 1, stale: 2 }
  return sessions
    .filter((s) => s.state !== 'ended' && s.state !== 'lost')
    .sort(
      (a, b) =>
        order[targetGroup(a)] - order[targetGroup(b)] ||
        Number((b.project_id ?? '') === (projectId ?? '')) - Number((a.project_id ?? '') === (projectId ?? '')) ||
        heavy(a) - heavy(b) || // 同等条件下先推荐上下文轻的会话

        b.last_active_at.localeCompare(a.last_active_at),
    )
}

// defaultTarget：新建需求时默认交给谁——同项目里待命的会话，其次一小时内活跃过的；收起的不算。
export function defaultTarget(sessions: Session[], projectId?: string | null): Session | undefined {
  const same = assignCandidates(sessions, projectId).filter((s) => (s.project_id ?? '') === (projectId ?? '') && targetGroup(s) !== 'stale')
  const light = same.filter((s) => ctxLoad(s)?.level !== 'high') // 很重的会话不默认推荐
  return light.find((s) => targetGroup(s) === 'now') ?? light.find((s) => Date.now() - Date.parse(s.last_active_at) < 3600_000)
}

// targetItems：按分组生成菜单项（下拉菜单与右键菜单共用）。
export function targetItems(sessions: Session[], projectId: string | null | undefined, onPick: (s: Session) => void, opts: { exclude?: string; checked?: string } = {}): MenuItem[] {
  const list = assignCandidates(sessions, projectId).filter((s) => s.key !== opts.exclude)
  const out: MenuItem[] = []
  for (const g of ['now', 'later', 'stale'] as const) {
    const part = list.filter((s) => targetGroup(s) === g).slice(0, 10)
    if (!part.length) continue
    if (out.length) out.push({ type: 'separator' })
    out.push({ type: 'label', label: t.assign.group[g] })
    for (const s of part)
      out.push({
        // 状态跟在标题后面，第二行放项目 · 设备 · 上下文，窄菜单里也不会被截掉（R-60）
        label: (
          <span className="flex min-w-0 items-baseline gap-1.5">
            <span className="truncate">{sessionTitle(s)}</span>
            <span className="shrink-0 text-xs text-fg-3">{t.state[displayState(s)]}</span>
          </span>
        ),
        hint: (
          <span className="flex min-w-0 items-center gap-1.5">
            <span className="truncate">{[s.project_name, s.device_name].filter(Boolean).join(t.common.sep)}</span>
            <CtxMark s={s} />
          </span>
        ),
        icon: g === 'now' ? <Zap className="text-accent" /> : <StatusDot state={displayState(s)} />,
        checked: opts.checked === s.key,
        onSelect: () => onPick(s),
      })
  }
  return out
}

export const targetLabel = (s: Session) => [sessionTitle(s), s.device_name].filter(Boolean).join(t.common.sep)

export function useAssign() {
  const { assign } = useActions()
  return async (code: string, s: Session) => {
    if (await assign(code, s.key)) toast.success(t.assign.done(code, sessionTitle(s), targetGroup(s)))
  }
}

// assignItems：右键菜单“交给会话”子菜单的条目。
export function assignItems(sessions: Session[], projectId: string | null | undefined, onPick: (s: Session) => void): CtxItem[] {
  const items = targetItems(sessions, projectId, onPick)
  return items.length ? (items as CtxItem[]) : [{ label: t.assign.none, onSelect: () => {}, disabled: true }]
}

// AssignButton：需求抽屉底部的“交给…”。
export function AssignButton({ code, projectId }: { code: string; projectId?: string | null }) {
  const sessions = useSessions()
  const pick = useAssign()
  const list = assignCandidates(sessions.data?.sessions ?? [], projectId)
  return (
    <Menu
      width="w-80"
      trigger={
        <Button variant="secondary" icon={<Send className="size-4" />} disabled={!list.length}>
          {t.assign.button}
        </Button>
      }
      items={targetItems(sessions.data?.sessions ?? [], projectId, (s) => void pick(code, s))}
    />
  )
}
