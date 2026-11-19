// 预览里的滚轮 / 触控板 / 触屏手势判别（R-378）。纯逻辑，不依赖 DOM，单测见 wheelKind.test.ts。
//
// 浏览器把三种输入都报成 wheel 事件：
// - 触控板双指捏合：带 ctrlKey 的 wheel（Chrome、Edge、Firefox；Safari 另发 gesturechange，调用方自己接）。
// - 鼠标滚轮：一格一大下。
// - 触控板双指滑动：一串小力度、间隔 7～16ms 的事件，起手很轻，后面跟惯性。
// 浏览器不直接说是哪种，只能按一段事件的第一下推断（WheelKind）。判别方法和取舍：
// 1. ctrlKey → 捏合（真的按住 Ctrl 转鼠标滚轮也算，效果一样是缩放）。
// 2. deltaMode 是按行 / 按页 → 鼠标（Windows、Linux 上的 Firefox 鼠标滚轮）。调用方要先读 deltaMode 再读 deltaX/Y：
//    Firefox 只在脚本先读 deltaMode 时才按行报。
// 3. 一段的第一下有横向分量（且没按 Shift）→ 触控板：普通鼠标滚轮只有竖向，Shift+滚轮才出横向。
// 4. 第一下的 wheelDeltaY（Chrome / Safari 的非标准字段）是 120 的整数倍 → 鼠标；触控板在这两家浏览器上 wheelDeltaY = -3 × deltaY，
//    起手几像素，凑不成 120 的倍数。
// 5. 第一下的力度（deltaY；Shift+滚轮时是 deltaX）是 4.000244140625 的整数倍 → 鼠标（macOS 上 Chrome 的鼠标滚轮固定报这个步长，触控板报整数）。
// 6. 第一下的力度就 ≥ 50px → 鼠标（Windows Chrome 一格 100 / 120，触控板起手不会这么大）。
// 7. 其它 → 触控板。
// 代价：带平滑滚动、一格只报几像素的高精度鼠标（如 MX 系列的自由滚轮）会被当成触控板——在可滚动区域里它照常滚页面，
// 在看大图时它不再缩放（改为放大后平移）；这类用户用捏合或双击缩放。宁可把少数鼠标当触控板，也不让触控板滑动去缩放。
// 一段之内类别不变（事件停了 KIND_GAP 才算新的一段），惯性尾巴不会被重新判别。

export type WheelKindName = 'pinch' | 'mouse' | 'trackpad'

export type WheelInput = {
  dx: number // 已换算成像素
  dy: number
  mode: number // deltaMode：0 像素、1 行、2 页
  ctrl: boolean
  shift?: boolean
  wdy?: number // wheelDeltaY（没有就不传）
  t: number // 时间戳 ms
}

export const KIND_GAP = 180 // ms：事件停这么久，下一下重新判别

const MAC_MOUSE_STEP = 4.000244140625

const isMultiple = (v: number, step: number) => {
  const k = Math.abs(v) / step
  return k >= 1 && Math.abs(k - Math.round(k)) < 1e-6
}

// firstKind：一段的第一下是鼠标还是触控板（不含捏合）。
export function firstKind(e: Omit<WheelInput, 't' | 'ctrl'>): 'mouse' | 'trackpad' {
  if (e.mode !== 0) return 'mouse'
  if (e.dx !== 0 && !e.shift) return 'trackpad'
  if (e.wdy && Math.abs(e.wdy) % 120 === 0) return 'mouse'
  const v = Math.abs(e.dx) > Math.abs(e.dy) ? e.dx : e.dy // Shift+滚轮时力度在横向
  if (!Number.isInteger(v) && isMultiple(v, MAC_MOUSE_STEP)) return 'mouse'
  if (Math.abs(v) >= 50) return 'mouse'
  return 'trackpad'
}

// WheelKind：按段记住类别。捏合每一下单独看（捏合和滑动可以在同一段里交替）。
export class WheelKind {
  private last = -Infinity
  private kind: 'mouse' | 'trackpad' = 'trackpad'
  feed(e: WheelInput): WheelKindName {
    const fresh = e.t - this.last > KIND_GAP
    this.last = e.t
    if (e.ctrl) return 'pinch'
    if (fresh) this.kind = firstKind(e)
    return this.kind
  }
}

// ---------- 各处预览怎么用 ----------

// 3D 查看：捏合、鼠标滚轮缩放（鼠标滚轮和改动前一样）；触控板双指滑动转模型。
// 嵌在能滚动的抽屉、页面里时（embedded），没点过模型（active）之前双指滑动照常滚页面，免得光标停在模型上就滚不动；
// 审片舞台这类不滚动的地方直接接管。
export type Wheel3D = 'zoom' | 'rotate' | 'pass'
export function wheel3d(kind: WheelKindName, o: { active: boolean; embedded: boolean }): Wheel3D {
  if (kind !== 'trackpad') return 'zoom'
  return o.active || !o.embedded ? 'rotate' : 'pass'
}

// 看大图（全屏）：捏合缩放；鼠标走原来的路（上下缩放、左右翻页 / 放大后平移）；
// 触控板双指滑动：放大后平移（跟手），没放大时左右翻页、上下不做事（全屏看图里没有可滚的页面，也不再拿来缩放）。
export type WheelImage = 'zoom' | 'mouse' | 'pan' | 'nav'
export function wheelImage(kind: WheelKindName, o: { zoomed: boolean }): WheelImage {
  if (kind === 'pinch') return 'zoom'
  if (kind === 'mouse') return 'mouse'
  return o.zoomed ? 'pan' : 'nav'
}

// 触控板滑动转模型：手指往右（deltaX 为负，系统默认的「自然」滚动方向）模型跟着往右转，和按住拖动一样。
// 返回相当于拖动了多少像素。
export const swipeAsDrag = (dx: number, dy: number) => ({ x: -dx, y: -dy })

// ---------- 触屏 ----------

// 3D 触屏：触点数 → 操作。一指旋转；两指捏合缩放、同时拖动平移；更多指不做事。
export type TouchOp = 'rotate' | 'zoomPan' | 'none'
export const touchOp = (n: number): TouchOp => (n === 1 ? 'rotate' : n === 2 ? 'zoomPan' : 'none')

// 双击（双指轻点）判定：两次轻点间隔 ≤ TAP_GAP、位置相距 ≤ TAP_DIST，且每次按下到抬起没怎么动、不太久。
export const TAP_GAP = 320 // ms
export const TAP_DIST = 28 // px
export const TAP_HOLD = 250 // ms：按住超过这么久不算轻点
export const TAP_SLOP = 10 // px：按下到抬起移动超过这么多不算轻点

export type Tap = { t: number; x: number; y: number }

// tapOf：一次按下 → 抬起是不是轻点；是就返回轻点（抬起时刻、按下位置）。
export function tapOf(down: Tap, up: Tap): Tap | null {
  if (up.t - down.t > TAP_HOLD) return null
  if (Math.hypot(up.x - down.x, up.y - down.y) > TAP_SLOP) return null
  return { t: up.t, x: down.x, y: down.y }
}

export const isDoubleTap = (prev: Tap | null, cur: Tap) => !!prev && cur.t - prev.t <= TAP_GAP && Math.hypot(cur.x - prev.x, cur.y - prev.y) <= TAP_DIST
