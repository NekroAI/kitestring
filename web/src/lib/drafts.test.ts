import { test } from 'node:test'
import assert from 'node:assert/strict'
import { clearPlan, clip, DEC_CLEAR, fit, hasContent, MAX_BYTES, MAX_TEXT, migrate, ownerOf, parse, prune, serialize, sizeOf, slotPad, slotTools, sweep, TTL_MS, type DraftMap } from './drafts.ts'

const DAY = 86_400_000

test('有没有内容：空白文字、空列表、开关不算，选了选项算', () => {
  assert.equal(hasContent({ text: '  ', files: [] }), false)
  assert.equal(hasContent({ choice: null, note: '', noteOpen: true }), false)
  assert.equal(hasContent({ choice: 0, note: '' }), true)
  assert.equal(hasContent({ text: '', files: [{ id: 'f1' }] }), true)
  assert.equal(hasContent({ text: '写了一半' }), true)
})

test('单段文字存下来时截到上限，嵌套的也截', () => {
  const long = 'x'.repeat(MAX_TEXT + 50)
  const v = clip({ text: long, stages: [{ title: long }] }) as { text: string; stages: { title: string }[] }
  assert.equal(v.text.length, MAX_TEXT)
  assert.equal(v.stages[0].title.length, MAX_TEXT)
})

test('7 天没动的清掉，坏数据丢掉', () => {
  const now = 100 * DAY
  const m = { a: { at: now - TTL_MS - 1, d: { text: 'old' } }, b: { at: now - DAY, d: { text: 'new' } }, c: { at: 'x', d: {} } } as unknown as DraftMap
  assert.deepEqual(Object.keys(prune(m, now)), ['b'])
})

test('总量超出上限先清最久没动的，正在写的最后动', () => {
  const big = 'y'.repeat(200_000) // 约 400KB
  const m: DraftMap = { old: { at: 1, d: { text: big } }, mid: { at: 2, d: { text: big } }, cur: { at: 0, d: { text: big } }, now: { at: 3, d: { text: big } } }
  const out = fit(m, MAX_BYTES, 'cur')
  assert.ok(sizeOf(out) <= MAX_BYTES)
  assert.deepEqual(Object.keys(out).sort(), ['cur', 'now'])
})

test('serialize：截字 + 过期 + 限总量一起做', () => {
  const now = 10 * DAY
  const out = serialize({ a: { at: now, d: { text: 'z'.repeat(MAX_TEXT + 1) } }, gone: { at: 0, d: { text: 'x' } } }, now)
  assert.deepEqual(Object.keys(out), ['a'])
  assert.equal((out.a.d.text as string).length, MAX_TEXT)
})

test('老的决策答复草稿（ks.drafts.v2）搬到 dec: 键，空的不搬', () => {
  const out = migrate({ 'D-8': { choice: 1, note: '附言', files: [{ id: 'f' }] }, 'D-9': { choice: null, note: '' } }, 5)
  assert.deepEqual(out, { 'dec:D-8': { at: 5, d: { choice: 1, note: '附言', files: [{ id: 'f' }] } } })
  assert.deepEqual(migrate(null, 1), {})
  assert.deepEqual(parse('not json'), {})
  assert.deepEqual(parse('[1]'), {})
})

test('草稿归属：需求 / 决策 / 计划编号，会话为 s:键，新建的没有归属', () => {
  assert.equal(ownerOf('req:R-12'), 'R-12')
  assert.equal(ownerOf('edit:R-12:title'), 'R-12')
  assert.equal(ownerOf('reason:R-12:back'), 'R-12')
  assert.equal(ownerOf('dec:D-8'), 'D-8')
  assert.equal(ownerOf('stage:P-3:2'), 'P-3')
  assert.equal(ownerOf('planedit:new'), '')
  assert.equal(ownerOf('newreq'), '')
  assert.equal(ownerOf('reply:claude:abc'), 's:claude:abc')
})

test('对象关闭后清草稿：关闭前写的清，关闭后再写的留', () => {
  const m: DraftMap = {
    'req:R-1': { at: 100, d: { text: '关之前写的' } },
    'req:R-2': { at: 300, d: { text: '关之后留言' } },
    'dec:D-1': { at: 999, d: { note: 'x' } },
    'req:R-3': { at: 1, d: { text: '还开着' } },
  }
  const gone = sweep(m, (k) => (k === 'req:R-1' || k === 'req:R-2' ? { at: 200 } : k === 'dec:D-1' ? {} : undefined))
  assert.deepEqual(gone.sort(), ['dec:D-1', 'req:R-1'])
})

test('非图片附件的引用照样存进草稿（R-221）：只存引用，不存文件本身', () => {
  const ref = { id: 'f9', name: 'gesture-log.json', mime: 'application/json', size: 4096 }
  const m: DraftMap = { 'req:R-73': { at: 1, d: { text: '日志见附件', files: [ref] } } }
  const out = serialize(m, 2)
  assert.deepEqual(out['req:R-73'].d.files, [ref])
  assert.equal(hasContent({ text: '', files: [ref] }), true)
})

// ---------- 框内工具槽（R-317）----------

test('工具槽：空和非空时按钮一样多、内边距一样，清空只是隐藏', () => {
  const tools = { attach: true, clear: true }
  const empty = { text: '', files: [] }
  const full = { text: '写了一半', files: [{ id: 'f1' }] }
  // 槽里放什么只看这个框能有什么，不看内容
  assert.deepEqual(slotTools(tools), ['clear', 'attach'])
  assert.equal(slotPad(slotTools(tools).length), 'pr-[68px] pointer-coarse:pr-[84px]')
  // 空：清空占位但隐藏；非空：显示
  assert.equal(clearPlan(empty, false).can, false)
  assert.equal(clearPlan(full, true).can, true)
  // 从右往左：附件、清空、恢复原文
  assert.deepEqual(slotTools({ attach: true, clear: true, revert: true }), ['revert', 'clear', 'attach'])
  assert.equal(slotPad(1), 'pr-10 pointer-coarse:pr-12')
  assert.equal(slotPad(0), '')
})

test('清空的悬停说明：有附件时写清空文字和附件', () => {
  assert.equal(clearPlan({ text: '一句话', files: [] }, true).files, false)
  assert.equal(clearPlan({ text: '', files: [{ id: 'f1' }] }, true).files, true)
  assert.equal(clearPlan({ title: '新需求', refs: [{ id: 'f1' }] }, true).files, true)
})

test('编辑已有内容：只清文字；文字已空时清空隐藏', () => {
  assert.deepEqual(clearPlan({ text: '改了一半' }, true, { text: '' }), { can: true, files: false, next: { text: '' } })
  assert.equal(clearPlan({ text: '' }, true, { text: '' }).can, false)
})

test('决策卡清空：只清附言、附件和理由，保留已选的选项', () => {
  const v = { choice: 1, picks: [0, 2], noteOpen: true, note: '附言', files: [{ id: 'f1' }], reason: '不选推荐的理由' }
  const p = clearPlan(v, true, DEC_CLEAR)
  assert.equal(p.can, true)
  assert.equal(p.files, true)
  assert.deepEqual(p.next, { choice: 1, picks: [0, 2], noteOpen: true, note: '', files: [], reason: '' })
  // 只选了选项、没写附言：没有可清的，清空隐藏（不会把选项清掉）
  assert.equal(clearPlan({ choice: 0, note: '' }, true, DEC_CLEAR).can, false)
  // 清完仍有选项，草稿还在（选项不丢）
  assert.equal(hasContent(p.next), true)
})
