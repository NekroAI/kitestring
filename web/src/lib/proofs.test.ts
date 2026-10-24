import { test } from 'node:test'
import assert from 'node:assert/strict'
import { actorOf, boxLabel, citesOf, cropLayout, focusView, latestEvidence, leadNum, proofsOf, shotsOf, splitLegacy } from './proofs.ts'
import { zh } from '../i18n/zh.ts'

const img = (id: string, extra: object = {}) => ({
  id,
  name: id + '.png',
  mime: 'image/png',
  size: 1,
  ...extra,
})

// 证据整理（R-195）：块里的图在前、附图在后；对比块的改后是主图；不是图片的附件不算截图。
test('截图按块的顺序、附图在后', () => {
  const e = {
    files: [img('a', { title: '附图 A' }), img('b'), img('fig'), { id: 'doc', name: 'x.pdf', mime: 'application/pdf', size: 1 }],
    payload: {
      blocks: [
        { id: 'c1', type: 'checklist', items: [] },
        {
          id: 'cmp',
          type: 'compare',
          title: '对比',
          before: img('old'),
          after: img('new', { boxes: [{ x: 0, y: 0, w: 0.5, h: 0.5 }] }),
        },
        { id: 'f1', type: 'figure', image: img('fig', { title: '工作台' }) },
      ],
    },
  }
  const shots = shotsOf(e as never)
  assert.deepEqual(
    shots.map((s) => s.id),
    ['cmp', 'f1', 'file-a', 'file-b'],
  )
  assert.equal(shots[0].file.id, 'new')
  assert.equal(shots[0].before?.id, 'old')
  assert.equal(shots[0].before?.boxes, null, '改前不画框')
  assert.equal(shots[0].boxes.length, 1)
  assert.equal(shots[1].file.title, '工作台')
  assert.equal(shots[3].named, false, '没写标题的图用文件名兜底')
})

// 框对应的验收点：新写法看 criterion；老证据按标注文字开头的编号；都对不上时退回到 see 指向的那张图。
test('标注框对应验收点：criterion 优先，老证据按编号兜底', () => {
  const items = [
    { criterion: 1, text: '简报条变浅', result: 'pass' },
    { criterion: 2, text: '图例去掉', result: 'pass', see: 'f2' },
    { criterion: 3, text: '没有证明', result: 'ask' },
  ] as never[]
  const old = shotsOf({
    files: [],
    payload: {
      blocks: [
        {
          id: 'f1',
          type: 'figure',
          image: img('x', {
            boxes: [
              { x: 0, y: 0, w: 0.1, h: 0.1, label: '1 简报条变浅' },
              { x: 0.5, y: 0, w: 0.1, h: 0.1, label: '14→29 条' },
            ],
          }),
        },
        { id: 'f2', type: 'figure', image: img('y') },
      ],
    },
  } as never)
  assert.deepEqual(proofsOf(old, items), [[{ si: 0, bi: 0 }], [{ si: 1, bi: null }], []])
  assert.equal(boxLabel(old[0].boxes[0], false), '简报条变浅', '老证据显示时去掉开头的编号')
  assert.equal(boxLabel(old[0].boxes[1], false), '14→29 条')
  assert.equal(leadNum('14→29 条'), 0)
  assert.equal(leadNum('6、最近动态'), 6)

  // 新写法：只认 criterion，不再猜标注开头的编号
  const fresh = shotsOf({
    files: [
      img('z', {
        boxes: [
          { x: 0, y: 0, w: 0.1, h: 0.1, label: '1 其实是第三条', criterion: 3 },
          { x: 0.2, y: 0, w: 0.1, h: 0.1, label: '2 没写对应' },
        ],
      }),
    ],
    payload: { blocks: [] },
  } as never)
  const ps = proofsOf(fresh, items)
  assert.deepEqual(ps, [[], [], [{ si: 0, bi: 0 }]])
  assert.equal(boxLabel(fresh[0].boxes[0], true), '1 其实是第三条', '新写法标注原样显示')
  assert.deepEqual(citesOf(0, items, ps), [{ k: 2, n: 3, text: '没有证明', result: 'ask', bi: 0 }])
})

// 切片的几何：框居中、图不会被拖出窗口；大切片至少按「看得清」放大，框太宽时从开头显示。
test('切片与放大', () => {
  const nat = { w: 3000, h: 1200 }
  const small = cropLayout({ x: 0.5, y: 0.5, w: 0.05, h: 0.05 }, nat, {
    w: 96,
    h: 60,
  })
  assert.ok(small.ol && Math.abs(small.ol.left + small.ol.w / 2 - 48) < 1, '小框居中')
  const wide = cropLayout({ x: 0, y: 0.02, w: 1, h: 0.05 }, nat, { w: 400, h: 190 }, true)
  assert.ok(wide.imgW >= 3000 * 0.62 - 1, '大切片至少按 0.62 放大')
  assert.equal(wide.left, 0, '框太宽时从开头显示，不拖出左边')
  assert.ok(wide.top <= 0 && wide.top >= 190 - wide.imgW * 0.4, '不拖出上下边')
  const none = cropLayout(null, nat, { w: 96, h: 60 })
  assert.deepEqual(none, { imgW: 96, left: 0, top: 0 })
  const v = focusView({ x: 0.45, y: 0.45, w: 0.1, h: 0.1 }, { w: 1000, h: 400 }, { w: 1000, h: 600 }, 3000)
  assert.ok(v.s > 1 && v.s <= 4)
  assert.ok(Math.abs(v.x) < 1 && Math.abs(v.y) < 1, '框在正中时不用平移')
})

// 默认展开本轮最新、没作废的那份；同一轮交了好几份不当成替换；刚被退回、本轮还没证据时不展开。
test('默认展开的证据', () => {
  const ev = (id: string, at: string, voided = false) => ({
    id,
    created_at: at,
    voided,
  })
  const r = {
    feedback: [],
    evidence: [ev('a', '2026-10-24T01:00:00Z'), ev('b', '2026-10-24T02:00:00Z'), ev('c', '2026-10-24T03:00:00Z', true)],
  }
  assert.equal(latestEvidence(r as never), 'b')
  const back = {
    ...r,
    feedback: [
      {
        kind: 'feedback',
        created_at: '2026-10-24T04:00:00Z',
        answered_at: '2026-10-24T04:00:00Z',
      },
    ],
  }
  assert.equal(latestEvidence(back as never), undefined)
})

test('老格式证据第一句当结论', () => {
  assert.deepEqual(splitLegacy('已去掉放大。带鱼屏多出来的宽度仍按内容分。'), {
    first: '已去掉放大。',
    rest: '带鱼屏多出来的宽度仍按内容分。',
  })
  assert.deepEqual(splitLegacy('只有一句'), { first: '只有一句', rest: '' })
})

// 谁做的：你 / 纸鸢 / 某个会话；Agent 做的但没记录会话（老数据）单独一类，界面写「Agent（没记录是哪个会话）」。
test('记录的会话归属与老数据兜底', () => {
  assert.deepEqual(actorOf({ actor: 'user' }), { kind: 'user' })
  assert.deepEqual(actorOf({ actor: 'system' }), { kind: 'system' })
  assert.deepEqual(
    actorOf({
      actor: 'agent',
      session: 'claude:1',
      role: 'pm',
      title: 'R-187留样与暂缓合并',
    }),
    {
      kind: 'session',
      key: 'claude:1',
      role: 'pm',
      title: 'R-187留样与暂缓合并',
    },
  )
  assert.deepEqual(actorOf({ actor: 'agent', session: '' }), {
    kind: 'unknown',
    role: undefined,
  })
  assert.deepEqual(actorOf({ session: 'claude:2' }), { kind: 'session', key: 'claude:2', role: undefined, title: undefined }, '证据只有会话键，没有 actor')
  assert.equal(zh.who.unknown, 'Agent（没记录是哪个会话）')
  assert.equal(zh.who.now('新标题'), '现在叫「新标题」')
})
