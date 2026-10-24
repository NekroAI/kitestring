import { test } from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { decide, G, WheelSegment, type WheelAct } from './gesture.ts'

// 回放用户在真机上导出的妙控板手势日志（Mac Chrome，R-73 退回后录的）：按原始时间戳和 deltaX / deltaY / deltaMode 重放。
// 这段日志里用户连续左右滑了 20 来次，很多次是上一次的惯性还没停就开始了下一次（事件流中间没有停顿）。
const log = JSON.parse(readFileSync(new URL('./fixtures/gesturelog-mac-chrome.json', import.meta.url), 'utf8')) as { W: number; wheel: [number, number, number, number][] }
const W = log.W
const ev = log.wheel.map(([t, dx, dy, mode]) => {
  const u = mode === 1 ? 16 : mode === 2 ? 400 : 1
  return { t, dx: dx * u, dy: dy * u }
})

// 人工从日志里认出来的「用户的一次滑动」的起点（事件下标）：停顿 40ms 以上之后的第一下，
// 加上没有停顿、但惯性被手指按停（力度骤降）后又重新起势 / 换了方向的几处（7056ms、9217ms、11461ms、14343ms 附近）
const pauses = ev.map((e, i) => (i === 0 || e.t - ev[i - 1].t > 40 ? i : -1)).filter((i) => i >= 0)
const inStream = [424, 569, 839, 1195]
const starts = [...new Set([...pauses, ...inStream])].sort((a, b) => a - b)

function replay() {
  const seg = new WheelSegment()
  const acts: { i: number; t: number; a: WheelAct }[] = []
  let prev = -Infinity
  ev.forEach((e, i) => {
    // 调用方的计时器：停了 G.wheelIdle 就结束这一段
    if (e.t - prev > G.wheelIdle) {
      const r = seg.idle(prev + G.wheelIdle)
      if (r) acts.push({ i: i - 1, t: prev + G.wheelIdle, a: r })
    }
    prev = e.t
    acts.push({ i, t: e.t, a: seg.feed(e.dx, e.dy, e.t, W) })
  })
  return acts
}

test('真机日志：用户的每一次滑动都被认成一段新手势（不会被当成上一次的惯性尾巴吞掉）', () => {
  const acts = replay()
  const begins = acts.filter((x) => x.a.t === 'begin' || x.a.t === 'restart').map((x) => x.i)
  const missed = starts.filter((s) => !begins.some((b) => b >= s && b <= s + 6))
  assert.deepEqual(missed.map((i) => `${i}@${ev[i].t}ms`), [], `这些滑动没有被接住：${missed.length} / ${starts.length}`)
})

test('真机日志：每一段只判定一次；判定后的惯性尾巴不再驱动位移', () => {
  const acts = replay()
  let state: 'idle' | 'tracking' | 'done' = 'idle'
  for (const { a, t } of acts) {
    if (a.t === 'begin' || a.t === 'restart') state = 'tracking'
    else if (a.t === 'move') assert.equal(state, 'tracking', `${t}ms：判定之后还在拉动带子`)
    else if (a.t === 'release') {
      assert.equal(state, 'tracking', `${t}ms：同一段判定了两次`)
      state = 'done'
    }
  }
})

test('真机日志：带子位移连续——每个事件最多走它自己的 deltaX，不跳', () => {
  const acts = replay()
  let last = 0
  for (const { a, i } of acts) {
    if (a.t === 'begin' || a.t === 'restart') last = 0
    if (a.t === 'begin' || a.t === 'restart' || a.t === 'move' || a.t === 'release') {
      const off = (a as { offset: number }).offset
      assert.ok(Math.abs(off - last) <= Math.abs(ev[i].dx) + 0.01, `${ev[i].t}ms 跳了 ${off - last}`)
      last = off
    }
  }
})

test('真机日志：每一次够远 / 够快的滑动都切换一次，方向和手指一致', () => {
  const acts = replay()
  const navs = acts.filter((x) => x.a.t === 'release' || x.a.t === 'restart').map((x) => {
    const r = x.a as { offset: number; velocity: number }
    return decide(-r.offset, -r.velocity, W, true)
  })
  // 这段日志里用户滑了 starts.length 次（其中一两次很轻），至少八成要切换出去
  const moved = navs.filter((d) => d !== 0).length
  assert.ok(moved >= Math.floor(starts.length * 0.8), `只切换了 ${moved} 次 / ${starts.length} 次滑动`)
})
