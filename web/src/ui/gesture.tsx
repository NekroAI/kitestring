import { createContext, useContext, useEffect, useLayoutEffect, useRef, useState, type ReactNode } from 'react'
import { animate, motion, useMotionValue, useMotionValueEvent, useTransform, type AnimationPlaybackControls, type MotionValue, type Transition } from 'motion/react'
import { cx } from '../lib/util'

// 全站手势（R-73，D-491 定稿）：一套参数、一种弹簧、按下即打断。
// - 所有跟手的动画（看图胶片带、抽屉切换上一件 / 下一件、抽屉下拉 / 右拖关闭、列表左滑、长按菜单面板）都用 spring()，
//   松手时的速度带进弹簧；动画进行中手指一按下，动画停在当前位置，手势从这里接着走（像 macOS 的可中断动画）。
// - 长按（菜单、拖拽、提示）统一 G.longPress；判定方向前先过 G.slop 死区。
// - 触屏横滑在屏幕左缘 G.edge 内起手时不接，留给浏览器的侧滑返回。
// - 妙控板双指横滑全站归纸鸢（installWheelGuard）：抽屉 / 看图里是上一件 / 下一件，能横向滚动的区域照常横滚，其它地方吃掉。
// 系统开了「减少动态效果」时不做弹簧动画（直接到位，和全站 CSS 动效的处理一致），手势本身照常跟手。

export const G = {
  stiffness: 380,
  damping: 38,
  longPress: 450, // ms：长按菜单、触屏拖拽浮起、长按提示同一个
  slop: 8, // px：判定方向前的死区
  ratio: 1.2, // 横向：|dx| > |dy| × 1.2
  edge: 20, // px：屏幕左缘这么宽内起手的横滑留给浏览器返回
  fling: 400, // px/s：甩动门槛
  flingWheel: 1500, // px/s：触控板日常滑动本身就快，甩动门槛要高
  commit: 0.22, // 切换：拖过宽度 22% 就算
  closeDist: 120, // px：下拉 / 右拖关闭的距离
  closeV: 600, // px/s：或甩得够快
  rowOpen: 64, // px：列表左滑过这么多松手停在打开
  lockMs: 800, // 触控板一次滑动落定后的锁定（惯性滚动不再翻页）
} as const

export const reducedMotion = () => typeof matchMedia !== 'undefined' && matchMedia('(prefers-reduced-motion: reduce)').matches

// spring：全站唯一一种弹簧（刚度 380、阻尼 38，接近临界阻尼、几乎不回弹）；velocity 是松手时的速度。减少动态效果时直接到位。
export function spring(velocity = 0): Transition {
  return reducedMotion() ? { duration: 0 } : { type: 'spring', stiffness: G.stiffness, damping: G.damping, velocity }
}

// springTo：把一个值用这种弹簧带到 to；返回的控制可以 stop()（按下即打断）。
export const springTo = (v: MotionValue<number>, to: number, velocity = 0, onComplete?: () => void): AnimationPlaybackControls =>
  animate(v, to, { ...(spring(velocity) as object), onComplete } as never)

// rub：越界阻尼（拖过头时越拖越沉），d 为越界距离、dim 为尺度。
export const rub = (d: number, dim: number) => (1 - 1 / ((d * 0.55) / dim + 1)) * dim

export const clamp = (v: number, lo: number, hi: number) => Math.min(hi, Math.max(lo, v))

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

// 触控板没有“松手”事件，从滚轮力度的走势推断（R-69、R-121）。
// last / peak / rises / drops：力度走势；done：已决定翻页（松手或走满一页）的时刻，之后的惯性忽略
type Gesture = { from: number; page: number; acc: number; samples: { t: number; v: number }[]; idle?: number; done?: number; wheel?: boolean; last: number; peak: number; rises: number; drops: number }

export type Strip = {
  pos: MotionValue<number>
  snapTo: (page: number, velocity?: number) => void
  go: (d: number) => void
  begin: () => void
  move: (dx: number) => void
  end: (velocity?: number) => void
  wheel: (dx: number, dy: number) => boolean
  dragging: () => boolean
  stop: () => void
}

// useStrip：一条按页排的带子（第 k 页在 k × W）。手指 / 触控板拖多少，带子就跟着走多少；松手按位移与速度决定翻页还是弹回；
// 一次手势最多走一页，弹簧动画随时可被新的手势打断、从当前位置接着拖。
// limit：带子能走的范围 [lo, hi]（px），超出部分越拖越沉；land：决定落到哪一页（默认直接弹过去）；onRest：动画停下。
export function useStrip(W: number, o: { limit?: () => [number, number] | null; land?: (page: number, velocity: number) => void; onRest?: () => void } = {}): Strip {
  const pos = useMotionValue(0)
  const w = useRef(W)
  w.current = W || 1
  const opts = useRef(o)
  opts.current = o
  const anim = useRef<AnimationPlaybackControls | null>(null)
  const aim = useRef<number | null>(null) // 正在弹向的那一页：动画中途再翻页时从它往后数
  const gesture = useRef<Gesture | null>(null)
  const ref = useRef<Strip>(null!)
  if (!ref.current) {
    const stop = () => {
      anim.current?.stop()
      anim.current = null
    }
    const snapTo = (page: number, velocity = 0) => {
      stop()
      aim.current = page
      anim.current = springTo(pos, page * w.current, velocity, () => {
        aim.current = null
        anim.current = null
        opts.current.onRest?.()
      })
    }
    const land = (page: number, velocity: number) => (opts.current.land ? opts.current.land(page, velocity) : snapTo(page, velocity))
    const go = (d: number) => land((aim.current ?? Math.round(pos.get() / w.current)) + d, 0)
    // 拖动开始：打断正在进行的动画，从当前位置接着走；以动画正要去的那一页为基准（连续翻页不丢页）
    const begin = () => {
      stop()
      const from = pos.get()
      const page = aim.current ?? Math.round(from / w.current)
      aim.current = null
      gesture.current = { from, page, acc: 0, samples: [{ t: performance.now(), v: from }], last: 0, peak: 0, rises: 0, drops: 0 }
    }
    // dx：手指 / 触控板相对开始时的水平位移（向左为负，即往下一页）。带子最多走到基准页的前后一页
    const move = (dx: number) => {
      const g = gesture.current
      if (!g || g.done !== undefined) return
      const W = w.current
      let v = clamp(g.from - dx, Math.min(g.from, (g.page - 1) * W), Math.max(g.from, (g.page + 1) * W))
      const lim = opts.current.limit?.()
      if (lim) {
        const [lo, hi] = [Math.min(lim[0], g.from), Math.max(lim[1], g.from)]
        if (v < lo) v = lo - rub(lo - v, W)
        else if (v > hi) v = hi + rub(v - hi, W)
      }
      pos.set(v)
      g.samples.push({ t: performance.now(), v })
      if (g.samples.length > 6) g.samples.shift()
    }
    // 松手：按这次手势走了多少与速度决定翻页还是弹回；往回甩 = 取消。velocity：触屏给的松手速度（px/s，正数是往下一页）
    const decide = (g: Gesture, given?: number) => {
      const [a, b] = [g.samples[0], g.samples[g.samples.length - 1]]
      const vel = given ?? (b.t > a.t ? ((b.v - a.v) / (b.t - a.t)) * 1000 : 0)
      const off = pos.get() - g.from
      const fling = g.wheel ? G.flingWheel : G.fling
      const W = w.current
      let d = vel > fling ? 1 : vel < -fling ? -1 : off > W * G.commit ? 1 : off < -W * G.commit ? -1 : 0
      if (d && Math.abs(off) > 4 && Math.sign(off) !== d) d = 0
      const lim = opts.current.limit?.()
      const page = g.page + d
      if (lim && (page * W < lim[0] - 1 || page * W > lim[1] + 1)) return snapTo(g.page, vel)
      land(page, vel)
    }
    const end = (velocity?: number) => {
      const g = gesture.current
      gesture.current = null
      if (!g) return
      window.clearTimeout(g.idle)
      if (g.done === undefined) decide(g, velocity)
    }
    // 触控板：力度连续几下回落（手指离开、进入惯性）就当松手，立即决定翻页还是弹回，余下的惯性忽略；走满一页也立即落定；
    // 滚轮停下 120ms 同样视为结束。回落之后力度又连续回升（新的一次滑动起势）、或方向反过来，就当成新手势；落定后的锁定最多 G.lockMs。
    // 返回 true 表示这次滚轮归翻页（竖向为主、又不在翻页手势里的不归）。
    const wheel = (dx: number, dy: number): boolean => {
      let g = gesture.current
      if (g && !g.wheel) return true // 触屏正在拖
      const a = Math.abs(dx)
      if (g) {
        g.rises = a > g.last + 1 ? g.rises + 1 : 0
        const dir = Math.sign(g.samples[g.samples.length - 1].v - g.from)
        const fresh =
          (g.done !== undefined && a >= 4 && (g.rises >= 2 || a >= g.last * 3 + 6)) || // 回落后又起势
          (dir !== 0 && Math.sign(dx) === -dir && a >= 3) || // 方向反过来
          (g.done !== undefined && performance.now() - g.done > G.lockMs) // 落定后的锁定到期
        if (fresh) {
          end()
          g = null
        } else {
          g.drops = a < g.last ? g.drops + 1 : 0
          if (a > g.peak) g.peak = a
          g.last = a
        }
      }
      if (!g) {
        if (a <= Math.abs(dy)) return false
        begin()
        g = gesture.current!
        g.wheel = true
        g.last = g.peak = a
      }
      const cur = g
      window.clearTimeout(cur.idle)
      cur.idle = window.setTimeout(() => gesture.current === cur && end(), 120)
      if (cur.done !== undefined) return true
      cur.acc -= dx
      move(cur.acc)
      const p = pos.get()
      const W = w.current
      if (p >= (cur.page + 1) * W - 1 || p <= (cur.page - 1) * W + 1) {
        // 走满一页
        cur.done = performance.now()
        land(cur.page + Math.sign(p - cur.page * W), 0)
      } else if (cur.drops >= 3 && a < cur.peak * 0.7) {
        // 松手进入惯性
        cur.done = performance.now()
        decide(cur)
      }
      return true
    }
    ref.current = { pos, snapTo, go, begin, move, end, wheel, dragging: () => !!gesture.current && gesture.current.done === undefined, stop }
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
        if (Math.hypot(dx, dy) < G.slop) return
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

// Leaving：Pager 里刚离开的那一件（正滑出去）。撤销条不再把它当作锚点，撤回立刻挪到顶部浮动条（R-201）
export const Leaving = createContext(false)

// PagerBind：Panel 提供，里面的 Pager 登记自己
export const PagerBind = createContext<((h: PagerHandle | null) => void) | null>(null)

type Leave = { k: string; node: ReactNode; side: number }

// Pager：抽屉内容按「上一件 / 这一件 / 下一件」排成一条带子（看图胶片带的同一套 useStrip）。
// 左右拖跟手，邻件露出（还没打开过的邻件先露出它的标题 peek，松手切过去后换成完整内容）；松手按位移与速度决定切换还是弹回，
// 两端没有邻件时越拖越沉。切换由 onNav 交给抽屉（改地址栏参数），k 变了之后带子从刚才的位置接着弹到新的这一件；
// 按钮、← →、盖印后自动翻到下一件也走同一条带子。动画中途再按住：停在手指下面接着拖，往回拖能回到刚离开的那一件。
// 妙控板双指横滑同样是切换（非被动 wheel 监听，横向为主时 preventDefault，不触发浏览器前进 / 后退）。
// dir：k 是怎么变过来的（-1 上一件，1 下一件 / 盖印后前进）。
export function Pager({ k, dir, prev, next, onNav, peek, children }: { k: string; dir: number; prev: boolean; next: boolean; onNav: (d: 1 | -1) => void; peek: (d: 1 | -1) => ReactNode; children: ReactNode }) {
  const bind = useContext(PagerBind)
  const [root, setRoot] = useState<HTMLDivElement | null>(null)
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
  const last = useRef<ReactNode>(children)
  const shift = useRef(0)
  const flingV = useRef(0)
  const waiting = useRef(0)
  const nb = useRef({ prev, next })
  nb.current = { prev, next }
  const navRef = useRef(onNav)
  navRef.current = onNav
  const strip = useStrip(W, {
    limit: () => [nb.current.prev || st.leave?.side === -1 ? -W : 0, nb.current.next || st.leave?.side === 1 ? W : 0],
    land: (page, v) => {
      if (page === 0) return strip.snapTo(0, v)
      flingV.current = v
      navRef.current(page > 0 ? 1 : -1)
      // 抽屉没有切过去（例如那一件刚被处理掉）：弹回
      window.cancelAnimationFrame(waiting.current)
      waiting.current = window.requestAnimationFrame(() => window.requestAnimationFrame(() => strip.snapTo(0, 0)))
    },
    onRest: () => setSt((s) => (s.leave && Math.abs(strip.pos.get()) < 1 ? { ...s, leave: null } : s)),
  })
  // k 变了：刚才那一件留在原地变成「离开的那一件」，新的这一件接在它旁边，带子从原位置弹过来
  if (st.k !== k) {
    const d = dir < 0 ? -1 : 1
    const real = !!st.k && !!k
    shift.current = real ? d : 0
    setSt({ k, leave: real ? { k: st.k, node: last.current, side: -d } : null })
  }
  useLayoutEffect(() => {
    last.current = children
  })
  useLayoutEffect(() => {
    window.cancelAnimationFrame(waiting.current)
    const d = shift.current
    if (!d) {
      strip.stop()
      strip.pos.set(0)
      return
    }
    shift.current = 0
    strip.stop()
    strip.pos.set(strip.pos.get() - d * W)
    strip.snapTo(0, flingV.current)
    flingV.current = 0
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [st.k])
  const trackX = useTransform(strip.pos, (v) => -v)
  // 交给抽屉的拖动入口；触控板横滑在这里接
  useEffect(() => {
    bind?.({ begin: strip.begin, move: strip.move, end: (vx) => strip.end(-vx) })
    return () => bind?.(null)
  }, [bind, strip])
  useEffect(() => {
    if (!root) return
    const onWheel = (e: WheelEvent) => {
      if (e.defaultPrevented || e.ctrlKey) return
      const { dx, dy } = wheelDelta(e)
      if (!strip.dragging() && Math.abs(dx) > Math.abs(dy) && scrollsX(e.target, dx, root)) return // 能横滚的代码块、表格照常横滚
      if (strip.wheel(dx, dy)) e.preventDefault()
    }
    root.addEventListener('wheel', onWheel, { passive: false })
    return () => root.removeEventListener('wheel', onWheel)
  }, [root, strip])
  const page = 'absolute inset-0 flex min-h-0 flex-col bg-surface'
  const leave = st.leave
  return (
    <div ref={setRoot} className="relative flex min-h-0 flex-1 flex-col overflow-hidden">
      <motion.div className="absolute inset-0" style={{ x: trackX }}>
        {leave && (
          <div key={leave.k} className={page} style={{ transform: `translateX(${leave.side * W}px)` }} aria-hidden>
            <Leaving value>{leave.node}</Leaving>
          </div>
        )}
        <div key={k} className={page}>
          <Leaving value={false}>{children}</Leaving>
        </div>
        {([-1, 1] as const).map((d) =>
          (d < 0 ? prev : next) && leave?.side !== d ? (
            <div key={`peek${d}`} className={cx(page, 'pointer-events-none')} style={{ transform: `translateX(${d * W}px)` }} aria-hidden>
              {peek(d)}
            </div>
          ) : null,
        )}
      </motion.div>
    </div>
  )
}

// PeekPage：邻件还没打开时露出来的样子——顶栏位置写它是什么、标题，下面几行淡色占位。
export function PeekPage({ kind, title }: { kind?: ReactNode; title: ReactNode }) {
  return (
    <div className="flex min-h-0 flex-1 flex-col px-5 pt-4">
      {kind && <div className="text-xs font-semibold tracking-wider text-fg-3">{kind}</div>}
      <div className="mt-2 line-clamp-2 font-display text-[19px] leading-snug text-fg">{title}</div>
      <div className="mt-5 space-y-2.5">
        <div className="h-3 w-11/12 rounded-full bg-sunken" />
        <div className="h-3 w-4/5 rounded-full bg-sunken" />
        <div className="h-3 w-2/3 rounded-full bg-sunken" />
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
export function SwipeRow({ actions, disabled, children, className }: { actions: RowAction[]; disabled?: boolean; children: ReactNode; className?: string }) {
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
    <div ref={setEl} className={cx('relative rounded-[14px]', clip && 'overflow-hidden', className)}>
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
