import { test } from 'node:test'
import assert from 'node:assert/strict'
import { movable, pruneSel, rangeSel, sharedCommit, skipGroups, tileCodes, toggleCodes } from './assetBatch.ts'
import { reviewQueue, type AssetLite, type WallTile } from './assets.ts'

const a = (code: string, status: AssetLite['status'] = 'pending', extra: Partial<AssetLite> = {}): AssetLite => ({
  id: code,
  code,
  project_id: 'p',
  batch: 'b',
  key: code,
  kind: 'image',
  category: 'c',
  status,
  version: 1,
  updated_at: '',
  ...extra,
})

const tiles: WallTile[] = [
  { type: 'one', asset: a('A-1') },
  { type: 'group', key: 'b/g', group: 'g', batch: 'b', assets: [a('A-2'), a('A-3')] },
  { type: 'one', asset: a('A-4') },
]

test('墙上的编号按顺序，候选组展开', () => {
  assert.deepEqual(tileCodes(tiles), ['A-1', 'A-2', 'A-3', 'A-4'])
})

test('勾上再点一次取消；候选组没全勾上时全部勾上，全勾上时全部取消', () => {
  const s = toggleCodes(new Set(), ['A-1'])
  assert.deepEqual([...s], ['A-1'])
  assert.deepEqual([...toggleCodes(s, ['A-1'])], [])
  const g = toggleCodes(new Set(['A-2']), ['A-2', 'A-3'])
  assert.deepEqual([...g].sort(), ['A-2', 'A-3'])
  assert.deepEqual([...toggleCodes(g, ['A-2', 'A-3'])], [])
})

test('Shift 连选两头都含，倒着选也行；没有起点时只勾这一个', () => {
  const order = tileCodes(tiles)
  assert.deepEqual([...rangeSel(order, new Set(), 'A-1', 'A-3')].sort(), ['A-1', 'A-2', 'A-3'])
  assert.deepEqual([...rangeSel(order, new Set(['A-1']), 'A-4', 'A-2')].sort(), ['A-1', 'A-2', 'A-3', 'A-4'])
  assert.deepEqual([...rangeSel(order, new Set(), null, 'A-3')], ['A-3'])
  assert.deepEqual([...rangeSel(order, new Set(), 'A-9', 'A-3')], ['A-3'])
})

test('换了筛选只留墙上还在的；没变化时原样返回', () => {
  const s = new Set(['A-1', 'A-9'])
  assert.deepEqual([...pruneSel(s, ['A-1', 'A-2'])], ['A-1'])
  const same = new Set(['A-1'])
  assert.equal(pruneSel(same, ['A-1', 'A-2']), same)
})

test('能下结论的个数：已是这个状态的、制作中的不算', () => {
  const list = [a('1', 'pending'), a('2', 'approved'), a('3', 'rework'), a('4', 'wip')]
  assert.equal(movable(list, 'approved'), 2)
  assert.equal(movable(list, 'rework'), 2)
})

test('跳过原因按个数汇总', () => {
  assert.deepEqual(skipGroups([{ why: '已经是通过' }, { why: '制作中的素材不能直接改为通过' }, { why: '已经是通过' }]), [
    { why: '已经是通过', n: 2 },
    { why: '制作中的素材不能直接改为通过', n: 1 },
  ])
})

test('批量提交只发一次，撤销了的不在里面，先到点的那条算上自己', async () => {
  const sent: string[][] = []
  const live = new Set(['A-2', 'A-4']) // A-1 是先到点的（页面关闭时已移出撤销期），A-3 撤销了
  const commit = sharedCommit(['A-1', 'A-2', 'A-3', 'A-4'], (c) => live.has(c), async (codes) => (sent.push(codes), true))
  const r = await Promise.all([commit('A-1'), commit('A-2'), commit('A-4')])
  assert.deepEqual(r, [true, true, true])
  assert.deepEqual(sent, [['A-1', 'A-2', 'A-4']])
})

test('申请标为已在用的素材不进审片队列', () => {
  const q = reviewQueue([{ type: 'one', asset: a('A-1') }, { type: 'one', asset: a('A-2', 'pending', { baseline: 'D-9' }) }])
  assert.deepEqual(q, ['A-1'])
})
