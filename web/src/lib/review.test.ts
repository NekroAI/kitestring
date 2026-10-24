import { test } from 'node:test'
import assert from 'node:assert/strict'
import { reviewHeld, reviewPhase } from './review.ts'

const now = Date.parse('2026-10-25T10:00:00Z')
const due = (min: number) => new Date(now + min * 60000).toISOString()

// 交给评审、还没到时限：评审中，不进「要你处理」
test('评审中的待验收需求不进要你处理', () => {
  const r = { status: 'review', review: { state: 'reviewing', due_at: due(10) } }
  assert.equal(reviewPhase(r, now), 'reviewing')
  assert.equal(reviewHeld(r, now), true)
})

// 过了 30 分钟时限（Hub 还没刷新）：按评审未完成算，照常弹给用户
test('过了时限按评审未完成算', () => {
  const r = { status: 'review', review: { state: 'reviewing', due_at: due(-1) } }
  assert.equal(reviewPhase(r, now), 'overdue')
  assert.equal(reviewHeld(r, now), false)
})

// 评审交了意见、没交给评审、不是待验收：都照常
test('交了意见、没交给评审、不是待验收的都不压', () => {
  assert.equal(reviewHeld({ status: 'review', review: { state: 'done' } }, now), false)
  assert.equal(reviewPhase({ status: 'review', review: { state: 'done' } }, now), 'done')
  assert.equal(reviewHeld({ status: 'review' }, now), false)
  assert.equal(reviewPhase({ status: 'done', review: { state: 'reviewing', due_at: due(10) } }, now), undefined)
})
