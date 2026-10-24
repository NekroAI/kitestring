import { memo, useEffect, useLayoutEffect, useRef, useState, useSyncExternalStore } from 'react'
import type { Session } from '../api/client'
import { serverNow, useConnected } from '../api/queries'
import { t } from '../i18n'
import { cx, duration, relTime, shortDuration } from '../lib/util'
import { Tip } from '../ui/base'
import { Ctx } from '../ui/contextMenu'
import { displayState, RoleBadge } from '../ui/marks'
import { useToolFeed } from './liveFeed'
import { useSessionMenu } from './menus'
import { useOpenSession } from './nav'
import { sessionExtras, sessionTitle, splitTool } from './SessionCard'
import { liveTasks, type BgTask } from './tasks'
import { reducedMotion } from './verdict'

// 工作台的会话泳道（R-170，D-228 原型）：每个会话一条——左边标题、身份（D-177 描边胶囊 + 在线点）、
// 状态标（灯与状态字合一，带已运行时长）；中间近 10 分钟的时间线（每次工具调用一个刻度，右边是现在）；
// 右边当前动作。泳道按内容给高度（在跑的高、等你适中、待命 / 空闲带最后一句话和上次动作），后台任务按 R-162 作为子泳道挂在会话下面。
// 动效只在有动作时出现：灯在主会话调用工具时闪一下，动作流新进一行时滑入一次；只有「等你」的灯会呼吸。
// 时间线不画闪光点、不画常驻波纹；标签页隐藏时停画，断线时静止变灰，减少动效时每 5 秒才重画一帧。

export const WINDOW = 10 * 60 * 1000
const MAX_TICKS = 200 // 每条时间线最多画这么多个点（事件点数上限）

// ---------- 刻度数据 ----------

// 主会话的工具调用时刻：Hub 给近 10 分钟打底（ticks），之后随实时推送的 last_tool_at 追加。
const tickStore = new Map<string, number[]>()
const tickSubs = new Set<() => void>()
let lastTickAt = 0 // 全部会话里最近一次工具调用（驱动顶部的风）

function feedTicks(key: string, list: number[]) {
  const cur = tickStore.get(key) ?? []
  const floor = serverNow() - WINDOW
  let changed = false
  const next = cur.filter((x) => x >= floor)
  for (const at of list) {
    if (!at || at < floor || next.some((x) => Math.abs(x - at) < 400)) continue
    next.push(at)
    changed = true
    if (at > lastTickAt) lastTickAt = at
  }
  if (!changed && next.length === cur.length) return
  next.sort((a, b) => a - b)
  tickStore.set(key, next.slice(-MAX_TICKS))
  tickSubs.forEach((f) => f())
}

export function useFeedTicks(s: Session) {
  useEffect(() => {
    feedTicks(s.key, [...(s.ticks ?? []).map(Date.parse), ...(s.last_tool_at ? [Date.parse(s.last_tool_at)] : [])])
  }, [s.key, s.ticks, s.last_tool_at])
}

// useLastTick：这个会话最近一次工具调用的时刻（灯据此闪一下）。
function useLastTick(key: string) {
  return useSyncExternalStore(
    (f) => {
      tickSubs.add(f)
      return () => tickSubs.delete(f)
    },
    () => tickStore.get(key)?.at(-1) ?? 0,
  )
}

// activity：全部会话近 1 分钟的调用数（0～1），顶部的风据此起伏。
export function activity(now = serverNow()) {
  let n = 0
  for (const list of tickStore.values()) for (let i = list.length - 1; i >= 0 && now - list[i] < 60_000; i--) n++
  return Math.min(1, n / 40)
}
export const lastAction = () => lastTickAt
export function onTicks(f: () => void) {
  tickSubs.add(f)
  return () => tickSubs.delete(f)
}

// ---------- 时间线画布 ----------

export type Tone = 'run' | 'bg' | 'you' | 'park' | 'idle' | 'sub'
type Painter = { cv: HTMLCanvasElement; ticks: () => number[]; tone: () => Tone }
const painters = new Set<Painter>()
let loop = 0
let offline = false

function colors() {
  const css = getComputedStyle(document.documentElement)
  const v = (k: string) => css.getPropertyValue(k).trim()
  return { run: v('--run'), you: v('--reply'), park: v('--accent'), idle: v('--idle'), grid: v('--line-strong') }
}

// pixelRatio：画布每个 CSS 像素对应几个设备像素——设备像素比 × 祖先的 CSS 缩放（工作台在超宽屏上整体放大，R-170）。
export function pixelRatio(cv: HTMLCanvasElement) {
  const dpr = window.devicePixelRatio || 1
  const zoom = cv.clientWidth ? cv.getBoundingClientRect().width / cv.clientWidth : 1
  return dpr * (zoom > 0.2 && zoom < 5 ? zoom : 1)
}

function paint(p: Painter, now: number, c: ReturnType<typeof colors>) {
  const { cv } = p
  const dpr = pixelRatio(cv)
  const w = cv.clientWidth
  const h = cv.clientHeight
  if (!w || !h) return
  // DPI 变化（拖到另一块屏、缩放）时按新的像素比重设画布
  if (cv.width !== Math.round(w * dpr) || cv.height !== Math.round(h * dpr)) {
    cv.width = Math.round(w * dpr)
    cv.height = Math.round(h * dpr)
  }
  const g = cv.getContext('2d')
  if (!g) return
  g.setTransform(dpr, 0, 0, dpr, 0, 0)
  g.clearRect(0, 0, w, h)
  const tone = p.tone()
  const sub = tone === 'sub'
  const live = tone === 'run' || tone === 'bg'
  const col = offline || sub || tone === 'idle' ? c.idle : tone === 'you' ? c.you : tone === 'park' ? c.park : c.run
  const y = Math.round(h / 2) + 0.5
  // 分钟刻度
  if (!sub) {
    g.globalAlpha = 0.5
    g.fillStyle = c.grid
    for (let m = 1; m < 10; m++) g.fillRect(Math.round(w - (m / 10) * w), y - 3, 1, 6)
  }
  // 基线
  g.globalAlpha = sub ? 0.3 : live ? 0.35 : 0.45
  g.strokeStyle = col
  g.lineWidth = 1
  g.setLineDash(tone === 'idle' ? [3, 4] : [])
  g.beginPath()
  g.moveTo(0, y)
  g.lineTo(w, y)
  g.stroke()
  g.setLineDash([])
  // 事件：在跑的会话画竖条（高低错落），其它画小圆点，子泳道画灰色细刻度
  g.fillStyle = col
  for (const at of p.ticks()) {
    const age = now - at
    if (age < 0 || age > WINDOW) continue
    const x = w - (age / WINDOW) * w
    g.globalAlpha = Math.max(0.2, 1 - age / WINDOW)
    if (sub) g.fillRect(x - 0.5, y - 3.5, 1, 7)
    else if (live) {
      const amp = Math.min(h * 0.34, 5 + ((at / 97) % 9))
      g.fillRect(x - 1, y - amp, 2, amp * 2)
    } else {
      g.beginPath()
      g.arc(x, y, 2, 0, Math.PI * 2)
      g.fill()
    }
  }
  g.globalAlpha = 1
}

export function paintAll() {
  if (typeof document === 'undefined' || document.hidden || !painters.size) return
  const c = colors()
  const now = serverNow()
  painters.forEach((p) => paint(p, now, c))
}

// 时间在流逝：刻度每半秒向左挪一点（10 分钟 / 画布宽度，约每秒 2px）。断线时画最后一帧灰色后停住；减少动效时 5 秒一帧。
function schedule() {
  window.clearTimeout(loop)
  if (!painters.size || offline) return
  loop = window.setTimeout(
    () => {
      paintAll()
      schedule()
    },
    reducedMotion() ? 5000 : 500,
  )
}
if (typeof document !== 'undefined')
  document.addEventListener('visibilitychange', () => {
    if (!document.hidden) {
      paintAll()
      schedule()
    }
  })

export function setOffline(v: boolean) {
  if (offline === v) return
  offline = v
  paintAll()
  schedule()
}

function Timeline({ ticks, tone, className }: { ticks: () => number[]; tone: Tone; className?: string }) {
  const ref = useRef<HTMLCanvasElement>(null)
  const toneRef = useRef(tone)
  toneRef.current = tone
  const ticksRef = useRef(ticks)
  ticksRef.current = ticks
  useLayoutEffect(() => {
    const cv = ref.current
    if (!cv) return
    const p: Painter = { cv, ticks: () => ticksRef.current(), tone: () => toneRef.current }
    painters.add(p)
    const ro = new ResizeObserver(() => paint(p, serverNow(), colors()))
    ro.observe(cv)
    paint(p, serverNow(), colors())
    schedule()
    return () => {
      painters.delete(p)
      ro.disconnect()
    }
  }, [])
  // 状态变了（例如在跑 → 等你）立刻换色，不等下一帧
  useEffect(() => {
    if (ref.current) paintAll()
  }, [tone])
  return <canvas ref={ref} aria-hidden className={cx('block h-full w-full', className)} />
}

// 新的刻度进来时重画（不用等下一帧），时间线和灯同时动；切换明暗主题时换色。
if (typeof window !== 'undefined') {
  onTicks(() => window.requestAnimationFrame(paintAll))
  new MutationObserver(() => window.requestAnimationFrame(paintAll)).observe(document.documentElement, { attributes: true, attributeFilter: ['class'] })
}

// ---------- 状态 ----------

// laneTone：泳道的状态（决定高低、颜色和状态标）。等你：在问你 / 等授权，或有事项在「要你处理」里而它没在跑。
export function laneTone(s: Session, waiting: boolean): Tone {
  const ds = displayState(s)
  if (ds === 'running') return 'run'
  if (ds === 'background') return 'bg'
  if (ds === 'waiting_approval' || ds === 'waiting_answer' || waiting) return 'you'
  if (ds === 'parked') return 'park'
  return 'idle'
}

const pill: Record<string, string> = {
  run: 'bg-run-soft text-run',
  bg: 'bg-run-soft text-run',
  you: 'bg-reply-soft text-reply',
  park: 'bg-accent-soft text-accent',
  idle: 'text-fg-3 ring-1 ring-inset ring-line',
}
const lampColor: Record<string, string> = { run: 'var(--run)', bg: 'var(--run)', you: 'var(--reply)', park: 'var(--accent)', idle: 'transparent' }

// Lamp：状态灯。主会话每次调用工具闪一下（只闪一次，不常驻）；等你时呼吸。
function Lamp({ tone, k }: { tone: Tone; k: string }) {
  const last = useLastTick(k)
  const prev = useRef(last)
  const [n, setN] = useState(0)
  useEffect(() => {
    if (last && prev.current && last > prev.current && (tone === 'run' || tone === 'bg')) setN((x) => x + 1)
    prev.current = last
  }, [last, tone])
  return (
    <i
      key={n}
      aria-hidden
      className={cx('desk-lamp', n > 0 && 'spark', tone === 'you' && 'you', tone === 'bg' && 'opacity-70', tone === 'idle' && 'ring-[1.5px] ring-idle ring-inset')}
      style={{ ['--lamp' as string]: lampColor[tone] }}
    />
  )
}

// stateLabel：状态标上的字。在跑写已运行时长；待命 / 空闲写「待命 1 小时」「7 小时没动」。
function stateLabel(s: Session, tone: Tone, now: number) {
  const ds = displayState(s)
  if (tone === 'run' || tone === 'bg') return { word: tone === 'run' ? t.state.running : t.desk.lane.bg, el: shortDuration(s.state_since, now) }
  if (tone === 'you') return { word: ds === 'waiting_approval' ? t.state.waiting_approval : t.desk.lane.you, el: '' }
  if (tone === 'park') return { word: t.desk.lane.parked(duration(s.state_since, now)), el: '' }
  if (ds === 'lost') return { word: t.state.lost, el: '' }
  return { word: t.desk.lane.quiet(duration(s.last_active_at, now)), el: '' }
}

export function StateTag({ s, tone, now }: { s: Session; tone: Tone; now: number }) {
  const { word, el } = stateLabel(s, tone, now)
  return (
    <Tip content={t.stateSince(t.state[displayState(s)] ?? word, duration(s.state_since, now), new Date(s.state_since).toLocaleString())}>
      <span className={cx('inline-flex shrink-0 items-center gap-1.5 rounded-full py-0.5 pr-2 pl-1.5 text-[11.5px] leading-4 whitespace-nowrap tabular', pill[tone])}>
        <Lamp tone={tone} k={s.key} />
        {word}
        {el && <span className="opacity-75">{el}</span>}
      </span>
    </Tip>
  )
}

// workLine：手上的需求（编号 + 标题）；没有需求时为空。
export function workReq(s: Session) {
  return s.requirements.find((x) => x.status !== 'done' && x.status !== 'shelved')
}

// lastWords：待命 / 空闲泳道上它最后说的一句话（做完了什么、在等什么），其次是摘要、你最后说的话。
export function lastWords(s: Session) {
  const ask = s.reply_ask?.split('\n').map((x) => x.trim()).filter(Boolean).at(-1)
  return s.digest_ask || ask || s.digest_done || s.summary || s.last_prompt || ''
}

// youLine：等你的会话在问什么。
function youLine(s: Session) {
  const ds = displayState(s)
  if (ds === 'waiting_approval') return t.desk.lane.approve(s.state_detail)
  if (ds === 'waiting_answer') return splitTool(s.state_detail)[1] || t.session.asking
  const r = s.requirements.find((x) => x.status === 'review')
  return s.digest_ask || s.reply_ask?.split('\n').filter(Boolean).at(-1) || (r ? t.desk.lane.review(r.code, r.title) : '') || s.digest_done || t.desk.lane.youHint
}

// ---------- 当前动作 ----------

function Feed({ s, n }: { s: Session; n: number }) {
  const feed = useToolFeed(s).slice(-n)
  if (!feed.length) return <div className="truncate text-fg-3">{s.last_prompt}</div>
  return (
    <>
      {feed.map((a, i) => {
        const last = i === feed.length - 1
        const [name, brief] = splitTool(a.label)
        return (
          <div key={Date.parse(a.at) + a.label} className={cx('truncate', last ? 'desk-in text-fg' : 'text-fg-3')}>
            {last ? '▸ ' : '✓ '}
            {name} {brief}
          </div>
        )
      })}
    </>
  )
}

// ActLine：没有右侧动作列时，当前动作写在状态标下一行。
function ActLine({ s }: { s: Session }) {
  const feed = useToolFeed(s)
  const a = feed.at(-1)
  if (!a) return null
  const [name, brief] = splitTool(a.label)
  return (
    <div key={a.at} className="desk-in truncate font-mono text-[11.5px] leading-4 text-fg-2">
      ▸ {name} {brief}
    </div>
  )
}

// LastAct：上次动作的时间（和是什么动作），待命 / 空闲泳道用。
function LastAct({ s, now, className }: { s: Session; now: number; className?: string }) {
  const at = s.last_tool_at || s.last_active_at
  if (!at) return null
  const [name] = s.last_tool ? splitTool(s.last_tool) : ['']
  return <div className={cx('truncate text-[11.5px] text-fg-3 tabular', className)}>{t.desk.lane.lastAct(relTime(at, now), name)}</div>
}

// ---------- 泳道 ----------

export type LaneMode = 'full' | 'mid' | 'narrow' | 'phone'

// 时间线一列有上限（R-170 返工：超宽屏上不让它横跨大半屏），多出的宽度给左边的标题和右边的动作 / 最后一句话。
const cols: Record<LaneMode, string> = {
  full: 'grid-cols-[minmax(230px,19rem)_minmax(160px,34rem)_minmax(260px,1fr)]',
  mid: 'grid-cols-[minmax(220px,17rem)_minmax(0,1fr)]',
  narrow: 'grid-cols-[minmax(160px,13rem)_minmax(0,1fr)]',
  phone: 'grid-cols-1',
}

function SubLane({ task, now, mode, last }: { task: BgTask; now: number; mode: LaneMode; last: boolean }) {
  const label = task.label || task.agent_type || t.session.taskKind[task.kind]
  const [tool, brief] = task.last_action ? splitTool(task.last_action) : ['', '']
  const ticks = (task.ticks ?? []).map(Date.parse)
  const tip = [t.session.taskKind[task.kind] + '：' + label, task.agent_type && task.label && task.agent_type, t.session.taskRunning(shortDuration(task.started_at, now))].filter(Boolean).join(t.common.sep)
  return (
    <div className={cx('grid h-6 items-center text-fg-3', cols[mode], mode === 'phone' && 'h-auto py-0.5')}>
      <div className="flex min-w-0 items-center gap-1.5 pr-3 pl-6 text-[11.5px]">
        {/* 树状缩进：竖线接上一行，横线指向这一行 */}
        <span aria-hidden className="relative h-6 w-2.5 shrink-0">
          <span className={cx('absolute top-0 left-0 border-l border-line-strong', last ? 'h-1/2' : 'h-full')} />
          <span className="absolute top-1/2 left-0 w-full border-t border-line-strong" />
        </span>
        <span className="w-9 shrink-0 text-[10.5px] opacity-80">{t.session.taskKind[task.kind]}</span>
        <Tip content={tip}>
          <span className="min-w-0 flex-1 truncate">{label}</span>
        </Tip>
        {mode !== 'full' && <span className="shrink-0 text-[11px] tabular">{shortDuration(task.started_at, now)}</span>}
      </div>
      {mode !== 'phone' && (
        <div className="h-6 min-w-0 opacity-70">
          <Timeline ticks={() => ticks} tone="sub" />
        </div>
      )}
      {mode === 'full' && (
        <div className="flex h-full min-w-0 items-center justify-between gap-2.5 border-l border-line px-3.5 font-mono text-[11px]">
          <span key={task.last_action_at ?? ''} className="desk-in min-w-0 truncate">{tool && `▸ ${tool} ${brief}`}</span>
          <span className="shrink-0 font-sans tabular">{shortDuration(task.started_at, now)}</span>
        </div>
      )}
    </div>
  )
}

// laneHeight：泳道按内容给高度（R-170 返工）：在跑的高（动作流 4 行 + 子泳道），等你的适中，待命 / 空闲也有两三行信息；
// 都是固定值，不随屏幕拉伸，空出来的地方留给下面「最近结束」的会话。
export function laneHeight(tone: Tone, tasks: number, mode: LaneMode) {
  const live = tone === 'run' || tone === 'bg'
  const base = live ? (mode === 'full' ? 108 : 100) : tone === 'you' ? 92 : mode === 'full' ? 74 : 80
  return base + (tasks ? tasks * 24 + 6 : 0)
}

export type LaneProps = { s: Session; tone: Tone; now: number; mode: LaneMode; lit?: boolean; onHover?: (k: string | null) => void }

export const Lane = memo(function Lane({ s, tone, now, mode, lit, onHover }: LaneProps) {
  useFeedTicks(s)
  const open = useOpenSession()
  const menu = useSessionMenu()
  const live = tone === 'run' || tone === 'bg'
  const quiet = tone === 'park' || tone === 'idle'
  const tasks = live || tone === 'you' ? liveTasks(s) : []
  const req = workReq(s)
  const ticks = () => tickStore.get(s.key) ?? []
  const phone = mode === 'phone'
  const style = phone ? undefined : { height: laneHeight(tone, tasks.length, mode) }
  const words = quiet ? lastWords(s) : ''

  // 左：标题 + 身份；状态标 + 手上的需求；窄处再加一行（当前动作 / 在问什么 / 最后一句话）
  const id = (
    <div className={cx('flex min-w-0 flex-col justify-center gap-1 px-3.5', quiet ? 'py-2' : 'py-2.5')}>
      <div className="flex min-w-0 items-center gap-2">
        <span className={cx('truncate font-semibold', quiet ? 'text-[14px] text-fg-2' : 'text-[15px] text-fg')}>{sessionTitle(s)}</span>
        <RoleBadge s={s} />
      </div>
      <div className="flex min-w-0 items-center gap-2">
        <StateTag s={s} tone={tone} now={now} />
        <Tip content={sessionExtras(s)}>
          <span className="min-w-0 truncate text-[12px] text-fg-2">
            {req ? (
              <>
                <span className="mr-1 font-mono text-[11.5px] text-fg-3">{req.code}</span>
                {req.title}
              </>
            ) : (
              <span className="text-fg-3">{live ? s.summary || s.last_prompt : ''}</span>
            )}
          </span>
        </Tip>
      </div>
      {live && mode !== 'full' && <ActLine s={s} />}
      {tone === 'you' && (mode === 'mid' || mode === 'narrow') && <div className="truncate text-[12px] leading-4 text-reply">{youLine(s)}</div>}
      {quiet && (mode === 'mid' || mode === 'narrow') && words && <div className="truncate text-[12px] leading-4 text-fg-3">{words}</div>}
    </div>
  )
  // 右（宽处）：在跑放最近 4 个动作；等你放它在问什么；待命 / 空闲放最后一句话和上次动作的时间
  const right =
    mode === 'full' &&
    (live ? (
      <div className="flex min-w-0 flex-col justify-center gap-0.5 overflow-hidden border-l border-line px-4 py-2 font-mono text-[12px] leading-[1.6]">
        <Feed s={s} n={4} />
      </div>
    ) : (
      <div className="flex min-w-0 flex-col justify-center gap-1 border-l border-line px-4 py-2">
        <p className={cx('line-clamp-2 text-[13px] leading-snug', tone === 'you' ? 'text-reply' : words ? 'text-fg-2' : 'text-fg-3')}>{tone === 'you' ? youLine(s) : words || t.desk.lane.noWords}</p>
        {quiet && <LastAct s={s} now={now} />}
      </div>
    ))

  return (
    <div style={style} className={cx(!phone && 'flex shrink-0')}>
      <Ctx items={menu(s)} className="flex min-h-0 w-full rounded-[14px]">
        <div
          role="button"
          tabIndex={0}
          data-lane={s.key}
          onClick={() => open(s.key)}
          onKeyDown={(e) => e.key === 'Enter' && e.target === e.currentTarget && open(s.key)}
          onMouseEnter={onHover && (() => onHover(s.key))}
          onMouseLeave={onHover && (() => onHover(null))}
          className={cx(
            'grid w-full min-w-0 cursor-pointer overflow-hidden rounded-[14px] text-left transition-[background,box-shadow] duration-200',
            tasks.length ? 'grid-rows-[minmax(0,1fr)_auto]' : 'grid-rows-1',
            quiet ? 'bg-surface/55 ring-1 ring-line ring-inset hover:bg-surface' : 'bg-surface shadow-card hover:bg-hover/40',
            tone === 'you' && 'ring-1 ring-reply/30 ring-inset',
            lit && 'desk-lit',
          )}
        >
          <div className={cx('grid min-h-0 min-w-0', cols[mode])}>
            {id}
            {!phone ? (
              <div className="relative min-h-0 min-w-0">
                <Timeline ticks={ticks} tone={tone} className="absolute inset-0" />
              </div>
            ) : (
              live && (
                <div className="relative mx-3.5 mb-2 h-10">
                  <Timeline ticks={ticks} tone={tone} className="absolute inset-0" />
                </div>
              )
            )}
            {right}
            {phone && tone === 'you' && <div className="line-clamp-2 px-3.5 pb-2.5 text-[12.5px] text-reply">{youLine(s)}</div>}
            {phone && quiet && words && <div className="line-clamp-2 px-3.5 pb-2.5 text-[12.5px] text-fg-3">{words}</div>}
          </div>
          {tasks.length > 0 && (
            <div className="pb-1.5">
              {tasks.map((x, i) => (
                <SubLane key={x.id} task={x} now={now} mode={mode} last={i === tasks.length - 1} />
              ))}
            </div>
          )}
        </div>
      </Ctx>
    </div>
  )
})

// ---------- 顶部的风 ----------

// Wind：简报位背后的一条波形，随整体活跃度起伏。只在有新的工具调用后起伏几秒，平时静止；
// 标签页隐藏、减少动效时不动，断线时压平变灰。
export function Wind() {
  const ref = useRef<HTMLCanvasElement>(null)
  const connected = useConnected()
  useEffect(() => {
    const cv = ref.current
    if (!cv) return
    let raf = 0
    let energy = activity()
    let phase = 0
    let t0 = 0
    const draw = () => {
      const dpr = pixelRatio(cv)
      const w = cv.clientWidth
      const h = cv.clientHeight
      if (!w || !h) return
      if (cv.width !== Math.round(w * dpr) || cv.height !== Math.round(h * dpr)) {
        cv.width = Math.round(w * dpr)
        cv.height = Math.round(h * dpr)
      }
      const g = cv.getContext('2d')
      if (!g) return
      g.setTransform(dpr, 0, 0, dpr, 0, 0)
      g.clearRect(0, 0, w, h)
      const c = colors()
      const e = connected ? 0.15 + energy * 0.85 : 0.04
      ;(
        [
          [connected ? c.run : c.idle, 1, 0.35],
          [connected ? c.park : c.idle, 0.6, 0.22],
        ] as const
      ).forEach(([col, k, a], j) => {
        g.beginPath()
        g.strokeStyle = col
        g.globalAlpha = a
        g.lineWidth = 1.3
        for (let x = 0; x <= w; x += 4) {
          const y = h * 0.62 + Math.sin(x / (38 - 10 * e) + phase / (1 + j * 0.4)) * h * 0.16 * e * k + Math.sin(x / 13 + phase * 2.6) * 1.4 * e
          if (x) g.lineTo(x, y)
          else g.moveTo(x, y)
        }
        g.stroke()
      })
    }
    // 有动作时起伏 2.5 秒，之后停在最后一帧
    const run = (ts: number) => {
      if (!t0) t0 = ts
      const target = activity()
      energy += (target - energy) * 0.04
      phase += 0.03
      draw()
      if (ts - t0 < 2500 && !document.hidden) raf = requestAnimationFrame(run)
      else t0 = 0
    }
    const kick = () => {
      if (!connected || reducedMotion() || document.hidden) {
        energy = activity()
        return draw()
      }
      if (t0) {
        t0 = performance.now() // 起伏中又来动作：延长
        return
      }
      cancelAnimationFrame(raf)
      raf = requestAnimationFrame(run)
    }
    draw()
    const off = onTicks(() => {
      if (serverNow() - lastAction() < 5000) kick()
    })
    const ro = new ResizeObserver(draw)
    ro.observe(cv)
    // 主题切换时换色
    const mo = new MutationObserver(draw)
    mo.observe(document.documentElement, { attributes: true, attributeFilter: ['class'] })
    return () => {
      off()
      ro.disconnect()
      mo.disconnect()
      cancelAnimationFrame(raf)
    }
  }, [connected])
  return <canvas ref={ref} aria-hidden className="pointer-events-none absolute inset-0 h-full w-full opacity-90" />
}
