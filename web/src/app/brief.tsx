import { useEffect, useRef, useState, type ReactNode } from 'react'
import { AnimatePresence, motion } from 'motion/react'
import { ChevronDown, Plane, WifiOff } from 'lucide-react'
import { toast } from 'sonner'
import type { MorningReport, Session } from '../api/client'
import { useAway, useAwayReport, useConnected, useMorning, useProjects, useRequirements } from '../api/queries'
import { t } from '../i18n'
import type { Zoom } from '../lib/lanedata'
import { headlineOf } from '../lib/brief'
import { cx, duration, relTime, usePref } from '../lib/util'
import { Button, Checkbox, Segmented } from '../ui/base'
import { presence, RoleTag } from '../ui/marks'
import { Modal } from '../ui/overlay'
import { Linkify, RefChip, useOpenRef } from '../ui/refmark'
import { useActions } from './actions'
import type { Ask } from './asks'
import { TokBrief, Wind } from './lanes'
import { useOpenAsk } from './nav'
import { ProxyLine, ReleaseLine } from './morning'

// 顶部简报条（R-212，D-770 方案一）：一行固定高度 =「谁写的 · 一句话结论 · 待你查看 n」，右边是「暂时离开」开关（R-210）。
//   - 有多条、多个作者的简报时，在这一行里定时轮播；鼠标悬停 / 触摸时暂停；点开看全部。
//   - 展开面板是时段报告，四层：待你查看 → 为你决策 → 阶段完成 → 尚在推进；每层默认露 4 条、一行一件事，点条目在左侧展开。
//   - 离开中这一条直接变成离开状态条（带「结束离开」），不出现两条顶栏；离开结束后的时段报告看过一次就让位给主调度简报。
//   - 老数据没有一句话结论时取总述 / 简报的第一句截到 40 字。

const briefTTL = 12 * 3600e3 // 超过 12 小时没更新的简报不再显示
const awayFold = 2 * 3600e3 // 没有离开记录的老数据：授权窗口结束超过这么久，小结收起
const rotateMs = 6000
const layerLimit = 4

// 没有离开记录时，老数据的小结按授权窗口 / 总述时间算新旧
function legacyEnd(r: MorningReport): string | undefined {
  const g = r.grant
  if (g) return g.status === 'active' ? undefined : g.revoked_at && g.revoked_at < g.expires_at ? g.revoked_at : g.expires_at
  return r.note?.at
}

type Item = { id: string; role: 'aide' | 'pm'; who?: Session; label: string; head: string; look: string[]; text: string; at: string; report?: boolean }

function useAwayView(now: number) {
  const q = useAway()
  const active = q.data?.away
  const last = q.data?.last
  const startMs = active ? Date.parse(active.started_at) : 0
  const elapsed = active ? duration(active.started_at, now) : ''
  const over = !!active && active.expect_minutes > 0 && now - startMs > active.expect_minutes * 60_000
  return { status: q.data, active, last, elapsed, over }
}

// ---------- 开始离开 ----------

function AwayStart({ open, onClose }: { open: boolean; onClose: () => void }) {
  const { startAway } = useActions()
  const [expect, setExpect] = useState('120')
  const [scope, setScope] = useState<'none' | 'low' | 'medium'>('low')
  const [hold, setHold] = useState(false)
  const [busy, setBusy] = useState(false)
  const go = async () => {
    setBusy(true)
    const r = await startAway({ expect_minutes: Number(expect) as 30 | 120 | 480 | 0, scope, hold_push: hold })
    setBusy(false)
    if (r) {
      toast.success(t.away.started)
      onClose()
    }
  }
  return (
    <Modal
      open={open}
      onClose={onClose}
      title={t.away.title}
      description={t.away.intro}
      footer={
        <>
          <Button onClick={onClose}>{t.common.cancel}</Button>
          <Button variant="primary" icon={<Plane className="size-4" />} disabled={busy} onClick={go}>
            {t.away.confirm}
          </Button>
        </>
      }
    >
      <div className="space-y-4 pb-1">
        <section className="space-y-1.5">
          <h4 className="text-[13px] font-medium text-fg-2">{t.away.expect}</h4>
          <Segmented value={expect} onChange={setExpect} options={t.away.expects} />
          <p className="text-xs text-fg-3">{t.away.expectHint}</p>
        </section>
        <section className="space-y-1.5">
          <h4 className="text-[13px] font-medium text-fg-2">{t.away.scope}</h4>
          <Segmented value={scope} onChange={setScope} options={t.away.scopes as { value: 'none' | 'low' | 'medium'; label: string }[]} />
          <p className="text-xs leading-relaxed text-fg-3">{t.away.scopeHint}</p>
        </section>
        <Checkbox checked={hold} onChange={setHold} hint={t.away.holdHint}>
          {t.away.hold}
        </Checkbox>
      </div>
    </Modal>
  )
}

// 结束离开：只有手动能结束（R-210）
function useEndAway() {
  const { endAway } = useActions()
  const [busy, setBusy] = useState(false)
  return {
    busy,
    end: async () => {
      setBusy(true)
      const r = await endAway()
      setBusy(false)
      if (r) toast(t.away.ended)
    },
  }
}

// AwayBar：非工作台页面顶部的离开状态条（工作台上由简报条自己变成它）。
export function AwayBar({ now }: { now: number }) {
  const v = useAwayView(now)
  const e = useEndAway()
  if (!v.active) return null
  return (
    <div className="mb-4 flex min-w-0 items-center gap-2.5 rounded-xl border border-accent/30 bg-accent-soft/60 px-3.5 py-2 text-sm">
      <Plane className="size-4 shrink-0 text-accent" />
      <span className="shrink-0 font-medium text-fg">{t.away.active}</span>
      <span className="min-w-0 flex-1 truncate text-fg-2">{[t.away.elapsed(v.elapsed), v.over ? t.away.over : ''].filter(Boolean).join(t.common.sep)}</span>
      <Button size="sm" disabled={e.busy} onClick={e.end}>
        {t.away.end}
      </Button>
    </div>
  )
}

// ---------- 展开面板 ----------

function Layer({ title, n, empty, children }: { title: string; n: number; empty: string; children: ReactNode[] }) {
  const [all, setAll] = useState(false)
  const rows = all ? children : children.slice(0, layerLimit)
  return (
    <section className="min-w-0">
      <h4 className="mb-1.5 flex items-baseline gap-2 text-[13px] font-semibold text-fg-2">
        {title}
        <span className="text-xs font-normal text-fg-3 tabular">{n}</span>
      </h4>
      {n ? (
        <>
          <ul className="space-y-0.5">{rows}</ul>
          {n > layerLimit && (
            <button onClick={() => setAll(!all)} className="mt-1 text-xs text-fg-3 hover:text-fg-2">
              {all ? t.brief.fewer : t.brief.more(n - layerLimit)}
            </button>
          )}
        </>
      ) : (
        <p className="text-[13px] text-fg-3">{empty}</p>
      )}
    </section>
  )
}

function Row({ children, onClick }: { children: ReactNode; onClick?: () => void }) {
  const cls = 'flex min-h-8 min-w-0 items-center gap-2 rounded-lg px-2 text-[13px]'
  return (
    <li>
      {onClick ? (
        <button onClick={onClick} className={cx(cls, 'w-full text-left hover:bg-hover/60')}>
          {children}
        </button>
      ) : (
        <div className={cls}>{children}</div>
      )}
    </li>
  )
}

function ItemRow({ x, now }: { x: Item; now: number }) {
  const [full, setFull] = useState(false)
  return (
    <div className="space-y-1 rounded-xl bg-surface px-3.5 py-2.5 shadow-xs">
      <div className="flex min-w-0 items-center gap-2">
        <RoleTag role={x.role} presence={x.who ? presence(x.who, x.role === 'aide') || undefined : undefined} />
        {x.label && <span className="shrink-0 text-xs text-fg-3">{x.label}</span>}
        <span className="min-w-0 flex-1 truncate text-sm font-medium text-fg">
          <Linkify text={x.head} />
        </span>
        <span className="shrink-0 text-xs text-fg-3 tabular">{relTime(x.at, now)}</span>
      </div>
      {x.look.length > 0 && (
        <ul className="space-y-0.5 pl-1 text-[13px] leading-relaxed text-fg-2">
          {x.look.map((l, i) => (
            <li key={i} className="flex gap-1.5">
              <span className="text-fg-3">·</span>
              <span className="min-w-0">
                <Linkify text={l} />
              </span>
            </li>
          ))}
        </ul>
      )}
      {x.text && x.text !== x.head && (
        <p className={cx('text-[13px] leading-relaxed text-fg-3', !full && 'line-clamp-2')}>
          <Linkify text={x.text} />
        </p>
      )}
      {x.text && x.text !== x.head && x.text.length > 90 && (
        <button onClick={() => setFull(!full)} className="text-xs text-fg-3 hover:text-fg-2">
          {full ? t.brief.textLess : t.brief.textMore}
        </button>
      )}
    </div>
  )
}

function Panel({ items, asks, reports, now, close, active }: { items: Item[]; asks: Ask[]; reports: MorningReport[]; now: number; close: () => void; active: boolean }) {
  const openAsk = useOpenAsk()
  const openRef = useOpenRef()
  const openCode = (code: string) => {
    close()
    openRef(code)
  }
  const projects = useProjects()
  const reqs = useRequirements()
  const away = useAway()
  const showReport = active || !!away.data?.last
  const rep = useAwayReport(showReport)
  const proxied = reports.flatMap((r) => r.proxied ?? [])
  const releases = reports.flatMap((r) => r.releases ?? [])
  const done = reports.flatMap((r) => r.done ?? [])
  const tells = reports.reduce((a, r) => a + (r.tell_count ?? 0), 0)
  const pname = (id?: string | null) => projects.data?.projects.find((p) => p.id === id)?.name ?? ''
  const going = (reqs.data ?? []).filter((r) => r.status === 'in_progress')
  const yours = (rep.data?.yours ?? []).slice(-3)
  const ordered = [...items].sort((a, b) => Number(!!b.report) - Number(!!a.report) || (a.role === 'pm' ? -1 : 1) - (b.role === 'pm' ? -1 : 1))
  return (
    <div className="space-y-4">
      {ordered.length > 0 && (
        <div className="space-y-2">
          {ordered.map((x) => (
            <ItemRow key={x.id} x={x} now={now} />
          ))}
        </div>
      )}
      <div className="grid grid-cols-1 gap-x-8 gap-y-5 lg:grid-cols-2">
        <Layer title={t.brief.layers.look} n={asks.length} empty={t.brief.empty.look}>
          {asks.map((a) => (
            <Row
              key={a.key}
              onClick={() => {
                close()
                openAsk(a.key)
              }}
            >
              {a.code && <span className="shrink-0 font-mono text-xs text-fg-3">{a.code}</span>}
              <span className="min-w-0 flex-1 truncate text-fg">{a.title}</span>
              <span className="shrink-0 text-xs text-fg-3 tabular max-sm:hidden">{relTime(a.at, now)}</span>
            </Row>
          ))}
        </Layer>
        <Layer title={t.brief.layers.decided} n={proxied.length + releases.length} empty={t.brief.empty.decided}>
          {[
            ...proxied.map((d) => (
              <li key={d.id}>
                <ProxyLine d={d} onOpen={close} />
              </li>
            )),
            ...releases.map((x) => (
              <li key={x.code}>
                <ReleaseLine x={x} onOpen={close} />
              </li>
            )),
          ]}
        </Layer>
        <Layer title={t.brief.layers.done} n={done.length} empty={t.brief.empty.done}>
          {done.map((x) => (
            <Row key={x.requirement_code} onClick={() => openCode(x.requirement_code)}>
              <RefChip code={x.requirement_code} status={x.status} />
              <span className="min-w-0 flex-1 truncate text-fg">{x.requirement_title}</span>
              <span className="shrink-0 text-xs text-fg-3 tabular">{relTime(x.at, now)}</span>
            </Row>
          ))}
        </Layer>
        <Layer title={t.brief.layers.going} n={going.length} empty={t.brief.empty.going}>
          {going.map((r) => (
            <Row key={r.code} onClick={() => openCode(r.code)}>
              <RefChip code={r.code} status={r.status} />
              <span className="min-w-0 flex-1 truncate text-fg">{r.title}</span>
              <span className="shrink-0 text-xs text-fg-3 max-sm:hidden">{pname(r.project_id)}</span>
            </Row>
          ))}
        </Layer>
      </div>
      {(tells > 0 || yours.length > 0) && (
        <div className="space-y-1 border-t border-line pt-3 text-xs text-fg-3">
          {tells > 0 && <p>{t.brief.tells(tells)}</p>}
          {yours.length > 0 && (
            <>
              <p>{t.brief.yours(rep.data?.counts.user ?? yours.length)}</p>
              <ul className="space-y-0.5 text-fg-2">
                {yours.map((e, i) => (
                  <li key={i} className="flex min-w-0 gap-2">
                    <span className="shrink-0">{t.brief.yoursItem(e.device ?? '')}</span>
                    <span className="min-w-0 truncate">{e.title || e.ref}</span>
                  </li>
                ))}
              </ul>
            </>
          )}
        </div>
      )}
    </div>
  )
}

// ---------- 简报条 ----------

export function BriefStrip({ sessions, now, zoom, asks }: { sessions: Session[]; now: number; zoom: Zoom; asks: Ask[] }) {
  const morning = useMorning()
  const projects = useProjects()
  const connected = useConnected()
  const av = useAwayView(now)
  const e = useEndAway()
  const [seen, setSeen] = usePref<string>('ks.away.seen', '')
  const [open, setOpen] = useState(false)
  const [starting, setStarting] = useState(false)
  const [idx, setIdx] = useState(0)
  const [paused, setPaused] = useState(false)
  const touchTimer = useRef<number | undefined>(undefined)

  const find = (k?: string) => (k ? sessions.find((x) => x.key === k) : undefined)
  const reports = morning.data?.reports ?? []
  const last = av.last
  const lastEnd = last?.ended_at ? Date.parse(last.ended_at) : 0
  const recent = !av.active && !!last && lastEnd > 0 && now - lastEnd < briefTTL
  const reportFresh = recent && seen !== last?.id
  // 离开中、或离开结束后还没看过时段报告：小结在条上；看过一次就让位给主调度简报。没有离开记录的老数据照旧按窗口算
  const shown = reports.filter((r) => {
    if (av.active) return true
    if (recent) return reportFresh
    const end = legacyEnd(r)
    return !end || now - Date.parse(end) < awayFold
  })
  const multi = new Set([...shown.filter((r) => r.note).map((r) => r.project_id), ...(projects.data?.projects ?? []).filter((p) => p.brief).map((p) => p.id)]).size > 1
  const items: Item[] = []
  if (reportFresh && last) items.push({ id: `away:${last.id}`, role: 'aide', label: '', head: t.brief.reportReady(duration(last.started_at, lastEnd)), look: [], text: '', at: last.ended_at ?? last.started_at, report: true })
  for (const p of projects.data?.projects ?? []) {
    if (!p.brief || !p.brief_at || p.hidden || now - Date.parse(p.brief_at) >= briefTTL) continue
    items.push({ id: `pm:${p.id}`, role: 'pm', who: find(p.brief_by), label: multi ? p.name : '', head: headlineOf(p.brief_headline, p.brief), look: p.brief_look ?? [], text: p.brief ?? '', at: p.brief_at })
  }
  for (const r of shown) {
    if (!r.note) continue
    items.push({ id: `aide:${r.project_id}`, role: 'aide', who: find(r.note.by ?? r.grant?.session_key), label: multi ? r.project_name : '', head: headlineOf(r.note.headline, r.note.text), look: r.note.look ?? [], text: r.note.text, at: r.note.at })
  }
  const n = items.length
  const cur = n ? items[idx % n] : undefined

  useEffect(() => {
    if (n < 2 || paused || open) return
    const id = window.setInterval(() => setIdx((i) => i + 1), rotateMs)
    return () => window.clearInterval(id)
  }, [n, paused, open])
  useEffect(() => {
    if (!open) return
    const onKey = (ev: KeyboardEvent) => ev.key === 'Escape' && close()
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open])
  useEffect(() => () => window.clearTimeout(touchTimer.current), [])

  const close = () => {
    setOpen(false)
    if (reportFresh && last) setSeen(last.id) // 时段报告看过一次就让位给主调度简报
  }
  const hold = {
    onPointerEnter: (ev: React.PointerEvent) => ev.pointerType === 'mouse' && setPaused(true),
    onPointerLeave: (ev: React.PointerEvent) => ev.pointerType === 'mouse' && setPaused(false),
    onTouchStart: () => {
      window.clearTimeout(touchTimer.current)
      setPaused(true)
    },
    onTouchEnd: () => {
      touchTimer.current = window.setTimeout(() => setPaused(false), 4000)
    },
  }
  const away = av.active
  const capsule = (
    <button
      onClick={() => setOpen(!open)}
      title={t.brief.capsuleTip}
      className={cx('relative shrink-0 rounded-full px-2.5 py-0.5 text-[12px] leading-[18px] font-semibold tabular transition-colors', asks.length ? 'bg-seal text-seal-fg' : 'bg-sunken text-fg-3')}
    >
      {t.brief.capsule(asks.length)}
    </button>
  )

  return (
    <div className="relative min-w-0">
      <div className={cx('relative flex h-11 w-full min-w-0 items-center gap-2 overflow-hidden rounded-xl pr-2 pl-3.5 shadow-card', away ? 'border border-accent/30 bg-accent-soft/60' : 'bg-surface')} {...hold}>
        {!away && <Wind />}
        {!connected && (
          <span className="relative inline-flex shrink-0 items-center gap-1.5 rounded-full bg-danger-soft px-2 py-0.5 text-[11.5px] font-medium text-danger">
            <WifiOff className="size-3.5" />
            {t.desk.offline}
          </span>
        )}
        <button onClick={() => (cur || away ? setOpen(!open) : undefined)} aria-expanded={cur || away ? open : undefined} className={cx('relative flex h-full min-w-0 flex-1 items-center gap-2.5 text-left', (cur || away) && 'cursor-pointer')}>
          {away ? (
            <>
              <Plane className="size-4 shrink-0 text-accent" />
              <span className="shrink-0 text-[13px] font-semibold text-fg">{t.away.active}</span>
              <span className="min-w-0 flex-1 truncate text-[13px] text-fg-2">
                {[t.away.elapsed(av.elapsed), av.over ? t.away.over : av.active && av.active.expect_minutes ? t.away.expectShort(av.active.expect_minutes) : '', t.away.handled(av.status?.handled ?? 0), t.away.pushed(av.status?.pushed ?? 0)]
                  .filter(Boolean)
                  .join(t.common.sep)}
              </span>
            </>
          ) : cur ? (
            <>
              <AnimatePresence mode="wait" initial={false}>
                <motion.span key={cur.id} initial={{ opacity: 0, y: 6 }} animate={{ opacity: 1, y: 0 }} exit={{ opacity: 0, y: -6 }} transition={{ duration: 0.18 }} className="flex min-w-0 flex-1 items-center gap-2.5">
                  <RoleTag role={cur.role} presence={cur.who ? presence(cur.who, cur.role === 'aide') || undefined : undefined} className="shrink-0 bg-surface" />
                  {cur.label && <span className="shrink-0 text-xs text-fg-3 max-sm:hidden">{cur.label}</span>}
                  <span className="min-w-0 flex-1 truncate text-[13px] text-fg">
                    <Linkify text={cur.head} />
                  </span>
                </motion.span>
              </AnimatePresence>
              {n > 1 && <span className="shrink-0 text-[11.5px] text-fg-3 tabular max-sm:hidden">{t.brief.pageOf((idx % n) + 1, n)}</span>}
            </>
          ) : (
            connected &&
            !morning.isPending &&
            !projects.isPending && <span className="min-w-0 flex-1 truncate text-[12.5px] text-fg-3">{t.desk.noBrief}</span>
          )}
        </button>
        <TokBrief zoom={zoom} className="relative shrink-0 text-[11.5px] text-fg-3 max-xl:hidden" />
        {capsule}
        {away ? (
          <Button size="sm" className="relative shrink-0" disabled={e.busy} onClick={e.end}>
            {t.away.end}
          </Button>
        ) : (
          <Button size="sm" variant="ghost" className="relative shrink-0" onClick={() => setStarting(true)} title={t.away.startTip} aria-label={t.away.start}>
            <Plane className="size-4" />
            <span className="max-md:hidden">{t.away.start}</span>
          </Button>
        )}
        <button onClick={() => setOpen(!open)} aria-label={t.desk.briefOpen} className="relative -mr-1 flex size-7 shrink-0 items-center justify-center rounded-lg text-fg-2 hover:bg-hover/60">
          <ChevronDown className={cx('size-4 transition-transform', open && 'rotate-180')} />
        </button>
      </div>
      {open && (
        <>
          <div className="fixed inset-0 z-30" onClick={close} />
          <div className="desk-scroll absolute inset-x-0 top-full z-40 mt-2 max-h-[70vh] overflow-y-auto rounded-2xl border border-line bg-bg p-3 shadow-pop">
            <Panel items={items} asks={asks} reports={reports} now={now} close={close} active={!!away} />
          </div>
        </>
      )}
      <AwayStart open={starting} onClose={() => setStarting(false)} />
    </div>
  )
}
