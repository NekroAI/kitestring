import { CircleDashed, Clock3, Inbox, Loader, Lock, PauseCircle, UserX } from 'lucide-react'
import type { Requirement, Session } from '../api/client'
import { useDecisions, useProjects, useRequirements, useSessions } from '../api/queries'
import { t } from '../i18n'
import { cx } from '../lib/util'
import { displayState } from '../ui/marks'
import { AssignButton } from './assign'
import { sessionTitle } from './SessionCard'

// 这条需求现在谁在管、什么时候会动（R-66）。和后端的排队规则一一对应：
// 待开始的需求归关联会话里最近活跃、没结束 / 没被收起的那个；它空闲时整批送达；没有这样的会话就是“没人接手”。

export type OwnTone = 'draft' | 'locked' | 'queued' | 'sent' | 'working' | 'nobody' | 'off'
export type Own = { tone: OwnTone; short: string; text: string; session?: Session; canAssign?: boolean }

const live = (s?: Session) => !!s && s.state !== 'ended' && s.state !== 'lost' && !s.done_at

export function useOwnership() {
  const sessions = useSessions()
  const decisions = useDecisions()
  const reqs = useRequirements()
  const projects = useProjects()
  const byKey = new Map((sessions.data?.sessions ?? []).map((s) => [s.key, s]))
  const owner = (r: Requirement) =>
    r.sessions
      .map((k) => byKey.get(k))
      .filter(live)
      .sort((a, b) => b!.last_active_at.localeCompare(a!.last_active_at))[0]
  const autoOff = (pid?: string) => !!pid && projects.data?.projects.find((p) => p.id === pid)?.auto_dispatch === false

  return (r: Requirement): Own | null => {
    if (r.stage_status === 'locked' && r.stage_seq)
      return { tone: 'locked', short: t.own.lockedShort, text: r.stage_seq > 1 ? t.own.locked(r.plan_code ?? '', r.stage_seq - 1) : t.own.planNotStarted(r.plan_code ?? '') }
    if (r.status === 'draft') return { tone: 'draft', short: t.own.draftShort, text: t.own.draft }
    if (r.status === 'in_progress') {
      const s = owner(r)
      return s ? { tone: 'working', short: t.own.workingShort, text: t.own.working(sessionTitle(s)), session: s } : { tone: 'nobody', short: t.own.nobodyShort, text: t.own.workingGone, canAssign: true }
    }
    if (r.status !== 'ready') return null
    if (r.offered_to) {
      const s = byKey.get(r.offered_to)
      const name = s ? sessionTitle(s) : r.offered_to
      const sent = (decisions.data ?? []).some((d) => d.requirement_id === r.id && d.session_key === r.offered_to && (d.kind === 'queue' || d.kind === 'assign') && d.status === 'delivered')
      if (sent) return { tone: 'sent', short: t.own.sentShort, text: t.own.sent(name), session: s }
      if (s && !live(s)) return { tone: 'nobody', short: t.own.nobodyShort, text: t.own.offeredGone(name), canAssign: true }
      return { tone: 'queued', short: t.own.offeredShort, text: s && displayState(s) === 'parked' ? t.own.offeredNow(name) : t.own.offeredNext(name), session: s }
    }
    const s = owner(r)
    if (!s) return { tone: 'nobody', short: t.own.nobodyShort, text: r.sessions.length ? t.own.nobodyGone : t.own.nobody, canAssign: true }
    if (autoOff(r.project_id)) return { tone: 'off', short: t.own.offShort, text: t.own.off(sessionTitle(s)), session: s, canAssign: true }
    const n = (reqs.data ?? []).filter((x) => x.status === 'ready' && !x.offered_to && x.stage_status !== 'locked' && owner(x)?.key === s.key).length
    const running = s.state === 'running' || s.state === 'background'
    return { tone: 'queued', short: t.own.queuedShort, text: running ? t.own.queuedRunning(sessionTitle(s), n) : t.own.queuedSoon(sessionTitle(s), n), session: s }
  }
}

const toneCls: Record<OwnTone, string> = {
  draft: 'bg-idle-soft text-idle',
  locked: 'bg-sunken text-fg-3',
  queued: 'bg-accent-soft text-accent',
  sent: 'bg-accent-soft text-accent',
  working: 'bg-run-soft text-run',
  nobody: 'bg-wait-soft text-wait',
  off: 'bg-sunken text-fg-2',
}
const toneIcon: Record<OwnTone, typeof Clock3> = { draft: CircleDashed, locked: Lock, queued: Clock3, sent: Inbox, working: Loader, nobody: UserX, off: PauseCircle }

// OwnBadge：看板卡片上的短标签，替代原来笼统的“轮到 Agent”。
export function OwnBadge({ own, className }: { own: Own; className?: string }) {
  const Icon = toneIcon[own.tone]
  return (
    <span title={own.text} className={cx('inline-flex shrink-0 items-center gap-1 rounded-md px-1.5 py-0.5 text-xs font-medium', toneCls[own.tone], className)}>
      <Icon className="size-3" />
      {own.short}
    </span>
  )
}

// OwnLine：需求详情顶部常驻的一行，说清现在谁在管、什么时候会动；没人接手时直接给“交给…”。
export function OwnLine({ r }: { r: Requirement }) {
  const own = useOwnership()(r)
  if (!own) return null
  const Icon = toneIcon[own.tone]
  return (
    <div className={cx('mt-2.5 flex flex-wrap items-center gap-x-2 gap-y-1.5 rounded-lg px-2.5 py-1.5 text-[13px]', toneCls[own.tone])}>
      <Icon className="size-3.5 shrink-0" />
      <span className="min-w-0 flex-1">{own.text}</span>
      {own.canAssign && <AssignButton code={r.code} projectId={r.project_id} />}
    </div>
  )
}

