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

test('第一句不按 40 字截，交给界面按宽度省略', () => {
  const mid = '字'.repeat(60) + '。'
  assert.equal(headlineOf('', mid), mid)
})

test('异常长的第一句兜底截到 160 字', () => {
  const long = '字'.repeat(300) + '。'
  const h = headlineOf('', long)
  assert.equal([...h].length, 161)
  assert.ok(h.endsWith('…'))
})
