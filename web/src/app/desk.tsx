import { useEffect, useMemo, useState } from 'react'
import { AnimatePresence, motion } from 'motion/react'
import { ChevronDown, WifiOff } from 'lucide-react'
import type { MorningReport, Plan, Session } from '../api/client'
import { useConnected, useDecisions, useMorning, usePlans, useProjects, useRequirements } from '../api/queries'
import { buildAsks, useStaying, type Ask } from './asks'
import { t } from '../i18n'
import type { Scene } from '../i18n/zh'
import { cx, loadPref, relTime, savePref } from '../lib/util'
import { Skeleton, Tip } from '../ui/base'
import { Item, List, useSettled } from '../ui/motion'
import { spring } from '../ui/gesture'
import { displayState, presence, RoleTag } from '../ui/marks'
import { Linkify, PlanGlyph, RefLine, shortTitle } from '../ui/refmark'
import type { ProjectGroup, useWorkbench } from './inbox'
import { useOpenPlan } from './nav'
import { needsApproval, passedWith, planState, StageRail } from './plans'
import { useRecent } from './recent'
import { MorningReports } from './morning'
import { TokBrief, Wind } from './lanes'
import type { Zoom } from '../lib/lanedata'
import { Team } from '../pages/Projects'
import { usePendingMap } from './verdict'

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

// asking：会话在等你授权，或在正式问你（提问 / 计划确认）。
export const asking = (s: Session) => {
  const ds = displayState(s)
  return ds === 'waiting_approval' || ds === 'waiting_answer'
}

// useTodo：计入件数的「要你处理」条目（R-177，app/asks.tsx）：一件事算一件——待验收需求连同挂在它上面的决策算一件，
// 会话等授权、在问你各算一件；只是一轮做完在等你回复、没有正式问题的会话不算。
export function useTodo(wb: WB): Ask[] {
  const all = useMemo(() => buildAsks(wb), [wb])
  return useStaying(all).filter((a) => a.counted)
}

// ---------- 进行中的计划：右栏 + 手机胶囊 ----------

function usePlansInFlight() {
  const plans = usePlans()
  // 进行中与等你批准的都留着，批准后不会从工作台消失（R-147）
  return (plans.data ?? []).filter((x) => x.status === 'active' || needsApproval(x))
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

// fill：在栏里撑满剩余高度，加载骨架里也照这样画（R-211，ui/shape.tsx 的 data-fill）
function Panel({ title, children, className, fill }: { title: string; children: React.ReactNode; className?: string; fill?: boolean }) {
  return (
    <section data-fill={fill ? '' : undefined} className={cx('rounded-[14px] bg-surface px-3.5 py-3 shadow-card', className)}>
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
// 加载中按上次的个数先画胶囊骨架（R-211），这一排后出来会把下面三栏往下推。
export function PlanChips({ className }: { className?: string }) {
  const list = usePlansInFlight()
  const pending = usePlans().isPending
  const open = useOpenPlan()
  const [last] = useState(() => loadPref('ks.skel.chips', 0))
  useEffect(() => {
    if (!pending) savePref('ks.skel.chips', list.length)
  }, [pending, list.length])
  if (pending)
    return last ? (
      <div aria-hidden className={cx('flex gap-2 overflow-hidden pb-0.5', className)}>
        {Array.from({ length: Math.min(last, 4) }).map((_, i) => (
          <Skeleton key={i} className="h-[26px] w-52 shrink-0 rounded-full" />
        ))}
      </div>
    ) : null
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
// fill：按列表区的可用高度算条数（R-187），把卡片填满；窗口 / 栏高变化时跟着重算。
export function RecentRail({ now, className, limit = 14, fill }: { now: number; className?: string; limit?: number; fill?: boolean }) {
  const [box, setBox] = useState<HTMLUListElement | null>(null)
  const [fit, setFit] = useState(0)
  useEffect(() => {
    if (!box || !fill) return
    const calc = () => {
      const row = box.querySelector('li')?.getBoundingClientRect().height || 29
      setFit(Math.max(4, Math.floor((box.clientHeight - 2) / row)))
    }
    const ro = new ResizeObserver(calc)
    ro.observe(box)
    calc()
    return () => ro.disconnect()
  }, [box, fill])
  const recent = useRecent(undefined, fill && fit ? fit : limit)
  // 首次加载（数据陆续到齐、按栏高算出条数）这段时间里的出现不算进场（R-216）
  const still = !useSettled(recent.length > 0 && (!fill || fit > 0))
  return (
    <Panel title={t.desk.recentTitle} fill={fill} className={cx('flex min-h-0 flex-col', className)}>
      {recent.length ? (
        <ul ref={setBox} className="desk-scroll relative -mr-2 min-h-0 flex-1 overflow-y-auto pr-2">
          <span aria-hidden className="absolute top-2.5 bottom-2.5 left-[4px] w-px bg-line-strong" />
          <AnimatePresence initial={false}>
            {recent.map((e) => (
              <motion.li
                key={`${e.at}|${e.text}`}
                layout="position"
                initial={still ? false : { height: 0, opacity: 0, overflow: 'hidden' }}
                animate={{ height: 'auto', opacity: 1, transitionEnd: { overflow: 'visible' } }}
                exit={still ? undefined : { height: 0, opacity: 0, overflow: 'hidden' }}
                transition={spring()}
              >
                <div className="relative grid grid-cols-[9px_minmax(0,1fr)_auto] items-baseline gap-2.5 py-[5px] text-[12.5px] text-fg-2">
                  <span className={cx('size-[9px] translate-y-px rounded-full bg-surface ring-2 ring-inset', ringOf[e.dot] ?? 'ring-line-strong')} />
                  <RefLine text={e.text} label={e.stage ? 'code' : 'title'} />
                  <span className="text-[11px] text-fg-3 tabular">{relTime(e.at, now)}</span>
                </div>
              </motion.li>
            ))}
          </AnimatePresence>
        </ul>
      ) : (
        <p className="text-xs text-fg-3">{t.projects.noRecent}</p>
      )}
    </Panel>
  )
}

const groupSessions = (g: ProjectGroup) => [...g.active, ...g.threads.map((th) => th.s).filter((x): x is Session => !!x)]

export function TeamRail({ groups, className }: { groups: ProjectGroup[]; className?: string }) {
  const rows = groups.filter((g) => groupSessions(g).some((s) => s.role === 'pm' || s.role === 'dev' || s.role === 'aide'))
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

// 离开结束（授权窗口到期 / 撤销；没有窗口时以总述写下的时间算）超过这么久，小结自动收起，PM 简报接回简报位（R-170 返工）。
const awayFold = 2 * 3600e3
function awayEnd(r: MorningReport): string | undefined {
  const g = r.grant
  if (g) return g.status === 'active' ? undefined : g.revoked_at && g.revoked_at < g.expires_at ? g.revoked_at : g.expires_at
  return r.note?.at
}

// BriefStrip：顶部唯一的简报位。有离开期间小结时放小结（顾问），否则放 PM 简报；背后是这一档所有会话新算 token 的波形，
// 右边写这一档的合计（R-206）。
// 最多两行换行显示，点开看全文（小结卡、各项目的 PM 简报）；离开结束两小时后小结收起成一行，PM 简报接回简报位。断线时整体静止变灰并写明。
export function BriefStrip({ sessions, now, zoom }: { sessions: Session[]; now: number; zoom: Zoom }) {
  const morning = useMorning()
  const projects = useProjects()
  const connected = useConnected()
  const [open, setOpen] = useState(false)
  const reports = morning.data?.reports ?? []
  const fresh = reports.filter((r) => {
    const end = awayEnd(r)
    return !end || now - Date.parse(end) < awayFold
  })
  const stale = reports.filter((r) => !fresh.includes(r))
  const skip = new Set(fresh.map((r) => r.project_id))
  const briefs = (projects.data?.projects ?? []).filter((p) => p.brief && p.brief_at && now - Date.parse(p.brief_at) < briefTTL && !p.hidden && !skip.has(p.id))
  const find = (k?: string) => (k ? sessions.find((x) => x.key === k) : undefined)
  const r = fresh[0]
  const b = briefs[0]
  const old = stale[0]
  const head = r
    ? { role: 'aide', who: find(r.note?.by ?? r.grant?.session_key), any: true, label: t.morning.title, text: r.note?.text ?? t.morning.noNote, at: r.note?.at ?? r.since, folded: false }
    : b
      ? { role: 'pm', who: find(b.brief_by), any: false, label: briefs.length > 1 ? b.name : '', text: b.brief ?? '', at: b.brief_at, folded: false }
      : old
        ? { role: 'aide', who: find(old.note?.by ?? old.grant?.session_key), any: true, label: '', text: t.desk.awayEnded(relTime(awayEnd(old), now)), at: awayEnd(old), folded: true }
        : null
  const more = fresh.length + briefs.length + (head?.folded ? 0 : stale.length) - (head && !head.folded ? 1 : 0)
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
        // 固定两行高（R-211）：简报有无、一行两行，顶栏和下面三栏都不跳
        className={cx('relative flex h-[54px] w-full min-w-0 items-center gap-2.5 overflow-hidden rounded-xl bg-surface px-3.5 py-2 text-left shadow-card', head && 'cursor-pointer hover:bg-hover/50')}
      >
        <Wind />
        {!connected && (
          <span className="relative inline-flex shrink-0 items-center gap-1.5 self-center rounded-full bg-danger-soft px-2 py-0.5 text-[11.5px] font-medium text-danger">
            <WifiOff className="size-3.5" />
            {t.desk.offline}
          </span>
        )}
        {head ? (
          <>
            <RoleTag role={head.role} presence={head.who ? presence(head.who, head.any) || undefined : undefined} className="relative shrink-0 bg-surface" />
            <span className={cx('relative min-w-0 flex-1 text-[13px] leading-[1.45]', head.folded ? 'truncate text-fg-3' : 'line-clamp-2 text-fg-2')}>
              {head.label && <b className="mr-1.5 font-semibold text-fg">{head.label}</b>}
              <Linkify text={head.text} />
            </span>
            <span className="relative flex shrink-0 items-center gap-2 text-[11.5px] text-fg-3">
              <TokBrief zoom={zoom} className="max-xl:hidden" />
              {more > 0 && <span>{t.desk.briefMore(more)}</span>}
              {!head.folded && <span className="tabular max-sm:hidden">{relTime(head.at, now)}</span>}
              <span className="inline-flex items-center gap-0.5 text-fg-2">
                <span className="max-md:hidden">{t.desk.briefOpen}</span>
                <ChevronDown className={cx('size-4 transition-transform', open && 'rotate-180')} />
              </span>
            </span>
          </>
        ) : (
          <>
            {connected && !morning.isPending && !projects.isPending && <span className="relative min-w-0 flex-1 truncate text-[12.5px] text-fg-3">{t.desk.noBrief}</span>}
            <TokBrief zoom={zoom} className="relative ml-auto shrink-0 text-[11.5px] text-fg-3 max-lg:hidden" />
          </>
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

type Handled = { at: string; act: 'pass' | 'back' | 'answer'; scene: Scene; text: string }
const handledTTL = 24 * 3600e3

// useHandled：你最近做过的判断（通过 / 退回、批准 / 拒绝、回答，按场景取词），左栏底部的「刚处理」。代答的不算。
export function useHandled(now: number, limit = 4): Handled[] {
  const reqs = useRequirements()
  const decisions = useDecisions()
  const plans = usePlans()
  const pend = usePendingMap()
  const local = [...pend.values()].filter((e) => e.act !== 'move' && e.phase !== 'wait')
  const localSig = local.map((e) => e.key + e.at).join('|')
  return useMemo(() => {
    const out: Handled[] = []
    for (const r of reqs.data ?? []) {
      for (const f of r.feedback ?? []) if (f.kind === 'feedback' && f.answered_at) out.push({ at: f.answered_at, act: 'back', scene: 'accept', text: r.title })
      if (r.status === 'done') out.push({ at: r.updated_at, act: 'pass', scene: 'accept', text: r.title })
    }
    for (const d of decisions.data ?? []) {
      if (d.kind !== 'decision' || !d.answered_at || d.timed_out || d.voided || (d.proxy_by && !d.overridden_at)) continue // 超时视为拒绝（R-169）、申请作废（R-188）都不是你做的判断
      const opt0 = d.options?.[0]
      const act = d.approval ? (opt0 && d.answer?.startsWith(opt0) ? 'pass' : 'back') : 'answer'
      out.push({ at: d.answered_at, act, scene: d.request_kind ? 'request' : d.approval ? 'approval' : 'answer', text: d.short_title || d.question })
    }
    // 阶段验收：你通过一个阶段时，后面需求早已逐条验收完的阶段会一并自动通过（D-60），计划随之完成——这是你的一次判断，
    // 只记一条，写你验收的那个阶段（R-211：P-2 曾因此出现两条一样的「通过 会话身份与工人池」）
    for (const x of plans.data ?? []) for (const st of x.stages) if (st.passed_at && !passedWith(x, st)) out.push({ at: st.passed_at, act: 'pass', scene: 'accept', text: `${x.title} · ${t.ref.stage(st.seq)}` })
    // 撤销期刚结束、服务端还没刷新到的判断：本地先记一条，「刚处理」和离开「要你处理」同一刻到位（R-230）；
    // 服务端的记录到了就由它接手（同一句话同一个键，不闪）。提交失败时调度器放掉这件事，这一条随之消失。
    for (const e of local) {
      if (e.act === 'move' || e.phase === 'wait') continue
      const text = (() => {
        if (e.key.startsWith('dec:')) {
          const d = decisions.data?.find((x) => x.code === e.key.slice(4))
          return d && (d.short_title || d.question)
        }
        if (e.key.startsWith('plan:')) return plans.data?.find((x) => x.code === e.key.slice(5))?.title
        if (e.key.startsWith('stage:')) {
          const [, code, seq] = e.key.split(':')
          const x = plans.data?.find((p) => p.code === code)
          return x && `${x.title} · ${t.ref.stage(Number(seq))}`
        }
        return reqs.data?.find((x) => x.code === e.key)?.title
      })()
      out.push({ at: new Date(e.until).toISOString(), act: e.act, scene: e.scene, text: text || e.subject })
    }
    const seen = new Set<string>()
    return out
      .filter((x) => now - Date.parse(x.at) < handledTTL)
      .sort((a, b) => b.at.localeCompare(a.at))
      .filter((x) => {
        const k = `${x.act}|${x.text}`
        if (seen.has(k)) return false
        seen.add(k)
        return true
      })
      .slice(0, limit)
    // now 只用来过滤 24 小时，按分钟变化即可
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [reqs.data, decisions.data, plans.data, limit, Math.floor(now / 60000), localSig])
}

const actTone: Record<string, string> = { pass: 'text-run', answer: 'text-accent', back: 'text-seal' }

export function HandledLog({ now, className }: { now: number; className?: string }) {
  const list = useHandled(now)
  const still = !useSettled(list.length > 0)
  if (!list.length) return null
  return (
    <div className={cx('grid gap-1.5 border-t border-dashed border-line-strong pt-2.5', className)}>
      <h5 className="text-[11.5px] font-semibold tracking-wider text-fg-3">{t.desk.handled}</h5>
      <List space={6}>
        {list.map((x) => (
          <Item key={`${x.act}|${x.text}`} space={6} layout fluid still={still}>
            <div className="flex min-w-0 items-baseline gap-2 text-[12.5px] text-fg-2">
              <span className={cx('shrink-0 text-[11px]', actTone[x.act])}>{x.act === 'answer' ? t.verdict.pass('answer') : x.act === 'pass' ? t.verdict.pass(x.scene) : t.verdict.back(x.scene)}</span>
              <span className="min-w-0 flex-1 truncate">{x.text}</span>
              <span className="shrink-0 text-[11px] text-fg-3 tabular">{relTime(x.at, now)}</span>
            </div>
          </Item>
        ))}
      </List>
    </div>
  )
}
