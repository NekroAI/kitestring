import { test } from 'node:test'
import assert from 'node:assert/strict'
import { decide, G, rebase, WheelSegment, type WheelAct } from './gesture.ts'

const W = 876

// 一段贴近真实的妙控板左滑：手指阶段力度先增后减，离手后惯性指数衰减，尾巴里夹着几个反向小值；事件间隔 16ms
function swipe(peak = 60, sign = 1) {
  const finger = [3, 8, 15, 24, 34, 44, 52, peak, peak, peak - 4, peak - 10]
  const out: number[] = finger.map((v) => v * sign)
  let v = peak - 14
  while (v > 1) {
    out.push(Math.round(v) * sign)
    v *= 0.9
  }
  out.push(-1 * sign, 0, -2 * sign, 1 * sign, -1 * sign) // 惯性尾巴里的反向小值
  return out
}

function run(seq: number[], seg = new WheelSegment(), t0 = 0, dy = 0) {
  const acts: WheelAct[] = []
  seq.forEach((dx, i) => acts.push(seg.feed(dx, dy, t0 + i * 16, W)))
  return { acts, seg, end: t0 + seq.length * 16 }
}

test('一段手势只判定一次，判定前位移单调跟随', () => {
  const { acts } = run(swipe())
  const releases = acts.filter((a) => a.t === 'release')
  assert.equal(releases.length, 1)
  assert.equal(acts[0].t, 'begin')
  const offs = acts.filter((a) => a.t === 'begin' || a.t === 'move' || a.t === 'release').map((a) => (a as { offset: number }).offset)
  for (let i = 1; i < offs.length; i++) assert.ok(offs[i] <= offs[i - 1], `位移应单调（向下一页为负）：${offs[i - 1]} → ${offs[i]}`)
})

test('判定之后的惯性尾巴（含反向小值）全部吞掉，不再驱动位移', () => {
  const { acts } = run(swipe())
  const i = acts.findIndex((a) => a.t === 'release')
  assert.ok(i > 0)
  for (const a of acts.slice(i + 1)) assert.equal(a.t, 'swallow')
})

test('判定时的位移与速度：deltaX 为正（向左滑）= 往下一页', () => {
  const { acts } = run(swipe())
  const r = acts.find((a) => a.t === 'release') as { offset: number; velocity: number }
  // offset 是 -sum(dx)：带子往下一页方向，useStrip 里 move(-offset) 后 pos 增大
  assert.ok(r.offset < -W * G.commit, `走过的距离够切换：${r.offset}`)
  assert.equal(decide(-r.offset, -r.velocity, W, true), 1)
})

test('竖向为主的滚动不接管', () => {
  const seg = new WheelSegment()
  assert.equal(seg.feed(2, 30, 0, W).t, 'pass')
  assert.equal(seg.feed(0, 40, 16, W).t, 'pass')
})

test('停顿超过 wheelIdle 后是新的一段；idle 判定还没判定的那一段', () => {
  const seg = new WheelSegment()
  assert.equal(seg.feed(10, 0, 0, W).t, 'begin')
  assert.equal(seg.feed(12, 0, 16, W).t, 'move')
  const r = seg.idle(16 + G.wheelIdle + 5)
  assert.equal(r?.t, 'release')
  assert.equal(seg.feed(10, 0, 400, W).t, 'begin')
})

test('判定之后惯性一直不断：锁定到期后来的明显新力度是新的一次滑动', () => {
  const { seg, acts, end } = run(swipe())
  const doneAt = acts.findIndex((a) => a.t === 'release') * 16
  let t = end
  while (t < doneAt + G.wheelLock + 40) {
    assert.equal(seg.feed(1, 0, t, W).t, 'swallow')
    t += 16
  }
  assert.equal(seg.feed(40, 0, t, W).t, 'begin')
})

test('手指阶段往回拖（先慢下来再反向）：跟着往回走，不算新手势', () => {
  const seg = new WheelSegment()
  const seq = [10, 20, 30, 30, 26, 20, 14, 9, 5, 2, -1, -3, -6, -12, -20]
  const acts = seq.map((dx, i) => seg.feed(dx, 0, i * 16, W))
  assert.deepEqual(acts.map((a) => a.t), ['begin', ...seq.slice(1).map(() => 'move')])
  assert.equal((acts[acts.length - 1] as { offset: number }).offset, -seq.reduce((x, y) => x + y, 0))
})

test('惯性还在快速推进时突然反向：这一段就此判定，后面是新的一段', () => {
  const seg = new WheelSegment()
  const seq = [10, 20, 30, 30, 26, 22, -4, -6, -9]
  const acts = seq.map((dx, i) => seg.feed(dx, 0, i * 8, W))
  assert.equal(acts[6].t, 'release')
  assert.equal(acts[7].t, 'begin')
})

test('走满一页立即判定', () => {
  const seg = new WheelSegment()
  let last: WheelAct = { t: 'pass' }
  for (let i = 0; i < 40 && last.t !== 'release'; i++) last = seg.feed(60, 0, i * 16, W)
  assert.equal(last.t, 'release')
})

test('松手判定：距离 / 甩动 / 往回甩取消', () => {
  assert.equal(decide(W * 0.3, 0, W), 1)
  assert.equal(decide(-W * 0.3, 0, W), -1)
  assert.equal(decide(W * 0.1, 0, W), 0)
  assert.equal(decide(20, 900, W), 1)
  assert.equal(decide(W * 0.3, -900, W), 0) // 拖过去了又往回甩 = 取消
  assert.equal(decide(20, 900, W, true), 0) // 触控板甩动门槛更高
})

test('接续：换参照后屏幕位置不变', () => {
  // 带子在往下一页走（pos = 0.8W），切换后这一件变成上一件：新的参照里位置 = -0.2W，和原来在屏幕上是同一处
  const pos = 0.8 * W
  const np = rebase(pos, 1, W)
  assert.equal(np, pos - W)
  // 新的这一件（原来在 +W）在屏幕上的位置：旧参照 W - pos，新参照 0 - np，一样
  assert.equal(W - pos, 0 - np)
  assert.equal(rebase(rebase(pos, 1, W), -1, W), pos)
})
