import { test } from 'node:test'
import assert from 'node:assert/strict'
import { involves, matches, rangeStart, unsent, type CommsFilter, type CommsMsgLike } from './comms.ts'

const msg = (x: Partial<CommsMsgLike> = {}): CommsMsgLike => ({
  system: false,
  broadcast: false,
  fyi: false,
  text: '已部署 eb0a506',
  full: 'PM「x」（claude:a） 留言：已部署 eb0a506',
  from: { key: 'claude:a', role: 'pm', title: '主调度' },
  recipients: [{ to: 'claude:b', role: 'aide', title: '主助理', status: 'delivered' }],
  ...x,
})
const f = (x: Partial<CommsFilter> = {}): CommsFilter => ({ kind: 'all', unsent: false, q: '', ...x })

test('按会话过滤：发的和收的都算，广播看每个收件人', () => {
  const cast = msg({ broadcast: true, recipients: [{ to: 'claude:b', status: 'delivered' }, { to: 'dsh:c', status: 'pending_wake' }] })
  assert.equal(involves(cast, 'dsh:c'), true)
  assert.equal(matches(msg(), f({ session: 'claude:a' })), true)
  assert.equal(matches(msg(), f({ session: 'claude:b' })), true)
  assert.equal(matches(msg(), f({ session: 'dsh:c' })), false)
})

test('按身份过滤：无身份用 none；纸鸢提醒没有发送方，看收件人', () => {
  const sys = msg({ system: true, from: undefined, recipients: [{ to: 'dsh:c', role: '', status: 'pending_wake' }] })
  assert.equal(matches(sys, f({ role: 'none' })), true)
  assert.equal(matches(sys, f({ role: 'pm' })), false)
  assert.equal(matches(msg(), f({ role: 'aide' })), true)
})

test('要动手 / 只告知 / 广播、没送到、需求、关键词', () => {
  assert.equal(matches(msg({ fyi: true }), f({ kind: 'action' })), false)
  assert.equal(matches(msg({ fyi: true }), f({ kind: 'fyi' })), true)
  assert.equal(matches(msg(), f({ kind: 'cast' })), false)
  assert.equal(unsent(msg()), false)
  assert.equal(matches(msg({ recipients: [{ to: 'x', status: 'offline' }] }), f({ unsent: true })), true)
  assert.equal(matches(msg({ requirement: 'R-249' }), f({ req: 'R-249' })), true)
  assert.equal(matches(msg(), f({ req: 'R-249' })), false)
  assert.equal(matches(msg(), f({ q: 'EB0A' })), true)
  assert.equal(matches(msg(), f({ q: '主助理' })), true)
  assert.equal(matches(msg(), f({ q: '没有的词' })), false)
})

test('时间范围：今天从本地零点起，1 周含今天共 7 天，1 小时按整分钟', () => {
  const now = new Date(2026, 10, 1, 16, 40, 31).getTime()
  assert.equal(rangeStart('today', now).getTime(), new Date(2026, 10, 1).getTime())
  assert.equal(rangeStart('week', now).getTime(), new Date(2026, 9, 26).getTime())
  assert.equal(rangeStart('h1', now).getTime(), new Date(2026, 10, 1, 15, 40).getTime())
})
