import { useEffect, useMemo, useRef, useState } from 'react'
import { Link, useSearchParams } from 'react-router'
import { motion } from 'motion/react'
import { ChevronRight, CircleCheck, History, MonitorSmartphone, Moon, Search } from 'lucide-react'
import type { Session } from '../api/client'
import { useConnected, useSettings } from '../api/queries'
import { t } from '../i18n'
import { cx, loadPref, relTime, savePref, useNow, usePref } from '../lib/util'
import { Button, Card, Empty, ErrorState, Input, Kbd, Skeleton, Tip } from '../ui/base'
import { ShapeSkeleton, useShape, type Shape } from '../ui/shape'
import { displayState } from '../ui/marks'
import { ProjGlyph } from '../ui/refmark'
import { useWorkbench } from '../app/inbox'
import { RecapPanel } from '../app/Recap'
import { sessionTitle } from '../app/SessionCard'
import { SessionRow } from '../app/thread'
import { AskItem, askLine, useAsks, useOpenAsk, useStaying, type Ask } from '../app/asks'
import { BriefStrip } from '../app/brief'
import { ArrangeStrip } from '../app/arrange'
import { HandledLog, PlanChips, PlansRail, RecentRail, TeamRail, useHandled } from '../app/desk'
import { EndedLane, Lane, LaneAxis, laneTone, setLaneZoom, setOffline, ZoomSwitch, type LaneMode, type Tone } from '../app/lanes'
import { ensureZoom, laneStore, prefetchLanes, useLaneHolds, useLanePrices, useLaneSnap } from '../app/laneFeed'
import { coarse, makeAxis, ZOOMS, type SessionMeta, type Zoom } from '../lib/lanedata'
import { serverNow, usePlans } from '../api/queries'
import { useFillHeight } from '../app/pool'
import { OrphanProcs } from '../app/orphans'
import { Collapse, Item, List, useSettled } from '../ui/motion'
import { SpotStyle, useSpotlight } from '../app/spotlight'

// 工作台（R-170，D-228 原型；R-170 返工按真实运行截图改）：一屏驾驶舱，整页不滚动，各栏在区域内滚动。
//   左「要你处理」：只放固定高度的条目（R-177），点开右侧的审批抽屉处理，底部「刚处理」；没有待办时放「一切正常」概况，栏宽不变（R-211）。
//   中「会话」泳道（app/lanes.tsx）：按内容给高度、不拉伸，下面接着最近结束的会话；多个项目同时有活跃会话时按项目分组；
//   右「风向」：进行中的计划、最近动态（一条竖线）、队伍固定在底部。
// 按容器宽度排版（R-133）；不做等比放大，字号与留白和其它页面同一比例；超宽屏多出来的宽度按内容分给各列。
// 宽 ≥ 1180 三栏；960～1180 三栏但右栏窄；640～960 右栏收成顶部计划胶囊；更窄（手机）分「要你处理 / 在跑 / 风向」页签，整页纵向滚动。
// 三栏宽度只由容器宽度决定，不随内容变（R-211）；加载时各栏按上次的样子画骨架（ui/shape.tsx），加载完不跳。

const match = (s: Session | undefined, k: string) => !k || (!!s && [sessionTitle(s), s.project_name, s.branch, s.cwd, s.last_prompt, s.summary, s.device_name, s.model].some((v) => v?.toLowerCase().includes(k)))

// 分组键是项目 ID 时才有项目页（没有识别出项目的会话按目录分组）
const projectHref = (key: string) => (key && !key.startsWith('cwd:') && key !== 'none' ? `/projects/${key}` : undefined)

// ---------- 数据 ----------

type LaneRow = { s: Session; pk: string; name: string; tone: Tone; you?: string }
type Group = { pk: string; name: string; rows: LaneRow[]; done: SessionMeta[] }

const toneRank: Record<Tone, number> = { you: 0, run: 1, bg: 2, park: 3, idle: 4, sub: 9 }
const endedTTL = 24 * 3600e3 // 泳道下面自动列出这么久之内结束的会话
const endedMax = 6
const endedLaneMax = 6 // 长档位里每组最多画几条已结束的泳道（R-205）

// activeEnded：近 1 天 / 1 周档里这段时间干过活的会话（键，逗号分隔），其中不在跑的画成「已结束」泳道（R-205）
function activeEnded(zoom: Zoom) {
  if (!coarse(zoom)) return ''
  const ax = makeAxis(zoom, 720, serverNow())
  const out: string[] = []
  for (const m of laneStore.sessions.values()) if (!m.background && laneStore.active(m.k, ax)) out.push(m.k)
  return out.sort().join(',')
}

const matchMeta = (m: SessionMeta, k: string) => !k || [m.title, m.project_name, m.cwd].some((v) => v?.toLowerCase().includes(k))

function useDesk(wb: ReturnType<typeof useWorkbench>, asks: Ask[], k: string, now: number, doneSig: string) {
  const minute = Math.floor(now / 60000)
  return useMemo(() => {
    const lanes: LaneRow[] = []
    const ended: Session[] = []
    // 每个会话在「要你处理」里的条目（已按先后排好）：泳道「等你」那一行写第一条明确记录本身（R-239）
    const mine = new Map<string, Ask[]>()
    for (const a of asks) if (a.s) mine.set(a.s.key, [...(mine.get(a.s.key) ?? []), a])
    const youOf = (k: string) => {
      const list = mine.get(k)
      return list?.length ? askLine(list[0]) + (list.length > 1 ? t.desk.lane.more(list.length - 1) : '') : undefined
    }
    const seen = new Set<string>()
    const place = (s: Session, pk: string, name: string) => {
      if (seen.has(s.key) || !match(s, k)) return
      seen.add(s.key)
      if (s.state === 'ended' || displayState(s) === 'done') ended.push(s)
      else lanes.push({ s, pk, name, tone: laneTone(s, mine.has(s.key)), you: youOf(s.key) })
    }
    for (const g of wb.projects) {
      for (const th of g.threads) if (th.s) place(th.s, g.key, g.name)
      for (const s of g.active) place(s, g.key, g.name)
      for (const s of g.ended) place(s, g.key, g.name)
    }
    // 泳道按状态排：等你 → 在跑 → 后台 → 待命 → 空闲；同状态 PM 在前，再按最近活跃
    lanes.sort((a, b) => toneRank[a.tone] - toneRank[b.tone] || Number(b.s.role === 'pm') - Number(a.s.role === 'pm') || b.s.last_active_at.localeCompare(a.s.last_active_at))
    // 多个项目同时有活跃会话时按项目分组（顾问意见）：组按最急的泳道排，组内仍按状态排
    const groups = new Map<string, Group>()
    for (const r of lanes) {
      let g = groups.get(r.pk)
      if (!g) groups.set(r.pk, (g = { pk: r.pk, name: r.name, rows: [], done: [] }))
      if (!g.name && r.name) g.name = r.name
      g.rows.push(r)
    }
    // 长档位：这段时间干过活、已不在跑的会话也画成泳道，排在组里活跃泳道后面，每组最多 6 条；其余照旧在下面的「最近结束」里
    const shown = new Set<string>()
    const live = new Set(lanes.map((r) => r.s.key))
    const metas = doneSig
      .split(',')
      .map((x) => laneStore.sessions.get(x))
      .filter((m): m is SessionMeta => !!m && !live.has(m.k) && matchMeta(m, k))
      .sort((a, b) => b.last_active_at.localeCompare(a.last_active_at))
    for (const m of metas) {
      const pk = m.project_id || 'none'
      let g = groups.get(pk)
      if (!g) groups.set(pk, (g = { pk, name: m.project_name, rows: [], done: [] }))
      if (g.done.length >= endedLaneMax) continue
      g.done.push(m)
      shown.add(m.k)
    }
    const rest = ended.filter((s) => !shown.has(s.key)).sort((a, b) => b.last_active_at.localeCompare(a.last_active_at))
    const cut = minute * 60000 - endedTTL
    const recent = rest.filter((s) => Date.parse(s.last_active_at) > cut).slice(0, endedMax)
    return { lanes, ended: rest, recent, older: rest.length - recent.length, groups: [...groups.values()], doneLanes: shown.size }
  }, [wb.projects, asks, k, minute, doneSig])
}

// ---------- 宽度与缩放 ----------

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

// ---------- 版面 ----------

// 宽版泳道三列的上限（标题 19rem + 时间线 34rem + 动作 44rem）加上滚动区的内边距，见 app/lanes.tsx 的 cols.full
const laneMax = (19 + 34 + 44) * 16 + 12

function PaneHead({ title, n, children }: { title: string; n?: React.ReactNode; children?: React.ReactNode }) {
  return (
    <header data-skel-skip className="flex h-8 min-w-0 shrink-0 items-center gap-2 px-0.5">
      {title && <h2 className="shrink-0 font-display text-[16px] font-bold tracking-wide text-fg">{title}</h2>}
      {n !== undefined && n !== null && <span className="min-w-0 truncate text-[12.5px] text-fg-3 tabular">{n}</span>}
      {children}
    </header>
  )
}

// AllClear：没有待办时放一张精简的「一切正常」概况（R-170 返工），撑满本栏宽度、栏宽不变（R-211）。
function AllClear({ run, park, now }: { run: number; park: number; now: number }) {
  const handled = useHandled(now, 1000).filter((x) => new Date(x.at).toDateString() === new Date(now).toDateString()).length
  return (
    <section className="shrink-0 rounded-[14px] bg-surface px-4 py-4 shadow-card">
      <div className="flex items-center gap-2.5">
        <span className="flex size-9 shrink-0 items-center justify-center rounded-xl bg-run-soft text-run">
          <CircleCheck className="size-5" />
        </span>
        <div className="min-w-0">
          <div className="font-display text-[17px] font-bold tracking-wide text-fg">{t.desk.allClear}</div>
          <div className="truncate text-[12.5px] text-fg-3">{t.desk.allClearSub}</div>
        </div>
      </div>
      <ul className="mt-3.5 space-y-1.5 border-t border-dashed border-line pt-3 text-[13px] text-fg-2 tabular">
        <li className="flex items-center gap-2">
          <i className="desk-lamp" style={{ ['--lamp' as string]: run ? 'var(--run)' : 'var(--idle)' }} />
          {t.desk.clearRun(run)}
        </li>
        <li className="flex items-center gap-2">
          <i className="desk-lamp" style={{ ['--lamp' as string]: park ? 'var(--accent)' : 'var(--idle)' }} />
          {t.desk.clearPark(park)}
        </li>
        <li className="flex items-center gap-2">
          <CircleCheck className="size-3.5 text-fg-3" />
          {t.desk.clearHandled(handled)}
        </li>
      </ul>
    </section>
  )
}

// NoRun：没有会话在跑时，泳道区顶部一句话（最后一次动作在什么时候）；完全没有会话时是整块空状态。
function NoRun({ sessions, now, full }: { sessions: Session[]; now: number; full?: boolean }) {
  const last = sessions.reduce((m, s) => (s.last_tool_at && s.last_tool_at > m ? s.last_tool_at : m), '')
  if (full)
    return (
      <div className="flex shrink-0 flex-col items-center gap-2 rounded-[14px] px-6 py-10 text-center ring-1 ring-line ring-inset">
        <span className="flex size-11 items-center justify-center rounded-2xl bg-sunken text-fg-3">
          <Moon className="size-5" />
        </span>
        <div className="font-display text-[17px] font-bold text-fg-2">{t.desk.noRun}</div>
        <p className="max-w-md text-[13px] leading-relaxed text-fg-3">
          {last ? `${t.desk.noRunSub(relTime(last, now))}。` : ''}
          {t.desk.noRunHint}
        </p>
      </div>
    )
  return (
    <div className="flex shrink-0 items-center gap-2 px-1 text-[12.5px] text-fg-3">
      <Moon className="size-3.5 shrink-0" />
      <span className="font-medium text-fg-2">{t.desk.noRun}</span>
      <span className="truncate">{t.desk.noRunSub(last ? relTime(last, now) : '')}</span>
    </div>
  )
}

// Held：加载中先占住上次那段文字的宽度（只画微光条，不显示旧数字），加载完换成真实文字，旁边的东西不跳。
function Held({ k, text, loading }: { k: string; text: string; loading: boolean }) {
  const [last] = useState(() => loadPref(k, text))
  useEffect(() => {
    if (!loading) savePref(k, text)
  }, [k, text, loading])
  if (!loading) return <>{text}</>
  return (
    <span aria-hidden className="relative inline-block align-middle">
      <span className="invisible">{last || text}</span>
      <Skeleton className="absolute inset-x-0 top-1/2 h-3 -translate-y-1/2" />
    </span>
  )
}

// 没记过形状时的默认骨架：两条待办；时间轴 + 一条在跑 + 一条待命；计划 + 最近动态 + 队伍
const askFallback: Shape = [
  { h: 86, card: true },
  { h: 86, card: true },
]
const laneFallback: Shape = [{ h: 20 }, { h: 110, card: true }, { h: 74, card: true }]
const sideFallback: Shape = [{ h: 92, card: true }, { h: 0, card: true, fill: true }, { h: 74, card: true }]

type Tab = 'ask' | 'run' | 'side'

export default function WorkbenchPage() {
  const now = useNow()
  const wb = useWorkbench()
  const settings = useSettings()
  const connected = useConnected()
  const asks = useStaying(useAsks(wb)) // 撤销期一结束就在本地离开（R-230）
  const todo = asks.filter((a) => a.counted)
  const openAsk = useOpenAsk()
  const [params] = useSearchParams()
  const openKey = params.get('a')
  const openSession = params.get('s')
  const [showOlder, setShowOlder] = usePref('ks.home.ended', false)
  const [tab, setTab] = usePref<Tab>('ks.home.tab', 'ask')
  const [q, setQ] = useState('')
  const [recap, setRecap] = useState(false)
  const k = q.trim().toLowerCase()
  const recapOn = !!settings.data?.ai?.recap
  const [zoomPref, setZoom] = usePref<Zoom>('ks.home.zoom', 'h1')
  const zoom: Zoom = ZOOMS.includes(zoomPref) ? zoomPref : 'h1'
  const doneSig = useLaneSnap(() => activeEnded(zoom))
  useLanePrices() // 设置里填全了的模型价格（R-206，选填）
  useLaneHolds() // 设置里手填的缓存保留时长（R-207，选填）
  const desk = useDesk(wb, asks, k, now, doneSig)
  const search = useRef<HTMLInputElement>(null)
  const [el, setEl, w] = useBox()
  const phone = w > 0 && w < 640
  const fillH = useFillHeight(el, !phone && w > 0)
  const W = w
  const rail = W >= 960
  const roomy = W >= 1800
  const plansQ = usePlans()
  // 泳道的子任务、时间线来自另一份数据（laneFeed），第一次到齐前泳道区也算加载中，否则泳道会在加载完后再长高
  const [laneReady, setLaneReady] = useState(false)
  const laneLoading = wb.loading || !laneReady
  const sideLoading = wb.loading || plansQ.isPending

  // 左右栏宽度（rem 按 16px 算），中栏拿剩下的；泳道版式按中栏宽度定。
  // 只看容器宽度，不看「要你处理」有没有内容（R-211：处理完只剩「一切正常」时栏宽不变）
  let leftW = (roomy ? 26 : W >= 1180 ? 23 : rail ? 20 : 19) * 16
  let rightW = rail ? (roomy ? 23 : W >= 1180 ? 21 : 16.5) * 16 : 0
  const gaps = roomy ? 40 : W >= 1180 ? 32 : 28
  // 超宽屏（带鱼屏）：中栏比泳道三列的上限还宽时封顶（R-187），不让动作列拉出大片空白；多出来的宽度按 6:4 先分给
  // 「要你处理」（最多 48rem）和「风向」（最多 36rem），一边到顶就给另一边，两边都到顶后剩下的才还给中栏。1920 及以下不触发。
  if (rail) {
    const extra = W - leftW - rightW - gaps - laneMax
    if (extra > 0) {
      const lCap = 48 * 16
      const rCap = 36 * 16
      let l = Math.min(lCap - leftW, extra * 0.6)
      let r = Math.min(rCap - rightW, extra - l)
      l = Math.min(lCap - leftW, extra - r)
      leftW += l
      rightW += r
    }
  }
  const centerW = W - leftW - rightW - gaps
  const mode: LaneMode = phone ? 'phone' : centerW >= 720 ? 'full' : centerW >= 440 ? 'mid' : 'narrow'

  // 断线：时间线停在最后一帧并变灰
  useEffect(() => setOffline(!connected), [connected])

  // 分度值（R-205）：所有泳道一起切；先补这一档要的数据，空闲时把其它档预取好，之后换档不再等网络
  useEffect(() => {
    setLaneZoom(zoom)
    void ensureZoom(zoom)
      .catch(() => {})
      .then(() => {
        setLaneReady(true)
        prefetchLanes()
      })
  }, [zoom])
  // 泳道数据迟迟不来（断线等）时不一直挂骨架
  useEffect(() => {
    const id = window.setTimeout(() => setLaneReady(true), 4000)
    return () => window.clearTimeout(id)
  }, [])
  const pickZoom = (z: Zoom) => {
    performance.mark('lane-zoom')
    setZoom(z)
    // 换档到画完的耗时（R-205 验收：近 1 周档 < 200ms），可在性能面板 / 自动化里读 lane-zoom 这条测量
    requestAnimationFrame(() => requestAnimationFrame(() => performance.measure('lane-zoom', 'lane-zoom')))
  }

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

  // 首次加载（骨架换成内容）这段时间里的出现不算进场，不播动画；之后各栏的增、删、换位置才用弹簧过渡（R-216）
  const still = !useSettled(!wb.loading && laneReady)

  // 聚光（R-215）：悬停 / Tab 聚焦卡片或泳道时，其余淡出；触屏点开审批抽屉 / 会话抽屉期间保持这组关联
  const { spot, handlers: spotHandlers } = useSpotlight((openKey ? asks.find((a) => a.key === openKey)?.s?.key : openSession) ?? null)

  // 各栏的骨架按上次加载完的样子画（R-211）；搜索过滤时的样子不记
  const lay = phone ? 'p' : mode
  const [askBox, setAskBox] = useState<HTMLDivElement | null>(null)
  const [laneBox, setLaneBox] = useState<HTMLDivElement | null>(null)
  const [sideBox, setSideBox] = useState<HTMLElement | null>(null)
  const askShape = useShape(`ks.skel.ask.${phone ? 'p' : 'd'}`, askBox, !wb.loading && W > 0, askFallback)
  const laneShape = useShape(`ks.skel.lane.${lay}`, laneBox, !laneLoading && W > 0, laneFallback, !k)
  const sideShape = useShape('ks.skel.side', sideBox, !sideLoading && W > 0 && !phone, sideFallback)
  const recapLast = loadPref('ks.skel.recap', false)
  useEffect(() => {
    if (settings.data) savePref('ks.skel.recap', recapOn)
  }, [settings.data, recapOn])
  const showRecap = settings.data ? recapOn : recapLast

  if (wb.error) return <ErrorState error={wb.error} onRetry={wb.refetch} />

  const run = desk.lanes.filter((x) => x.tone === 'run' || x.tone === 'bg').length
  const park = desk.lanes.filter((x) => x.tone === 'park').length
  const quiet = desk.lanes.filter((x) => x.tone === 'idle').length
  const multi = desk.groups.length > 1
  const multiAsk = new Set(asks.map((a) => a.projectId)).size > 1

  // ---- 左：要你处理 ----
  const askPane = (
    <section className={cx('flex min-h-0 min-w-0 flex-col gap-2.5', phone && tab !== 'ask' && 'hidden')}>
      {!phone && (
        <PaneHead title={t.desk.paneAsk} n={wb.loading || asks.length ? <Held k="ks.skel.askN" text={String(todo.length)} loading={wb.loading} /> : undefined} />
      )}
      <motion.div layoutScroll ref={setAskBox} className={cx('desk-scroll flex min-h-0 flex-col gap-2', !phone && 'flex-1 overflow-y-auto overscroll-contain p-0.5 pr-1.5')}>
        {wb.loading ? (
          <ShapeSkeleton shape={askShape} />
        ) : asks.length ? (
          <List space={8}>
            {asks.map((a) => (
              <Item key={a.key} space={8} layout fluid still={still}>
                <AskItem
                  a={a}
                  now={now}
                  onOpen={openAsk}
                  active={openKey === a.key}
                  showProject={multiAsk}
                />
              </Item>
            ))}
          </List>
        ) : (
          <AllClear run={run} park={park} now={now} />
        )}
      </motion.div>
      <HandledLog now={now} className="shrink-0" />
    </section>
  )

  // ---- 中：会话泳道 ----
  // 泳道、分组标题、已结束的泳道排成一维列表，增、删、换位置都用同一套弹簧（R-216）
  const laneEls = desk.groups.flatMap((g) => {
    const href = projectHref(g.pk)
    const gr = g.rows.filter((x) => x.tone === 'run' || x.tone === 'bg').length
    const gp = g.rows.filter((x) => x.tone === 'park').length
    return [
      multi && (
        <Item key={`g:${g.pk}`} space={8} layout fluid still={still}>
          <div className="flex shrink-0 items-center gap-2 px-1 pt-1 text-[12px] text-fg-3">
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
        </Item>
      ),
      ...g.rows.map((r) => (
        <Item key={r.s.key} space={8} layout fluid still={still}>
          <Lane s={r.s} tone={r.tone} now={now} mode={mode} zoom={zoom} still={still} you={r.you} />
        </Item>
      )),
      ...g.done.map((m) => (
        <Item key={`e:${m.k}`} space={8} layout fluid still={still}>
          <EndedLane m={m} now={now} mode={mode} zoom={zoom} />
        </Item>
      )),
    ]
  })
  const endedEls = (desk.recent.length > 0 || (showOlder && desk.older > 0)) && (
    <div className="shrink-0">
      <div className="flex items-center gap-2 px-1 pt-2 pb-1 text-[12px] text-fg-3">
        <span className="font-medium text-fg-2">{t.desk.recentEnded}</span>
        <span aria-hidden className="h-0 min-w-4 flex-1 border-t border-dashed border-line" />
      </div>
      <div className="@container rounded-[14px] p-1 ring-1 ring-line ring-inset">
        <List>
          {(showOlder ? desk.ended : desk.recent).map((s) => (
            <Item key={s.key} layout fluid still={still}>
              <SessionRow s={s} now={now} showProject={multi} />
            </Item>
          ))}
        </List>
      </div>
    </div>
  )
  const lanePane = (
    <section className={cx('flex min-h-0 min-w-0 flex-col gap-2.5', phone && tab !== 'run' && 'hidden')}>
      <PaneHead title={phone ? '' : t.desk.paneLanes} n={<Held k={`ks.skel.laneN.${mode === 'full' ? 'f' : 's'}`} text={mode === 'full' ? t.desk.laneSub(run, park, quiet) : t.desk.laneSubShort(run, desk.lanes.length)} loading={laneLoading} />}>
        <ZoomSwitch zoom={zoom} onChange={pickZoom} short={mode !== 'full'} />
        <span className="flex-1" />
        <div className={cx('relative min-w-[5.5rem] shrink', mode === 'full' ? 'w-56' : 'w-36')}>
          <Search className="pointer-events-none absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2 text-fg-3" />
          <Input ref={search} value={q} onChange={(e) => setQ(e.target.value)} onKeyDown={(e) => e.key === 'Escape' && (setQ(''), e.currentTarget.blur())} placeholder={t.home.search} className="h-8 pr-8 pl-8 text-[13px]" />
          <span className={cx('pointer-events-none absolute top-1/2 right-2 -translate-y-1/2', mode !== 'full' && 'hidden')}>
            <Kbd>/</Kbd>
          </span>
        </div>
      </PaneHead>
      <motion.div layoutScroll ref={setLaneBox} className={cx('desk-scroll flex min-h-0 flex-col gap-2', !phone && 'flex-1 overflow-y-auto overscroll-contain p-0.5 pr-1.5')}>
        {laneLoading ? (
          <ShapeSkeleton shape={laneShape} />
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
        ) : desk.lanes.length || desk.doneLanes ? (
          <>
            <Collapse show={run === 0 && !k} fluid still={still}>
              <NoRun sessions={wb.sessions} now={now} />
            </Collapse>
            {!phone && <LaneAxis zoom={zoom} mode={mode} now={now} />}
            <List space={8}>{laneEls}</List>
          </>
        ) : k ? (
          <p className="px-4 py-10 text-center font-display text-[15px] text-fg-3">{t.home.noMatch}</p>
        ) : wb.sessions.length ? (
          <NoRun sessions={wb.sessions} now={now} full />
        ) : (
          <p className="px-4 py-10 text-center font-display text-[15px] text-fg-3">{t.desk.noLanes}</p>
        )}
        {!laneLoading && endedEls}
        {!laneLoading && <OrphanProcs now={now} />}
      </motion.div>
      {!laneLoading && desk.older > 0 && (
        <div className="flex shrink-0 items-center gap-2 px-0.5 text-xs text-fg-3">
          <button onClick={() => setShowOlder(!showOlder)} aria-expanded={showOlder} className="inline-flex shrink-0 items-center gap-1 hover:text-fg-2">
            <ChevronRight className={cx('size-3.5 transition-transform', showOlder && 'rotate-90')} />
            {t.desk.olderEnded(desk.older)}
          </button>
        </div>
      )}
    </section>
  )

  // ---- 右：风向 ----
  const sidePane = (
    <aside ref={setSideBox} className={cx('flex min-h-0 min-w-0 flex-col gap-2.5', phone && tab !== 'side' && 'hidden')}>
      {!phone && <PaneHead title={t.desk.paneWind} />}
      {sideLoading && !phone ? (
        <ShapeSkeleton shape={sideShape} />
      ) : (
        <>
          <PlansRail className={cx('shrink-0', !phone && 'desk-scroll max-h-[42%] overflow-y-auto')} />
          <RecentRail now={now} fill={!phone} className={cx(!phone ? 'min-h-[8rem] flex-1' : 'max-h-[60vh]')} />
          <TeamRail groups={wb.projects} className="shrink-0" />
        </>
      )}
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
      className="grid min-h-0 flex-1"
      // 列宽按容器宽度（R-133），不按视口：侧栏展开 / 收起时跟着变
      style={{ gridTemplateColumns: [`minmax(250px,${leftW}px)`, 'minmax(0,1fr)', rail ? `minmax(230px,${rightW}px)` : ''].filter(Boolean).join(' '), columnGap: gaps / (rail ? 2 : 1) }}
    >
      {askPane}
      {lanePane}
      {rail && sidePane}
    </div>
  )

  return (
    <div ref={setEl} style={fillH ? { height: fillH } : undefined} className="min-w-0" {...spotHandlers}>
      <SpotStyle spot={spot} />
      <div style={{ height: fillH }} className={cx('flex min-w-0 flex-col', phone ? 'gap-3' : 'gap-3.5')}>
        {/* 顶栏：标题与件数 · 简报位（背后的风，最多两行）；不放状态图例（R-187） */}
        <div className={cx('grid min-w-0 items-start gap-x-4 gap-y-2.5', phone ? 'grid-cols-1' : 'grid-cols-[auto_minmax(0,1fr)]')}>
          <div className="flex min-w-0 items-baseline gap-3 pt-2">
            <h1 className="shrink-0 font-display text-2xl leading-none font-bold tracking-wider text-fg">{t.home.title}</h1>
            <span className="truncate text-[13px] whitespace-nowrap text-fg-3 tabular">
              <Held k="ks.skel.sum" text={t.desk.sumLine(todo.length, run)} loading={laneLoading} />
            </span>
            {showRecap && (
              <Tip content={t.home.recap}>
                <Button size="icon-sm" variant="ghost" aria-label={t.home.recap} disabled={!recapOn} onClick={() => setRecap(true)} className="ml-auto self-center">
                  <History className="size-4" />
                </Button>
              </Tip>
            )}
          </div>
          <BriefStrip sessions={wb.sessions} now={now} zoom={zoom} asks={todo} />
          <ArrangeStrip sessions={wb.sessions} className={cx(!phone && 'col-start-2')} />
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
                  {(n > 0 || (wb.loading && v !== 'side')) && (
                    <span className="ml-1.5 text-xs text-fg-3 tabular">
                      <Held k={`ks.skel.tab.${v}`} text={String(n)} loading={wb.loading} />
                    </span>
                  )}
                </button>
              ))}
            </div>
          )}
        </div>
        <div className={cx('flex min-h-0 min-w-0 flex-1 flex-col', !connected && 'desk-offline')}>{body}</div>
        {recapOn && <RecapPanel open={recap} onClose={() => setRecap(false)} />}
      </div>
    </div>
  )
}
