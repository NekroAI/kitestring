import { memo, useEffect, useLayoutEffect, useMemo, useRef, useState, useSyncExternalStore } from 'react'
import { createPortal } from 'react-dom'
import { Hourglass, Snowflake } from 'lucide-react'
import { AnimatePresence, motion } from 'motion/react'
import type { Session } from '../api/client'
import { serverNow, useConnected } from '../api/queries'
import { t } from '../i18n'
import { addCell, agentEnd, axisX, barHeight, cellX, coarse, emptyCell, gridTicks, hitRate, hitTone, inWindow, LONG_TOOL, laneAxis, makeAxis, shellEnd, shellLive, spanEnd, zoomSpec, ZOOMS, type Agent, type Axis, type Cold, type SessionMeta, type Shell, type TokCell, type Zoom } from '../lib/lanedata'
import { modelShort, type ModelUse } from '../lib/model'
import { cx, duration, shortDuration, wan } from '../lib/util'
import { Tip } from '../ui/base'
import { spring } from '../ui/gesture'
import { Ctx } from '../ui/contextMenu'
import { displayState, ProfileBadge, RoleBadge, RoleTag, ToolMark } from '../ui/marks'
import { Linkify } from '../ui/refmark'
import { laneStore, money, onLaneData, useLaneSnap, useWarmFlag } from './laneFeed'
import { useToolFeed } from './liveFeed'
import { useSessionMenu } from './menus'
import { useOpenSession } from './nav'
import { sessionTitle, splitTool } from './SessionCard'
import { liveTasks, taskName, type BgTask } from './tasks'
import { CtxBadge, IdleTag, OfflineTag } from './compact'
import { reducedMotion } from './verdict'

// 工作台的会话泳道（R-170，D-228 原型）：每个会话一条——左边工具图标（Claude / Codex，R-187）、标题、身份（D-177 描边胶囊 + 在线点）、
// 状态标（灯与状态字合一，带已运行时长）；中间是时间线（R-205：4 档分度值，每格调用次数柱 + 长命令细带 + 待命虚线，右边是现在）；
// 右边当前动作。泳道按内容给高度（在跑的高、等你适中、待命 / 空闲带最后一句话和上次动作），后台任务按 R-162 作为子泳道挂在会话下面。
// 动效只在有动作时出现：灯在主会话调用工具时闪一下，动作流新进一行时滑入一次；只有「等你」的灯会呼吸。
// 时间线不画闪光点、不画常驻波纹；标签页隐藏时停画，断线时静止变灰，减少动效时每 5 秒才重画一帧。

const WINDOW = 10 * 60 * 1000
const MAX_TICKS = 200

// ---------- 灯与风 ----------

// 主会话的工具调用时刻：只给状态灯闪一下（时间线本身画每分钟汇总，见下面的画布）。
// Hub 给近 10 分钟打底（ticks），之后随实时推送的 last_tool_at 追加。
const tickStore = new Map<string, number[]>()
const tickSubs = new Set<() => void>()

function feedTicks(key: string, list: number[]) {
  const cur = tickStore.get(key) ?? []
  const floor = serverNow() - WINDOW
  let changed = false
  const next = cur.filter((x) => x >= floor)
  for (const at of list) {
    if (!at || at < floor || next.some((x) => Math.abs(x - at) < 400)) continue
    next.push(at)
    changed = true
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


// ---------- 时间线画布（R-205） ----------
// 每条泳道一张 canvas：中线以上是每格的调用次数（所有主泳道共用一个上限、子泳道共用一个上限，平方根缩放，有动作至少 2px）；
// 中线上的实心细带是长命令（工具开始到结束超过 20 秒），虚线是 ks wait 待命；悬停显示这一格的数字。
// 中线以下是每格新算的 token（R-206，所有主泳道共用一个上限，平方根缩放），颜色是这一格的缓存命中率（≥90% 深绿 / 60–90% 琥珀 / <60% 红），
// 底边的红色小三角是缓存重建（冷启动）；读不到对话记录的会话不画下半部分。子任务行只画上半部分，柱子颜色用命中率。
// 数据来自 laneFeed（每分钟汇总，历史只增不改）。

export type Tone = 'run' | 'bg' | 'you' | 'park' | 'idle' | 'sub'
// sh：shell 泳道（R-208）的一行，只画这条后台命令从开始到结束的横段
type PaintSpec = { k: string; agents: string[]; sub: boolean; faded: boolean; sh?: Shell }
type Painter = { cv: HTMLCanvasElement; spec: () => PaintSpec; hover: number; ax?: Axis; calls?: number[]; tok?: TokCell[]; colds?: Cold[] }
const painters = new Set<Painter>()
let loop = 0
let offline = false
let zoomNow: Zoom = 'h1'
let laneWidth = 0 // 最近一次主泳道时间线的宽度（近 1 小时档的格数随宽度）
let sharedAx: Axis | null = null // 所有泳道共用的时间轴（R-219）：只在 paintAll 里按一个宽度算一次
let maxMain = 1
let maxSub = 1
let maxTok = 1
const tokListeners = new Set<() => void>() // 顶部波形跟着重画

// setLaneZoom：切换分度值（所有泳道一起切），立刻重画。
export function setLaneZoom(z: Zoom) {
  if (zoomNow === z) return
  zoomNow = z
  paintAll()
}

function colors() {
  const css = getComputedStyle(document.documentElement)
  const v = (k: string) => css.getPropertyValue(k).trim()
  return { run: v('--run'), accent: v('--accent'), park: v('--accent'), idle: v('--idle'), grid: v('--line-strong'), line: v('--line'), fg: v('--fg'), hi: v('--run'), mid: v('--wait'), lo: v('--reply'), danger: v('--danger'), wait: v('--wait') }
}
type Colors = ReturnType<typeof colors>

export function pixelRatio(_cv: HTMLCanvasElement) {
  return window.devicePixelRatio || 1
}

function compute(p: Painter, now: number) {
  const w = p.cv.clientWidth
  if (!w) return
  const sp = p.spec()
  // 用共享的轴；只有一条泳道单独补画（还没有共享轴）时才按自己的宽度算
  p.ax = sharedAx && sharedAx.zoom === zoomNow ? sharedAx : makeAxis(zoomNow, w, now)
  p.calls = laneStore.calls(sp.k, sp.agents, p.ax)
  const tok = laneStore.hasTokens(sp.k)
  p.tok = tok ? laneStore.tokens(sp.k, sp.agents, p.ax) : undefined
  // 子任务的第一次回复总要写一遍缓存（每个子任务都有），不算缓存重建，不画
  p.colds = tok ? laneStore.coldsOf(sp.k, sp.agents, p.ax).filter((c) => !(c.a && c.cause === 'start')) : undefined
  p.cv.dataset.axis = `${p.ax.start}:${p.ax.n}:${p.ax.bucket}` // 这一行用的轴（测试核对所有行是同一条）
}

function paint(p: Painter, now: number, c: Colors) {
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
  if (!p.ax || p.ax.zoom !== zoomNow) compute(p, now)
  const ax = p.ax
  const calls = p.calls
  if (!ax || !calls) return
  g.setTransform(dpr, 0, 0, dpr, 0, 0)
  g.clearRect(0, 0, w, h)
  const sp = p.spec()
  const fade = sp.faded ? 0.5 : 1
  if (sp.sh) {
    paintShell(g, sp.sh, ax, w, h, now, c)
    g.globalAlpha = 1
    return
  }
  const X = (t: number) => axisX(ax, w, t)
  const bw = w / ax.n
  const mid = Math.round(h / 2) + 0.5
  const room = sp.sub ? mid - 3 : mid - 7
  const roomBelow = h - mid - 7
  // 轴上的刻度（10 分钟 / 1 小时 / 3 小时 / 1 天）
  if (!sp.sub) {
    g.globalAlpha = 0.6 * fade
    g.fillStyle = c.line
    for (const m of gridTicks(ax)) g.fillRect(Math.round(X(m * 60000)), 3, 1, h - 6)
  }
  if (p.hover >= 0 && p.hover < ax.n) {
    g.globalAlpha = 0.07
    g.fillStyle = c.fg
    g.fillRect(cellX(ax, w, p.hover), 0, bw, h)
  }
  // 中线
  g.globalAlpha = (sp.sub ? 0.35 : 0.6) * fade
  g.fillStyle = c.grid
  g.fillRect(0, mid - 0.5, w, 1)
  // 待命虚线、长命令细带
  for (const s of laneStore.spansOf(sp.k, sp.agents, ax, now)) {
    const open = s.end == null
    if (s.kind === 'tool' && open && now - s.start < LONG_TOOL) continue
    const x0 = Math.max(0, X(s.start))
    const x1 = Math.min(w, X(spanEnd(s, now)))
    if (x1 <= x0 && x0 >= w) continue
    if (s.kind === 'wait') {
      g.globalAlpha = 0.9 * fade
      g.strokeStyle = offline ? c.idle : c.accent
      g.lineWidth = 1.5
      g.setLineDash([4, 3])
      g.beginPath()
      g.moveTo(x0, mid)
      g.lineTo(Math.max(x0 + 2, x1), mid)
      g.stroke()
      g.setLineDash([])
    } else {
      g.globalAlpha = (open ? 0.9 : 0.7) * fade
      g.fillStyle = offline ? c.idle : c.run
      g.beginPath()
      g.roundRect(x0, mid - 2, Math.max(3, x1 - x0), 4, 2)
      g.fill()
    }
  }
  // 调用次数柱；子任务行的柱子颜色用这一格的命中率（读不到用量的照旧灰色）
  const max = sp.sub ? maxSub : maxMain
  const pad = Math.max(0.5, Math.min(3, bw * 0.18))
  const tok = p.tok
  const hitColor = (cell: TokCell | undefined) => {
    const tone = cell && !offline ? hitTone(hitRate(cell)) : null
    return tone ? c[tone] : null
  }
  g.globalAlpha = (sp.sub ? 0.7 : 0.85) * fade
  for (let i = 0; i < ax.n; i++) {
    const hh = barHeight(calls[i], max, room)
    if (!hh) continue
    g.fillStyle = sp.sub ? (hitColor(tok?.[i]) ?? c.idle) : offline ? c.idle : c.accent
    g.fillRect(cellX(ax, w, i) + pad / 2, mid - 1 - hh, Math.max(1, bw - pad), hh)
  }
  // 中线以下：每格新算的 token，颜色是命中率
  if (!sp.sub && tok) {
    g.globalAlpha = 0.85 * fade
    for (let i = 0; i < ax.n; i++) {
      const hh = barHeight(tok[i].fresh, maxTok, roomBelow)
      if (!hh) continue
      g.fillStyle = hitColor(tok[i]) ?? c.idle
      g.fillRect(cellX(ax, w, i) + pad / 2, mid + 1, Math.max(1, bw - pad), hh)
    }
  }
  // 缓存重建：底边的红色小三角（近 1 周档太密，不画）
  if (p.colds?.length && ax.zoom !== 'w1') {
    g.globalAlpha = fade
    g.fillStyle = offline ? c.idle : c.lo
    const s = sp.sub ? 3 : 4
    for (const cd of p.colds) {
      const x = X(cd.at)
      g.beginPath()
      g.moveTo(x - s, h - 1)
      g.lineTo(x + s, h - 1)
      g.lineTo(x, h - 1 - s * 1.75)
      g.closePath()
      g.fill()
    }
  }
  g.globalAlpha = 1
}

// paintShell：一条后台命令（R-208），不画刻度，只画一段横条——在跑绿色（跑飞黄色），成功灰色末端打勾，
// 失败红色末端打叉加退出码，被停止灰色末端方块，退出码未知的灰色末端一道竖线，结束时间也不知道的画成渐隐。
function paintShell(g: CanvasRenderingContext2D, x: Shell, ax: Axis, w: number, h: number, now: number, c: Colors) {
  const X = (t: number) => axisX(ax, w, t)
  const mid = Math.round(h / 2)
  const live = shellLive(x)
  const raw1 = X(shellEnd(x, now))
  const x0 = Math.max(0, X(x.start))
  const x1 = Math.min(w, raw1)
  if (raw1 < 0 || x0 >= w) return
  const fail = x.status === 'failed'
  const col = offline ? c.idle : live ? (x.hot ? c.wait : c.run) : fail ? c.danger : c.idle
  const bw = Math.max(3, x1 - x0)
  if (x.status === 'lost') {
    const gr = g.createLinearGradient(x0, 0, x0 + bw, 0)
    gr.addColorStop(0, col)
    gr.addColorStop(1, 'transparent')
    g.globalAlpha = 0.6
    g.fillStyle = gr
  } else {
    g.globalAlpha = live ? 0.85 : fail ? 0.75 : 0.5
    g.fillStyle = col
  }
  g.beginPath()
  g.roundRect(x0, mid - 3, bw, 6, 3)
  g.fill()
  if (live || x.status === 'lost' || raw1 > w + 1) return
  // 末端记号：紧跟在横条后面（刚结束、贴着右边时收进画面里）
  const m = Math.min(x0 + bw + 3, w - (fail ? 10 : 9))
  g.globalAlpha = 1
  g.lineWidth = fail ? 1.8 : 1.6
  g.strokeStyle = col
  g.fillStyle = col
  if (x.status === 'ok') {
    g.beginPath()
    g.moveTo(m, mid)
    g.lineTo(m + 3, mid + 3)
    g.lineTo(m + 8, mid - 3)
    g.stroke()
  } else if (fail) {
    g.beginPath()
    g.moveTo(m, mid - 3.5)
    g.lineTo(m + 7, mid + 3.5)
    g.moveTo(m + 7, mid - 3.5)
    g.lineTo(m, mid + 3.5)
    g.stroke()
    if (x.exit != null && m + 12 + 18 < w) {
      g.font = '600 10px ui-sans-serif, system-ui, sans-serif'
      g.textBaseline = 'middle'
      g.fillText(String(x.exit), m + 10, mid + 0.5)
    }
  } else if (x.status === 'stopped') {
    g.fillRect(m, mid - 3, 6, 6)
  } else {
    g.globalAlpha = 0.7
    g.fillRect(m - 2, mid - 4.5, 1.5, 9)
  }
}

// paintAll：先算出每条泳道这一档的格子，求共用上限，再统一画。
export function paintAll() {
  if (typeof document === 'undefined' || document.hidden || !painters.size) return
  const c = colors()
  const now = serverNow()
  let mm = 1
  let ms = 1
  let mt = 1
  // 共享的时间轴：按主泳道时间线列的宽度算一次，所有行（含子任务行、已结束泳道）都用它
  const rows = [...painters].map((p) => ({ w: p.cv.clientWidth, sub: p.spec().sub }))
  sharedAx = laneAxis(zoomNow, rows, now)
  laneWidth = rows.find((r) => !r.sub && r.w > 0)?.w ?? laneWidth
  painters.forEach((p) => {
    compute(p, now)
    const sub = p.spec().sub
    for (const v of p.calls ?? []) {
      if (sub) ms = Math.max(ms, v)
      else mm = Math.max(mm, v)
    }
    if (!sub) for (const v of p.tok ?? []) mt = Math.max(mt, v.fresh)
  })
  maxMain = mm
  maxSub = ms
  maxTok = mt
  tokListeners.forEach((f) => f())
  painters.forEach((p) => paint(p, now, c))
}

// 时间在流逝：每秒重画一次（还在跑的长命令往右长，最右一格满了换下一格）。断线时画最后一帧灰色后停住；减少动效时 5 秒一帧。
function schedule() {
  window.clearTimeout(loop)
  if (!painters.size || offline) return
  loop = window.setTimeout(
    () => {
      paintAll()
      schedule()
    },
    reducedMotion() ? 5000 : 1000,
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

const clock = (t: number) => new Date(t).toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit', hour12: false })
const day = (t: number) => new Date(t).toLocaleDateString(undefined, { month: 'numeric', day: 'numeric', weekday: 'short' })
const mday = (t: number) => `${new Date(t).getMonth() + 1}/${new Date(t).getDate()}` // 轴上一天只有几十像素：只写月 / 日
const iso = (t: number) => new Date(t).toISOString()

// cellText：悬停那一格的数字：时间范围、调用次数、碰到的长命令和待命。
function cellText(p: Painter, i: number, now: number): string[] {
  const ax = p.ax
  const sh = p.spec().sh
  if (sh) return shellTip(sh, now)
  if (!ax || !p.calls || i < 0 || i >= ax.n) return []
  const t0 = (ax.start + i * ax.bucket) * 60000
  const t1 = t0 + ax.bucket * 60000
  const range = `${ax.zoom === 'w1' ? day(t0) + ' ' : ''}${clock(t0)}–${clock(Math.min(t1, now))}`
  const out = [`${range} · ${t.desk.tl.calls(p.calls[i])}`]
  const sp = p.spec()
  const hit = laneStore.spansOf(sp.k, sp.agents, ax, now).filter((s) => s.start < t1 && spanEnd(s, now) >= t0 && !(s.kind === 'tool' && s.end == null && now - s.start < LONG_TOOL))
  const tools = hit.filter((s) => s.kind === 'tool')
  for (const s of tools.slice(0, 2)) out.push(s.end == null ? t.desk.tl.longRunning(s.label, duration(iso(s.start), now)) : t.desk.tl.long(s.label, duration(iso(s.start), s.end)))
  if (tools.length > 2) out.push(t.desk.tl.moreLong(tools.length - 2))
  if (hit.some((s) => s.kind === 'wait')) out.push(t.desk.tl.wait)
  const cell = p.tok?.[i]
  if (cell && cell.n > 0) out.push(tokText(cell))
  const colds = (p.colds ?? []).filter((x) => x.at >= t0 && x.at < t1)
  for (const x of colds.slice(0, 2)) out.push(coldText(x))
  if (colds.length > 2) out.push(t.desk.tl.moreCold(colds.length - 2))
  return out
}

// ---------- shell 泳道（R-208） ----------

const at = (v: number, now: number) => (now - v > 20 * 3600e3 ? `${day(v)} ${clock(v)}` : clock(v))

// shellResult：右边写的结果（在跑的写跑飞或最近一行输出）。tone 决定颜色。
function shellResult(x: Shell, now: number): { text: string; tone: 'fail' | 'hot' | 'run' | 'done'; mono?: boolean } {
  const S = t.desk.tl.sh
  if (shellLive(x)) {
    if (x.hot) return { text: hotText(x, now), tone: 'hot' }
    return x.last ? { text: x.last, tone: 'run', mono: true } : { text: S.running, tone: 'run' }
  }
  switch (x.status) {
    case 'ok':
      return { text: S.ok(duration(iso(x.start), shellEnd(x, now))), tone: 'done' }
    case 'failed':
      return { text: S.failed(x.exit ?? null), tone: 'fail' }
    case 'stopped':
      return { text: S.stopped, tone: 'done' }
    case 'lost':
      return { text: S.lost, tone: 'done' }
  }
  return { text: S.ended, tone: 'done' }
}

function hotText(x: Shell, now: number) {
  const S = t.desk.tl.sh
  const d = duration(iso(x.hot_at || x.start), now)
  if (x.hot === 'mem') return S.hotMem(x.rss_kb ? (x.rss_kb >= 1 << 20 ? `${(x.rss_kb / (1 << 20)).toFixed(1)} GB` : `${Math.round(x.rss_kb / 1024)} MB`) : '', d).replace(' ·  · ', ' · ')
  return S.hotCpu(d)
}

// shellTip：悬停看完整命令、起止时间、最后一行输出（描述、类型、结果已经写在这一行里，不再重复）。
function shellTip(x: Shell, now: number): string[] {
  const S = t.desk.tl.sh
  const cmd = x.cmd.split('\n').slice(0, 6).join('\n')
  const out = [cmd.length > 600 ? cmd.slice(0, 600) + '…' : cmd]
  const end = x.end ? S.tipEnd(at(x.end, now), duration(iso(x.start), x.end)) : shellLive(x) ? S.tipRunning(duration(iso(x.start), now)) : S.tipEndUnknown
  out.push(`${S.tipStart(at(x.start, now))} · ${end}`)
  // 结果（退出码、跑飞）已经写在这一行右边，不再重复；在跑的最近一行输出也已经在右边
  if (x.last && !(shellLive(x) && !x.hot)) out.push(S.tipLast(x.last))
  if (x.a) {
    const g = laneStore.agents.get(x.k + '\u0001' + x.a)
    out.push(S.tipAgent(g?.label || g?.type || t.session.taskUnnamed))
  }
  return out
}

// failedShells：这一档里这个会话失败了几个后台命令（会话标题旁的小红点）
function failedShells(k: string, zoom: Zoom, now: number) {
  const ax = sharedAx && sharedAx.zoom === zoom ? sharedAx : makeAxis(zoom, laneWidth || 720, now)
  return laneStore.shellsOf(k, ax, now).filter((x) => x.status === 'failed').length
}

function FailDot({ k, zoom, now }: { k: string; zoom: Zoom; now: number }) {
  const minute = Math.floor(now / 60000)
  const n = useLaneSnap(() => (void minute, failedShells(k, zoom, serverNow())))
  if (!n) return null
  return (
    <Tip content={t.desk.tl.sh.failDot(n)}>
      <i data-shell-fail={n} aria-label={t.desk.tl.sh.failDot(n)} className="size-[7px] shrink-0 rounded-full bg-danger" />
    </Tip>
  )
}

export { wan }

// pct：命中率的写法（99% 以上留一位小数，免得都写成 100%）
export function pct(h: number | null) {
  if (h == null) return '—'
  const v = h * 100
  return v >= 99 && v < 100 ? `${(Math.floor(v * 10) / 10).toFixed(1)}%` : `${Math.round(v)}%`
}

function withMoney(text: string, cost: number | null) {
  const m = money(cost)
  return m ? `${text} · ${t.desk.tl.money(m)}` : text
}

function tokText(c: TokCell) {
  return withMoney(t.desk.tl.tok(wan(c.fresh), pct(hitRate(c))), c.cost)
}

function coldText(x: Cold) {
  const C = t.desk.tl.coldCause
  const cause = x.cause === 'idle' ? C.idle(x.idle_min) : x.cause === 'compact' ? C.compact : x.cause === 'model' ? C.model(x.prev_model ?? '', x.model) : x.cause === 'start' ? C.start : C.other
  return t.desk.tl.cold(cause, wan(x.tokens))
}

// sumCells：一段格子的合计
function sumCells(cells: TokCell[]) {
  const s = emptyCell()
  for (const c of cells) addCell(s, c)
  return s
}

// laneTokLabel：泳道末尾的「近 1 小时 · 新算 n 万 · 命中 n%」（随档位；填了价格再附金额）。这一档没有用量时为空。
// bare：不写档位（同一行旁边已经写了档位时用）。
export function laneTokLabel(k: string, zoom: Zoom, now: number, bare = false) {
  if (!laneStore.hasTokens(k)) return ''
  const ax = sharedAx && sharedAx.zoom === zoom ? sharedAx : makeAxis(zoom, laneWidth || 720, now)
  const s = sumCells(laneStore.tokens(k, [''], ax))
  if (!s.n) return ''
  return withMoney(bare ? t.desk.tl.tok(wan(s.fresh), pct(hitRate(s))) : t.desk.tl.tokLabel(t.desk.zoom[zoom], wan(s.fresh), pct(hitRate(s))), s.cost)
}

function useTokLabel(k: string, zoom: Zoom, now: number, bare = false) {
  const minute = Math.floor(now / 60000)
  return useLaneSnap(() => (void minute, laneTokLabel(k, zoom, serverNow(), bare)))
}

function TokLabel({ k, zoom, now, className, bare }: { k: string; zoom: Zoom; now: number; className?: string; bare?: boolean }) {
  const text = useTokLabel(k, zoom, now, bare)
  if (!text) return null
  return (
    <Tip truncated content={text}>
      <span data-tok-label className={cx('truncate text-[11.5px] leading-4 text-fg-3 tabular', className)}>
        {text}
      </span>
    </Tip>
  )
}

// WarmChip：缓存临近过期 / 已冷却时，状态行里多一个小标签（R-207）；正常的不显示，细节在悬停提示里。
function WarmChip({ k, now }: { k: string; now: number }) {
  const w = useWarmFlag(k, now)
  if (!w) return null
  return (
    <Tip content={w.title}>
      <span data-warm={w.cold ? 'cold' : 'soon'} className={cx('inline-flex shrink-0 items-center gap-0.5 text-[11.5px] leading-4', w.cold ? 'text-fg-3' : 'text-reply')}>
        {w.cold ? <Snowflake className="size-3" /> : <Hourglass className="size-3" />}
        {w.label}
      </span>
    </Tip>
  )
}

function Timeline({ spec, className }: { spec: PaintSpec; className?: string }) {
  const ref = useRef<HTMLCanvasElement>(null)
  const pref = useRef<Painter | null>(null)
  const specRef = useRef(spec)
  specRef.current = spec
  const [tip, setTip] = useState<{ x: number; y: number; lines: string[] } | null>(null)
  useLayoutEffect(() => {
    const cv = ref.current
    if (!cv) return
    const p: Painter = { cv, spec: () => specRef.current, hover: -1 }
    pref.current = p
    painters.add(p)
    const ro = new ResizeObserver(() => paintAll())
    ro.observe(cv)
    paintAll()
    schedule()
    return () => {
      painters.delete(p)
      ro.disconnect()
    }
  }, [])
  // 换了要画的行（子任务合并、结束变淡）立刻重画，不等下一帧
  const sig = `${spec.k}|${spec.agents.join(',')}|${spec.sub}|${spec.faded}|${spec.sh ? `${spec.sh.status}:${spec.sh.end ?? ''}:${spec.sh.hot ?? ''}` : ''}`
  useEffect(() => {
    if (ref.current) paintAll()
  }, [sig])
  const move = (e: React.PointerEvent<HTMLCanvasElement>) => {
    const p = pref.current
    if (!p?.ax) return
    const r = e.currentTarget.getBoundingClientRect()
    const i = Math.floor(((e.clientX - r.left) / r.width) * p.ax.n)
    if (i !== p.hover) {
      p.hover = i
      paint(p, serverNow(), colors())
    }
    setTip({ x: e.clientX, y: r.top, lines: cellText(p, i, serverNow()) })
  }
  const leave = () => {
    const p = pref.current
    if (p) {
      p.hover = -1
      paint(p, serverNow(), colors())
    }
    setTip(null)
  }
  return (
    <>
      <canvas ref={ref} aria-hidden onPointerMove={move} onPointerLeave={leave} className={cx('block h-full w-full', className)} />
      {tip &&
        tip.lines.length > 0 &&
        createPortal(
          <div
            className="pointer-events-none fixed z-[90] max-w-[320px] -translate-y-full rounded-lg bg-fg px-2.5 py-1.5 text-xs leading-relaxed text-bg shadow-pop tabular"
            style={{ left: Math.min(tip.x + 10, window.innerWidth - 330), top: tip.y - 6 }}
          >
            {tip.lines.map((l, i) => (
              <div key={i} className={cx(i > 0 && 'opacity-80')}>
                {l}
              </div>
            ))}
          </div>,
          document.body,
        )}
    </>
  )
}

// 新数据进来（实时推送、拉取完成）时重画；切换明暗主题时换色。
if (typeof window !== 'undefined') {
  onLaneData(() => window.requestAnimationFrame(paintAll))
  new MutationObserver(() => window.requestAnimationFrame(paintAll)).observe(document.documentElement, { attributes: true, attributeFilter: ['class'] })
}

// ---------- 分度值与时间轴 ----------

// ZoomSwitch：「会话」标题右边的分度值切换（R-205），所有泳道一起切。
export function ZoomSwitch({ zoom, onChange, short }: { zoom: Zoom; onChange: (z: Zoom) => void; short?: boolean }) {
  return (
    <Tip content={t.desk.zoomTip(t.desk.tl.bucket(zoomSpec[zoom].bucket))}>
      <div role="radiogroup" aria-label={t.desk.zoomTip(t.desk.tl.bucket(zoomSpec[zoom].bucket))} className="inline-flex shrink-0 overflow-hidden rounded-lg ring-1 ring-line-strong ring-inset">
        {ZOOMS.map((z) => (
          <button
            key={z}
            role="radio"
            aria-checked={z === zoom}
            data-zoom={z}
            onClick={() => onChange(z)}
            className={cx('px-2 py-[3px] text-[12px] leading-4 whitespace-nowrap transition-colors', z === zoom ? 'bg-fg text-bg' : 'text-fg-2 hover:bg-hover')}
          >
            {(short ? t.desk.zoomShort : t.desk.zoom)[z]}
          </button>
        ))}
      </div>
    </Tip>
  )
}

// LaneAxis：泳道上方的一行时间刻度，和泳道的时间线列对齐。
export function LaneAxis({ zoom, mode, now }: { zoom: Zoom; mode: LaneMode; now: number }) {
  const ref = useRef<HTMLDivElement>(null)
  const [w, setW] = useState(0)
  useLayoutEffect(() => {
    const el = ref.current
    if (!el) return
    const ro = new ResizeObserver(([e]) => setW(e.contentRect.width))
    ro.observe(el)
    return () => ro.disconnect()
  }, [])
  // 和泳道画布用同一条轴（laneAxis）：宽度取整，和画布的 clientWidth 一致
  const ax = laneAxis(zoom, [{ w: Math.round(w), sub: false }], now)
  const total = ax.n * ax.bucket
  return (
    <div aria-hidden className={cx('sticky top-0 z-10 grid h-4 shrink-0 bg-bg text-[10.5px] leading-4 text-fg-3 tabular', cols[mode])}>
      <div />
      <div ref={ref} className="relative min-w-0 overflow-hidden">
        {w > 0 &&
          gridTicks(ax).map((m) => {
            const left = ((m - ax.start) / total) * 100
            if ((left / 100) * w < 18 || (1 - left / 100) * w < 18) return null // 贴边的刻度字会被切掉，不写
            return (
              <span key={m} className="absolute -translate-x-1/2 whitespace-nowrap" style={{ left: `${left}%` }}>
                {zoom === 'w1' ? mday(m * 60000) : clock(m * 60000)}
              </span>
            )
          })}
      </div>
    </div>
  )
}

// ---------- 状态 ----------

// laneTone：泳道的状态（决定高低、颜色和状态标）。等你：在问你 / 等授权，或有事项在「要你处理」里而它没在跑。
// 只认明确记录（R-239）：waiting 来自「要你处理」的条目（决策、验收、授权、提问工具），对话里的问句不算。
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

// lastWords：待命 / 空闲泳道上它这一轮做了什么，其次是摘要、你最后说的话。
export function lastWords(s: Session) {
  return s.digest_done || s.summary || s.last_prompt || ''
}

// youLine：等你的会话在等什么——写「要你处理」里那条明确记录本身（you，由工作台按条目给出，R-239）；
// 没给时按会话自己的等待类型（等授权 / 提问工具），都没有写一句「有事在要你处理里等你」。不从对话或模型摘要里猜。
function youLine(s: Session, you?: string) {
  if (you) return you
  const ds = displayState(s)
  if (ds === 'waiting_approval') return t.desk.lane.approve(s.state_detail)
  if (ds === 'waiting_answer') return splitTool(s.state_detail)[1] || t.session.asking
  return t.desk.lane.youHint
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
          <Tip key={Date.parse(a.at) + a.label} truncated content={a.label}>
            <div className={cx('truncate', last ? 'desk-in text-fg' : 'text-fg-3')}>
              {last ? '▸ ' : '✓ '}
              {name} <Linkify text={brief} label="code" />
            </div>
          </Tip>
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
    <Tip key={a.at} truncated content={a.label}>
      <div className="desk-in truncate font-mono text-[11.5px] leading-4 text-fg-2">
        ▸ {name} <Linkify text={brief} label="code" />
      </div>
    </Tip>
  )
}

// LastAct：上次动作的时间（和是什么动作），待命 / 空闲泳道用。
// 时间已经在左边的状态标里（待命 / 没动多久），这里只写是什么动作，一行只留一个时间（R-214）。
function LastAct({ s, className }: { s: Session; className?: string }) {
  const [name] = s.last_tool ? splitTool(s.last_tool) : ['']
  if (!name) return null
  return <div className={cx('truncate text-[11.5px] leading-4 text-fg-3', className)}>{t.desk.lane.lastAct(name)}</div>
}

// ---------- 泳道 ----------

export type LaneMode = 'full' | 'mid' | 'narrow' | 'phone'

// 时间线一列有上限（R-170 返工：超宽屏上不让它横跨大半屏），多出的宽度给左边的标题和右边的动作 / 最后一句话。
// 动作列在超宽屏上也不无限拉宽：工作台按三列合计约 97rem 给中栏封顶（R-187，pages/Workbench.tsx 的 laneMax）。
const cols: Record<LaneMode, string> = {
  full: 'grid-cols-[minmax(230px,19rem)_minmax(160px,34rem)_minmax(260px,1fr)]',
  mid: 'grid-cols-[minmax(220px,17rem)_minmax(0,1fr)]',
  narrow: 'grid-cols-[minmax(160px,13rem)_minmax(0,1fr)]',
  phone: 'grid-cols-1',
}

// ---------- 子泳道 ----------

// 子任务按每分钟汇总画（R-205）：结束后不立刻删，变淡、标「已结束」，留到滑出窗口；近 1 / 6 小时每个会话最多列 6 个
// （在跑的优先），多的合成一行「还有 n 个」；近 1 天 / 1 周合成一行「子任务（共 n 个）」。后台 shell 照旧只列在跑的（R-162）。
// sh：shell 泳道的一条后台命令（R-208）；shmore：多出来的合成一行；shagg：近 1 天 / 1 周档只给计数。shell：没有记录的 R-162 后台 shell（旧版 daemon）。
type SubRow = { id: string; kind: 'agent' | 'shell' | 'agg' | 'more' | 'sh' | 'shmore' | 'shagg'; label: string; guess: boolean; agents: string[]; ended: number; start: number; task?: BgTask; sh?: Shell; models?: ModelUse[] }

const SUB_MAX = 6
const SH_MAX = 5

function agentName(g: Agent, task?: BgTask): { text: string; guess: boolean } {
  if (g.label) return { text: g.label, guess: false }
  if (task) return taskName(task)
  if (g.type) return { text: g.type, guess: true }
  return { text: t.session.taskUnnamed, guess: true }
}

function subRows(s: Session, zoom: Zoom, now: number): SubRow[] {
  const ax = makeAxis(zoom, laneWidth || 720, now)
  const tasks = liveTasks(s)
  const live = new Map(tasks.filter((x) => x.kind === 'agent').map((x) => [x.id, x]))
  const seen = new Set<string>()
  const list: { g: Agent; last: number; task?: BgTask }[] = []
  for (const g of laneStore.agentsOf(s.key)) {
    seen.add(g.a)
    list.push({ g, last: laneStore.lastActive(s.key, g.a), task: live.get(g.a) })
  }
  // 时间线数据还没到（刚打开）时，先按会话里在跑的子代理列出来
  for (const x of live.values())
    if (!seen.has(x.id)) list.push({ g: { k: s.key, a: x.id, label: x.label, type: x.agent_type ?? '', start: Date.parse(x.started_at) }, last: x.last_action_at ? Date.parse(x.last_action_at) : 0, task: x })
  const vis = list
    .filter((x) => inWindow(x.g, x.last, ax, now))
    .map((x) => ({ ...x, end: x.task && x.g.end == null ? 0 : agentEnd(x.g, x.last, now) }))
  const rows: SubRow[] = []
  if (coarse(zoom)) {
    if (vis.length)
      rows.push({ id: 'agg', kind: 'agg', label: t.desk.tl.subAgg(vis.length), guess: false, agents: vis.map((x) => x.g.a), ended: vis.every((x) => x.end) ? Math.max(...vis.map((x) => x.end)) : 0, start: Math.min(...vis.map((x) => x.g.start)) })
  } else {
    const pick = [...vis].sort((a, b) => Number(!!a.end) - Number(!!b.end) || (b.end || now) - (a.end || now)).slice(0, SUB_MAX)
    pick.sort((a, b) => a.g.start - b.g.start)
    for (const x of pick) {
      const nm = agentName(x.g, x.task)
      rows.push({ id: x.g.a, kind: 'agent', label: nm.text, guess: nm.guess, agents: [x.g.a], ended: x.end, start: x.g.start, task: x.task, models: laneStore.modelsOf(s.key, x.g.a) })
    }
    const rest = vis.filter((x) => !pick.includes(x))
    if (rest.length) rows.push({ id: 'more', kind: 'more', label: t.desk.tl.subMore(rest.length), guess: false, agents: rest.map((x) => x.g.a), ended: 1, start: Math.min(...rest.map((x) => x.g.start)) })
  }
  // 后台命令（R-208）：近 1 / 6 小时每条一行（在跑的优先，最多 SH_MAX 条，多的合成一行）；近 1 天 / 1 周只给计数
  const shs = laneStore.shellsOf(s.key, ax, now)
  if (coarse(zoom)) {
    if (shs.length)
      rows.push({ id: 'shagg', kind: 'shagg', label: t.desk.tl.sh.count(shs.length, shs.filter((x) => x.status === 'failed').length), guess: false, agents: [], ended: 1, start: shs[0].start })
  } else {
    const pick = [...shs].sort((a, b) => Number(!shellLive(a)) - Number(!shellLive(b)) || shellEnd(b, now) - shellEnd(a, now)).slice(0, SH_MAX)
    pick.sort((a, b) => a.start - b.start)
    for (const x of pick) rows.push({ id: 'sh:' + x.ref, kind: 'sh', label: x.label, guess: false, agents: [], ended: shellLive(x) ? 0 : shellEnd(x, now), start: x.start, sh: x })
    if (shs.length > pick.length) rows.push({ id: 'shmore', kind: 'shmore', label: t.desk.tl.sh.more(shs.length - pick.length), guess: false, agents: [], ended: 1, start: 0 })
  }
  // 没有记录的后台 shell（旧版 daemon、认不出调用的）照旧按 R-162 列出在跑的
  const known = new Set<string>()
  for (const x of laneStore.shells.values()) if (x.k === s.key && x.tid && shellLive(x)) known.add(x.tid)
  for (const x of tasks) if (x.kind === 'shell' && !known.has(x.id)) rows.push({ id: x.id, kind: 'shell', label: taskName(x).text, guess: false, agents: [], ended: 0, start: Date.parse(x.started_at), task: x })
  return rows
}

// useSubRows：子泳道只在名单、结束状态变了时才重算（时间线数据每 300ms 可能推一次），时间每过一分钟也重算一次。
function useSubRows(s: Session, zoom: Zoom, now: number) {
  const sig = useLaneSnap(() =>
    subRows(s, zoom, serverNow())
      .map((r) => `${r.id}:${r.ended ? 1 : 0}:${r.label}:${r.models?.map((m) => m.id).join(',') ?? ''}${r.sh ? `:${r.sh.status}:${r.sh.end ?? ''}:${r.sh.hot ?? ''}:${r.sh.exit ?? ''}:${r.sh.last ?? ''}` : ''}`)
      .join('|'),
  )
  const minute = Math.floor(now / 60000)
  // eslint-disable-next-line react-hooks/exhaustive-deps
  return useMemo(() => subRows(s, zoom, now), [sig, s.tasks, s.state, zoom, minute])
}

// 时间的中文完整说法（R-214）：子任务行只写一个时间，写清它是什么时间。
function agoText(at: number | string, now: number) {
  const v = typeof at === 'string' ? Date.parse(at) : at
  const s = Math.max(0, Math.round((now - v) / 1000))
  const a = t.desk.tl.ago
  if (s < 10) return a.now
  if (s < 60) return a.sec(s)
  const m = Math.floor(s / 60)
  if (m < 60) return a.min(m)
  const h = Math.floor(m / 60)
  return h < 48 ? a.hour(h) : a.day(Math.floor(h / 24))
}

function spanText(from: string, now: number) {
  const s = Math.max(0, Math.round((now - Date.parse(from)) / 1000))
  const a = t.desk.tl.span
  if (s < 60) return a.sec(s)
  const m = Math.floor(s / 60)
  if (m < 60) return a.min(m)
  const h = Math.floor(m / 60)
  return h < 24 ? a.hour(h) : a.day(Math.floor(h / 24))
}

// subTime：子任务行唯一的时间。结束了写「多久前结束」；在跑的写「最近动作 多久前」，还没有动作记录就写「已跑 多久」。
// 窄屏（iPad 竖屏）上名字列太窄，在跑的只留「多久前」，完整说法放悬停提示里。
function subTime(row: SubRow, now: number, short = false) {
  if (row.kind === 'more' || row.kind === 'shmore' || row.kind === 'shagg') return ''
  if (row.ended) return row.ended > 1e12 ? t.desk.tl.endedAt(agoText(row.ended, now)) : ''
  if (row.kind === 'agg') return ''
  if (row.task?.last_action_at) return short ? agoText(row.task.last_action_at, now) : t.desk.tl.lastAct(agoText(row.task.last_action_at, now))
  return t.desk.tl.running(spanText(iso(row.start), now))
}

const resultTone = { fail: 'text-danger', hot: 'text-wait', run: 'text-fg-2', done: 'text-fg-3' }

// ShellLane：shell 泳道的一行（R-208）——左边类型 + 描述，中间横段，右边结果或最近一行输出；高度和子任务行一样固定。
function ShellLane({ row, now, mode, last }: { row: SubRow; now: number; mode: LaneMode; last: boolean }) {
  const x = row.sh!
  const res = shellResult(x, now)
  const ended = !shellLive(x)
  const spec = { k: x.k, agents: [], sub: true, faded: false, sh: x }
  const result = (
    <Tip truncated content={res.text}>
      <span data-shell-result={x.status} className={cx('min-w-0 truncate', resultTone[res.tone], res.mono && 'font-mono text-[11px]', mode !== 'full' && 'max-w-[45%] shrink-0 text-[11px]')}>
        {res.text}
      </span>
    </Tip>
  )
  return (
    <div data-shell={x.ref} data-shell-status={x.status} data-shell-hot={x.hot || undefined} className={cx('grid h-6 items-center text-fg-3', cols[mode], mode === 'phone' && 'h-auto py-0.5')}>
      <div className={cx('flex min-w-0 items-center gap-1.5 pr-3 pl-6 text-[11.5px]', ended && x.status !== 'failed' && 'opacity-75')}>
        <span aria-hidden className="relative h-6 w-2.5 shrink-0">
          <span className={cx('absolute top-0 left-0 border-l border-line-strong', last ? 'h-1/2' : 'h-full')} />
          <span className="absolute top-1/2 left-0 w-full border-t border-line-strong" />
        </span>
        <span className="min-w-[3.8em] shrink-0 rounded border border-line bg-surface-2 px-1 text-center text-[10.5px] leading-[15px] text-fg-2">{t.desk.tl.sh.kind[x.kind] ?? t.desk.tl.sh.kind.other}</span>
        <Tip truncated content={x.label}>
          <span className="min-w-0 flex-1 truncate">{x.label}</span>
        </Tip>
        {mode !== 'full' && result}
      </div>
      {mode !== 'phone' && (
        <div className="h-6 min-w-0">
          <Timeline spec={spec} />
        </div>
      )}
      {mode === 'full' && <div className="flex h-full min-w-0 items-center gap-2.5 border-l border-line px-4 text-[11.5px]">{result}</div>}
    </div>
  )
}

function SubLane({ row, k, now, mode, last }: { row: SubRow; k: string; now: number; mode: LaneMode; last: boolean }) {
  if (row.kind === 'sh') return <ShellLane row={row} now={now} mode={mode} last={last} />
  const task = row.task
  const ended = row.ended > 0
  const [tool, brief] = !ended && task?.last_action ? splitTool(task.last_action) : ['', '']
  const when = subTime(row, now)
  const short = mode === 'narrow' && !ended && !!task?.last_action_at
  const whenShort = short ? subTime(row, now, true) : when
  const kind = row.kind === 'shell' ? t.session.taskKind.shell : row.kind === 'agent' ? t.session.taskKind.agent : ''
  const count = row.kind === 'shmore' || row.kind === 'shagg'
  const spec = { k, agents: row.agents, sub: true, faded: ended }
  return (
    <div className={cx('grid h-6 items-center text-fg-3', cols[mode], mode === 'phone' && 'h-auto py-0.5')}>
      <div className={cx('flex min-w-0 items-center gap-1.5 pr-3 pl-6 text-[11.5px]', ended && 'opacity-70')}>
        {/* 树状缩进：竖线接上一行，横线指向这一行 */}
        <span aria-hidden className="relative h-6 w-2.5 shrink-0">
          <span className={cx('absolute top-0 left-0 border-l border-line-strong', last ? 'h-1/2' : 'h-full')} />
          <span className="absolute top-1/2 left-0 w-full border-t border-line-strong" />
        </span>
        {kind && <span className="w-9 shrink-0 text-[10.5px] opacity-80">{kind}</span>}
        <Tip truncated={!row.guess} content={row.guess ? `${t.session.taskUnnamed}${t.common.sep}${row.label}` : row.label}>
          <span data-shell-count={count ? row.kind : undefined} className={cx('min-w-0 flex-1 truncate', (row.guess || row.kind === 'more' || row.kind === 'shmore') && 'italic opacity-80')}>
            {row.label}
          </span>
        </Tip>
        {row.kind === 'agent' && !!row.models?.length && (
          <Tip content={t.desk.tl.modelTip(row.models.map((m) => modelShort(m.id)).join('、'))}>
            <span data-sub-model className="shrink-0 text-[10.5px] text-fg-2">
              {modelShort(row.models[0].id)}
            </span>
          </Tip>
        )}
        {mode !== 'full' && when && (short ? <Tip content={when}><span className="shrink-0 text-[11px] tabular">{whenShort}</span></Tip> : <span className="shrink-0 text-[11px] tabular">{when}</span>)}
      </div>
      {mode !== 'phone' && <div className="h-6 min-w-0">{!count && <Timeline spec={spec} />}</div>}
      {mode === 'full' && (
        <div className="flex h-full min-w-0 items-center gap-2.5 border-l border-line px-4 font-mono text-[11px]">
          {tool && (
            <Tip key={task?.last_action_at ?? ''} truncated content={task?.last_action}>
              <span className="desk-in min-w-0 truncate">
                ▸ {tool} {brief}
              </span>
            </Tip>
          )}
          {when && <span className="shrink-0 font-sans tabular">{when}</span>}
        </div>
      )}
    </div>
  )
}

// laneHeight：泳道按内容给高度（R-170 返工）：在跑的高（动作流 4 行 + 子泳道），等你的适中，待命 / 空闲也有两三行信息；
// 都是固定值，不随屏幕拉伸，空出来的地方留给下面「最近结束」的会话。
// 窄一些（没有右侧动作列）时，token 标签（R-206）在左边多占一行。
export function laneHeight(tone: Tone, tasks: number, mode: LaneMode, tokLine = false) {
  const live = tone === 'run' || tone === 'bg'
  const base = live ? (mode === 'full' ? 108 : 100) : tone === 'you' ? 92 : mode === 'full' ? 74 : 80
  return base + (tasks ? tasks * 24 + 6 : 0) + (tokLine && (mode === 'mid' || mode === 'narrow') ? 16 : 0)
}

export type LaneProps = { s: Session; tone: Tone; now: number; mode: LaneMode; zoom: Zoom; still?: boolean; you?: string }

export const Lane = memo(function Lane({ s, tone, now, mode, zoom, still, you }: LaneProps) {
  useFeedTicks(s)
  const open = useOpenSession()
  const menu = useSessionMenu()
  const live = tone === 'run' || tone === 'bg'
  const quiet = tone === 'park' || tone === 'idle'
  const tasks = useSubRows(s, zoom, now)
  const req = workReq(s)
  const spec = { k: s.key, agents: [''], sub: false, faded: false }
  const phone = mode === 'phone'
  const tokLabel = useTokLabel(s.key, zoom, now)
  // 高度按内容给：子任务增减、状态变化时用同一套弹簧长高 / 收矮（R-216），首次出现不播
  const height = phone ? undefined : laneHeight(tone, tasks.length, mode, !!tokLabel)
  const words = quiet ? lastWords(s) : ''

  // 左：标题 + 身份；状态标 + 手上的需求；窄处再加一行（当前动作 / 在问什么 / 最后一句话）
  const id = (
    <div className={cx('@container flex min-w-0 flex-col justify-center gap-1 px-3.5', quiet ? 'py-2' : 'py-2.5')}>
      <div className="flex min-w-0 items-center gap-2">
        <ToolMark tool={s.tool} size="sm" />
        <span className={cx('truncate font-semibold', quiet ? 'text-[14px] text-fg-2' : 'text-[15px] text-fg')}>{sessionTitle(s)}</span>
        <FailDot k={s.key} zoom={zoom} now={now} />
        <RoleBadge s={s} />
        <ProfileBadge s={s} />
        <CtxBadge s={s} />
      </div>
      <div className="flex min-w-0 items-center gap-2">
        <StateTag s={s} tone={tone} now={now} />
        <IdleTag s={s} />
        <OfflineTag s={s} />
        {!live && <WarmChip k={s.key} now={now} />}
        <Tip truncated content={req ? `${req.code} ${req.title}` : live ? s.summary || s.last_prompt : ''}>
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
      {tone === 'you' && (mode === 'mid' || mode === 'narrow') && <div className="truncate text-[12px] leading-4 text-reply"><Linkify text={youLine(s, you)} flow /></div>}
      {quiet && (mode === 'mid' || mode === 'narrow') && words && <div className="truncate text-[12px] leading-4 text-fg-3"><Linkify text={words} flow /></div>}
      {(mode === 'mid' || mode === 'narrow') && <TokLabel k={s.key} zoom={zoom} now={now} />}
    </div>
  )
  // 右（宽处）：在跑放最近 4 个动作；等你放它在问什么；待命 / 空闲放最后一句话和上次动作的时间
  const right =
    mode === 'full' &&
    (live ? (
      <div className="flex min-w-0 flex-col justify-center gap-0.5 overflow-hidden border-l border-line px-4 py-2 font-mono text-[12px] leading-[1.6]">
        <Feed s={s} n={tokLabel ? 3 : 4} />
        {tokLabel && <TokLabel k={s.key} zoom={zoom} now={now} className="font-sans" />}
      </div>
    ) : (
      <div className="flex min-w-0 flex-col justify-center gap-1 border-l border-line px-4 py-2">
        <p className={cx('line-clamp-2 text-[13px] leading-snug', tone === 'you' ? 'text-reply' : words ? 'text-fg-2' : 'text-fg-3')}>{tone === 'you' ? <Linkify text={youLine(s, you)} flow /> : words ? <Linkify text={words} flow /> : t.desk.lane.noWords}</p>
        {/* 底行按优先级取舍（R-225）：用量在右、先放；上次动作放得下才出现，放不下整项换到看不见的第二行，不截成一个字 */}
        {(quiet || tokLabel) && (
          <div className="flex h-4 min-w-0 flex-row-reverse flex-wrap items-center gap-x-3 overflow-hidden">
            <TokLabel k={s.key} zoom={zoom} now={now} className="min-w-0 shrink" />
            {quiet && <LastAct s={s} className="shrink-0 overflow-visible" />}
          </div>
        )}
      </div>
    ))

  return (
    <motion.div initial={false} animate={height === undefined ? undefined : { height }} transition={still ? { duration: 0 } : spring()} className={cx(!phone && 'flex shrink-0')}>
      <Ctx items={menu(s)} className="flex min-h-0 w-full rounded-[14px]">
        <div
          role="button"
          tabIndex={0}
          data-lane={s.key}
          onClick={() => open(s.key)}
          onKeyDown={(e) => e.key === 'Enter' && e.target === e.currentTarget && open(s.key)}
          data-spot-key={s.key}
          className={cx(
            'grid w-full min-w-0 cursor-pointer overflow-hidden rounded-[14px] text-left transition-[background,box-shadow,opacity,filter] duration-200',
            'grid-rows-[minmax(0,1fr)_auto]',
            quiet ? 'bg-surface/55 ring-1 ring-line ring-inset hover:bg-surface' : 'bg-surface shadow-card hover:bg-hover/40',
            tone === 'you' && 'ring-1 ring-reply/30 ring-inset',
          )}
        >
          <div className={cx('grid min-h-0 min-w-0', cols[mode])}>
            {id}
            {!phone ? (
              <div className="relative min-h-0 min-w-0">
                <Timeline spec={spec} className="absolute inset-0" />
              </div>
            ) : (
              live && (
                <div className="relative mx-3.5 mb-2 h-10">
                  <Timeline spec={spec} className="absolute inset-0" />
                </div>
              )
            )}
            {right}
            {phone && tone === 'you' && <div className="line-clamp-2 px-3.5 pb-2.5 text-[12.5px] text-reply"><Linkify text={youLine(s, you)} flow /></div>}
            {phone && quiet && words && <div className="line-clamp-2 px-3.5 pb-2.5 text-[12.5px] text-fg-3"><Linkify text={words} flow /></div>}
          </div>
          <div className={cx('relative', tasks.length > 0 && 'pb-1.5')}>
            <AnimatePresence initial={false} mode="popLayout">
              {tasks.map((x, i) => (
                <motion.div key={x.id} layout="position" initial={{ opacity: 0 }} animate={{ opacity: 1 }} exit={{ opacity: 0 }} transition={still ? { duration: 0 } : spring()}>
                  <SubLane row={x} k={s.key} now={now} mode={mode} last={i === tasks.length - 1} />
                </motion.div>
              ))}
            </AnimatePresence>
          </div>
        </div>
      </Ctx>
    </motion.div>
  )
})

// ---------- 已结束的会话（长档位） ----------

// EndedLane：近 1 天 / 1 周档里，这段时间里干过活、已经结束（或早已没动静）的会话也画一条泳道（R-205）：
// 固定高度、变淡，左边标题 + 身份 + 「已结束 · 多久前」，中间时间线，右边这一档的调用总数；子任务合成一行。
export const ENDED_LANE_H = 56

export function endedLaneHeight(subs: number) {
  return ENDED_LANE_H + (subs ? subs * 24 + 6 : 0)
}

export const EndedLane = memo(function EndedLane({ m, now, mode, zoom }: { m: SessionMeta; now: number; mode: LaneMode; zoom: Zoom }) {
  const open = useOpenSession()
  const ver = useLaneSnap(() => laneStore.version)
  const ax = makeAxis(zoom, laneWidth || 720, now)
  const agents = laneStore.agentsOf(m.k).filter((g) => inWindow(g, laneStore.lastActive(m.k, g.a), ax, now))
  const sum = laneStore.calls(m.k, [''], ax).reduce((a, b) => a + b, 0) + (agents.length ? laneStore.calls(m.k, agents.map((g) => g.a), ax).reduce((a, b) => a + b, 0) : 0)
  void ver
  const sub: SubRow | null = agents.length
    ? { id: 'agg', kind: 'agg', label: t.desk.tl.subAgg(agents.length), guess: false, agents: agents.map((g) => g.a), ended: 1, start: Math.min(...agents.map((g) => g.start)) }
    : null
  const shs = laneStore.shellsOf(m.k, ax, now)
  const shRow: SubRow | null = shs.length
    ? { id: 'shagg', kind: 'shagg', label: t.desk.tl.sh.count(shs.length, shs.filter((x) => x.status === 'failed').length), guess: false, agents: [], ended: 1, start: shs[0].start }
    : null
  const subs = [sub, shRow].filter((x): x is SubRow => !!x)
  const title = m.title || m.project_name || m.cwd.split('/').filter(Boolean).at(-1) || t.session.noProject
  const when = m.ended_at ? t.desk.tl.endedSession(agoText(m.ended_at, now)) : t.desk.lane.quiet(duration(m.last_active_at, now))
  const phone = mode === 'phone'
  return (
    <div style={phone ? undefined : { height: endedLaneHeight(subs.length) }} className={cx(!phone && 'flex shrink-0')}>
      <div
        role="button"
        tabIndex={0}
        data-lane={m.k}
        data-ended-lane
        data-spot-key={m.k}
        onClick={() => open(m.k)}
        onKeyDown={(e) => e.key === 'Enter' && e.target === e.currentTarget && open(m.k)}
        className={cx('grid w-full min-w-0 cursor-pointer overflow-hidden rounded-[14px] bg-surface/40 text-left ring-1 ring-line ring-inset transition-[background-color,opacity,filter] duration-200 hover:bg-surface', subs.length ? 'grid-rows-[minmax(0,1fr)_auto]' : 'grid-rows-1')}
      >
        <div className={cx('grid min-h-0 min-w-0', cols[mode])}>
          <div className="flex min-w-0 flex-col justify-center gap-0.5 px-3.5 py-1.5 opacity-80">
            <div className="flex min-w-0 items-center gap-2">
              <ToolMark tool={m.tool} size="sm" />
              <span className="truncate text-[13.5px] font-semibold text-fg-2">{title}</span>
              <FailDot k={m.k} zoom={zoom} now={now} />
              {m.role && <RoleTag role={m.role} presence="offline" />}
            </div>
            <div className="truncate text-[11.5px] text-fg-3">{when}</div>
          </div>
          {!phone && (
            <div className="relative min-h-0 min-w-0">
              <Timeline spec={{ k: m.k, agents: [''], sub: false, faded: true }} className="absolute inset-0" />
            </div>
          )}
          {mode === 'full' && (
            <div className="flex min-w-0 flex-col justify-center border-l border-line px-4 text-[12px] leading-4 text-fg-3 tabular">
              <span className="truncate">{t.desk.tl.sum(t.desk.zoom[zoom], sum)}</span>
              <TokLabel k={m.k} zoom={zoom} now={now} bare />
            </div>
          )}
        </div>
        {subs.length > 0 && (
          <div className="pb-1.5">
            {subs.map((r, i) => (
              <SubLane key={r.id} row={r} k={m.k} now={now} mode={mode} last={i === subs.length - 1} />
            ))}
          </div>
        )}
      </div>
    </div>
  )
})

// ---------- 顶部的波形（R-206） ----------

// briefAxis：顶部波形和简报条右边的合计用的轴——和泳道同一档、同一条轴（还没有泳道画过时按默认宽度算）。
function briefAxis(now: number) {
  return sharedAx && sharedAx.zoom === zoomNow ? sharedAx : makeAxis(zoomNow, laneWidth || 720, now)
}

// tokenWave：所有会话（含子任务）每格新算 token 之和，按自身最大值平方根缩放到 0～1，再做一次三格平滑；没有用量时为空。
export function tokenWave(cells: TokCell[]): number[] {
  let max = 0
  for (const c of cells) max = Math.max(max, c.fresh)
  if (!max) return []
  const v = cells.map((c) => Math.sqrt(c.fresh / max))
  return v.map((x, i) => (2 * x + (v[i - 1] ?? x) + (v[i + 1] ?? x)) / 4)
}

// Wind：简报条背后的波形——所有会话（含子任务）每格新算的 token 之和（和泳道同一档），平滑；颜色随这一档的整体命中率；
// 没动作的格子回落到底。只是背景，填色很浅，不抢正文。数据变了、换档时重画；断线时变灰。
export function Wind() {
  const ref = useRef<HTMLCanvasElement>(null)
  const connected = useConnected()
  useEffect(() => {
    const cv = ref.current
    if (!cv) return
    let raf = 0
    const draw = () => {
      raf = 0
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
      const cells = laneStore.tokenTotal(briefAxis(serverNow()))
      const wave = tokenWave(cells)
      cv.dataset.wave = wave.length ? String(cells.reduce((a, c) => a + c.fresh, 0)) : ''
      if (!wave.length) return
      const c = colors()
      const tone = connected ? hitTone(hitRate(sumCells(cells))) : null
      const col = tone ? c[tone] : c.idle
      const top = 6
      const pts = wave.map((v, i) => [((i + 0.5) / wave.length) * w, h - v * (h - top)] as const)
      // 平滑曲线：相邻两点的中点之间用二次曲线连
      g.beginPath()
      g.moveTo(0, h)
      g.lineTo(0, pts[0][1])
      for (let i = 0; i < pts.length - 1; i++) {
        const [x0, y0] = pts[i]
        const [x1, y1] = pts[i + 1]
        g.quadraticCurveTo(x0, y0, (x0 + x1) / 2, (y0 + y1) / 2)
      }
      g.lineTo(w, pts[pts.length - 1][1])
      g.lineTo(w, h)
      g.closePath()
      g.globalAlpha = 0.16
      g.fillStyle = col
      g.fill()
      g.globalAlpha = 1
    }
    const kick = () => {
      if (!raf) raf = requestAnimationFrame(draw)
    }
    draw()
    tokListeners.add(kick)
    const off = onLaneData(kick)
    const ro = new ResizeObserver(kick)
    ro.observe(cv)
    // 主题切换时换色
    const mo = new MutationObserver(kick)
    mo.observe(document.documentElement, { attributes: true, attributeFilter: ['class'] })
    return () => {
      tokListeners.delete(kick)
      off()
      ro.disconnect()
      mo.disconnect()
      cancelAnimationFrame(raf)
    }
  }, [connected])
  return <canvas ref={ref} aria-hidden data-token-wave className="pointer-events-none absolute inset-0 h-full w-full" />
}

// TokBrief：简报条右边这一档的合计「近 1 小时 · 新算 n 万 token · 命中 n%」（填了价格再附金额）；没有用量时不写。
export function TokBrief({ zoom, className }: { zoom: Zoom; className?: string }) {
  const minute = Math.floor(serverNow() / 60000)
  const text = useLaneSnap(() => {
    void minute
    const cells = laneStore.tokenTotal(briefAxis(serverNow()))
    const s = sumCells(cells)
    return s.n ? withMoney(t.desk.tl.tokBrief(t.desk.zoom[zoom], wan(s.fresh), pct(hitRate(s))), s.cost) : ''
  })
  if (!text) return null
  return <span className={cx('tabular whitespace-nowrap', className)}>{text}</span>
}
