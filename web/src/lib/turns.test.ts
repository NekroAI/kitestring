import { test } from 'node:test'
import assert from 'node:assert/strict'
import { countHits, fitRows, mergePages, relatedIds, sortObjects, toolLine } from './turns.ts'

const turn = (id: string, start: string, x: Partial<{ prompt_id: string; reply_id: string; items: { id: string }[]; inbox: { id: string }[]; after: { id: string }[] }> = {}) => ({
  id,
  start,
  inbox: x.inbox ?? [],
  items: x.items ?? [],
  after: x.after ?? [],
  prompt_id: x.prompt_id,
  reply_id: x.reply_id,
})

test('选中对象：只亮它的条目；没选中不筛选', () => {
  const objs = [{ code: 'R-1', kind: 'req', items: ['a', 'r:e2'] }]
  assert.equal(relatedIds(objs, null), null)
  const ids = relatedIds(objs, 'R-1')!
  const ts = [turn('t1', '1', { items: [{ id: 'a' }, { id: 'b' }], reply_id: 'e2' }), turn('t2', '2', { after: [{ id: 'c' }] })]
  assert.equal(countHits(ts, ids), 2)
  assert.equal(countHits(ts, relatedIds(objs, 'R-9')!), 0)
})

test('工具一行最多写三个名字，单次不写次数', () => {
  assert.deepEqual(toolLine([{ name: 'Bash', n: 7 }, { name: 'Edit', n: 3 }, { name: 'Read', n: 1 }, { name: 'Grep', n: 1 }]), ['Bash 7', 'Edit 3', 'Read'])
  assert.deepEqual(toolLine(null), [])
})

test('分页合并：时间正序，边界重复的轮只留一份', () => {
  const p0 = { turns: [turn('t3', '3'), turn('t4', '4')] }
  const p1 = { turns: [turn('t1', '1'), turn('t2', '2'), turn('t3', '3')] }
  assert.deepEqual(mergePages([p0, p1]).map((t) => t.id), ['t1', 't2', 't3', 't4'])
})

test('索引顺序：待你答的决策最前，计数项最后', () => {
  const objs = [
    { code: 'R-1', kind: 'req', items: [] },
    { code: 'agents', kind: 'agents', items: [] },
    { code: 'D-2', kind: 'dec', items: [], pending: true },
    { code: 'D-3', kind: 'dec', items: [] },
  ]
  assert.deepEqual(sortObjects(objs).map((o) => o.code), ['D-2', 'R-1', 'D-3', 'agents'])
})

test('一行半：第一行放满，第二行只用左半边，末尾给 +n 让位', () => {
  const b = (left: number, top: number, width = 90) => ({ left, top, width })
  // 宽 300：第一行 3 个，第二行 3 个（第二行只能放到 150 内）
  const boxes = [b(0, 0), b(100, 0), b(200, 0), b(0, 30), b(100, 30), b(200, 30), b(0, 60)]
  // 第二行第一个（0..90）在左半边；第二个（100..190）超出 150；+n 宽 30 放在 96..126 内
  assert.equal(fitRows(boxes, 300, 30), 4)
  // 全部放得下时不留 +n
  assert.equal(fitRows([b(0, 0), b(100, 0)], 300, 30), 2)
  // +n 放不下时往回让一个
  assert.equal(fitRows([b(0, 0), b(100, 0), b(200, 0), b(0, 30, 140), b(0, 60)], 300, 30), 3)
  // 第一行满了：+n 换到第二行开头
  assert.equal(fitRows([b(0, 0), b(100, 0), b(200, 0), b(0, 30, 200)], 300, 30), 3)
})
