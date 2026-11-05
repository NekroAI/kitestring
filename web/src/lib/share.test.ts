import { test } from 'node:test'
import assert from 'node:assert/strict'
import { buildTree, check, counts, graph, keyOf, missing, preset, resolve, setAuto, tidy, toggle, uncheck, type ShareItem, type ShareNode } from './share.ts'

// 一个计划两个阶段：阶段 1 有 R-1、R-2，阶段 2 有 R-3；R-9 不在计划里。会话 a 挂在 R-1，b 挂在 R-1 和 R-9，c 挂在 R-2。
const nodes: ShareNode[] = [
  { kind: 'plan', ref: 'p', code: 'P-1', title: '计划', parents: [] },
  { kind: 'stage', ref: 's1', code: 'P-1', seq: 1, title: '一', parents: ['p'] },
  { kind: 'stage', ref: 's2', code: 'P-1', seq: 2, title: '二', parents: ['p'] },
  { kind: 'req', ref: 'r1', code: 'R-1', title: '需求一', parents: ['s1'] },
  { kind: 'req', ref: 'r2', code: 'R-2', title: '需求二', parents: ['s1'] },
  { kind: 'req', ref: 'r3', code: 'R-3', title: '需求三', parents: ['s2'] },
  { kind: 'req', ref: 'r9', code: 'R-9', title: '计划外', parents: [] },
  { kind: 'session', ref: 'a', title: 'A', parents: ['r1'] },
  { kind: 'session', ref: 'b', title: 'B', parents: ['r1', 'r9'] },
  { kind: 'session', ref: 'c', title: 'C', parents: ['r2'] },
]
const tr = buildTree(nodes)
const K = { p: keyOf('plan', 'p'), s1: keyOf('stage', 's1'), s2: keyOf('stage', 's2'), r1: keyOf('req', 'r1'), r2: keyOf('req', 'r2'), r3: keyOf('req', 'r3'), r9: keyOf('req', 'r9'), a: keyOf('session', 'a'), b: keyOf('session', 'b'), c: keyOf('session', 'c') }
const inc = (items: ShareItem[]) => [...resolve(tr, items).inc].sort()
const all = Object.values(K).sort()

test('树：计划外的需求单独成组，会话挂在每条关联的需求下', () => {
  assert.deepEqual(tr.plans, [K.p])
  assert.deepEqual(tr.loose, [K.r9])
  assert.deepEqual(tr.ups.get(K.b), [K.r1, K.r9])
  assert.deepEqual(tr.kids.get(K.r9), [K.b])
})

test('勾计划：默认包含以后新增的，下面全部带上，只写一条', () => {
  const items = check(tr, [], K.p)
  assert.deepEqual(items, [{ kind: 'plan', ref: 'p', auto: true, excluded: false }])
  assert.deepEqual(inc(items), all.filter((k) => k !== K.r9))
})

test('上级开着 auto 时取消下级记成单独取消，不会被带回来；会话各处同步', () => {
  let items = check(tr, [], K.p)
  items = uncheck(tr, items, K.r2)
  assert.ok(items.some((x) => x.ref === 'r2' && x.excluded))
  assert.ok(!inc(items).includes(K.r2))
  assert.ok(!inc(items).includes(K.c), '只经由 R-2 带进来的会话也不算')
  assert.equal(missing(tr, resolve(tr, items), K.p), 2)
  assert.equal(missing(tr, resolve(tr, items), K.s1), 2)
  items = uncheck(tr, items, K.b)
  assert.ok(!inc(items).includes(K.b))
  items = setAuto(tr, check(tr, items, K.r9), K.r9, true)
  items = uncheck(tr, items, K.b)
  assert.ok(!inc(items).includes(K.b), '在别的需求下也不出现')
  items = check(tr, items, K.b)
  assert.ok(inc(items).includes(K.b))
  assert.ok(!items.some((x) => x.ref === 'b' && x.excluded))
})

test('勾需求不开 auto：下面现有的会话逐条写上；取消需求连同它们一起去掉，但还挂在别处的会话保留', () => {
  let items = check(tr, [], K.r1)
  assert.equal(resolve(tr, items).auto.get(K.r1), false)
  assert.deepEqual(inc(items), [K.a, K.b, K.r1].sort())
  items = check(tr, items, K.r9)
  items = uncheck(tr, items, K.r1)
  assert.deepEqual(inc(items), [K.b, K.r9].sort())
})

test('打开 auto：没勾的下级记成单独取消；关掉 auto：现有的下级逐条写上，范围不变', () => {
  let items = check(tr, [], K.r1)
  items = uncheck(tr, items, K.a)
  items = setAuto(tr, items, K.r1, true)
  assert.deepEqual(inc(items), [K.b, K.r1].sort())
  assert.ok(items.some((x) => x.ref === 'a' && x.excluded))
  let plan = check(tr, [], K.p)
  const before = inc(plan)
  plan = setAuto(tr, plan, K.p, false)
  assert.deepEqual(inc(plan), before)
  assert.equal(resolve(tr, plan).auto.get(K.s1), true, '阶段原来的 auto 保留')
})

test('预选、计数、整理与关系图', () => {
  const items = preset(tr, 'R-1')
  assert.deepEqual(items, [{ kind: 'req', ref: 'r1', auto: true, excluded: false }])
  assert.deepEqual(counts(tr, resolve(tr, items)), { plans: 0, stages: 0, reqs: 1, sessions: 2 })
  assert.deepEqual(tidy(tr, [...items, { kind: 'req', ref: 'r3', auto: false, excluded: true }]), items)
  // 只勾计划和 R-3（阶段 2 被单独取消）：R-3 用虚线连到计划
  let x = check(tr, [], K.p)
  x = uncheck(tr, x, K.s2)
  x = check(tr, x, K.r3)
  const g = graph(tr, resolve(tr, x).inc)
  assert.deepEqual(g.edges.find((e) => e.to === K.r3), { from: K.p, to: K.r3, direct: false })
  assert.deepEqual(g.edges.find((e) => e.to === K.r1), { from: K.s1, to: K.r1, direct: true })
  assert.deepEqual(toggle(tr, toggle(tr, [], K.r9), K.r9), [])
})
