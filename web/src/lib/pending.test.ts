import { test } from 'node:test'
import assert from 'node:assert/strict'
import { dockView, moves, shownStatus } from './pending.ts'

const it = (key: string, at: number, extra: object = {}) => ({ key, at, phase: 'wait', ...extra })

// 元素看得见时撤回就在元素上，不出浮动条（R-201 第 2 条）
test('看得见的不进浮动条', () => {
  const v = dockView([it('R-1', 1), it('R-2', 2)], (k) => k === 'R-1')
  assert.equal(v.latest?.key, 'R-2')
  assert.deepEqual(v.rest, [])
})

// 连续审批：只显示最近一件，其余写「还有 n 件」
test('看不见的只显示最近一件，其余计数', () => {
  const v = dockView([it('R-1', 1), it('R-3', 3), it('R-2', 2)], () => false)
  assert.equal(v.latest?.key, 'R-3')
  assert.deepEqual(
    v.rest.map((x) => x.key),
    ['R-2', 'R-1'],
  )
})

// 盖印中 / 已提交的不能再撤回，不进浮动条
test('只看撤销期里的', () => {
  const v = dockView([it('R-1', 1, { phase: 'seal' }), it('R-2', 2, { phase: 'done' })], () => false)
  assert.equal(v.latest, undefined)
})

// 批量操作只出一条汇总；只剩一件时当成单件
test('批量只出一条汇总', () => {
  const v = dockView([it('R-1', 1, { group: 'g' }), it('R-2', 2, { group: 'g' }), it('R-3', 3, { group: 'h' })], () => true)
  assert.equal(v.groups.length, 1)
  assert.deepEqual(
    v.groups[0].items.map((x) => x.key),
    ['R-1', 'R-2'],
  )
  assert.equal(v.latest, undefined) // R-3 看得见
  const w = dockView([it('R-3', 3, { group: 'h' })], () => false)
  assert.equal(w.groups.length, 0)
  assert.equal(w.latest?.key, 'R-3')
})

test('撤销期里显示操作后的状态', () => {
  assert.equal(shownStatus('ready'), 'ready')
  assert.equal(shownStatus('ready', {}), 'ready')
  assert.equal(shownStatus('ready', { to: 'in_progress' }), 'in_progress')
})

// 换列：本机操作（遮罩出现 / 撤回 / 失败回滚）与 Agent 改的分开；筛选带来的增减不算换列
test('换列区分本机操作和 Agent', () => {
  const prev = new Map([
    ['R-1', 'ready'],
    ['R-2', 'ready'],
    ['R-3', 'blocked'],
  ])
  const next = new Map([
    ['R-1', 'in_progress'], // 用户拖过去（撤销期开始）
    ['R-2', 'review'], // Agent 改的
    ['R-4', 'ready'], // 新出现
  ])
  const m = moves(prev, next, new Map(), new Map([['R-1', 'in_progress']]))
  assert.deepEqual(m, [
    { code: 'R-1', from: 'ready', to: 'in_progress', local: true },
    { code: 'R-2', from: 'ready', to: 'review', local: false },
  ])
  // 撤回：遮罩消失，回到原列，也是本机操作
  const back = moves(new Map([['R-1', 'in_progress']]), new Map([['R-1', 'ready']]), new Map([['R-1', 'in_progress']]), new Map())
  assert.deepEqual(back, [{ code: 'R-1', from: 'in_progress', to: 'ready', local: true }])
})
