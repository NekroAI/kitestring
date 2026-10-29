import { test } from 'node:test'
import assert from 'node:assert/strict'
import { waitIdle } from './settle.ts'

// 假时钟：sleep 只推进时间
const clock = (busyUntil: number) => {
  let t = 0
  return { busy: () => t < busyUntil, sleep: async (ms: number) => void (t += ms), now: () => t, at: () => t }
}

test('列表还在刷新时一直等，刷新回来才放手（R-266：慢网络下不再先消失又弹回）', async () => {
  const c = clock(4000) // 刷新要 4 秒
  assert.equal(await waitIdle(c), true)
  assert.ok(c.at() >= 4000)
})

test('没有在刷新的请求：立刻放手', async () => {
  const c = clock(0)
  assert.equal(await waitIdle(c), true)
  assert.equal(c.at(), 0)
})

test('一直刷不回来：最多等 15 秒', async () => {
  const c = clock(Infinity)
  assert.equal(await waitIdle(c), false)
  assert.ok(c.at() >= 15_000 && c.at() < 15_200)
})
