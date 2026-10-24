import { createContext, useContext, useEffect, useLayoutEffect, useRef, useState, type ReactNode } from 'react'
import { animate, motion, useMotionValue, useMotionValueEvent, useTransform, type AnimationPlaybackControls, type MotionValue, type Transition } from 'motion/react'
import { cx } from '../lib/util'
import { clamp, decide, G, rebase, rub, WheelSegment } from '../lib/gesture'

// 全站手势（R-73，D-491 定稿）：一套参数、一种弹簧、按下即打断。
// - 参数与纯逻辑（松手判定、妙控板一段手势的聚合）在 lib/gesture.ts，这里是接到 DOM / React 上的部分。
// - 所有跟手的动画（看图胶片带、抽屉切换上一件 / 下一件、抽屉下拉 / 右拖关闭、列表左滑、长按菜单面板）都用 spring()，
//   松手时的速度带进弹簧；动画进行中手指一按下，动画停在当前位置，手势从这里接着走（像 macOS 的可中断动画）。
// - 拖动过程中不触发 React 渲染：位移写进 motion value / 直接写 transform。
// - 长按（菜单、拖拽、提示）统一 G.longPress；判定方向前先过 G.slop 死区。
// - 触屏横滑在屏幕左缘 G.edge 内起手时不接，留给浏览器的侧滑返回。
// - 妙控板双指横滑全站归纸鸢（installWheelGuard）：抽屉 / 看图里是上一件 / 下一件，能横向滚动的区域照常横滚，其它地方吃掉。
// 系统开了「减少动态效果」时不做弹簧动画（直接到位，和全站 CSS 动效的处理一致），手势本身照常跟手。

export { clamp, G, rub }

export const reducedMotion = () => typeof matchMedia !== 'undefined' && matchMedia('(prefers-reduced-motion: reduce)').matches

// spring：全站唯一一种弹簧（刚度 380、阻尼 38，接近临界阻尼、几乎不回弹）；velocity 是松手时的速度。减少动态效果时直接到位。
export function spring(velocity = 0): Transition {
  return reducedMotion() ? { duration: 0 } : { type: 'spring', stiffness: G.stiffness, damping: G.damping, velocity }
}

// springTo：把一个值用这种弹簧带到 to；返回的控制可以 stop()（按下即打断）。
export const springTo = (v: MotionValue<number>, to: number, velocity = 0, onComplete?: () => void): AnimationPlaybackControls =>
  animate(v, to, { ...(spring(velocity) as object), onComplete } as never)

// ---------- 手势日志（排查真机手感用）----------
// 地址带 ?gesturelog 时才启用（本标签页之后一直开着）：记录原始的 wheel / pointer / touch 事件序列和每帧带子位置、判定，
// 右下角一个只有图标的按钮导出 JSON（下载，同时尽量复制到剪贴板）。不带参数时什么都不做、什么都不显示。
type LogRow = Record<string, unknown>
const LOG_MAX = 40000
const logRows: LogRow[] = []
let logOn = false
export const glog = (row: LogRow) => {
  if (!logOn) return
  logRows.push({ t: Math.round(performance.now() * 10) / 10, ...row })
  if (logRows.length > LOG_MAX) logRows.splice(0, logRows.length - LOG_MAX)
}
export function installGestureLog() {
  if (typeof window === 'undefined') return
  try {
    if (new URLSearchParams(location.search).has('gesturelog')) sessionStorage.setItem('ks.gesturelog', '1')
    logOn = sessionStorage.getItem('ks.gesturelog') === '1'
  } catch {
    logOn = new URLSearchParams(location.search).has('gesturelog')
  }
  if (!logOn) return
  const r = (n: number) => Math.round(n * 100) / 100
  const opts = { capture: true, passive: true } as const
  window.addEventListener('wheel', (e) => glog({ k: 'wheel', dx: r(e.deltaX), dy: r(e.deltaY), mode: e.deltaMode, x: e.clientX, y: e.clientY, ctrl: e.ctrlKey || undefined }), opts)
  for (const type of ['pointerdown', 'pointermove', 'pointerup', 'pointercancel'] as const)
    window.addEventListener(type, (e) => glog({ k: type, pt: e.pointerType, id: e.pointerId, x: r(e.clientX), y: r(e.clientY) }), opts)
  for (const type of ['touchstart', 'touchmove', 'touchend', 'touchcancel'] as const)
    window.addEventListener(type, (e) => glog({ k: type, n: e.touches.length, c: e.cancelable, pts: [...e.changedTouches].map((p) => [r(p.clientX), r(p.clientY)]) }), opts)
  // 事件之后还有没有被拦下（defaultPrevented）：冒泡阶段最后再记一次
  window.addEventListener('wheel', (e) => glog({ k: 'wheel-end', prevented: e.defaultPrevented }), { passive: true })
  window.addEventListener('touchmove', (e) => glog({ k: 'touchmove-end', prevented: e.defaultPrevented }), { passive: true })
  const env = { ua: navigator.userAgent, w: innerWidth, h: innerHeight, dpr: devicePixelRatio, touch: navigator.maxTouchPoints, reduced: reducedMotion() }
  const exportLog = () => {
    const json = JSON.stringify({ env, at: new Date().toISOString(), rows: logRows })
    try {
      void navigator.clipboard?.writeText(json)
    } catch {
      /* 剪贴板不可用时只下载 */
    }
    const a = document.createElement('a')
    a.href = URL.createObjectURL(new Blob([json], { type: 'application/json' }))
    a.download = `gesturelog-${Date.now()}.json`
    document.body.appendChild(a)
    a.click()
    a.remove()
  }
  const mount = () => {
    const b = document.createElement('button')
    b.setAttribute('aria-label', 'export')
    b.style.cssText = 'position:fixed;right:10px;bottom:calc(10px + env(safe-area-inset-bottom));z-index:2147483647;width:34px;height:34px;border-radius:17px;border:1px solid rgba(0,0,0,.15);background:rgba(255,255,255,.85);display:flex;align-items:center;justify-content:center;opacity:.7;pointer-events:auto'
    b.innerHTML = '<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="#333" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M12 3v12"/><path d="m7 10 5 5 5-5"/><path d="M5 21h14"/></svg>'
    // 抽屉开着时（模态）点它不算点到抽屉外面
    for (const type of ['pointerdown', 'mousedown', 'touchstart'] as const) b.addEventListener(type, (e) => e.stopPropagation())
    b.addEventListener('click', (e) => {
      e.stopPropagation()
      exportLog()
    })
    document.body.appendChild(b)
  }
  if (document.body) mount()
  else window.addEventListener('DOMContentLoaded', mount)
  ;(window as unknown as { ksGestureLog: unknown }).ksGestureLog = { rows: logRows, export: exportLog }
}

// tracker：最近 100ms 的位置采样，松手时算速度；停住再松手 = 没有速度。
export function tracker() {
  const s: { x: number; y: number; t: number }[] = []
  return {
    add(x: number, y: number, t = performance.now()) {
      s.push({ x, y, t })
      while (s.length > 2 && t - s[0].t > 100) s.shift()
    },
    v() {
      if (s.length < 2) return { x: 0, y: 0 }
      const a = s[0]
      const b = s[s.length - 1]
      if (performance.now() - b.t > 80) return { x: 0, y: 0 }
      const dt = (b.t - a.t) / 1000 || 1
      return { x: (b.x - a.x) / dt, y: (b.y - a.y) / dt }
    },
  }
}

// swallowClick：拖动后松手的那次点击不算点击。
export function swallowClick() {
  const f = (e: Event) => {
    e.stopPropagation()
    e.preventDefault()
  }
  window.addEventListener('click', f, { capture: true, once: true })
  window.setTimeout(() => window.removeEventListener('click', f, { capture: true }), 400)
}

// scrollsX：el 往 dx 方向（正数向右）还能横向滚动的最近祖先（到 stop 为止）；没有返回 null。
export function scrollsX(target: EventTarget | null, dx: number, stop?: Element | null): HTMLElement | null {
  let el = target instanceof Element ? target : null
  while (el && el !== stop && el !== document.body && el !== document.documentElement) {
    if (el instanceof HTMLElement && el.scrollWidth > el.clientWidth + 1) {
      const ox = getComputedStyle(el).overflowX
      if (ox === 'auto' || ox === 'scroll') {
        const max = el.scrollWidth - el.clientWidth
        if (dx > 0 ? el.scrollLeft < max - 1 : el.scrollLeft > 1) return el
      }
    }
    el = el.parentElement
  }
  return null
}

// scrollerOf：手指下最近的竖向滚动区（下拉关闭时看它是否已在顶部）。
export function scrollerOf(target: EventTarget | null, stop?: Element | null): HTMLElement | null {
  let el = target instanceof Element ? target : null
  while (el && el !== stop) {
    if (el instanceof HTMLElement && el.scrollHeight > el.clientHeight + 1) {
      const oy = getComputedStyle(el).overflowY
      if (oy === 'auto' || oy === 'scroll') return el
    }
    el = el.parentElement
  }
  return null
}

const unit = (e: WheelEvent) => (e.deltaMode === 1 ? 16 : e.deltaMode === 2 ? 400 : 1)
export const wheelDelta = (e: WheelEvent) => ({ dx: e.deltaX * unit(e), dy: e.deltaY * unit(e) })

// installWheelGuard：妙控板双指横滑全站归纸鸢（D-491）。配合根元素的 overscroll-behavior-x: none：
// 抽屉、看图自己的监听先处理（切换上一件 / 下一件）并 preventDefault；能横向滚动的区域照常横滚；
// 其它地方的横向滚动一律吃掉，不触发浏览器的前进 / 后退。
export function installWheelGuard() {
  window.addEventListener(
    'wheel',
    (e) => {
      if (e.defaultPrevented || e.ctrlKey) return
      const { dx, dy } = wheelDelta(e)
      if (Math.abs(dx) <= Math.abs(dy)) return
      if (scrollsX(e.target, dx)) return
      e.preventDefault()
    },
    { passive: false },
  )
}

// ---------- 胶片带 ----------

type Gesture = { from: number; page: number; samples: { t: number; v: number }[]; wheel?: boolean }

export type Strip = {
  pos: MotionValue<number>
  snapTo: (page: number, velocity?: number, then?: () => void) => void
  go: (d: number) => void
  begin: () => void
  move: (dx: number) => void
  end: (velocity?: number) => void
  wheel: (dx: number, dy: number, t?: number) => boolean
  shift: (dx: number, settleTo?: number) => void
  dragging: () => boolean
  stop: () => void
}

// useStrip：一条按页排的带子（第 k 页在 k × W）。手指 / 触控板拖多少，带子就跟着走多少（只写 motion value，不触发 React 渲染）；
// 松手按位移与速度决定翻页还是弹回；一次手势最多走一页，弹簧动画随时可被新的手势打断、从当前位置接着拖。
// 妙控板：一串 wheel 事件按「一段手势」聚合（lib/gesture.ts 的 WheelSegment），一段只判定一次，惯性尾巴吞掉。
// limit：带子能走的范围 [lo, hi]（px），超出部分越拖越沉；land：决定落到哪一页（默认直接弹过去）；onRest：动画停下。
export function useStrip(W: number, o: { limit?: () => [number, number] | null; land?: (page: number, velocity: number) => void; onRest?: () => void; name?: string } = {}): Strip {
  const pos = useMotionValue(0)
  const w = useRef(W)
  w.current = W || 1
  const opts = useRef(o)
  opts.current = o
  const anim = useRef<AnimationPlaybackControls | null>(null)
  const aim = useRef<number | null>(null) // 正在弹向的那一页：动画中途再翻页时从它往后数
  const gesture = useRef<Gesture | null>(null)
  const seg = useRef(new WheelSegment())
  const pending = useRef<(() => void) | null>(null)
  const idle = useRef(0)
  const ref = useRef<Strip>(null!)
  if (!ref.current) {
    const stop = () => {
      anim.current?.stop()
      anim.current = null
    }
    // then：走到之后要做的事（抽屉切换）。差不多到了（1.5px 内）、动画走完、或者中途被新的手势按住，都立刻做——
    // 不能因为被打断就不做：真机上用户常在上一次还没完全停稳时就开始下一次滑动（R-73 真机日志）
    const flush = () => {
      const f = pending.current
      pending.current = null
      f?.()
    }
    const snapTo = (page: number, velocity = 0, then?: () => void) => {
      stop()
      // 朝目标走的速度太大会冲过头再弹回来（妙控板一下 100 多 px 的尖峰很常见）：限制到这种弹簧刚好不过冲的速度，
      // 往回甩（离开目标）的速度照常带上
      const dist = page * w.current - pos.get()
      if (Math.abs(dist) < 1 || Math.sign(velocity) === Math.sign(dist)) velocity = Math.sign(velocity) * Math.min(Math.abs(velocity), Math.sqrt(G.stiffness) * Math.abs(dist))
      aim.current = page
      pending.current = then ?? null
      glog({ k: 'snap', src: opts.current.name, page, v: Math.round(velocity), from: Math.round(pos.get()) })
      const a = springTo(pos, page * w.current, velocity, () => {
        if (anim.current !== a) return
        aim.current = null
        anim.current = null
        flush()
        opts.current.onRest?.()
      })
      anim.current = a
    }
    const land = (page: number, velocity: number) => (opts.current.land ? opts.current.land(page, velocity) : snapTo(page, velocity))
    const go = (d: number) => land((aim.current ?? Math.round(pos.get() / w.current)) + d, 0)
    // 拖动开始：打断正在进行的动画，从当前位置接着走；以动画正要去的那一页为基准（连续翻页不丢页）
    const begin = () => {
      stop()
      flush()
      const from = pos.get()
      const page = aim.current ?? Math.round(from / w.current)
      aim.current = null
      gesture.current = { from, page, samples: [{ t: performance.now(), v: from }] }
    }
    // dx：手指 / 触控板相对开始时的水平位移（向左为负，即往下一页）。带子最多走到基准页的前后一页
    const move = (dx: number) => {
      const g = gesture.current
      if (!g) return
      const W = w.current
      let v = clamp(g.from - dx, Math.min(g.from, (g.page - 1) * W), Math.max(g.from, (g.page + 1) * W))
      const lim = opts.current.limit?.()
      if (lim) {
        const [lo, hi] = [Math.min(lim[0], g.from), Math.max(lim[1], g.from)]
        // 两端没有邻件：越拖越沉，最多露出 200px 左右（妙控板一次滑动常常推一千多 px）
        if (v < lo) v = lo - rub(lo - v, 200)
        else if (v > hi) v = hi + rub(v - hi, 200)
      }
      pos.set(v)
      g.samples.push({ t: performance.now(), v })
      while (g.samples.length > 2 && performance.now() - g.samples[0].t > 100) g.samples.shift()
    }
    // 松手：按这次手势走了多少与速度决定翻页还是弹回。velocity：给定的松手速度（px/s，正数是往下一页）
    const end = (velocity?: number) => {
      const g = gesture.current
      gesture.current = null
      if (!g) return
      const [a, b] = [g.samples[0], g.samples[g.samples.length - 1]]
      const vel = velocity ?? (b.t > a.t ? ((b.v - a.v) / (b.t - a.t)) * 1000 : 0)
      const W = w.current
      const off = pos.get() - g.from
      const page = g.page + decide(off, vel, W, g.wheel)
      const lim = opts.current.limit?.()
      glog({ k: 'release', src: opts.current.name, off: Math.round(off), v: Math.round(vel), page, base: g.page, wheel: !!g.wheel })
      if (lim && (page * W < lim[0] - 1 || page * W > lim[1] + 1)) return snapTo(g.page, vel)
      if (page === g.page) return snapTo(page, vel)
      land(page, vel)
    }
    // 返回 true 表示这次滚轮归翻页（竖向为主、又不在一段横滑里的不归）
    // t：事件自己的时间戳（event.timeStamp）。主线程忙（例如刚切换、新页面在渲染）时事件会攒一会儿再一起派发，
    // 用处理时刻判断停顿会把惯性尾巴误认成新的一次滑动（R-73 真机日志回放发现）
    const wheel = (dx: number, dy: number, t?: number): boolean => {
      if (gesture.current && !gesture.current.wheel) return true // 触屏正在拖
      const now = t ?? performance.now()
      const act = seg.current.feed(dx, dy, now, w.current)
      if (act.t === 'pass') return false
      window.clearTimeout(idle.current)
      idle.current = window.setTimeout(() => {
        const r = seg.current.idle(performance.now())
        if (r?.t === 'release' && gesture.current?.wheel) end(-r.velocity)
      }, G.wheelIdle + 10)
      if (act.t === 'swallow') return true
      if (act.t === 'begin') {
        if (gesture.current) end()
        begin()
        gesture.current!.wheel = true
      }
      if (!gesture.current?.wheel) return true
      move(act.offset)
      if (act.t === 'release') end(-act.velocity)
      return true
    }
    // shift：带子换了基准页（抽屉里这一件变成了邻件）——屏幕上不动，只换参照；进行中的拖动和动画接着走、速度不丢
    const shift = (dx: number, settleTo?: number) => {
      const W = w.current
      const running = !!anim.current
      const v = running ? pos.getVelocity() : 0
      const target = aim.current
      stop()
      pos.set(pos.get() + dx)
      const g = gesture.current
      if (g) {
        g.from += dx
        g.page += dx / W
        for (const s of g.samples) s.v += dx
      }
      glog({ k: 'shift', src: opts.current.name, dx: Math.round(dx), pos: Math.round(pos.get()), v: Math.round(v) })
      if (g) return
      if (settleTo !== undefined) {
        if (running || Math.abs(pos.get() - settleTo * W) > 0.5) snapTo(settleTo, v)
      } else if (running && target !== null) snapTo(target + dx / W, v)
    }
    pos.on('change', (v) => {
      if (logOn) glog({ k: 'pos', src: opts.current.name, v: Math.round(v * 10) / 10 })
      if (pending.current && aim.current !== null && Math.abs(v - aim.current * w.current) < 1.5) flush()
    })
    ref.current = { pos, snapTo, go, begin, move, end, wheel, shift, dragging: () => !!gesture.current, stop }
  }
  return ref.current
}

// ---------- 统一的拖动识别 ----------

export type DragStart = { x0: number; y0: number; target: Element; touch: boolean }
export type DragHandlers<S> = {
  // 按下：返回 null 表示这次不接（例如点在按钮上）；返回的状态在 move / end 里带着
  start: (p: DragStart) => S | null
  // 过了死区之后第一次判定方向：返回 false 表示交还浏览器（竖向滚动、选字……）
  lock: (s: S, axis: 'x' | 'y', dx: number, dy: number) => boolean
  move: (s: S, dx: number, dy: number) => void
  end: (s: S, v: { x: number; y: number }, cancel: boolean) => void
  // 没有接成拖动就结束（只是点了一下，或交还了浏览器）：被按下打断的动画在这里接着走完
  tap?: (s: S) => void
  mouse?: (p: DragStart) => boolean // 鼠标也接（例如右侧面板按住顶栏右拖关闭）；默认只接触屏
  early?: (s: S, dx: number, dy: number) => boolean // 还在死区里时是否先拦下浏览器默认
}

// useDrag：触屏（以及按需的鼠标）拖动识别——先过死区再定方向，定下来之后吃掉浏览器默认（不滚动页面）。
// 用原生非被动监听：iOS 只有这样才能在确定横向后 preventDefault 掉竖向滚动。多指触摸时放弃。
export function useDrag<S>(el: HTMLElement | null, h: DragHandlers<S>) {
  const hr = useRef(h)
  hr.current = h
  useEffect(() => {
    if (!el) return
    type A = { s: S; x0: number; y0: number; tr: ReturnType<typeof tracker>; axis: 'x' | 'y' | null; dropped: boolean }
    let a: A | null = null
    let lastTouch = 0
    const begin = (x: number, y: number, target: Element, touch: boolean) => {
      const s = hr.current.start({ x0: x, y0: y, target, touch })
      if (s === null) return (a = null)
      a = { s, x0: x, y0: y, tr: tracker(), axis: null, dropped: false }
      a.tr.add(x, y)
    }
    const step = (x: number, y: number, ev: Event) => {
      if (!a || a.dropped) return
      a.tr.add(x, y)
      const dx = x - a.x0
      const dy = y - a.y0
      if (!a.axis) {
        if (Math.hypot(dx, dy) < G.slop) {
          // 死区里方向已经很明确、而且会接：先拦下浏览器默认，免得 iOS 先开始竖向滚动、之后的 touchmove 拦不住
          if (ev.cancelable && hr.current.early?.(a.s, dx, dy)) ev.preventDefault()
          return
        }
        const axis = Math.abs(dx) > Math.abs(dy) * G.ratio ? 'x' : 'y'
        if (!hr.current.lock(a.s, axis, dx, dy)) {
          a.dropped = true
          return
        }
        a.axis = axis
      }
      if (ev.cancelable) ev.preventDefault()
      if (ev instanceof MouseEvent) getSelection()?.removeAllRanges() // 鼠标拖顶栏时不顺带选中文字
      hr.current.move(a.s, dx, dy)
    }
    const finish = (cancel: boolean) => {
      const cur = a
      a = null
      if (!cur) return
      if (cur.axis) {
        swallowClick()
        hr.current.end(cur.s, cancel ? { x: 0, y: 0 } : cur.tr.v(), cancel)
      } else hr.current.tap?.(cur.s)
    }
    const ts = (e: TouchEvent) => {
      lastTouch = performance.now()
      if (e.touches.length !== 1) return finish(true)
      const t = e.touches[0]
      begin(t.clientX, t.clientY, e.target as Element, true)
    }
    const tm = (e: TouchEvent) => {
      if (e.touches.length !== 1) return finish(true)
      const t = e.touches[0]
      step(t.clientX, t.clientY, e)
    }
    const te = () => {
      lastTouch = performance.now()
      finish(false)
    }
    const tc = () => finish(true)
    const md = (e: MouseEvent) => {
      if (e.button !== 0 || performance.now() - lastTouch < 800) return
      const p = { x0: e.clientX, y0: e.clientY, target: e.target as Element, touch: false }
      if (!hr.current.mouse?.(p)) return
      begin(e.clientX, e.clientY, p.target, false)
      if (!a) return
      const mm = (ev: MouseEvent) => step(ev.clientX, ev.clientY, ev)
      const mu = () => {
        window.removeEventListener('mousemove', mm)
        window.removeEventListener('mouseup', mu)
        finish(false)
      }
      window.addEventListener('mousemove', mm)
      window.addEventListener('mouseup', mu)
    }
    el.addEventListener('touchstart', ts, { passive: true })
    el.addEventListener('touchmove', tm, { passive: false })
    el.addEventListener('touchend', te)
    el.addEventListener('touchcancel', tc)
    el.addEventListener('mousedown', md)
    return () => {
      el.removeEventListener('touchstart', ts)
      el.removeEventListener('touchmove', tm)
      el.removeEventListener('touchend', te)
      el.removeEventListener('touchcancel', tc)
      el.removeEventListener('mousedown', md)
    }
  }, [el])
}

// 不接拖动的地方：输入框、正在选文字、标注画布（R-180）
export const noDrag = (target: Element) => !!target.closest('input, textarea, select, [contenteditable="true"], .touch-none, [data-no-drag]') || (() => {
  const sel = typeof getSelection === 'function' ? getSelection() : null
  return !!sel && !sel.isCollapsed && !!sel.anchorNode && target.contains(sel.anchorNode)
})()

// ---------- 抽屉里的上一件 / 下一件 ----------

// PagerHandle：抽屉（ui/overlay.tsx 的 Panel）把横向拖动交给里面的 Pager。
export type PagerHandle = { begin: () => void; move: (dx: number) => void; end: (vx: number) => void }

// Leaving：Pager 里不是这一件的页（邻件、刚离开的那一件）。撤销条不把它当作锚点，撤回挪到顶部浮动条（R-201）
export const Leaving = createContext(false)

// PagerBind：Panel 提供，里面的 Pager 登记自己
export const PagerBind = createContext<((h: PagerHandle | null) => void) | null>(null)

type Leave = { k: string; side: number }

// Pager：抽屉内容按「上一件 / 这一件 / 下一件」排成一条带子（看图胶片带的同一套 useStrip）。
// - 三页都是真实内容：打开这一件时上一件、下一件就已经渲染好（数据也随之取好），拖出来看到的就是它们本身，不是骨架；
//   邻件不可交互（inert），也不登记撤销锚点。页按 key 挂在同一个父元素下，切换只是改它们的位置，不重新挂载。
// - 左右拖 / 妙控板双指横滑：带子跟手（位移直接写 transform，拖动中不触发 React 渲染）；松手按位移与速度决定切换还是弹回，
//   两端没有邻件时越拖越沉。决定切换时从当前位移、带当前速度用弹簧走到邻件，走完的那一刻才交给抽屉切换（onNav），
//   邻件变成这一件、带子位置在同一帧换参照归零，屏幕上不动。
// - 按钮、← →、盖印后自动翻到下一件：k 直接变了，带子从原位置换参照后弹过来；动画中途再变也接着当前速度走。
// - 动画中途再按住：停在手指下面接着拖。
// render(key)：画任意一件（这一件和邻件同一个函数）。dir：k 不是邻件时（跳着前进）从哪边来。
export function Pager({ k, dir, prev, next, onNav, render }: { k: string; dir: number; prev?: string; next?: string; onNav: (d: 1 | -1) => boolean | void; render: (key: string) => ReactNode }) {
  const bind = useContext(PagerBind)
  const [root, setRoot] = useState<HTMLDivElement | null>(null)
  const track = useRef<HTMLDivElement>(null)
  const [W, setW] = useState(0)
  const GAP = 16
  useLayoutEffect(() => {
    if (!root) return
    const measure = () => setW(root.clientWidth + GAP)
    measure()
    const ro = new ResizeObserver(measure)
    ro.observe(root)
    return () => ro.disconnect()
  }, [root])
  const [st, setSt] = useState<{ k: string; leave: Leave | null }>({ k, leave: null })
  const lastNb = useRef({ prev, next })
  const pendingShift = useRef(0)
  const nb = useRef({ prev, next, leave: st.leave })
  nb.current = { prev, next, leave: st.leave }
  const navRef = useRef(onNav)
  navRef.current = onNav
  const strip = useStrip(W, {
    name: 'pager',
    limit: () => [nb.current.prev || nb.current.leave?.side === -1 ? -W : 0, nb.current.next || nb.current.leave?.side === 1 ? W : 0],
    land: (page, v) => {
      if (page === 0) return strip.snapTo(0, v)
      const d = page > 0 ? 1 : -1
      // 先走到邻件，走完才切换：切换前后屏幕上不动
      strip.snapTo(page, v, () => {
        glog({ k: 'nav', d })
        if (navRef.current(d) === false && !strip.dragging()) strip.snapTo(0)
      })
    },
    onRest: () => setSt((s) => (s.leave && Math.abs(strip.pos.get()) < 1 ? { ...s, leave: null } : s)),
  })
  // 带子位置直接写 transform（同步，不等下一帧）：换参照的那一帧页的新位置和带子的新位置一起生效
  useLayoutEffect(() => {
    const el = track.current
    if (!el) return
    const write = (v: number) => (el.style.transform = `translate3d(${-v}px,0,0)`)
    write(strip.pos.get())
    return strip.pos.on('change', write)
  }, [strip])
  // k 变了：在渲染时就算出新旧关系（新的这一件原来在哪边），布局生效前换参照
  if (st.k !== k) {
    const old = st.k
    const adj = k === lastNb.current.next ? 1 : k === lastNb.current.prev ? -1 : 0
    const d = adj || (dir < 0 ? -1 : 1)
    pendingShift.current = old && k ? d : 0
    setSt({ k, leave: old && k && !adj ? { k: old, side: -d } : null })
  }
  useLayoutEffect(() => {
    lastNb.current = { prev, next }
  })
  useLayoutEffect(() => {
    const d = pendingShift.current
    pendingShift.current = 0
    glog({ k: 'key', key: st.k, d })
    if (!d) {
      strip.stop()
      strip.pos.set(0)
      return
    }
    strip.shift(rebase(0, d, W), 0)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [st.k])
  useEffect(() => {
    bind?.({ begin: strip.begin, move: strip.move, end: (vx) => strip.end(-vx) })
    return () => bind?.(null)
  }, [bind, strip])
  // 妙控板：一段手势开头落在能横向滚动的代码块、表格上时，这一整段都交给它横滚；否则整段归带子
  useEffect(() => {
    if (!root) return
    let last = 0
    let owner: 'scroll' | 'strip' = 'strip'
    const onWheel = (e: WheelEvent) => {
      if (e.defaultPrevented || e.ctrlKey) return
      const { dx, dy } = wheelDelta(e)
      const now = performance.now()
      if (now - last > G.wheelIdle && !strip.dragging()) owner = Math.abs(dx) > Math.abs(dy) && scrollsX(e.target, dx, root) ? 'scroll' : 'strip'
      last = now
      if (owner === 'scroll') return
      if (strip.wheel(dx, dy, e.timeStamp)) e.preventDefault()
    }
    root.addEventListener('wheel', onWheel, { passive: false })
    return () => root.removeEventListener('wheel', onWheel)
  }, [root, strip])
  const slots: { key: string; off: number }[] = []
  if (prev) slots.push({ key: prev, off: -1 })
  if (k) slots.push({ key: k, off: 0 })
  if (next) slots.push({ key: next, off: 1 })
  const lv = st.leave
  if (lv && !slots.some((s) => s.key === lv.k || s.off === lv.side)) slots.push({ key: lv.k, off: lv.side })
  return (
    <div ref={setRoot} className="ks-pager relative flex min-h-0 flex-1 flex-col overflow-hidden">
      <div ref={track} className="absolute inset-0 will-change-transform">
        {slots.map((s) => (
          <div
            key={s.key}
            data-page={s.key}
            className="absolute inset-0 flex min-h-0 flex-col bg-surface"
            style={{ transform: `translate3d(${s.off * W}px,0,0)`, visibility: s.off && !W ? 'hidden' : undefined }}
            inert={s.off !== 0}
            aria-hidden={s.off !== 0 || undefined}
          >
            <Leaving value={s.off !== 0}>{render(s.key)}</Leaving>
          </div>
        ))}
      </div>
    </div>
  )
}

// ---------- 列表左滑 ----------

export type RowAction = { label: ReactNode; icon?: ReactNode; tone: 'primary' | 'plain'; run: () => void }

let openRow: { close: () => void; el: HTMLElement } | null = null
if (typeof document !== 'undefined')
  document.addEventListener(
    'touchstart',
    (e) => {
      // 点到别处：收起开着的那一行
      if (openRow && !openRow.el.contains(e.target as Node)) openRow.close()
    },
    { passive: true, capture: true },
  )

const ACT_W = 84

// SwipeRow：列表条目左滑露出快捷操作（D-491：「通过 / 退回」，再点一下才执行）。只接触屏；
// 过 G.rowOpen 或左甩松手停在打开，右滑或点条目本身收起；同一时间只开一行。
export function SwipeRow({ actions, disabled, children, className, spotKey }: { actions: RowAction[]; disabled?: boolean; children: ReactNode; className?: string; spotKey?: string }) {
  const [el, setEl] = useState<HTMLDivElement | null>(null)
  const x = useMotionValue(0)
  const reveal = useTransform(x, (v) => Math.max(0, -v))
  const anim = useRef<AnimationPlaybackControls | null>(null)
  const [opened, setOpened] = useState(false)
  // 只在拖开时裁剪（收着时不裁掉卡片的阴影和选中描边）
  const [clip, setClip] = useState(false)
  useMotionValueEvent(x, 'change', (v) => setClip(Math.abs(v) > 0.5))
  const full = ACT_W * actions.length
  const to = (target: number, v = 0) => {
    anim.current?.stop()
    anim.current = springTo(x, target, v)
    const open = target !== 0
    setOpened(open)
    if (open && el) {
      if (openRow && openRow.el !== el) openRow.close()
      openRow = { el, close: () => to(0) }
    } else if (openRow?.el === el) openRow = null
  }
  const self = useRef(to)
  self.current = to
  useEffect(() => {
    if (disabled && x.get() !== 0) self.current(0)
  }, [disabled, x])
  useEffect(() => () => void (openRow?.el === el && (openRow = null)), [el])
  useDrag<{ from: number; edge: boolean }>(disabled || !actions.length ? null : el, {
    start: (p) => {
      if (!p.touch || p.target.closest('[data-row-act]')) return null
      anim.current?.stop()
      return { from: x.get(), edge: p.x0 < G.edge }
    },
    // 只接横向；收着的时候往右滑不接，左缘起手留给浏览器返回
    lock: (s, axis, dx) => axis === 'x' && !s.edge && !(s.from === 0 && dx > 0),
    early: (s, dx, dy) => !s.edge && Math.abs(dx) >= 3 && Math.abs(dx) > Math.abs(dy) * G.ratio && !(s.from === 0 && dx > 0),
    move: (s, dx) => {
      let v = s.from + dx
      if (v > 0) v = rub(v, 120)
      if (v < -full) v = -full - rub(-full - v, 120)
      x.set(v)
    },
    end: (_s, v) => {
      const cur = x.get()
      if (v.x > G.fling) to(0, v.x)
      else if (v.x < -G.fling || cur < -G.rowOpen) to(-full, v.x)
      else to(0, v.x)
    },
  })
  return (
    <div ref={setEl} data-spot-key={spotKey} className={cx('relative touch-pan-y rounded-[14px] transition-[opacity,filter] duration-200', clip && 'overflow-hidden', className)}>
      <motion.div className="absolute inset-y-0 right-0 flex overflow-hidden" style={{ width: reveal }} aria-hidden={!opened}>
        {actions.map((a, i) => (
          <button
            key={i}
            type="button"
            data-row-act=""
            tabIndex={opened ? 0 : -1}
            onClick={(e) => {
              e.stopPropagation()
              to(0)
              a.run()
            }}
            className={cx(
              'flex h-full flex-1 flex-col items-center justify-center gap-1 text-[13px] font-medium whitespace-nowrap [&_svg]:size-[18px]',
              a.tone === 'primary' ? 'bg-seal text-seal-fg' : 'bg-sunken text-fg-2',
            )}
          >
            {a.icon}
            {a.label}
          </button>
        ))}
      </motion.div>
      <motion.div
        style={{ x }}
        className="relative rounded-[inherit]"
        onClickCapture={(e) => {
          // 开着的时候点条目本身：先收起，不打开
          if (x.get() === 0) return
          e.preventDefault()
          e.stopPropagation()
          to(0)
        }}
      >
        {children}
      </motion.div>
    </div>
  )
}
