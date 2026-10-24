import { useEffect, useMemo, useState } from 'react'
import { ChevronDown, WifiOff } from 'lucide-react'
import type { Decision, Plan, Requirement, Session } from '../api/client'
import { useConnected, useDecisions, useMorning, usePlans, useProjects, useRequirements } from '../api/queries'
import { t } from '../i18n'
import { cx, relTime } from '../lib/util'
import { Tip } from '../ui/base'
import { displayState, presence, RoleTag } from '../ui/marks'
import { Linkify, PlanGlyph, shortTitle } from '../ui/refmark'
import type { ProjectGroup, useWorkbench } from './inbox'
import { useOpenPlan } from './nav'
import { planState, StageRail } from './plans'
import { useRecent } from './recent'
import { MorningReports } from './morning'
import { Wind } from './lanes'
import { Team } from '../pages/Projects'

// 工作台的周边部件（R-113 → R-147 → R-170）：顶部唯一的简报位（离开期间小结 / PM 简报，背后一条随活跃度起伏的风）、
// 窄窗的计划胶囊、右栏「风向」（进行中的计划、最近动态串成一条竖线、队伍固定在底部）、左栏底部「刚处理」。
// 会话泳道见 app/lanes.tsx，版面见 pages/Workbench.tsx。

type WB = ReturnType<typeof useWorkbench>

const briefTTL = 12 * 3600e3 // 超过 12 小时没更新的简报不再显示

// ---------- PM 简报：一行 ----------

// skip：已由离开期间小结占了简报位的项目（R-165），它们的 PM 简报收进小结卡里，这里不再单独显示。
export function Briefs({ sessions, now, skip }: { sessions: Session[]; now: number; skip?: Set<string> }) {
  const projects = useProjects()
  const list = (projects.data?.projects ?? []).filter((p) => p.brief && p.brief_at && now - Date.parse(p.brief_at) < briefTTL && !p.hidden && !skip?.has(p.id))
  if (!list.length) return null
  return (
    <div className="mb-4 space-y-2">
      {list.map((p) => {
        const pm = sessions.find((x) => x.key === p.brief_by)
        return (
          <section key={p.id} className="flex items-start gap-3 rounded-xl border border-line bg-surface px-4 py-2.5 shadow-xs">
            <RoleTag role="pm" presence={pm ? presence(pm) || undefined : undefined} className="mt-0.5" />
            <p className="min-w-0 flex-1 text-sm leading-relaxed text-fg max-sm:line-clamp-3">
              {list.length > 1 && <span className="mr-1.5 text-fg-3">{p.name}</span>}
              <Linkify text={p.brief} label="title" />
            </p>
            <span className="mt-0.5 shrink-0 text-xs text-fg-3 tabular max-sm:hidden">{relTime(p.brief_at, now)}</span>
          </section>
        )
      })}
    </div>
  )
}

// ---------- 要你处理的事有几件（标题、侧栏角标同一口径） ----------

type Todo =
  | { kind: 'req'; key: string; code: string; review?: Requirement; decisions: Decision[]; at: string }
  | { kind: 'dec'; key: string; d: Decision; at: string }
  | { kind: 'plan'; key: string; x: Plan; at: string }
  | { kind: 'orphan'; key: string; d: Decision; at: string }
  | { kind: 'session'; key: string; s: Session; at: string }

// asking：会话在等你授权，或在正式问你（提问 / 计划确认）。
export const asking = (s: Session) => {
  const ds = displayState(s)
  return ds === 'waiting_approval' || ds === 'waiting_answer'
}

// useTodo：一件事算一件：同一条需求的验收和决策合成一件；会话等授权、在问你各算一件。
// 只是一轮做完在等你回复、没有正式问题的会话不算（它在「待命 / 久未回复」档）。
export function useTodo(wb: WB): Todo[] {
  const reqs = useRequirements()
  return useMemo(() => {
    const out: Todo[] = []
    const byReq = new Map<string, Extract<Todo, { kind: 'req' }>>()
    const rowOf = (code: string, at: string) => {
      let row = byReq.get(code)
      if (!row) {
        row = { kind: 'req', key: `r:${code}`, code, decisions: [], at }
        byReq.set(code, row)
        out.push(row)
      }
      if (at < row.at) row.at = at
      return row
    }
    for (const th of wb.threads) {
      for (const r of th.reviews) rowOf(r.code, r.updated_at).review = r
      for (const d of th.decisions) {
        if (d.requirement_code) rowOf(d.requirement_code, d.created_at).decisions.push(d)
        else out.push({ kind: 'dec', key: `d:${d.code}`, d, at: d.created_at })
      }
      for (const d of th.orphans) out.push({ kind: 'orphan', key: `o:${d.code}`, d, at: d.answered_at ?? d.created_at })
      const s = th.s
      if (s && s.state !== 'ended' && asking(s)) out.push({ kind: 'session', key: `s:${s.key}`, s, at: s.state_since || s.last_active_at })
    }
    for (const x of wb.plans) out.push({ kind: 'plan', key: `p:${x.code}`, x, at: x.updated_at })
    return out
    // reqs.data：需求标题 / 状态更新时重算
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [wb.threads, wb.plans, reqs.data])
}

// ---------- 进行中的计划：右栏 + 手机胶囊 ----------

function usePlansInFlight() {
  const plans = usePlans()
  // 进行中与等你批准的都留着，批准后不会从工作台消失（R-147）
  return (plans.data ?? []).filter((x) => x.status === 'active' || (x.status === 'draft' && x.created_by === 'agent'))
}

const finished = (r: { status: string }) => r.status === 'done' || r.status === 'shelved'

// stageLine：当前阶段名 + 进度，如「阶段 2 / 3 · 印记与统一用词 · 3/5」；未开工的计划只写状态。
function stageLine(x: Plan, label: string) {
  const cur = x.stages.find((s) => s.seq === x.current)
  if (x.status !== 'active' || !cur) return label
  const n = cur.requirements.length
  return [t.desk.planAt(x.current, x.stages.length), cur.title, n ? `${cur.requirements.filter(finished).length}/${n}` : '', label].filter(Boolean).join(t.common.sep)
}

function StageDetail({ x }: { x: Plan }) {
  return (
    <div className="space-y-0.5">
      {x.stages.map((s) => (
        <div key={s.id} className="flex justify-between gap-3">
          <span className="truncate">{t.ref.stage(s.seq) + t.common.sep + s.title}</span>
          <span className="shrink-0 tabular opacity-80">
            {s.requirements.filter(finished).length}/{s.requirements.length}
          </span>
        </div>
      ))}
    </div>
  )
}

const toneText: Record<string, string> = { review: 'text-reply', approve: 'text-wait', paused: 'text-danger', active: 'text-accent' }

function Panel({ title, children, className }: { title: string; children: React.ReactNode; className?: string }) {
  return (
    <section className={cx('rounded-[14px] bg-surface px-3.5 py-3 shadow-card', className)}>
      <h3 className="mb-2.5 font-display text-[13.5px] font-bold tracking-wide text-fg">{title}</h3>
      {children}
    </section>
  )
}

export function PlansRail({ className }: { className?: string }) {
  const list = usePlansInFlight()
  const open = useOpenPlan()
  if (!list.length) return null
  return (
    <Panel title={t.desk.plansTitle} className={className}>
      <div className="space-y-3">
        {list.map((x) => {
          const st = planState(x)
          return (
            // 计划行只写阶段名 + 进度，不逐个列需求编号；悬停看各阶段明细（D-171）
            <Tip key={x.id} content={<StageDetail x={x} />} side="left">
              <button onClick={() => open(x.code)} className="group block w-full text-left">
                <div className="flex min-w-0 items-center gap-2">
                  <PlanGlyph status={x.status} />
                  <span className="min-w-0 flex-1 truncate text-[13px] font-medium text-fg group-hover:text-accent">{x.title}</span>
                </div>
                <div className={cx('mt-1 truncate text-xs', toneText[st.tone] ?? 'text-fg-3')}>{stageLine(x, st.label)}</div>
                <StageRail x={x} className="mt-1.5" />
              </button>
            </Tip>
          )
        })}
      </div>
    </Panel>
  )
}

// PlanChips：右栏放不下时（手机、窄窗），计划排成一排可横滑的胶囊，点开计划。
export function PlanChips({ className }: { className?: string }) {
  const list = usePlansInFlight()
  const open = useOpenPlan()
  if (!list.length) return null
  return (
    <div className={cx('flex gap-2 overflow-x-auto pb-0.5 [scrollbar-width:none]', className)}>
      {list.map((x) => {
        const st = planState(x)
        return (
          <button key={x.id} onClick={() => open(x.code)} className="inline-flex shrink-0 items-center gap-1.5 rounded-full border border-line bg-surface px-3 py-1 text-xs shadow-xs">
            <PlanGlyph status={x.status} />
            <span className="whitespace-nowrap text-fg">{shortTitle(x.title)}</span>
            <span className={cx('whitespace-nowrap', toneText[st.tone] ?? 'text-fg-3')}>{st.label}</span>
          </button>
        )
      })}
    </div>
  )
}

// 圆点的颜色（最近动态给的是底色类名）→ 竖线上空心节点的描边色
const ringOf: Record<string, string> = { 'bg-run': 'ring-run', 'bg-accent': 'ring-accent', 'bg-reply': 'ring-reply', 'bg-wait': 'ring-wait' }

// RecentRail：最近动态串成一条竖线，内容多时在卡片内部滚动。
export function RecentRail({ now, className, limit = 14 }: { now: number; className?: string; limit?: number }) {
  const recent = useRecent(undefined, limit)
  return (
    <Panel title={t.desk.recentTitle} className={cx('flex min-h-0 flex-col', className)}>
      {recent.length ? (
        <ul className="desk-scroll relative -mr-2 min-h-0 flex-1 overflow-y-auto pr-2">
          <span aria-hidden className="absolute top-2.5 bottom-2.5 left-[4px] w-px bg-line-strong" />
          {recent.map((e, i) => (
            <li key={i} className="relative grid grid-cols-[9px_minmax(0,1fr)_auto] items-baseline gap-2.5 py-[5px] text-[12.5px] text-fg-2">
              <span className={cx('size-[9px] translate-y-px rounded-full bg-surface ring-2 ring-inset', ringOf[e.dot] ?? 'ring-line-strong')} />
              <span className="truncate">
                <Linkify text={e.text} />
              </span>
              <span className="text-[11px] text-fg-3 tabular">{relTime(e.at, now)}</span>
            </li>
          ))}
        </ul>
      ) : (
        <p className="text-xs text-fg-3">{t.projects.noRecent}</p>
      )}
    </Panel>
  )
}

const groupSessions = (g: ProjectGroup) => [...g.active, ...g.threads.map((th) => th.s).filter((x): x is Session => !!x)]

export function TeamRail({ groups, className }: { groups: ProjectGroup[]; className?: string }) {
  const rows = groups.filter((g) => groupSessions(g).some((s) => s.role === 'pm' || s.role === 'dev'))
  if (!rows.length) return null
  return (
    <Panel title={t.desk.teamTitle} className={className}>
      <div className="space-y-2">
        {rows.map((g) => (
          <div key={g.key} className="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-fg-3">
            <span className="text-fg-2">{g.name}</span>
            <Team sessions={groupSessions(g)} />
          </div>
        ))}
      </div>
    </Panel>
  )
}

// ---------- 简报位（R-165 / R-170） ----------

// BriefStrip：顶部唯一的简报位。有离开期间小结时放小结（顾问），否则放 PM 简报；背后一条随整体活跃度起伏的风。
// 一行放不下的内容点开看全文（小结卡、各项目的 PM 简报）；断线时整体静止变灰并写明。
export function BriefStrip({ sessions, now }: { sessions: Session[]; now: number }) {
  const morning = useMorning()
  const projects = useProjects()
  const connected = useConnected()
  const [open, setOpen] = useState(false)
  const reports = morning.data?.reports ?? []
  const skip = new Set(reports.map((r) => r.project_id))
  const briefs = (projects.data?.projects ?? []).filter((p) => p.brief && p.brief_at && now - Date.parse(p.brief_at) < briefTTL && !p.hidden && !skip.has(p.id))
  const find = (k?: string) => (k ? sessions.find((x) => x.key === k) : undefined)
  const r = reports[0]
  const b = briefs[0]
  const head = r
    ? { role: 'advisor', who: find(r.note?.by ?? r.grant?.session_key), any: true, label: t.morning.title, text: r.note?.text ?? t.morning.noNote, at: r.note?.at ?? r.since }
    : b
      ? { role: 'pm', who: find(b.brief_by), any: false, label: briefs.length > 1 ? b.name : '', text: b.brief ?? '', at: b.brief_at }
      : null
  const more = reports.length + briefs.length - 1
  useEffect(() => {
    if (!open) return
    const onKey = (e: KeyboardEvent) => e.key === 'Escape' && setOpen(false)
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [open])
  return (
    <div className="relative min-w-0">
      <button
        onClick={() => head && setOpen(!open)}
        aria-expanded={head ? open : undefined}
        className={cx('relative flex h-11 w-full min-w-0 items-center gap-2.5 overflow-hidden rounded-xl bg-surface px-3.5 text-left shadow-card', head && 'cursor-pointer hover:bg-hover/50')}
      >
        <Wind />
        {!connected && (
          <span className="relative inline-flex shrink-0 items-center gap-1.5 rounded-full bg-danger-soft px-2 py-0.5 text-[11.5px] font-medium text-danger">
            <WifiOff className="size-3.5" />
            {t.desk.offline}
          </span>
        )}
        {head ? (
          <>
            <RoleTag role={head.role} presence={head.who ? presence(head.who, head.any) || undefined : undefined} className="relative bg-surface" />
            {head.label && <span className="relative shrink-0 text-[12px] font-semibold text-fg-2">{head.label}</span>}
            <span className="relative min-w-0 flex-1 truncate text-[12.5px] text-fg-2">{head.text}</span>
            {more > 0 && <span className="relative shrink-0 text-[11.5px] text-fg-3">{t.desk.briefMore(more)}</span>}
            <span className="relative shrink-0 text-[11px] text-fg-3 tabular max-sm:hidden">{relTime(head.at, now)}</span>
            <ChevronDown className={cx('relative size-4 shrink-0 text-fg-3 transition-transform', open && 'rotate-180')} />
          </>
        ) : (
          connected && <span className="relative min-w-0 truncate text-[12.5px] text-fg-3">{t.desk.noBrief}</span>
        )}
      </button>
      {open && head && (
        <>
          <div className="fixed inset-0 z-30" onClick={() => setOpen(false)} />
          <div className="desk-scroll absolute inset-x-0 top-full z-40 mt-2 max-h-[70vh] overflow-y-auto rounded-2xl border border-line bg-bg p-3 shadow-pop [&>div:last-child]:mb-0">
            <MorningReports sessions={sessions} now={now} />
            <Briefs sessions={sessions} now={now} skip={skip} />
          </div>
        </>
      )}
    </div>
  )
}

// ---------- 刚处理 ----------

type Handled = { at: string; act: 'pass' | 'back' | 'answer'; text: string }
const handledTTL = 24 * 3600e3

// useHandled：你最近做过的判断（批准 / 退回 / 回答），左栏底部的「刚处理」。代答的不算。
export function useHandled(now: number, limit = 4): Handled[] {
  const reqs = useRequirements()
  const decisions = useDecisions()
  const plans = usePlans()
  return useMemo(() => {
    const out: Handled[] = []
    for (const r of reqs.data ?? []) {
      for (const f of r.feedback ?? []) if (f.kind === 'feedback' && f.answered_at) out.push({ at: f.answered_at, act: 'back', text: r.title })
      if (r.status === 'done') out.push({ at: r.updated_at, act: 'pass', text: r.title })
    }
    for (const d of decisions.data ?? []) {
      if (d.kind !== 'decision' || !d.answered_at || (d.proxy_by && !d.overridden_at)) continue
      const opt0 = d.options?.[0]
      const act = d.approval ? (opt0 && d.answer?.startsWith(opt0) ? 'pass' : 'back') : 'answer'
      out.push({ at: d.answered_at, act, text: d.short_title || d.question })
    }
    for (const x of plans.data ?? []) for (const st of x.stages) if (st.passed_at) out.push({ at: st.passed_at, act: 'pass', text: `${x.title} · ${t.ref.stage(st.seq)}` })
    return out
      .filter((x) => now - Date.parse(x.at) < handledTTL)
      .sort((a, b) => b.at.localeCompare(a.at))
      .slice(0, limit)
    // now 只用来过滤 24 小时，按分钟变化即可
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [reqs.data, decisions.data, plans.data, limit, Math.floor(now / 60000)])
}

const actTone: Record<string, string> = { pass: 'text-run', answer: 'text-accent', back: 'text-seal' }

export function HandledLog({ now, className }: { now: number; className?: string }) {
  const list = useHandled(now)
  if (!list.length) return null
  return (
    <div className={cx('grid gap-1.5 border-t border-dashed border-line-strong pt-2.5', className)}>
      <h5 className="text-[11.5px] font-semibold tracking-wider text-fg-3">{t.desk.handled}</h5>
      {list.map((x, i) => (
        <div key={i} className="flex min-w-0 items-baseline gap-2 text-[12.5px] text-fg-2">
          <span className={cx('shrink-0 text-[11px]', actTone[x.act])}>{x.act === 'pass' ? t.verdict.pass : x.act === 'back' ? t.verdict.back : t.verdict.answer}</span>
          <span className="min-w-0 flex-1 truncate">{x.text}</span>
          <span className="shrink-0 text-[11px] text-fg-3 tabular">{relTime(x.at, now)}</span>
        </div>
      ))}
    </div>
  )
}
