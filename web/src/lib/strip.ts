// 胶片带的逻辑核心（R-73）：看图的胶片带和抽屉的上一件 / 下一件共用。不依赖 DOM / React / 动画库——
// 位置读写、弹簧动画、计时器、时钟都由外面注入（ui/gesture.tsx 的 useStrip 接 motion；单测接虚拟时钟），
// 这样真机日志可以原样回放（gesture-replay.test.ts）。

import { clamp, decide, G, rub, WheelSegment } from './gesture.ts'

export type Anim = { stop: () => void }

// springCurve：这种弹簧从 from 带初速度 v 走到 to 的轨迹，每 step 毫秒一个点，直到停稳（逐毫秒积分）。
// 交给 WAAPI 当关键帧，动画由合成线程跑（iPad 120Hz 下不受页面 60Hz 渲染限制）。
export function springCurve(from: number, to: number, v: number, step = 1000 / 120, k: number = G.stiffness, c: number = G.damping) {
  const xs = [from]
  let x = from
  let vel = v
  let t = 0
  let next = step
  for (let i = 0; i < 5000; i++) {
    const a = -k * (x - to) - c * vel
    vel += a / 1000
    x += vel / 1000
    t += 1
    if (t >= next) {
      xs.push(x)
      next += step
    }
    if (Math.abs(vel) < 5 && Math.abs(x - to) < 0.5) break
  }
  xs.push(to)
  return { xs, step, duration: (xs.length - 1) * step }
}
export type StripEnv = {
  get: () => number
  set: (v: number) => void
  velocity: () => number // 当前速度（px/s）
  // 弹簧：从当前位置走到 to，带初速度；onDone 只在走完时调用（stop 不调用）
  spring: (to: number, velocity: number, onDone: () => void) => Anim
  now: () => number
  setTimer: (fn: () => void, ms: number) => number
  clearTimer: (id: number) => void
  log?: (row: Record<string, unknown>) => void
}
export type StripOpts = {
  W: () => number
  limit?: () => [number, number] | null // 带子能走的范围 [lo, hi]（px），超出部分越拖越沉
  land?: (page: number, velocity: number) => void // 决定落到哪一页（默认直接弹过去）
  onRest?: () => void
  name?: string
}

type Gesture = { from: number; page: number; samples: { t: number; v: number }[]; wheel?: boolean }

export class StripCore {
  private anim: Anim | null = null
  private aim: number | null = null // 正在弹向的那一页
  private gesture: Gesture | null = null
  private seg = new WheelSegment()
  private pending: (() => void) | null = null
  private idle = 0
  private env: StripEnv
  opts: StripOpts

  constructor(env: StripEnv, opts: StripOpts) {
    this.env = env
    this.opts = opts
  }

  private get W() {
    return this.opts.W() || 1
  }

  private log(row: Record<string, unknown>) {
    this.env.log?.({ src: this.opts.name, ...row })
  }

  stop() {
    this.anim?.stop()
    this.anim = null
  }

  dragging() {
    return !!this.gesture
  }

  // then：走到之后要做的事（抽屉切换）。已经走过半页（屏幕上主要是那一件了）、或者走完，就做；
  // 半路被新的手势按住时不做——用户可能是想把它拉回来（R-73 第三轮真机日志：反向滑动「抓住」）
  private flush() {
    const f = this.pending
    this.pending = null
    f?.()
  }

  // 位置变了（拖动或动画的每一帧）
  tick(v: number) {
    if (this.pending && this.aim !== null && Math.abs(v - this.aim * this.W) < this.W / 2) this.flush()
  }

  snapTo(page: number, velocity = 0, then?: () => void) {
    this.stop()
    const pos = this.env.get()
    // 朝目标走的速度太大会冲过头再弹回来：限制到这种弹簧刚好不过冲的速度；往回甩（离开目标）的速度照常带上
    const dist = page * this.W - pos
    if (Math.abs(dist) < 1 || Math.sign(velocity) === Math.sign(dist)) velocity = Math.sign(velocity) * Math.min(Math.abs(velocity), Math.sqrt(G.stiffness) * Math.abs(dist))
    this.aim = page
    this.pending = then ?? null
    this.log({ k: 'snap', page, v: Math.round(velocity), from: Math.round(pos) })
    const a = this.env.spring(page * this.W, velocity, () => {
      if (this.anim !== a) return
      this.aim = null
      this.anim = null
      this.flush()
      this.opts.onRest?.()
    })
    this.anim = a
    this.tick(pos)
  }

  private land(page: number, velocity: number) {
    if (this.opts.land) this.opts.land(page, velocity)
    else this.snapTo(page, velocity)
  }

  go(d: number) {
    this.land((this.aim ?? Math.round(this.env.get() / this.W)) + d, 0)
  }

  // 拖动开始：打断正在进行的动画，停在当前屏幕位置、从这里接着走（速度归零，之后跟手指 / 触控板）；
  // 以动画正要去的那一页为基准：还没切过去时往回拉能回到原来那一件，继续往前就是那一件
  begin() {
    this.stop()
    const from = this.env.get()
    const page = this.aim ?? Math.round(from / this.W)
    this.aim = null
    this.gesture = { from, page, samples: [{ t: this.env.now(), v: from }] }
    this.log({ k: 'grab', pos: Math.round(from), page, pending: !!this.pending })
  }

  // dx：手指 / 触控板相对开始时的水平位移（向左为负，即往下一页）。带子最多走到基准页的前后一页
  move(dx: number) {
    const g = this.gesture
    if (!g) return
    const W = this.W
    let v = clamp(g.from - dx, Math.min(g.from, (g.page - 1) * W), Math.max(g.from, (g.page + 1) * W))
    const lim = this.opts.limit?.()
    if (lim) {
      const [lo, hi] = [Math.min(lim[0], g.from), Math.max(lim[1], g.from)]
      // 两端没有邻件：越拖越沉，最多露出 200px 左右
      if (v < lo) v = lo - rub(lo - v, 200)
      else if (v > hi) v = hi + rub(v - hi, 200)
    }
    this.env.set(v)
    const t = this.env.now()
    g.samples.push({ t, v })
    while (g.samples.length > 2 && t - g.samples[0].t > 100) g.samples.shift()
    this.tick(v)
  }

  // 松手：按这次手势走了多少与速度决定翻页还是弹回。velocity：给定的松手速度（px/s，正数是往下一页）
  end(velocity?: number) {
    const g = this.gesture
    this.gesture = null
    if (!g) return
    const [a, b] = [g.samples[0], g.samples[g.samples.length - 1]]
    const vel = velocity ?? (b.t > a.t ? ((b.v - a.v) / (b.t - a.t)) * 1000 : 0)
    const W = this.W
    const pos = this.env.get()
    const off = pos - g.from // 这次手势本身走了多少
    // 按离基准页的位置判断：动画半路被抓住时，手势起点不在页上——往回拉过了两成多就回去，哪怕手势本身只拉了一点；
    // 只是按住没动（或抖了一下）就照原样落到基准页；判断方向必须和这次手势的方向一致
    let d = decide(pos - g.page * W, vel, W, g.wheel)
    const fling = g.wheel ? G.flingWheel : G.fling
    if (Math.abs(off) < G.slop && Math.abs(vel) < fling) d = 0
    else if (d && Math.abs(off) >= G.slop && Math.sign(off) !== d) d = 0
    // 松手那一刻正往反方向走（手指已经在往回拖）：不往那边切，回到基准页，方向和手一致
    if (d && Math.sign(vel) === -d && Math.abs(vel) > 300) d = 0
    let page = g.page + d
    const lim = this.opts.limit?.()
    if (lim && (page * W < lim[0] - 1 || page * W > lim[1] + 1)) page = g.page
    this.log({ k: 'release', off: Math.round(off), v: Math.round(vel), page, base: g.page, wheel: !!g.wheel })
    this.land(page, vel)
  }

  // 妙控板：返回 true 表示这次滚轮归翻页（竖向为主、又不在一段横滑里的不归）。t：事件自己的时间戳
  wheel(dx: number, dy: number, t?: number): boolean {
    if (this.gesture && !this.gesture.wheel) return true // 触屏正在拖
    const now = t ?? this.env.now()
    const act = this.seg.feed(dx, dy, now, this.W)
    if (act.t === 'pass') return false
    this.env.clearTimer(this.idle)
    this.idle = this.env.setTimer(() => {
      const r = this.seg.idle(this.env.now())
      if (r?.t === 'release' && this.gesture?.wheel) this.end(-r.velocity)
    }, G.wheelIdle + 10)
    if (act.t === 'swallow') return true
    if (act.t === 'restart') {
      // 上一段就此判定，同一下立刻抓住开始新的一段：弹簧刚起步就被按住，屏幕上不往前多走
      if (this.gesture?.wheel) this.end(-act.velocity)
      this.begin()
      this.gesture!.wheel = true
    } else if (act.t === 'begin') {
      if (this.gesture) this.end()
      this.begin()
      this.gesture!.wheel = true
    }
    if (!this.gesture?.wheel) return true
    this.move(act.offset)
    if (act.t === 'release') this.end(-act.velocity)
    return true
  }

  // shift：带子换了基准页（抽屉里这一件变成了邻件）——屏幕上不动，只换参照；进行中的拖动和动画接着走、速度不丢
  shift(dx: number, settleTo?: number) {
    const W = this.W
    const running = !!this.anim
    const v = running ? this.env.velocity() : 0
    const target = this.aim
    this.stop()
    this.env.set(this.env.get() + dx)
    const g = this.gesture
    if (g) {
      g.from += dx
      g.page += dx / W
      for (const s of g.samples) s.v += dx
    }
    this.log({ k: 'shift', dx: Math.round(dx), pos: Math.round(this.env.get()), v: Math.round(v) })
    if (g) return
    if (settleTo !== undefined) {
      if (running || Math.abs(this.env.get() - settleTo * W) > 0.5) this.snapTo(settleTo, v)
    } else if (running && target !== null) this.snapTo(target + dx / W, v)
  }
}
