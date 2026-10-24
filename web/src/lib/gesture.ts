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
  wheelGap: 40, // ms：判定之后（惯性阶段）事件停顿这么久，下一下就是新的一次滑动（惯性事件是连续的，约 7～8ms 一个）
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
  | { t: 'restart'; offset: number; velocity: number } // 上一段就此判定（velocity 是它的速度），这一下开始新的一段（offset 是新一段的）
  | { t: 'release'; offset: number; velocity: number } // 判定时刻（手指离开进入惯性，或走满一页）；velocity 是 offset 的变化速度（px/s）

// WheelSegment：把妙控板的一串 wheel 事件聚合成「一段手势」。浏览器不告诉我们手指什么时候离开，
// 只能从力度走势推断：
// - 一段的开始：没有正在进行的一段（或上一段停了 G.wheelIdle 以上）时，横向为主的事件开一段；竖向为主的不接管。
// - 手指阶段：每个事件的 deltaX 累加成位移，带子 1:1 跟着走（方向反过来就往回走，不算新手势）。
// - 判定：走满一页，或者惯性基本停了（力度连续降到峰值一成以下），或者事件停了 G.wheelIdle，就判定一次（release），
//   之后这一段剩下的惯性事件全部吞掉——不再驱动位移，反向的小值也不会把带子拉回去，一段手势最多切换一次。
// - 结束：事件停 G.wheelIdle（idle()）；判定之后，停顿 G.wheelGap、方向反过来、或衰减中又连续起势（用户在惯性没停时开始了
//   下一次滑动），都算新的一段；兜底：判定后过了 G.wheelLock 又来了明显的新力度。
export class WheelSegment {
  private active = false
  private done = false
  private lastT = 0
  private doneT = 0
  private acc = 0
  private peak = 0
  private prev = 0
  private prev2 = 0
  private drops = 0
  private samples: { t: number; v: number }[] = []
  private dir = 0 // 这一段现在的方向（deltaX 的符号）
  private tailMin = 0 // 判定之后惯性尾巴里最小的力度
  private tailPrev = 0
  private rises = 0

  // fresh：判定之后（惯性阶段）来的这一下是不是用户新的一次滑动。真机上用户常常在上一次惯性还没停时就开始下一次，
  // 事件流中间没有停顿（R-73 真机日志）：手指一按上，惯性立刻停，力度骤降到 1～3 再重新起势；或者方向直接反过来。
  private fresh(dx: number, t: number) {
    const a = Math.abs(dx)
    if (t - this.lastT > G.wheelGap) {
      // 停了一下：力度明显的就是新的一次；只有 1～2px 的，可能是很慢的惯性尾巴（真机上越到最后事件越稀，
      // R-73 第三轮日志里一串 -1 中间隔着 50～80ms），重新从这里看它会不会起势
      if (a >= 3) return true
      this.tailMin = this.tailPrev = a
      this.rises = 0
      return false
    }
    if (a >= 3 && Math.sign(dx) === -this.dir) return true // 方向反过来（真机的惯性不会反向；1～2 的反向小抖动不算）
    if (a >= this.tailPrev * 2 + 4) return true // 衰减中突然一大下（1 → 9）：手指重新推起来了
    this.rises = a > this.tailPrev ? this.rises + 1 : a === this.tailPrev ? this.rises : 0 // 持平不算起势也不清零
    this.tailPrev = a
    if (a < this.tailMin) this.tailMin = a
    return this.rises >= 3 && a >= this.tailMin + 3 // 衰减中又连续起势
  }

  feed(dx: number, dy: number, t: number, W: number): WheelAct {
    const a = Math.abs(dx)
    if (this.active && t - this.lastT > G.wheelIdle) this.active = false // 停过了：上一段已经结束
    if (this.active && this.done && t - this.doneT > G.wheelLock && a >= G.wheelFresh) this.active = false // 锁定到期又起新力度
    if (this.active && this.done && a > 0 && this.fresh(dx, t)) this.active = false
    if (this.active) {
      this.lastT = t
      if (this.done) return { t: 'swallow' }
      // 还在快速滑动（惯性中）时方向突然反过来：是用户新的一次反向滑动，这一段就此判定，下一下开始新的一段。
      // 手指自己往回拖（想取消）时会先慢下来再反向，不走这里，照常跟着往回走。
      // 新手势优先：这一下就是新一段的第一下（不先让上一段的弹簧往前走一帧再接住）
      if (a >= 3 && Math.sign(dx) === -this.dir && this.prev >= 8) {
        const r = this.release(t) as { offset: number; velocity: number }
        this.start(dx, t)
        return { t: 'restart', offset: this.acc, velocity: r.velocity }
      }
      // 惯性被手指按停：力度在两下之内从 10 以上骤降到三成以下（惯性本身是平滑衰减的）——用户开始了下一次滑动，这一段就此判定
      if (this.prev2 >= 10 && a <= this.prev2 * 0.3 && a <= this.prev * 0.6) {
        this.prev2 = this.prev = a
        return this.release(t)
      }
      this.acc -= dx
      this.push(t)
      // 手指往回拖（方向反过来）：从这里起是新的一段运动，峰值和回落从头算——否则反向刚起步的 1、3、4 会被当成惯性停了
      if (dx && Math.sign(dx) !== this.dir) {
        this.peak = a
        this.drops = 0
      }
      // 判定时刻：走满一页；或者惯性基本停了（力度连续几下降到峰值的一成以下）。手指阶段的减速、离手前的回落都不判定——
      // 真机上 Chrome 在离手后先有一段惯性起势（力度回升甚至一个尖峰）再衰减，在回落处判定会把起势误认成新的一次滑动，
      // 一次滑动切两件（R-73 真机日志）。惯性推动带子是跟手的一部分（方向不变，不会往回拉）。
      if (a > this.peak) this.peak = a
      this.drops = this.peak >= 6 && a <= Math.max(2, this.peak * 0.1) && Math.sign(dx) !== -this.dir ? this.drops + 1 : 0
      this.prev2 = this.prev
      this.prev = a
      if (dx) this.dir = Math.sign(dx) // 现在往哪边走（手指慢慢往回拖时跟着变）
      if (Math.abs(this.acc) >= W - 1 || this.drops >= 3) return this.release(t)
      return { t: 'move', offset: this.acc }
    }
    if (a <= Math.abs(dy) || a === 0) return { t: 'pass' }
    this.start(dx, t)
    return { t: 'begin', offset: this.acc }
  }

  private start(dx: number, t: number) {
    const a = Math.abs(dx)
    this.active = true
    this.done = false
    this.lastT = t
    this.acc = -dx
    this.peak = this.prev = a
    this.prev2 = 0
    this.drops = 0
    this.dir = Math.sign(dx)
    this.samples = []
    this.push(t)
  }

  // idle：事件停了 G.wheelIdle（由调用方的计时器调用）。还没判定的这一段在这里判定。
  // 只判定、不结束这一段：主线程忙时计时器可能比攒着的事件先到，这一段算不算结束只看事件自己的时间戳（feed 里）。
  idle(t: number): WheelAct | null {
    if (!this.active || this.done) return null
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
    this.tailMin = this.tailPrev = this.prev
    this.rises = 0
    const [a, b] = [this.samples[0], this.samples[this.samples.length - 1]]
    const velocity = b && a && b.t > a.t ? ((b.v - a.v) / (b.t - a.t)) * 1000 : 0
    return { t: 'release', offset: this.acc, velocity }
  }
}
