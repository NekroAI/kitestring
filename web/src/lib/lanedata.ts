// 工作台泳道时间线的数据（R-205）：每个会话、每个子任务每分钟的调用次数，长命令与待命时段，子任务名单。
// 历史只增不改：接口给的分钟和实时推送的分钟都按「会话 + 子任务 + 分钟」合并，次数只会变大（取较大值），
// 刷新、换设备、推送和拉取谁先到都一样。换档只是把分钟合并成大格（近 1 周档直接用小时级），不重新取数。
// token 用量（R-206）也在这里合并，口径同上。
// 只放纯计算，不碰 React，单测直接跑（lib/lanedata.test.ts）。

export type Zoom = 'h1' | 'h6' | 'd1' | 'w1'
export const ZOOMS: Zoom[] = ['h1', 'h6', 'd1', 'w1']

// 每档：每格几分钟、多少格（近 1 小时按宽度 15～60 格）、轴上多久一个刻度（分钟）
export const zoomSpec: Record<Zoom, { bucket: number; n: number; grid: number; hourly: boolean }> = {
  h1: { bucket: 1, n: 60, grid: 10, hourly: false },
  h6: { bucket: 5, n: 72, grid: 60, hourly: false },
  d1: { bucket: 10, n: 144, grid: 180, hourly: false },
  w1: { bucket: 60, n: 168, grid: 1440, hourly: true },
}

// 长档位：子任务合成一行，已结束的会话也画成泳道
export const coarse = (z: Zoom) => z === 'd1' || z === 'w1'

export type Axis = { zoom: Zoom; bucket: number; n: number; start: number; end: number } // start / end：Unix 分钟，[start, end)

export function makeAxis(zoom: Zoom, width: number, nowMs: number): Axis {
  const z = zoomSpec[zoom]
  const n = zoom === 'h1' ? Math.max(15, Math.min(60, Math.floor(width / 12) || 60)) : z.n
  const nowMin = Math.floor(nowMs / 60000)
  const end = (Math.floor(nowMin / z.bucket) + 1) * z.bucket // 最右一格是现在所在的这一格
  return { zoom, bucket: z.bucket, n, start: end - n * z.bucket, end }
}

// laneAxis：所有泳道共用的一条时间轴（R-219）。只按一个参照宽度算一次——主泳道时间线列的宽度（没有主泳道时取第一条），
// 子任务行、已结束泳道、shell 行都用这同一条轴，同一竖线在每一行都是同一时刻；各行画布的宽度只决定把格子画多宽。
export type AxisRow = { w: number; sub: boolean }
export function laneAxis(zoom: Zoom, rows: AxisRow[], nowMs: number): Axis {
  const ref = rows.find((r) => !r.sub && r.w > 0) ?? rows.find((r) => r.w > 0)
  return makeAxis(zoom, ref?.w ?? 0, nowMs)
}

// axisX：时刻（毫秒）在宽 w 的一行里的横坐标；cellX 是第 i 格的左边缘。位置一律按「时刻占整条轴的比例」换算。
export const axisX = (ax: Axis, w: number, tMs: number) => ((tMs / 60000 - ax.start) / (ax.n * ax.bucket)) * w
export const cellX = (ax: Axis, w: number, i: number) => (i / ax.n) * w

// gridTicks：轴上的刻度（Unix 分钟），按本地时间对齐（整 10 分钟 / 整点 / 3 小时 / 0 点）
export function gridTicks(ax: Axis, tzOffsetMin = -new Date().getTimezoneOffset()): number[] {
  const g = zoomSpec[ax.zoom].grid
  const out: number[] = []
  for (let m = Math.ceil((ax.start + tzOffsetMin) / g) * g - tzOffsetMin; m < ax.end; m += g) if (m > ax.start) out.push(m)
  return out
}

// ---------- 数据 ----------

export type LaneRowIn = { k: string; a: string; m: number[] | null; c: number[] | null }
export type Span = { k: string; a: string; kind: 'tool' | 'wait' | string; ref: string; label: string; start: number; end?: number; until?: number }
export type Agent = { k: string; a: string; label: string; type: string; start: number; end?: number }
export type SessionMeta = { k: string; title: string; role: string; tool: string; project_id: string; project_name: string; cwd: string; background: boolean; ended_at?: string; last_active_at: string }
export type LaneData = { now: number; step: number; from: number; to: number; rows: LaneRowIn[] | null; spans: Span[] | null; agents: Agent[] | null; sessions?: SessionMeta[] | null; tok?: TokRowIn[] | null; colds?: Cold[] | null }
export type LaneMinute = { k: string; a: string; m: number; c: number; hc: number }
export type LanePush = { m: LaneMinute[] | null; s: Span[] | null; g: Agent[] | null; t?: TokMinute[] | null; c?: Cold[] | null }

// ---------- token 用量（R-206） ----------
// 本机 daemon 从对话记录读出、按分钟汇总上传的数字。新算 = 没命中缓存的输入 + 写缓存 + 输出；命中率 = 读缓存 / (读缓存 + 输入 + 写缓存)。
// 值是累计的绝对值，只会变大：拉取和推送谁先到都取回复次数（其次总量）较大的那个。
export type TokVals = { in: number; cw: number; cw1h: number; cr: number; out: number; n: number }
export type TokRowIn = { k: string; a: string; model: string; m: number[] | null; in: number[] | null; cw: number[] | null; cw1h: number[] | null; cr: number[] | null; out: number[] | null; n: number[] | null }
export type TokMinute = { k: string; a: string; model: string; m: number; t: TokVals; h: TokVals }
export type Cold = { k: string; a: string; model: string; at: number; tokens: number; cause: 'idle' | 'compact' | 'model' | 'start' | 'other' | string; idle_min: number; prev_model?: string }
// 每百万 token 的价格（设置里选填）；四项都填了的模型才算金额
export type Price = { in: number; out: number; cw: number; cr: number }
export type Prices = Record<string, Price>

// 一格（或一段）的用量合计；cost 只在这里面的每个模型都有完整价格时才有
export type TokCell = { fresh: number; read: number; input: number; n: number; cost: number | null }
export const emptyCell = (): TokCell => ({ fresh: 0, read: 0, input: 0, n: 0, cost: 0 })

export const freshOf = (v: TokVals) => v.in + v.cw + v.out
// hitRate：命中率（0～1）；没有输入时为 null
export function hitRate(c: { read: number; input: number }): number | null {
  const d = c.read + c.input
  return d > 0 ? c.read / d : null
}
// hitTone：≥90% 深绿 / 60–90% 琥珀 / <60% 红
export function hitTone(h: number | null): 'hi' | 'mid' | 'lo' | null {
  if (h == null) return null
  return h >= 0.9 ? 'hi' : h >= 0.6 ? 'mid' : 'lo'
}

function addVals(c: TokCell, v: TokVals, price: Price | undefined) {
  c.fresh += freshOf(v)
  c.read += v.cr
  c.input += v.in + v.cw
  c.n += v.n
  if (c.cost == null) return
  if (!price) {
    if (freshOf(v) + v.cr > 0) c.cost = null
    return
  }
  c.cost += (v.in * price.in + v.cw * price.cw + v.cr * price.cr + v.out * price.out) / 1e6
}

export function addCell(a: TokCell, b: TokCell) {
  a.fresh += b.fresh
  a.read += b.read
  a.input += b.input
  a.n += b.n
  a.cost = a.cost == null || b.cost == null ? null : a.cost + b.cost
}

// complete：四项价格都填了（数字、不小于 0）
export function priceComplete(p: Partial<Record<keyof Price, number | null | undefined>> | undefined): p is Price {
  return !!p && (['in', 'out', 'cw', 'cr'] as const).every((k) => typeof p[k] === 'number' && Number.isFinite(p[k]) && (p[k] as number) >= 0)
}

// 冷启动阈值：单次写缓存超过这么多才画（和 daemon 一致）
export const COLD_MIN = 15000

export const rowKey = (k: string, a: string) => k + '\u0001' + a
const spanKey = (s: Span) => s.k + '\u0001' + s.kind + '\u0001' + s.ref

// 子代理没收到结束（被中断等）时，这么久没有动作就当它已结束（和 Hub 的 LaneAgentIdle 一致）
export const AGENT_IDLE = 30 * 60_000
export const LONG_TOOL = 20_000

type Row = Map<number, number>

export class LaneStore {
  min = new Map<string, Row>() // 行 → 分钟 → 次数
  hour = new Map<string, Row>() // 行 → 整点分钟 → 次数
  last = new Map<string, number>() // 行 → 最近一次有调用的分钟（分钟级）
  lastH = new Map<string, number>() // 行 → 最近一次有调用的整点（小时级）
  rowsOf = new Map<string, Set<string>>() // 会话 → 它的子任务（agent_id，主会话为空串）
  minFrom = Infinity // 分钟级已拉到的最早分钟
  hourFrom = Infinity
  spans = new Map<string, Span>()
  agents = new Map<string, Agent>()
  sessions = new Map<string, SessionMeta>()
  // token 用量（R-206）：行 → 模型 → 分钟 → 用量；小时级同样
  tokMin = new Map<string, Map<string, Map<number, TokVals>>>()
  tokHour = new Map<string, Map<string, Map<number, TokVals>>>()
  tokKeys = new Set<string>() // 有用量数据的会话（拿不到对话记录的会话不画下半部分）
  colds = new Map<string, Cold>()
  prices: Prices = {}
  version = 0
  private cache = new Map<string, number[]>()
  private cacheV = -1
  private tokCache = new Map<string, TokCell[]>()
  private tokCacheV = -1
  private spanIdx: Map<string, Span[]> | null = null
  private spanV = -1

  private put(tab: Map<string, Row>, k: string, a: string, m: number, c: number) {
    const rk = rowKey(k, a)
    let r = tab.get(rk)
    if (!r) tab.set(rk, (r = new Map()))
    // 次数只增不减：拉取和推送谁先到都取较大的那个
    if ((r.get(m) ?? 0) >= c) return
    r.set(m, c)
    let s = this.rowsOf.get(k)
    if (!s) this.rowsOf.set(k, (s = new Set()))
    s.add(a)
    const last = tab === this.min ? this.last : this.lastH
    if ((last.get(rk) ?? -1) < m) last.set(rk, m)
  }

  private putTok(tab: Map<string, Map<string, Map<number, TokVals>>>, k: string, a: string, model: string, m: number, v: TokVals) {
    const rk = rowKey(k, a)
    let r = tab.get(rk)
    if (!r) tab.set(rk, (r = new Map()))
    let mm = r.get(model)
    if (!mm) r.set(model, (mm = new Map()))
    const cur = mm.get(m)
    // 累计值只会变大：回复次数多的（一样多时总量大的）是新的
    if (cur && (cur.n > v.n || (cur.n === v.n && freshOf(cur) + cur.cr >= freshOf(v) + v.cr))) return
    mm.set(m, v)
    this.tokKeys.add(k)
    let s = this.rowsOf.get(k)
    if (!s) this.rowsOf.set(k, (s = new Set()))
    s.add(a)
  }

  private putCold(c: Cold) {
    this.colds.set(`${c.k}\u0001${c.a}\u0001${c.at}`, c)
  }

  setPrices(p: Prices) {
    this.prices = p
    this.version++
  }

  private putSpan(s: Span) {
    const key = spanKey(s)
    const cur = this.spans.get(key)
    if (cur && cur.end != null && s.end == null) return // 已经知道结束了，不被还在跑的旧数据盖掉
    this.spans.set(key, s)
  }

  private putAgent(g: Agent) {
    const key = rowKey(g.k, g.a)
    const cur = this.agents.get(key)
    this.agents.set(key, cur ? { ...cur, ...g, label: g.label || cur.label, type: g.type || cur.type, end: g.end ?? cur.end, start: Math.min(cur.start, g.start) } : g)
    let s = this.rowsOf.get(g.k)
    if (!s) this.rowsOf.set(g.k, (s = new Set()))
    s.add(g.a)
  }

  merge(d: LaneData) {
    const tab = d.step === 60 ? this.hour : this.min
    for (const r of d.rows ?? []) {
      const m = r.m ?? []
      const c = r.c ?? []
      for (let i = 0; i < m.length; i++) this.put(tab, r.k, r.a, m[i], c[i])
    }
    if (d.step === 60) this.hourFrom = Math.min(this.hourFrom, d.from)
    else this.minFrom = Math.min(this.minFrom, d.from)
    for (const s of d.spans ?? []) this.putSpan(s)
    for (const g of d.agents ?? []) this.putAgent(g)
    for (const s of d.sessions ?? []) this.sessions.set(s.k, s)
    const tt = d.step === 60 ? this.tokHour : this.tokMin
    for (const r of d.tok ?? []) {
      const m = r.m ?? []
      for (let i = 0; i < m.length; i++)
        this.putTok(tt, r.k, r.a, r.model, m[i], { in: r.in?.[i] ?? 0, cw: r.cw?.[i] ?? 0, cw1h: r.cw1h?.[i] ?? 0, cr: r.cr?.[i] ?? 0, out: r.out?.[i] ?? 0, n: r.n?.[i] ?? 0 })
    }
    for (const c of d.colds ?? []) this.putCold(c)
    this.version++
  }

  push(p: LanePush) {
    for (const x of p.m ?? []) {
      this.put(this.min, x.k, x.a, x.m, x.c)
      this.put(this.hour, x.k, x.a, Math.floor(x.m / 60) * 60, x.hc)
    }
    for (const s of p.s ?? []) this.putSpan(s)
    for (const g of p.g ?? []) this.putAgent(g)
    for (const x of p.t ?? []) {
      this.putTok(this.tokMin, x.k, x.a, x.model, x.m, x.t)
      this.putTok(this.tokHour, x.k, x.a, x.model, Math.floor(x.m / 60) * 60, x.h)
    }
    for (const c of p.c ?? []) this.putCold(c)
    this.version++
  }

  // tokens：这几行（同一会话的主会话 / 若干子任务）每一格的用量合计（各模型相加，价格齐全时附金额）
  tokens(k: string, agents: string[], ax: Axis): TokCell[] {
    if (this.tokCacheV !== this.version) {
      this.tokCache.clear()
      this.tokCacheV = this.version
    }
    const ck = `${k}\u0002${agents.join('\u0002')}\u0002${ax.start}\u0002${ax.n}\u0002${ax.bucket}`
    const hit = this.tokCache.get(ck)
    if (hit) return hit
    const out = Array.from({ length: ax.n }, emptyCell)
    const hourly = zoomSpec[ax.zoom].hourly
    for (const a of agents) {
      const r = (hourly ? this.tokHour : this.tokMin).get(rowKey(k, a))
      if (!r) continue
      for (const [model, mm] of r) {
        const price = this.prices[model]
        for (const [m, v] of mm) if (m >= ax.start && m < ax.end) addVals(out[Math.floor((m - ax.start) / ax.bucket)], v, price)
      }
    }
    this.tokCache.set(ck, out)
    return out
  }

  // tokenTotal：所有会话（含子任务）每一格的用量之和（顶部波形用）
  tokenTotal(ax: Axis): TokCell[] {
    const out = Array.from({ length: ax.n }, emptyCell)
    for (const k of this.tokKeys) {
      const cells = this.tokens(k, [...(this.rowsOf.get(k) ?? [])], ax)
      for (let i = 0; i < ax.n; i++) addCell(out[i], cells[i])
    }
    return out
  }

  hasTokens(k: string) {
    return this.tokKeys.has(k)
  }

  // coldsOf：这几行在这段时间里的冷启动（单次写缓存超过阈值）
  coldsOf(k: string, agents: string[], ax: Axis): Cold[] {
    const from = ax.start * 60000
    const to = ax.end * 60000
    const out: Cold[] = []
    for (const c of this.colds.values()) if (c.k === k && agents.includes(c.a) && c.at >= from && c.at < to && c.tokens > COLD_MIN) out.push(c)
    return out.sort((a, b) => a.at - b.at)
  }

  // calls：这几行（同一会话的主会话 / 若干子任务）在每一格的调用次数之和
  calls(k: string, agents: string[], ax: Axis): number[] {
    if (this.cacheV !== this.version) {
      this.cache.clear()
      this.cacheV = this.version
    }
    const ck = `${k}\u0002${agents.join('\u0002')}\u0002${ax.start}\u0002${ax.n}\u0002${ax.bucket}`
    const hit = this.cache.get(ck)
    if (hit) return hit
    const out = new Array<number>(ax.n).fill(0)
    const hourly = zoomSpec[ax.zoom].hourly
    for (const a of agents) {
      const r = (hourly ? this.hour : this.min).get(rowKey(k, a))
      if (!r || !r.size) continue
      if (hourly) {
        for (let i = 0; i < ax.n; i++) out[i] += r.get(ax.start + i * ax.bucket) ?? 0
      } else if (r.size < ax.n * ax.bucket) {
        // 有调用的分钟比格子里的分钟少：直接遍历这一行
        for (const [m, c] of r) if (m >= ax.start && m < ax.end) out[Math.floor((m - ax.start) / ax.bucket)] += c
      } else {
        for (let i = 0; i < ax.n; i++) for (let j = 0; j < ax.bucket; j++) out[i] += r.get(ax.start + i * ax.bucket + j) ?? 0
      }
    }
    this.cache.set(ck, out)
    return out
  }

  // spansOf：这个会话（指定子任务；主会话为空串）在这段时间里的时段
  spansOf(k: string, agents: string[], ax: Axis, nowMs: number): Span[] {
    if (!this.spanIdx || this.spanV !== this.version) {
      this.spanIdx = new Map()
      for (const s of this.spans.values()) {
        let l = this.spanIdx.get(s.k)
        if (!l) this.spanIdx.set(s.k, (l = []))
        l.push(s)
      }
      this.spanV = this.version
    }
    const from = ax.start * 60000
    const to = ax.end * 60000
    return (this.spanIdx.get(k) ?? []).filter((s) => agents.includes(s.a) && s.start < to && spanEnd(s, nowMs) >= from)
  }

  agentsOf(k: string): Agent[] {
    const out: Agent[] = []
    for (const a of this.rowsOf.get(k) ?? []) {
      if (!a) continue
      const g = this.agents.get(rowKey(k, a))
      const last = this.lastActive(k, a)
      if (g) out.push(g)
      else if (last) out.push({ k, a, label: '', type: '', start: last - 59_999 }) // 名单里没有（Hub 升级前开始的），从最近一次动作算
    }
    return out
  }

  // lastActive：这一行最近一次有调用的时刻（毫秒）；没有为 0
  lastActive(k: string, a: string) {
    const m = this.last.get(rowKey(k, a))
    const h = this.lastH.get(rowKey(k, a))
    if (m != null && (h == null || m >= h)) return m * 60000 + 59_999
    return h == null ? 0 : h * 60000 + 3_599_999
  }

  // active：这个会话（含子任务）在这段时间里有没有调用
  active(k: string, ax: Axis) {
    const hourly = zoomSpec[ax.zoom].hourly
    for (const a of this.rowsOf.get(k) ?? []) {
      // 按这一档用的那一级判断（小时级的整点会比窗口起点早，按分钟判断才准）
      const l = (hourly ? this.lastH : this.last).get(rowKey(k, a))
      if (l != null && l + (hourly ? 60 : 1) > ax.start) return true
    }
    return false
  }
}

export function spanEnd(s: Span, nowMs: number) {
  if (s.end != null) return s.end
  return s.until != null ? Math.min(nowMs, s.until) : nowMs
}

// agentEnd：子任务结束的时刻；还在跑为 0。没收到结束、30 分钟没动作的算在最后一次动作时结束
export function agentEnd(g: Agent, last: number, nowMs: number) {
  if (g.end != null) return g.end
  const l = Math.max(g.start, last)
  return nowMs - l > AGENT_IDLE ? l : 0
}

// inWindow：子任务在这段时间里出现过（结束了的留到滑出窗口为止）
export function inWindow(g: Agent, last: number, ax: Axis, nowMs: number) {
  const end = agentEnd(g, last, nowMs) || nowMs
  return g.start < ax.end * 60000 && end >= ax.start * 60000
}

// sharedMax：所有泳道共用的上限（主泳道一套、子泳道一套），至少为 1
export function sharedMax(series: number[][]) {
  let m = 1
  for (const s of series) for (const v of s) if (v > m) m = v
  return m
}

// barHeight：平方根缩放，有动作至少 2px
export function barHeight(c: number, max: number, room: number) {
  if (c <= 0) return 0
  return Math.max(2, Math.sqrt(c / max) * room)
}
