import assert from 'node:assert/strict'
import { test } from 'node:test'
import { headlineOf } from './brief.ts'

test('有一句话结论就用它', () => {
  assert.equal(headlineOf(' R-12 已交验收 ', '很长的总述。'), 'R-12 已交验收')
})

test('老数据没有结论：取第一句', () => {
  assert.equal(headlineOf('', '今天推进了三件事。其余见下方。'), '今天推进了三件事。')
  assert.equal(headlineOf(undefined, ''), '')
})

test('第一句超过 40 字截到 40 字', () => {
  const long = '字'.repeat(60) + '。'
  const h = headlineOf('', long)
  assert.equal([...h].length, 41)
  assert.ok(h.endsWith('…'))
})
