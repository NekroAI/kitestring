import { test } from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { G } from './gesture.ts'
import { StripCore } from './strip.ts'

// 用 StripCore 回放真机妙控板日志：虚拟时钟、同一种弹簧（刚度 380 / 阻尼 38，逐毫秒积分）、模拟抽屉——
// 切换时 onNav 之后约 50ms 抽屉换 key、带子换参照（和真实的 Pager 一样）。记录「内容在屏幕上的位置」
// （带子位置 + 当前是第几件 × W，换参照不影响），断言它连续、不来回抖、反向手势一开始就抓住。

type Log = { W: number; wheel: [number, number, number, number][] }
const load = (f: string) => JSON.parse(readFileSync(new URL(`./fixtures/${f}`, import.meta.url), 'utf8')) as Log

function sim(log: Log) {
  const W = log.W
  let now = Math.floor(log.wheel[0][0]) - 500
  let pos = 0
  let vel = 0
  let index = 0 // 当前是第几件（两边都有邻件）
  let spring: { to: number; done: () => void; alive: boolean } | null = null
  const timers: { at: number; fn: () => void; id: number }[] = []
  let tid = 0
  const frames: { t: number; abs: number; index: number }[] = []
  const navs: { t: number; d: number }[] = []
  const core: StripCore = new StripCore(
    {
      get: () => pos,
      set: (v) => {
        vel = 0
        pos = v
      },
      velocity: () => vel,
      spring: (to, v, done) => {
        vel = v
        const s = { to, done, alive: true }
        spring = s
        return { stop: () => ((s.alive = false), spring === s && (spring = null)) }
      },
      now: () => now,
      setTimer: (fn, ms) => (timers.push({ at: now + ms, fn, id: ++tid }), tid),
      clearTimer: (id) => {
        const i = timers.findIndex((x) => x.id === id)
        if (i >= 0) timers.splice(i, 1)
      },
    },
    {
      W: () => W,
      limit: () => [-W, W],
      land: (page, v) => {
        if (page === 0) return core.snapTo(0, v)
        const d = page > 0 ? 1 : -1
        core.snapTo(page, v, () => {
          navs.push({ t: now, d })
          // 抽屉换 key：约 50ms 后（React 渲染）带子换参照
          timers.push({ at: now + 50, id: ++tid, fn: () => ((index += d), core.shift(-d * W, 0)) })
        })
      },
    },
  )
  const step = (to: number) => {
    while (now < to) {
      now += 1
      for (const tm of timers.filter((x) => x.at <= now)) {
        timers.splice(timers.indexOf(tm), 1)
        tm.fn()
      }
      const s = spring
      if (s && s.alive) {
        const a = -G.stiffness * (pos - s.to) - G.damping * vel
        vel += a / 1000
        pos += vel / 1000
        core.tick(pos)
        if (Math.abs(vel) < 5 && Math.abs(pos - s.to) < 0.5) {
          pos = s.to
          vel = 0
          s.alive = false
          spring = null
          s.done()
        }
      }
      if (now % 8 === 0) frames.push({ t: now, abs: pos + index * W, index })
    }
  }
  for (const [t, dx, dy, mode] of log.wheel) {
    step(t)
    const u = mode === 1 ? 16 : mode === 2 ? 400 : 1
    core.wheel(dx * u, dy * u, t)
    frames.push({ t: now, abs: pos + index * W, index })
  }
  step(now + 2500)
  return { frames, navs, W }
}

// 内容位置的折返点（滞回 15px）：[时刻, 位置]
function turnsAt(fs: { t: number; abs: number }[]) {
  const out: [number, number][] = []
  let dir = 0
  let ext = fs[0].abs
  let at = fs[0].t
  for (const { t, abs: v } of fs) {
    if (dir >= 0 && v < ext - 15) {
      if (dir > 0) out.push([at, ext])
      dir = -1
      ext = v
      at = t
    } else if (dir <= 0 && v > ext + 15) {
      if (dir < 0) out.push([at, ext])
      dir = 1
      ext = v
      at = t
    } else if ((dir > 0 && v > ext) || (dir < 0 && v < ext)) {
      ext = v
      at = t
    }
  }
  return out
}
const turns = (xs: number[]) => turnsAt(xs.map((abs, t) => ({ t, abs }))).length
// 来回抖：两次折返挨得很近（300ms 内）
const jitter = (fs: { t: number; abs: number }[]) => {
  const ts = turnsAt(fs)
  return ts.filter((x, i) => i > 0 && x[0] - ts[i - 1][0] < 300)
}

test('反向抓住（真机日志二）：松手后动画还没停就反向滑，带子从当前位置接住往回走，不抖、不先往前再回来', () => {
  const log = load('gesturelog-mac-chrome-reverse.json')
  const { frames, navs } = sim(log)
  // 日志里：往下一件 → 反向（往回）→ 一串很慢的 -1 → 再往回一大下 → 往前两下。内容位置只在用户换方向处折返（最多 3 处），
  // 不能有 300ms 内的来回抖
  assert.deepEqual(jitter(frames), [], '来回抖')
  assert.ok(turnsAt(frames).length <= 3, `内容位置折返 ${turnsAt(frames).length} 次`)
  // 每一帧位移有界：不跳
  let worst = 0
  for (let i = 1; i < frames.length; i++) worst = Math.max(worst, Math.abs(frames[i].abs - frames[i - 1].abs))
  assert.ok(worst <= 60, `单帧最大位移 ${worst.toFixed(1)}px`)
  // 反向的那一刻（第一个负的 deltaX）之后，内容不再往前（下一件方向）走
  const rev = log.wheel.find(([, dx]) => dx < 0)![0]
  const at = frames.find((f) => f.t >= rev)!.abs
  const after = frames.filter((f) => f.t >= rev && f.t <= rev + 400).map((f) => f.abs)
  assert.ok(Math.max(...after) <= at + 2, `反向后还往前走了 ${(Math.max(...after) - at).toFixed(1)}px`)
  // 第一下切到下一件，反向拉回两成多又回到原来那一件
  assert.deepEqual(navs.slice(0, 2).map((n) => n.d), [1, -1])
})

test('连续滑动（真机日志一）：每次滑动方向的改变才折返，没有多余的来回', () => {
  const log = load('gesturelog-mac-chrome.json')
  const { frames, navs } = sim(log)
  const xs = frames.map((f) => f.abs)
  // 日志一里用户换了 6 次方向（+ - + + - ×7 + - ×6 + +）
  assert.equal(turns(xs), 6, `内容位置折返 ${turns(xs)} 次（应为 6）`)
  assert.deepEqual(jitter(frames), [], '来回抖')
  assert.ok(navs.length >= 16, `只切换了 ${navs.length} 次`)
  let worst = 0
  for (let i = 1; i < frames.length; i++) worst = Math.max(worst, Math.abs(frames[i].abs - frames[i - 1].abs))
  assert.ok(worst <= 180, `单帧最大位移 ${worst.toFixed(1)}px`)
})
