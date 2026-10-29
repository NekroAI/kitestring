import { test } from 'node:test'
import assert from 'node:assert/strict'
import { attention, stallLimit, type WDec, type WReq, type WSes } from './waiton.ts'

const now = Date.parse('2026-10-29T08:00:00Z')
const ago = (min: number) => new Date(now - min * 60_000).toISOString()
const req = (code: string, status: string, x: Partial<WReq> = {}): WReq => ({ code, status, sessions: [], updated_at: ago(5), ...x })

test('普通草稿不进；被受阻需求指向的草稿进，写明谁在等它（R-210 → R-212）', () => {
  const reqs = [req('R-210', 'blocked', { blocked_on: 'R-212' }), req('R-212', 'draft'), req('R-300', 'draft')]
  const a = attention(reqs, [], [], now)
  assert.deepEqual(a.drafts.map((r) => r.code), ['R-212'])
  assert.deepEqual(a.waitedBy.get('R-212'), ['R-210'])
  assert.equal(a.waitedBy.has('R-300'), false)
  assert.equal(a.blocked.length, 0) // 指向了、链条落到你身上：R-210 本身不再另列一条
})

test('等待链：R-1 等 R-2，R-2 等 D-9（待答），D-9 上写明 R-1、R-2 都在等它', () => {
  const reqs = [req('R-1', 'blocked', { blocked_on: 'R-2' }), req('R-2', 'blocked', { blocked_on: 'D-9' })]
  const decs: WDec[] = [{ code: 'D-9', status: 'pending' }]
  const a = attention(reqs, decs, [], now)
  assert.deepEqual(a.waitedBy.get('D-9'), ['R-1', 'R-2'])
  assert.deepEqual(a.waitedBy.get('R-2'), ['R-1'])
  assert.equal(a.blocked.length, 0)
})

test('等的需求在待验收、或挂着待答决策：那几条上标谁在等', () => {
  const reqs = [req('R-1', 'blocked', { blocked_on: 'R-2' }), req('R-2', 'review'), req('R-3', 'blocked', { blocked_on: 'R-4' }), req('R-4', 'in_progress')]
  const decs: WDec[] = [{ code: 'D-5', status: 'pending', requirement_code: 'R-4' }]
  const a = attention(reqs, decs, [], now)
  assert.deepEqual(a.waitedBy.get('R-2'), ['R-1'])
  assert.deepEqual(a.waitedBy.get('D-5'), ['R-3'])
})

test('受阻没写等什么的本身进；等的已经结束仍受阻、或编号不存在的也本身进', () => {
  const reqs = [
    req('R-1', 'blocked'),
    req('R-2', 'blocked', { blocked_on: 'D-7' }),
    req('R-3', 'blocked', { blocked_on: 'R-9' }),
    req('R-9', 'done'),
    req('R-4', 'blocked', { blocked_on: 'R-404' }),
  ]
  const a = attention(reqs, [{ code: 'D-7', status: 'delivered' }], [], now)
  assert.deepEqual(
    a.blocked.map((x) => [x.r.code, x.why]),
    [['R-1', 'bare'], ['R-2', 'gone'], ['R-3', 'gone'], ['R-4', 'missing']],
  )
})

test('等的需求还在会话手上做（进行中、没有待答决策）：不进要你处理', () => {
  const reqs = [req('R-1', 'blocked', { blocked_on: 'R-2' }), req('R-2', 'in_progress', { sessions: ['c:a'] })]
  const a = attention(reqs, [], [{ key: 'c:a', state: 'running', last_active_at: ago(1) }], now)
  assert.equal(a.drafts.length + a.blocked.length + a.stalled.length, 0)
  assert.deepEqual(a.waitedBy.get('R-2'), ['R-1'])
})

test('互相等成圈的老数据不死循环', () => {
  const reqs = [req('R-1', 'blocked', { blocked_on: 'R-2' }), req('R-2', 'blocked', { blocked_on: 'R-1' })]
  const a = attention(reqs, [], [], now)
  assert.deepEqual(a.waitedBy.get('R-2'), ['R-1'])
  assert.deepEqual(a.waitedBy.get('R-1'), ['R-2'])
})

test('停滞：进行中、没有会话在跑、需求和会话都超过阈值没动静才算', () => {
  const s: WSes[] = [
    { key: 'c:idle', state: 'parked', last_active_at: ago(300) },
    { key: 'c:run', state: 'running', last_active_at: ago(300) },
    { key: 'c:fresh', state: 'parked', last_active_at: ago(10) },
    { key: 'c:gone', state: 'ended', last_active_at: ago(500) },
  ]
  const reqs = [
    req('R-1', 'in_progress', { sessions: ['c:idle'], updated_at: ago(400) }), // 停滞
    req('R-2', 'in_progress', { sessions: ['c:run'], updated_at: ago(400) }), // 有会话在跑
    req('R-3', 'in_progress', { sessions: ['c:fresh'], updated_at: ago(400) }), // 会话刚有动静
    req('R-4', 'in_progress', { sessions: ['c:gone'], updated_at: ago(130) }), // 会话结束了也算停滞
    req('R-5', 'in_progress', { updated_at: ago(60) }), // 不到 2 小时
    req('R-6', 'in_progress', { updated_at: ago(400) }), // 挂着待答决策：已经在要你处理里
    req('R-7', 'draft', { updated_at: ago(900) }), // 草稿不算
  ]
  const a = attention(reqs, [{ code: 'D-1', status: 'pending', requirement_code: 'R-6' }], s, now)
  assert.deepEqual(a.stalled.map((x) => x.r.code), ['R-1', 'R-4'])
  assert.equal(a.stalled[0].since, ago(300))
})

test('停滞阈值按项目设置，0 = 默认 2 小时', () => {
  const reqs = [req('R-1', 'in_progress', { updated_at: ago(45), project_id: 'p1' }), req('R-2', 'in_progress', { updated_at: ago(45), project_id: 'p2' })]
  const a = attention(reqs, [], [], now, (pid) => stallLimit(pid === 'p1' ? 30 : 0))
  assert.deepEqual(a.stalled.map((x) => x.r.code), ['R-1'])
  assert.equal(stallLimit(0), 120 * 60_000)
})
