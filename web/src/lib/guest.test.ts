import { test } from 'node:test'
import assert from 'node:assert/strict'
import { actKey, freshOf, guestPath, guestTree, navRows, navSessions, refIndex, statusBar } from './guest.ts'
import type { ShareNode } from './share.ts'

const nodes: ShareNode[] = [
  { kind: 'plan', ref: 'p', code: 'P-1', title: '计划', status: 'active', parents: [] },
  { kind: 'stage', ref: 's1', code: 'P-1', seq: 1, title: '一', parents: ['p'] },
  { kind: 'req', ref: 'r1', code: 'R-1', title: '需求一', status: 'in_progress', parents: ['s1'] },
  { kind: 'req', ref: 'r9', code: 'R-9', title: '计划外', status: 'review', parents: [] },
  { kind: 'session', ref: 'claude:a', title: 'A', parents: ['r1', 'r9'] },
  { kind: 'session', ref: 'claude:z', title: 'Z', parents: [] },
]

test('guestPath：会话键整段编码', () => {
  assert.equal(guestPath('kss_x'), '/s/kss_x')
  assert.equal(guestPath('kss_x', 'req', 'R-1'), '/s/kss_x/r/R-1')
  assert.equal(guestPath('kss_x', 'session', 'claude:a'), '/s/kss_x/s/claude%3Aa')
})

test('refIndex：只收分享里的计划、需求，以及这些需求上的决策', () => {
  const m = refIndex(nodes, [
    { code: 'D-5', req: 'R-1', title: '用分页吗', status: 'closed' },
    { code: 'D-6', req: 'R-77', title: '范围外', status: 'pending' },
  ])
  assert.equal(m.get('R-1')?.status, 'in_progress')
  assert.equal(m.get('P-1')?.kind, 'plan')
  assert.equal(m.get('D-5')?.reqCode, 'R-1')
  assert.equal(m.has('D-6'), false)
  assert.equal(m.has('R-77'), false)
})

test('navRows：计划 → 阶段 → 需求到此为止，计划外需求在后，不列会话', () => {
  const rows = navRows(guestTree(nodes)).map((r) => `${r.depth}:${r.node.code ?? r.node.ref}`)
  assert.deepEqual(rows, ['0:P-1', '1:P-1', '2:R-1', '0:R-9'])
})

test('navSessions：会话单独成组，挂在几条需求下的会话也只出现一次', () => {
  assert.deepEqual(navSessions(guestTree(nodes)).map((n) => n.ref), ['claude:a', 'claude:z'])
})

test('statusBar：流转中的在前，没数的不列', () => {
  assert.deepEqual(statusBar({ done: 2, in_progress: 1, ready: 0, review: 3 }), [
    ['in_progress', 1],
    ['review', 3],
    ['done', 2],
  ])
})

test('freshOf：第一次加载不算新，之后只标新出现的', () => {
  const a = { at: '2026-11-06T01:00:00Z', kind: 'status', req: 'R-1', to: 'review', actor: 'agent' }
  const b = { at: '2026-11-06T01:01:00Z', kind: 'comment', req: 'R-1', actor: 'user' }
  assert.equal(freshOf(null, [a]).size, 0)
  const f = freshOf(new Set([actKey(a)]), [b, a])
  assert.deepEqual([...f], [actKey(b)])
})
