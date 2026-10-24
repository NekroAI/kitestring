import { test } from 'node:test'
import assert from 'node:assert/strict'
import { progressAt, secsAt } from './undo.ts'

const MS = 3000

test('进度按真实时间走：1.5 秒时一半，3 秒时走完', () => {
  const start = 1000
  assert.equal(progressAt(start, start, MS), 0)
  assert.equal(progressAt(start, start + 1500, MS), 0.5)
  assert.equal(progressAt(start, start + 3000, MS), 1)
  assert.equal(progressAt(start, start + 4000, MS), 1)
})

test('重新挂载（换列、翻页）不重来：同一个起点、同一时刻进度相同', () => {
  const start = 5000
  // 第一次挂载在 5100，换列后在 6200 重新挂载：两处在 6200 时刻算出来的进度一样
  assert.equal(progressAt(start, 6200, MS), (6200 - start) / MS)
})

test('秒数和进度同一个起点：3、2、1，到点前最少 1', () => {
  const start = 0
  assert.equal(secsAt(start, 0, MS), 3)
  assert.equal(secsAt(start, 999, MS), 3)
  assert.equal(secsAt(start, 1001, MS), 2)
  assert.equal(secsAt(start, 2001, MS), 1)
  assert.equal(secsAt(start, 2999, MS), 1)
  assert.equal(secsAt(start, 3500, MS), 1)
})
