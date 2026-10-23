import { useMemo } from 'react'
import type { Decision, Plan, Requirement, Session } from '../api/client'
import { usePlans, useProjects, useRequirements } from '../api/queries'
import { t } from '../i18n'
import { cx, relTime } from '../lib/util'
import { Tip } from '../ui/base'
import { displayState, presence, RoleTag } from '../ui/marks'
import { Linkify, PlanGlyph, shortTitle } from '../ui/refmark'
import type { ProjectGroup, useWorkbench } from './inbox'
import { useOpenPlan } from './nav'
import { planState, StageRail } from './plans'
import { useRecent } from './recent'
import { Team } from '../pages/Projects'

// 工作台的周边部件（R-113 → R-147）：顶部一行 PM 简报、手机 / 窄窗的计划胶囊、宽屏右栏（进行中的计划、最近动态、队伍在线）。
// 会话本身按状态分三档，见 pages/Workbench.tsx。

type WB = ReturnType<typeof useWorkbench>

const briefTTL = 12 * 3600e3 // 超过 12 小时没更新的简报不再显示

// ---------- PM 简报：一行 ----------

export function Briefs({ sessions, now }: { sessions: Session[]; now: number }) {
  const projects = useProjects()
  const list = (projects.data?.projects ?? []).filter((p) => p.brief && p.brief_at && now - Date.parse(p.brief_at) < briefTTL && !p.hidden)
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

function Panel({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <section className="rounded-xl border border-line bg-surface p-4 shadow-xs">
      <h3 className="mb-3 text-sm font-semibold text-fg">{title}</h3>
      {children}
    </section>
  )
}

export function PlansRail() {
  const list = usePlansInFlight()
  const open = useOpenPlan()
  if (!list.length) return null
  return (
    <Panel title={t.desk.plansTitle}>
      <div className="space-y-3.5">
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
    <div className={cx('-mx-4 mb-4 flex gap-2 overflow-x-auto px-4 pb-1 [scrollbar-width:none] sm:-mx-6 sm:px-6', className)}>
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

export function RecentRail({ now }: { now: number }) {
  const recent = useRecent(undefined, 6)
  if (!recent.length) return null
  return (
    <Panel title={t.desk.recentTitle}>
      <ul className="space-y-2">
        {recent.map((e, i) => (
          <li key={i} className="flex items-start gap-2 text-[13px] text-fg-2">
            <span className={cx('mt-[7px] inline-block size-[7px] shrink-0 rotate-45 rounded-[1.5px]', e.dot)} />
            <span className="line-clamp-2 min-w-0 flex-1">
              <Linkify text={e.text} />
            </span>
            <span className="shrink-0 text-xs text-fg-3 tabular">{relTime(e.at, now)}</span>
          </li>
        ))}
      </ul>
    </Panel>
  )
}

const groupSessions = (g: ProjectGroup) => [...g.active, ...g.threads.map((th) => th.s).filter((x): x is Session => !!x)]

export function TeamRail({ groups }: { groups: ProjectGroup[] }) {
  const rows = groups.filter((g) => groupSessions(g).some((s) => s.role === 'pm' || s.role === 'dev'))
  if (!rows.length) return null
  return (
    <Panel title={t.desk.teamTitle}>
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
