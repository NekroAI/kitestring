// 手势的纯逻辑（R-73）：参数、松手判定、妙控板一段手势的聚合。不依赖 DOM / React，单测覆盖（gesture.test.ts）。

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
  wheelIdle: 140, // ms：妙控板事件停这么久 = 这一段手势结束
  wheelLock: 1200, // ms：一段手势判定之后，惯性尾巴最长按这么久吞掉
  wheelFresh: 24, // px：锁定到期后，力度到这么大才算新的一次滑动
} as const

export const clamp = (v: number, lo: number, hi: number) => Math.min(hi, Math.max(lo, v))

// rub：越界阻尼（拖过头时越拖越沉），d 为越界距离、dim 为尺度。
export const rub = (d: number, dim: number) => (1 - 1 / ((d * 0.55) / dim + 1)) * dim

// decide：松手时走一页还是弹回。offset / velocity 正数是往下一页；往回甩 = 取消。返回 -1 / 0 / 1。
export function decide(offset: number, velocity: number, W: number, wheel = false): -1 | 0 | 1 {
  const fling = wheel ? G.flingWheel : G.fling
  let d: -1 | 0 | 1 = velocity > fling ? 1 : velocity < -fling ? -1 : offset > W * G.commit ? 1 : offset < -W * G.commit ? -1 : 0
  if (d && Math.abs(offset) > 4 && Math.sign(offset) !== d) d = 0
  return d
}

// rebase：带子换了基准页（这一件变成了上一件 / 下一件）之后的位置——屏幕上不动，只换参照。
export const rebase = (pos: number, d: number, W: number) => pos - d * W

export type WheelAct =
  | { t: 'pass' } // 不归翻页（竖向为主、又不在一段横滑里）
  | { t: 'swallow' } // 这一段已经判定过，惯性尾巴（含反向的小值）吞掉，不再驱动位移
  | { t: 'begin'; offset: number } // 新的一段手势。offset 和触屏的手指位移同义：-ΣdeltaX，负数 = 往下一页
  | { t: 'move'; offset: number } // 手指还在：带子跟到 offset
  | { t: 'release'; offset: number; velocity: number } // 判定时刻（手指离开进入惯性，或走满一页）；velocity 是 offset 的变化速度（px/s）

// WheelSegment：把妙控板的一串 wheel 事件聚合成「一段手势」。浏览器不告诉我们手指什么时候离开，
// 只能从力度走势推断：
// - 一段的开始：没有正在进行的一段（或上一段停了 G.wheelIdle 以上）时，横向为主的事件开一段；竖向为主的不接管。
// - 手指阶段：每个事件的 deltaX 累加成位移，带子 1:1 跟着走（方向反过来就往回走，不算新手势）。
// - 判定：力度连续回落、明显低于峰值（手指离开、进入惯性），或走满一页，就判定一次（release），之后这一段剩下的
//   惯性事件全部吞掉——不再驱动位移，反向的小值也不会把带子拉回去，一段手势最多切换一次。
// - 结束：事件停 G.wheelIdle（idle()），或判定后过了 G.wheelLock 又来了明显的新力度，才开新的一段。
export class WheelSegment {
  private active = false
  private done = false
  private lastT = 0
  private doneT = 0
  private acc = 0
  private peak = 0
  private prev = 0
  private drops = 0
  private samples: { t: number; v: number }[] = []

  feed(dx: number, dy: number, t: number, W: number): WheelAct {
    const a = Math.abs(dx)
    if (this.active && t - this.lastT > G.wheelIdle) this.active = false // 停过了：上一段已经结束
    if (this.active && this.done && t - this.doneT > G.wheelLock && a >= G.wheelFresh) this.active = false // 锁定到期又起新力度
    if (this.active) {
      this.lastT = t
      if (this.done) return { t: 'swallow' }
      this.acc -= dx
      this.push(t)
      if (a < this.prev) this.drops++
      else if (a > this.prev * 1.05 + 0.5) this.drops = 0
      if (a > this.peak) this.peak = a
      this.prev = a
      if (Math.abs(this.acc) >= W - 1 || (this.drops >= 4 && a < this.peak * 0.6)) return this.release(t)
      return { t: 'move', offset: this.acc }
    }
    if (a <= Math.abs(dy) || a === 0) return { t: 'pass' }
    this.active = true
    this.done = false
    this.lastT = t
    this.acc = -dx
    this.peak = this.prev = a
    this.drops = 0
    this.samples = []
    this.push(t)
    return { t: 'begin', offset: this.acc }
  }

  // idle：事件停了 G.wheelIdle（由调用方的计时器调用）。还没判定的这一段在这里判定。
  idle(t: number): WheelAct | null {
    if (!this.active) return null
    this.active = false
    if (this.done) return null
    return this.release(t)
  }

  get busy() {
    return this.active && !this.done
  }

  reset() {
    this.active = false
    this.done = false
  }

  private push(t: number) {
    this.samples.push({ t, v: this.acc })
    while (this.samples.length > 2 && t - this.samples[0].t > 100) this.samples.shift()
  }

  private release(t: number): WheelAct {
    this.done = true
    this.doneT = t
    const [a, b] = [this.samples[0], this.samples[this.samples.length - 1]]
    const velocity = b && a && b.t > a.t ? ((b.v - a.v) / (b.t - a.t)) * 1000 : 0
    return { t: 'release', offset: this.acc, velocity }
  }
}
