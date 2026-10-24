import { Fragment, useEffect, useMemo, useRef, useState } from 'react'
import { Link } from 'react-router'
import { AnimatePresence, motion } from 'motion/react'
import { ChevronRight, CornerDownLeft, History, MonitorSmartphone, Search } from 'lucide-react'
import type { Plan, Session } from '../api/client'
import { useConnected, useSettings } from '../api/queries'
import { t } from '../i18n'
import { cx, relTime, useNow, usePref } from '../lib/util'
import { Button, Card, CardSkeleton, Empty, ErrorState, Input, Kbd, Tip } from '../ui/base'
import { displayState, RoleBadge } from '../ui/marks'
import { ProjGlyph } from '../ui/refmark'
import { useWorkbench, type Thread } from '../app/inbox'
import { composeAnswer, useOutbox } from '../app/outbox'
import { undoToast, UNDO_MS } from '../app/verdict'
import { RecapPanel } from '../app/Recap'
import { Callout, sessionTitle } from '../app/SessionCard'
import { SessionRow } from '../app/thread'
import { PlanCard } from '../app/plans'
import { DecisionComposer, OrphanCard } from '../app/DecisionCard'
import { ReviewList } from '../app/review'
import { awaitingReply, ReplyBox } from '../app/replyBox'
import { asking, BriefStrip, HandledLog, PlanChips, PlansRail, RecentRail, TeamRail, useTodo } from '../app/desk'
import { Lane, laneTone, setOffline, type LaneMode, type Tone } from '../app/lanes'
import { useFillHeight } from '../app/pool'
import { useOpenSession } from '../app/nav'

// 工作台（R-170，D-228 原型；替换 R-147 的三档布局）：一屏驾驶舱，整页不滚动，各栏在区域内滚动。
//   左「要你处理」（就地作答，R-161 统一的判断动作，R-176 按场景取词：通过 / 退回、批准 / 拒绝、回答）+ 底部「刚处理」；
//   中「会话」泳道（app/lanes.tsx），多个项目同时有活跃会话时按项目分组；
//   右「风向」：进行中的计划、最近动态（一条竖线）、队伍固定在底部。
// 按容器宽度排版（R-133）：宽 ≥ 1180 泳道带右侧动作列；960～1180 动作列收起、当前动作写在状态标下一行；
// 640～960 右栏收成顶部计划胶囊；更窄（手机）分「要你处理 / 在跑 / 风向」页签，整页纵向滚动。

const match = (s: Session | undefined, k: string) => !k || (!!s && [sessionTitle(s), s.project_name, s.branch, s.cwd, s.last_prompt, s.summary, s.device_name, s.model].some((v) => v?.toLowerCase().includes(k)))

// 分组键是项目 ID 时才有项目页（没有识别出项目的会话按目录分组）
const projectHref = (key: string) => (key && !key.startsWith('cwd:') && key !== 'none' ? `/projects/${key}` : undefined)

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
                undoToast(t.verdict.many(ready.length), t.verdict.pass('answer'), Date.now() + UNDO_MS, () => ready.forEach((x) => cancel(x.code)))
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

// ---------- 数据 ----------

type Ask = { key: string; pk: string; name: string; th?: Thread; plan?: Plan }
type LaneRow = { s: Session; pk: string; name: string; tone: Tone }

const toneRank: Record<Tone, number> = { you: 0, run: 1, bg: 2, park: 3, idle: 4, sub: 9 }

function useDesk(wb: ReturnType<typeof useWorkbench>, k: string) {
  return useMemo(() => {
    const asks: Ask[] = []
    const lanes: LaneRow[] = []
    const ended: Session[] = []
    const waiting = new Set<string>()
    for (const g of wb.projects) {
      for (const th of g.threads) {
        const s = th.s
        // 在等你回复（R-117）：最后回复在问你、没提正式决策的会话也进「要你处理」，但不计入件数
        const act = th.decisions.length > 0 || th.reviews.length > 0 || th.orphans.length > 0 || (!!s && asking(s)) || awaitingReply(s)
        if (act && (match(s, k) || !s)) {
          asks.push({ key: th.key, pk: g.key, name: g.name, th })
          if (s) waiting.add(s.key)
        }
      }
      for (const s of g.active)
        if (awaitingReply(s) && match(s, k)) {
          asks.push({ key: s.key, pk: g.key, name: g.name, th: { key: s.key, s, decisions: [], reviews: [], orphans: [], urgency: 5, items: 0 } })
          waiting.add(s.key)
        }
    }
    const place = (s: Session, pk: string, name: string) => {
      if (!match(s, k)) return
      if (s.state === 'ended' || displayState(s) === 'done') ended.push(s)
      else lanes.push({ s, pk, name, tone: laneTone(s, waiting.has(s.key)) })
    }
    for (const g of wb.projects) {
      for (const th of g.threads) if (th.s) place(th.s, g.key, g.name)
      for (const s of g.active) place(s, g.key, g.name)
      for (const s of g.ended) place(s, g.key, g.name)
    }
    if (!k) for (const x of wb.plans) asks.push({ key: `plan:${x.code}`, pk: x.project_id || 'none', name: x.project_name || '', plan: x })
    // 泳道按状态排：等你 → 在跑 → 后台 → 待命 → 空闲；同状态 PM 在前，再按最近活跃
    lanes.sort((a, b) => toneRank[a.tone] - toneRank[b.tone] || Number(b.s.role === 'pm') - Number(a.s.role === 'pm') || b.s.last_active_at.localeCompare(a.s.last_active_at))
    ended.sort((a, b) => b.last_active_at.localeCompare(a.last_active_at))
    // 多个项目同时有活跃会话时按项目分组（顾问意见）：组按最急的泳道排，组内仍按状态排
    const groups = new Map<string, { pk: string; name: string; rows: LaneRow[] }>()
    for (const r of lanes) {
      let g = groups.get(r.pk)
      if (!g) groups.set(r.pk, (g = { pk: r.pk, name: r.name, rows: [] }))
      if (!g.name && r.name) g.name = r.name
      g.rows.push(r)
    }
    return { asks, lanes, ended, groups: [...groups.values()] }
  }, [wb.projects, wb.plans, k])
}

// ---------- 宽度 ----------

function useBox() {
  const [el, setEl] = useState<HTMLDivElement | null>(null)
  const [w, setW] = useState(0)
  useEffect(() => {
    if (!el) return
    const ro = new ResizeObserver(([e]) => setW(e.contentRect.width))
    ro.observe(el)
    return () => ro.disconnect()
  }, [el])
  return [el, setEl, w] as const
}

// ---------- 要你处理 ----------

const kindTone: Record<string, string> = { review: 'text-seal', decide: 'text-wait', plan: 'text-accent', orphan: 'text-wait', approve: 'text-wait', answer: 'text-reply', reply: 'text-reply' }

function kinds(th: Thread) {
  const out: string[] = []
  const s = th.s
  const ds = s ? displayState(s) : ''
  if (ds === 'waiting_approval') out.push('approve')
  if (ds === 'waiting_answer') out.push('answer')
  if (th.decisions.length) out.push('decide')
  if (th.reviews.length) out.push('review')
  if (th.orphans.length) out.push('orphan')
  if (awaitingReply(s)) out.push('reply')
  return out
}

function AskCard({ x, now, sessions, fresh, lit, onHover }: { x: Ask; now: number; sessions: Session[]; fresh: boolean; lit: boolean; onHover: (k: string | null) => void }) {
  const open = useOpenSession()
  if (x.plan)
    return (
      <div className={cx('shrink-0', fresh && 'desk-drop')}>
        <PlanCard x={x.plan} act />
      </div>
    )
  const th = x.th!
  const s = th.s
  const ks = kinds(th)
  const at = [s?.state_since, ...th.decisions.map((d) => d.created_at), ...th.reviews.map((r) => r.updated_at)].filter((v): v is string => !!v).sort().at(-1)
  return (
    <article
      onMouseEnter={() => s && onHover(s.key)}
      onMouseLeave={() => onHover(null)}
      className={cx('@container shrink-0 rounded-[14px] border border-reply/30 bg-surface p-3 shadow-card', fresh && 'desk-drop', lit && 'desk-lit')}
    >
      <header className="flex min-w-0 items-center gap-2 text-[11.5px] text-fg-3">
        {ks.map((k) => (
          <b key={k} className={cx('shrink-0 font-semibold tracking-wider', kindTone[k])}>
            {t.desk.kind[k]}
          </b>
        ))}
        {s && <RoleBadge s={s} />}
        {s ? (
          <button onClick={() => open(s.key)} className="min-w-0 truncate hover:text-accent">
            {sessionTitle(s)}
          </button>
        ) : (
          <span className="min-w-0 truncate">{x.name || t.thread.noSession}</span>
        )}
        {at && <span className="ml-auto shrink-0 tabular">{relTime(at, now)}</span>}
      </header>
      <div className="mt-2 space-y-2">
        {s && asking(s) && <Callout s={s} />}
        {awaitingReply(s) && <ReplyBox s={s} />}
        {th.decisions.map((d) => (
          <DecisionComposer key={d.id} d={d} embedded />
        ))}
        {th.orphans.map((d) => (
          <OrphanCard key={d.id} d={d} sessions={sessions} />
        ))}
        {th.reviews.length > 0 && <ReviewList items={th.reviews} advice />}
      </div>
    </article>
  )
}

// ---------- 版面 ----------

function PaneHead({ title, n, children }: { title: string; n?: number | string; children?: React.ReactNode }) {
  return (
    <header className="flex h-8 min-w-0 shrink-0 items-center gap-2 px-0.5">
      {title && <h2 className="shrink-0 font-display text-[15px] font-bold tracking-wide text-fg">{title}</h2>}
      {n !== undefined && <span className="min-w-0 truncate text-xs text-fg-3 tabular">{n}</span>}
      {children}
    </header>
  )
}

const legendTone = [
  ['run', 'bg-run-soft text-run', 'var(--run)'],
  ['you', 'bg-reply-soft text-reply', 'var(--reply)'],
  ['park', 'bg-accent-soft text-accent', 'var(--accent)'],
  ['idle', 'text-fg-3 ring-1 ring-inset ring-line', ''],
] as const

function Legend() {
  return (
    <div className="flex shrink-0 items-center gap-1.5 text-[11.5px] whitespace-nowrap">
      {legendTone.map(([k, cls, c]) => (
        <span key={k} className={cx('inline-flex items-center gap-1.5 rounded-full py-0.5 pr-2 pl-1.5', cls)}>
          <i className={cx('desk-lamp', k === 'idle' && 'ring-[1.5px] ring-idle ring-inset')} style={{ ['--lamp' as string]: c || 'transparent' }} />
          {t.desk.legend[k]}
        </span>
      ))}
    </div>
  )
}

type Tab = 'ask' | 'run' | 'side'

export default function WorkbenchPage() {
  const now = useNow()
  const wb = useWorkbench()
  const settings = useSettings()
  const todo = useTodo(wb)
  const connected = useConnected()
  const [showEnded, setShowEnded] = usePref('ks.home.ended', false)
  const [tab, setTab] = usePref<Tab>('ks.home.tab', 'ask')
  const [q, setQ] = useState('')
  const [recap, setRecap] = useState(false)
  const [hover, setHover] = useState<{ k: string; from: 'ask' | 'lane' } | null>(null)
  const k = q.trim().toLowerCase()
  const recapOn = !!settings.data?.ai?.recap
  const desk = useDesk(wb, k)
  const search = useRef<HTMLInputElement>(null)
  const [el, setEl, w] = useBox()
  const phone = w > 0 && w < 640
  const rail = w >= 960
  const mode: LaneMode = phone ? 'phone' : w >= 1180 ? 'full' : rail ? 'mid' : 'narrow'
  const fillH = useFillHeight(el, !phone && w > 0)

  // 断线：时间线停在最后一帧并变灰
  useEffect(() => setOffline(!connected), [connected])

  // 「/」聚焦搜索
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const el = e.target as HTMLElement
      if (e.key !== '/' || e.metaKey || e.ctrlKey || el.closest('input, textarea, [contenteditable="true"]')) return
      e.preventDefault()
      if (phone) setTab('run')
      window.setTimeout(() => search.current?.focus(), 0)
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [phone, setTab])

  // 新进「要你处理」的卡片落下一次
  const seen = useRef<Set<string> | null>(null)
  const askKeys = desk.asks.map((x) => x.key)
  const fresh = new Set(seen.current ? askKeys.filter((x) => !seen.current!.has(x)) : [])
  useEffect(() => {
    if (!wb.loading) seen.current = new Set(askKeys)
  })

  if (wb.error) return <ErrorState error={wb.error} onRetry={wb.refetch} />

  const run = desk.lanes.filter((x) => x.tone === 'run' || x.tone === 'bg').length
  const park = desk.lanes.filter((x) => x.tone === 'park').length
  const quiet = desk.lanes.filter((x) => x.tone === 'idle').length
  const multi = desk.groups.length > 1
  const replies = desk.asks.filter((x) => x.th && !x.th.items && awaitingReply(x.th.s)).length

  // ---- 左：要你处理 ----
  const askPane = (
    <section className={cx('flex min-h-0 min-w-0 flex-col gap-2.5', phone && tab !== 'ask' && 'hidden')}>
      {(!phone || replies > 0) && (
      <PaneHead title={phone ? '' : t.desk.paneAsk} n={phone ? undefined : todo.length}>
        {replies > 0 && (
          <Tip content={t.desk.asksTip}>
            <span className="truncate rounded-full bg-reply-soft px-2 py-0.5 text-[11px] text-reply">{t.desk.asksN(replies)}</span>
          </Tip>
        )}
      </PaneHead>
      )}
      <div className={cx('desk-scroll flex min-h-0 flex-col gap-2.5', !phone && 'flex-1 overflow-y-auto overscroll-contain p-0.5 pr-1.5')}>
        {wb.loading ? (
          <CardSkeleton rows={2} />
        ) : desk.asks.length ? (
          desk.asks.map((x) => (
            <AskCard key={x.key} x={x} now={now} sessions={wb.sessions} fresh={fresh.has(x.key)} lit={!!x.th?.s && hover?.from === 'lane' && hover.k === x.th.s.key} onHover={(key) => setHover(key ? { k: key, from: 'ask' } : null)} />
          ))
        ) : (
          <p className="px-2 py-7 text-center font-display text-[15px] text-fg-3">{t.desk.noAsk}</p>
        )}
      </div>
      <HandledLog now={now} className="shrink-0" />
    </section>
  )

  // ---- 中：会话泳道 ----
  const laneEls = desk.groups.map((g) => {
    const href = projectHref(g.pk)
    const gr = g.rows.filter((x) => x.tone === 'run' || x.tone === 'bg').length
    const gp = g.rows.filter((x) => x.tone === 'park').length
    return (
      <Fragment key={g.pk}>
        {multi && (
          <div className="flex shrink-0 items-center gap-2 px-1 pt-1 text-[11.5px] text-fg-3">
            <ProjGlyph />
            {href ? (
              <Link to={href} className="shrink-0 font-medium text-fg-2 hover:text-accent">
                {g.name || t.thread.noProject}
              </Link>
            ) : (
              <span className="shrink-0 font-medium text-fg-2">{g.name || t.thread.noProject}</span>
            )}
            <span className="shrink-0 tabular">{t.desk.groupSub(gr, gp)}</span>
            <span aria-hidden className="h-0 min-w-4 flex-1 border-t border-dashed border-line" />
          </div>
        )}
        {g.rows.map((r) => (
          <Lane key={r.s.key} s={r.s} tone={r.tone} now={now} mode={mode} lit={hover?.from === 'ask' && hover.k === r.s.key} onHover={(key) => setHover(key ? { k: key, from: 'lane' } : null)} />
        ))}
      </Fragment>
    )
  })
  const lanePane = (
    <section className={cx('flex min-h-0 min-w-0 flex-col gap-2.5', phone && tab !== 'run' && 'hidden')}>
      <PaneHead title={phone ? '' : t.desk.paneLanes} n={wb.loading ? '' : mode === 'full' ? t.desk.laneSub(run, park, quiet) : t.desk.laneSubShort(run, desk.lanes.length)}>
        <span className="flex-1" />
        <div className={cx('relative shrink-0', mode === 'full' ? 'w-52' : 'w-36')}>
          <Search className="pointer-events-none absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2 text-fg-3" />
          <Input ref={search} value={q} onChange={(e) => setQ(e.target.value)} onKeyDown={(e) => e.key === 'Escape' && (setQ(''), e.currentTarget.blur())} placeholder={t.home.search} className="h-8 pr-8 pl-8 text-[13px]" />
          <span className={cx('pointer-events-none absolute top-1/2 right-2 -translate-y-1/2', mode !== 'full' && 'hidden')}>
            <Kbd>/</Kbd>
          </span>
        </div>
      </PaneHead>
      <div className={cx('desk-scroll flex min-h-0 flex-col gap-2', !phone && 'flex-1 overflow-y-auto overscroll-contain p-0.5 pr-1.5')}>
        {wb.loading ? (
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
        ) : desk.lanes.length ? (
          laneEls
        ) : (
          <p className="px-4 py-10 text-center font-display text-[15px] text-fg-3">{k ? t.home.noMatch : t.desk.noLanes}</p>
        )}
        {showEnded && desk.ended.length > 0 && (
          <div className="@container shrink-0 rounded-[14px] p-1 ring-1 ring-line ring-inset">
            {desk.ended.map((s) => (
              <SessionRow key={s.key} s={s} now={now} showProject={multi} />
            ))}
          </div>
        )}
      </div>
      <div className="flex shrink-0 items-center gap-2 px-0.5 text-xs text-fg-3">
        {desk.ended.length > 0 && (
          <button onClick={() => setShowEnded(!showEnded)} aria-expanded={showEnded} className="inline-flex shrink-0 items-center gap-1 hover:text-fg-2">
            <ChevronRight className={cx('size-3.5 transition-transform', showEnded && 'rotate-90')} />
            {t.desk.endedFoot(desk.ended.length)}
          </button>
        )}
        {desk.ended.length > 0 && !phone && <span aria-hidden>·</span>}
        {!phone && <span className="truncate">{t.desk.axis}</span>}
      </div>
    </section>
  )

  // ---- 右：风向 ----
  const sidePane = (
    <aside className={cx('flex min-h-0 min-w-0 flex-col gap-2.5', phone && tab !== 'side' && 'hidden')}>
      {!phone && <PaneHead title={t.desk.paneWind} />}
      <PlansRail className={cx('shrink-0', !phone && 'desk-scroll max-h-[42%] overflow-y-auto')} />
      <RecentRail now={now} className={cx(!phone ? 'min-h-[8rem] flex-1' : 'max-h-[60vh]')} />
      <TeamRail groups={wb.projects} className="shrink-0" />
    </aside>
  )

  const body = phone ? (
    <div className="min-w-0">
      {askPane}
      {lanePane}
      {sidePane}
    </div>
  ) : (
    <div
      className={cx(
        'grid min-h-0 flex-1',
        // 列宽按容器宽度（R-133），不按视口：侧栏展开 / 收起时跟着变
        w >= 2200
          ? 'grid-cols-[minmax(320px,28rem)_minmax(0,1fr)_minmax(300px,24rem)] gap-5'
          : w >= 1180
            ? 'grid-cols-[minmax(300px,25rem)_minmax(0,1fr)_minmax(280px,22rem)] gap-4'
            : rail
              ? 'grid-cols-[minmax(270px,20rem)_minmax(0,1fr)_minmax(230px,16.5rem)] gap-3.5'
              : 'grid-cols-[minmax(250px,19rem)_minmax(0,1fr)] gap-3',
      )}
    >
      {askPane}
      {lanePane}
      {rail && sidePane}
    </div>
  )

  return (
    <div ref={setEl} style={fillH ? { height: fillH } : undefined} className={cx('flex min-w-0 flex-col', phone ? 'gap-3' : 'gap-3.5')}>
      {/* 顶栏：标题与件数 · 简报位（背后的风） · 状态说明 */}
      <div className={cx('grid min-w-0 items-center gap-x-4 gap-y-2.5', phone ? 'grid-cols-1' : mode === 'full' ? 'grid-cols-[auto_minmax(0,1fr)_auto]' : 'grid-cols-[auto_minmax(0,1fr)]')}>
        <div className="flex min-w-0 items-baseline gap-3">
          <h1 className="shrink-0 font-display text-2xl leading-none font-bold tracking-wider text-fg">{t.home.title}</h1>
          <span className="truncate text-[12.5px] whitespace-nowrap text-fg-3 tabular">{t.desk.sumLine(todo.length, run)}</span>
          {recapOn && (
            <Tip content={t.home.recap}>
              <Button size="icon-sm" variant="ghost" aria-label={t.home.recap} onClick={() => setRecap(true)} className="ml-auto self-center">
                <History className="size-4" />
              </Button>
            </Tip>
          )}
        </div>
        <BriefStrip sessions={wb.sessions} now={now} />
        {mode === 'full' && <Legend />}
        {!rail && <PlanChips className={cx(!phone && 'col-span-2')} />}
        {phone && (
          <div role="tablist" className="flex gap-1.5">
            {(
              [
                ['ask', t.desk.paneAsk, todo.length],
                ['run', t.desk.tabRun, run],
                ['side', t.desk.paneWind, 0],
              ] as const
            ).map(([v, label, n]) => (
              <button
                key={v}
                role="tab"
                aria-selected={tab === v}
                onClick={() => setTab(v)}
                className={cx('rounded-full border px-3.5 py-1.5 text-[13px] transition-colors', tab === v ? 'border-line-strong bg-surface text-fg shadow-card' : 'border-line text-fg-3')}
              >
                {label}
                {n > 0 && <span className="ml-1.5 text-xs text-fg-3 tabular">{n}</span>}
              </button>
            ))}
          </div>
        )}
      </div>
      <div className={cx('flex min-h-0 min-w-0 flex-1 flex-col', !connected && 'desk-offline')}>{body}</div>
      <SendAllBar />
      {recapOn && <RecapPanel open={recap} onClose={() => setRecap(false)} />}
    </div>
  )
}
