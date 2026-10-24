import { useEffect, useMemo, useRef, useState } from 'react'
import { Link } from 'react-router'
import { AnimatePresence, LayoutGroup, motion } from 'motion/react'
import { CheckCheck, ChevronRight, CornerDownLeft, History, MonitorSmartphone, Search, SearchX } from 'lucide-react'
import type { Plan, Session } from '../api/client'
import { useSettings } from '../api/queries'
import { t } from '../i18n'
import { cx, useNow, usePref } from '../lib/util'
import { Button, Card, CardSkeleton, Empty, ErrorState, Input, Kbd, PageHeader, Segmented, Tip } from '../ui/base'
import { displayState } from '../ui/marks'
import { useWorkbench, type ProjectGroup, type Thread } from '../app/inbox'
import { composeAnswer, useOutbox } from '../app/outbox'
import { undoToast, UNDO_MS } from '../app/verdict'
import { RecapPanel } from '../app/Recap'
import { SessionCard, sessionTitle } from '../app/SessionCard'
import { SessionRow, ThreadCard } from '../app/thread'
import { PlanCard } from '../app/plans'
import { asking, Briefs, PlanChips, PlansRail, RecentRail, TeamRail, useTodo } from '../app/desk'
import { ProjGlyph } from '../ui/refmark'
import { awaitingReply } from '../app/replyBox'
import { MorningReports, useMorningProjects } from '../app/morning'

// 工作台（R-147，D-151）：一个会话视图，按状态自动分三档——
//   要你处理（大卡，就地作答）→ 在跑（中卡，实时动作）→ 待命 / 空闲 / 久未回复（一行）；已结束折叠在最底下。
// 档内按项目分组；会话换档时用布局动画移过去，新进「要你处理」的卡片轻轻提示一次。宽屏右栏放进行中的计划、最近动态、队伍在线。

type Filter = 'all' | 'todo'

// 分组键是项目 ID 时才有项目页（没有识别出项目的会话按目录分组）
const projectHref = (key: string) => (key && !key.startsWith('cwd:') && key !== 'none' ? `/projects/${key}` : undefined)

const match = (s: Session | undefined, k: string) => !k || (!!s && [sessionTitle(s), s.project_name, s.branch, s.cwd, s.last_prompt, s.summary, s.device_name, s.model].some((v) => v?.toLowerCase().includes(k)))

function SendAllBar() {
  const wb = useWorkbench()
  const { drafts, send, clearDrafts, queued, cancel } = useOutbox()
  const ready = wb.decisions
    .filter((d) => d.status === 'pending')
    .map((d) => ({ code: d.code, answer: composeAnswer(drafts[d.code], d.options ?? []), files: drafts[d.code]?.files?.map((f) => f.id) }))
    .filter((x) => x.answer && !queued[x.code])
  return (
    <AnimatePresence>
      {ready.length >= 2 && (
        <motion.div initial={{ y: 80, opacity: 0 }} animate={{ y: 0, opacity: 1 }} exit={{ y: 80, opacity: 0 }} transition={{ type: 'spring', bounce: 0.2, duration: 0.4 }} className="fixed inset-x-0 bottom-20 z-20 flex justify-center px-4 lg:bottom-8 lg:pl-[var(--sidebar-w,232px)]">
          <div className="flex items-center gap-2 rounded-2xl border border-line bg-surface/95 py-2 pr-2 pl-4 shadow-pop backdrop-blur">
            <span className="text-sm text-fg-2">{t.decision.drafts(ready.length)}</span>
            <Button size="sm" variant="ghost" onClick={() => clearDrafts(ready.map((x) => x.code))}>
              {t.decision.clearDrafts}
            </Button>
            <Button
              size="sm"
              variant="primary"
              icon={<CornerDownLeft className="size-3.5" />}
              onClick={() => {
                // 批量回答（R-161）：每张卡上各有撤销条，另给一条 toast 一次撤销全部
                send(ready)
                undoToast(t.verdict.many(ready.length), t.verdict.answer, Date.now() + UNDO_MS, () => ready.forEach((x) => cancel(x.code)))
              }}
            >
              {t.verdict.answerAll(ready.length)}
            </Button>
          </div>
        </motion.div>
      )}
    </AnimatePresence>
  )
}


// ---------- 分档 ----------

type Act = { key: string; pk: string; name: string; th?: Thread; plan?: Plan }
type Row = { key: string; pk: string; name: string; s: Session }
type Tiers = { act: Act[]; run: Row[]; rest: Row[]; ended: Row[] }

const restRank: Record<string, number> = { parked: 0, waiting_reply: 1, idle: 2, lost: 3 }

function useTiers(wb: ReturnType<typeof useWorkbench>, k: string): Tiers {
  return useMemo(() => {
    const out: Tiers = { act: [], run: [], rest: [], ended: [] }
    const place = (s: Session, g: ProjectGroup) => {
      if (!match(s, k)) return
      const ds = displayState(s)
      const row = { key: s.key, pk: g.key, name: g.name, s }
      if (s.state === 'ended' || ds === 'done') out.ended.push(row)
      else if (ds === 'running' || ds === 'background') out.run.push(row)
      else out.rest.push(row)
    }
    for (const g of wb.projects) {
      for (const th of g.threads) {
        const s = th.s
        // 在等你回复（R-117）：最后回复在问你、没提正式决策的会话也进这一档，但不计入件数
        const act = th.decisions.length > 0 || th.reviews.length > 0 || th.orphans.length > 0 || (!!s && asking(s)) || awaitingReply(s)
        if (act) {
          if (match(s, k) || !s) out.act.push({ key: th.key, pk: g.key, name: g.name, th })
        } else if (s) place(s, g)
      }
      for (const s of g.active) {
        if (awaitingReply(s) && match(s, k)) out.act.push({ key: s.key, pk: g.key, name: g.name, th: { key: s.key, s, decisions: [], reviews: [], orphans: [], urgency: 5, items: 0 } })
        else place(s, g)
      }
      for (const s of g.ended) place(s, g)
    }
    if (!k)
      for (const x of wb.plans) out.act.push({ key: `plan:${x.code}`, pk: x.project_id || 'none', name: x.project_name || '', plan: x })
    out.rest.sort((a, b) => (restRank[displayState(a.s)] ?? 9) - (restRank[displayState(b.s)] ?? 9) || b.s.last_active_at.localeCompare(a.s.last_active_at))
    out.ended.sort((a, b) => b.s.last_active_at.localeCompare(a.s.last_active_at))
    return out
  }, [wb.projects, wb.plans, k])
}

// byProject：档内按项目分组（保持出现顺序）。
function byProject<T extends { pk: string; name: string }>(items: T[]) {
  const m = new Map<string, { pk: string; name: string; items: T[] }>()
  for (const x of items) {
    let g = m.get(x.pk)
    if (!g) m.set(x.pk, (g = { pk: x.pk, name: x.name, items: [] }))
    if (!g.name && x.name) g.name = x.name
    g.items.push(x)
  }
  return [...m.values()]
}

// ---------- 版面 ----------

function TierTitle({ title, count, extra }: { title: string; count: number; extra?: React.ReactNode }) {
  return (
    <div className="mb-3 flex items-center gap-2.5">
      <h2 className="shrink-0 text-[13px] font-semibold text-fg-2">{title}</h2>
      <span className="text-xs text-fg-3 tabular">{count}</span>
      {extra}
      <span aria-hidden className="h-0 min-w-6 flex-1 border-t border-dashed border-line-strong/80" />
    </div>
  )
}

function ProjectLabel({ pk, name }: { pk: string; name: string }) {
  const href = projectHref(pk)
  const label = (
    <span className="inline-flex items-center gap-1.5">
      <ProjGlyph />
      {name || t.thread.noProject}
    </span>
  )
  return <div className="mb-2 px-1 text-xs text-fg-3">{href ? <Link to={href} className="hover:text-accent">{label}</Link> : label}</div>
}

// Moving：会话卡片在三档之间移动时的布局动画；layoutId 跟着会话走。
function Moving({ id, children, className, flash }: { id: string; children: React.ReactNode; className?: string; flash?: boolean }) {
  return (
    <motion.div layout="position" layoutId={id} transition={{ type: 'spring', bounce: 0.12, duration: 0.5 }} className={cx(flash && 'tier-flash', className)}>
      {children}
    </motion.div>
  )
}

// count：标题旁的数字；「要你处理」档按件数算（同一张大卡里可能有好几件），与页头、侧栏角标一致。
function Section<T extends { pk: string; name: string; key: string }>({ title, items, multi, render, wrap, count, extra }: { title: string; items: T[]; multi: boolean; render: (x: T) => React.ReactNode; wrap?: string; count?: number; extra?: React.ReactNode }) {
  if (!items.length) return null
  return (
    <section className="mb-8">
      <TierTitle title={title} count={count ?? items.length} extra={extra} />
      <div className="space-y-4">
        {byProject(items).map((g) => (
          <div key={g.pk}>
            {multi && <ProjectLabel pk={g.pk} name={g.name} />}
            <div className={wrap ?? 'space-y-3'}>{g.items.map(render)}</div>
          </div>
        ))}
      </div>
    </section>
  )
}

// ProjectDots：工具栏右侧，每个项目三档各有几个（红：要你处理，绿：在跑，蓝：待命）。
function ProjectDots({ tiers }: { tiers: Tiers }) {
  const rows = new Map<string, { name: string; act: number; run: number; park: number }>()
  const add = (pk: string, name: string, key: 'act' | 'run' | 'park') => {
    const r = rows.get(pk) ?? { name, act: 0, run: 0, park: 0 }
    if (!r.name && name) r.name = name
    r[key]++
    rows.set(pk, r)
  }
  tiers.act.forEach((x) => add(x.pk, x.name, 'act'))
  tiers.run.forEach((x) => add(x.pk, x.name, 'run'))
  tiers.rest.filter((x) => displayState(x.s) === 'parked').forEach((x) => add(x.pk, x.name, 'park'))
  if (!rows.size) return null
  return (
    <div className="flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-fg-3 max-sm:hidden">
      {[...rows.values()].map((r) => (
        <span key={r.name} className="inline-flex items-center gap-2">
          <span className="text-fg-2">{r.name || t.thread.noProject}</span>
          {(
            [
              ['act', 'bg-reply'],
              ['run', 'bg-run'],
              ['park', 'bg-accent'],
            ] as const
          ).map(([k, c]) =>
            r[k] ? (
              <Tip key={k} content={t.desk.dotTip[k]}>
                <span className="inline-flex items-center gap-1 tabular">
                  <span className={cx('size-1.5 rounded-full', c)} />
                  {r[k]}
                </span>
              </Tip>
            ) : null,
          )}
        </span>
      ))}
    </div>
  )
}

export default function WorkbenchPage() {
  const now = useNow()
  const wb = useWorkbench()
  const settings = useSettings()
  const todo = useTodo(wb)
  const [filter, setFilter] = usePref<Filter>('ks.home.filter', 'all')
  const [showEnded, setShowEnded] = usePref('ks.home.ended', false)
  const [q, setQ] = useState('')
  const [recap, setRecap] = useState(false)
  const k = q.trim().toLowerCase()
  const recapOn = !!settings.data?.ai?.recap
  const tiers = useTiers(wb, k)
  const search = useRef<HTMLInputElement>(null)
  const morningProjects = useMorningProjects()

  // 「/」聚焦搜索
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const el = e.target as HTMLElement
      if (e.key !== '/' || e.metaKey || e.ctrlKey || el.closest('input, textarea, [contenteditable="true"]')) return
      e.preventDefault()
      search.current?.focus()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])

  // 新进「要你处理」的卡片轻轻提示一次
  const seen = useRef<Set<string> | null>(null)
  const actKeys = tiers.act.map((x) => x.key)
  const fresh = new Set(seen.current ? actKeys.filter((x) => !seen.current!.has(x)) : [])
  useEffect(() => {
    if (!wb.loading) seen.current = new Set(actKeys)
  })

  if (wb.error) return <ErrorState error={wb.error} onRetry={wb.refetch} />

  const parked = tiers.rest.filter((x) => displayState(x.s) === 'parked').length
  const replies = tiers.rest.filter((x) => displayState(x.s) === 'waiting_reply').length
  // 只是在等你回复、没有正式事项的会话：在「要你处理」档里，但件数只算正式事项（R-147 口径不变），单独标注
  const asks = tiers.act.filter((x) => x.th && awaitingReply(x.th.s) && !x.th.decisions.length && !x.th.reviews.length && !x.th.orphans.length && !(x.th.s && asking(x.th.s))).length
  const projectsSeen = new Set([...tiers.act, ...tiers.run, ...tiers.rest].map((x) => x.pk))
  const multi = projectsSeen.size > 1
  const todoOnly = filter === 'todo'
  const nothing = !tiers.act.length && (todoOnly || (!tiers.run.length && !tiers.rest.length))

  const main = wb.loading ? (
    <CardSkeleton rows={3} />
  ) : !wb.devices.length ? (
    <Card>
      <Empty
        icon={<MonitorSmartphone />}
        title={t.home.noDevices}
        action={
          <Link to="/settings?tab=devices">
            <Button variant="primary">{t.home.addDevice}</Button>
          </Link>
        }
      >
        {t.home.noDevicesSub}
      </Empty>
    </Card>
  ) : (
    <LayoutGroup>
      <Section
        title={t.desk.tier.act}
        count={todo.length}
        extra={
          asks > 0 && (
            <Tip content={t.desk.asksTip}>
              <span className="rounded-full bg-reply-soft px-2 py-0.5 text-xs text-reply">{t.desk.asks(asks)}</span>
            </Tip>
          )
        }
        items={tiers.act}
        multi={multi}
        render={(x) => (
          <Moving key={x.key} id={x.th?.s?.key ?? x.key} flash={fresh.has(x.key)}>
            {x.plan ? <PlanCard x={x.plan} act /> : <ThreadCard th={x.th!} now={now} projectName={x.name} sessions={wb.sessions} />}
          </Moving>
        )}
      />
      {!todoOnly && (
        <>
          <Section
            title={t.desk.tier.run}
            items={tiers.run}
            multi={multi}
            wrap="grid grid-cols-1 gap-3 lg:grid-cols-2 min-[1700px]:grid-cols-3"
            render={(x) => (
              <Moving key={x.key} id={x.key} className="h-full">
                <SessionCard s={x.s} now={now} />
              </Moving>
            )}
          />
          <Section
            title={t.desk.tier.rest}
            items={tiers.rest}
            multi={multi}
            wrap="@container rounded-xl border border-line bg-surface p-1.5 shadow-xs"
            render={(x) => (
              <Moving key={x.key} id={x.key}>
                <SessionRow s={x.s} now={now} />
              </Moving>
            )}
          />
        </>
      )}
      {nothing && (
        <Card>
          {k ? (
            <Empty icon={<SearchX />} title={t.home.noMatch} />
          ) : !wb.sessions.length ? (
            <Empty icon={<MonitorSmartphone />} title={t.home.empty}>
              {t.home.emptySub}
            </Empty>
          ) : (
            <Empty icon={<CheckCheck />} title={t.home.allClear}>{t.home.allClearSub(tiers.run.length)}</Empty>
          )}
        </Card>
      )}
      {!todoOnly && tiers.ended.length > 0 && (
        <section>
          <button onClick={() => setShowEnded(!showEnded)} aria-expanded={showEnded} className="mb-2 flex items-center gap-1.5 rounded-md px-1 text-[13px] text-fg-3 hover:text-fg-2">
            <ChevronRight className={cx('size-3.5 transition-transform', showEnded && 'rotate-90')} />
            {t.desk.ended(tiers.ended.length)}
          </button>
          {showEnded && (
            <div className="@container rounded-xl border border-line bg-surface p-1.5 shadow-xs">
              {tiers.ended.map((x) => (
                <Moving key={x.key} id={x.key}>
                  <SessionRow s={x.s} now={now} showProject />
                </Moving>
              ))}
            </div>
          )}
        </section>
      )}
    </LayoutGroup>
  )

  return (
    <>
      <PageHeader
        title={t.home.title}
        subtitle={wb.loading ? ' ' : t.desk.counts(todo.length, tiers.run.length, parked, replies, asks)}
        actions={
          recapOn && (
            <Button icon={<History className="size-4" />} onClick={() => setRecap(true)}>
              {t.home.recap}
            </Button>
          )
        }
      />
      <MorningReports sessions={wb.sessions} now={now} />
      <Briefs sessions={wb.sessions} now={now} skip={morningProjects} />
      <PlanChips className="xl:hidden" />
      <div className="xl:grid xl:grid-cols-[minmax(0,1fr)_300px] 2xl:grid-cols-[minmax(0,1fr)_340px] xl:items-start xl:gap-6">
        <div className="min-w-0">
          <div className="mb-5 flex flex-wrap items-center gap-2">
            <div className="relative w-full sm:w-64">
              <Search className="pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2 text-fg-3" />
              <Input ref={search} value={q} onChange={(e) => setQ(e.target.value)} onKeyDown={(e) => e.key === 'Escape' && (setQ(''), e.currentTarget.blur())} placeholder={t.home.search} className="pr-9 pl-9" />
              <span className="pointer-events-none absolute top-1/2 right-3 -translate-y-1/2 max-sm:hidden">
                <Kbd>/</Kbd>
              </span>
            </div>
            <Segmented
              value={filter}
              onChange={setFilter}
              options={[
                { value: 'all', label: t.home.filter.all },
                { value: 'todo', label: t.desk.tier.act, count: todo.length },
              ]}
            />
            <span className="ml-auto" />
            <ProjectDots tiers={tiers} />
          </div>
          {main}
        </div>
        <aside className="hidden space-y-4 xl:sticky xl:top-6 xl:block">
          <PlansRail />
          <RecentRail now={now} />
          <TeamRail groups={wb.projects} />
        </aside>
      </div>
      <SendAllBar />
      {recapOn && <RecapPanel open={recap} onClose={() => setRecap(false)} />}
    </>
  )
}
