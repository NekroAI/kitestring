import { test } from 'node:test'
import assert from 'node:assert/strict'
import { agentEnd, axisX, barHeight, cellX, gridTicks, laneAxis, inWindow, LaneStore, makeAxis, sharedMax, warmView, ZOOMS, zoomSpec, WARM_SOON, type LaneData } from './lanedata.ts'

const NOW = Date.UTC(2026, 9, 24, 11, 30, 20) // 19:30:20 北京时间
const nowMin = Math.floor(NOW / 60000)
const data = (rows: LaneData['rows'], step = 1, extra: Partial<LaneData> = {}): LaneData => ({ now: NOW, step, from: nowMin - 600, to: nowMin + 1, rows, spans: [], agents: [], ...extra })

test('分度值：每档的格子大小、格数，最右一格是现在', () => {
  const h1 = makeAxis('h1', 600, NOW)
  assert.equal(h1.n, 50) // 600px / 12
  assert.equal(h1.end, nowMin + 1)
  assert.equal(makeAxis('h1', 100, NOW).n, 15)
  assert.equal(makeAxis('h1', 5000, NOW).n, 60)
  for (const z of ZOOMS) {
    const ax = makeAxis(z, 700, NOW)
    assert.ok(ax.n >= 15 && ax.n <= 170, `${z} ${ax.n} 格`)
    assert.equal(ax.end - ax.start, ax.n * ax.bucket)
    assert.ok(ax.end > nowMin && ax.end - ax.bucket <= nowMin)
    assert.equal(ax.start % zoomSpec[z].bucket, 0)
  }
})

test('轴上的刻度按本地时间对齐：10 分钟 / 1 小时 / 3 小时 / 0 点', () => {
  const tz = 480
  const at = (m: number) => new Date((m + tz) * 60000).toISOString().slice(11, 16)
  assert.deepEqual(gridTicks(makeAxis('h1', 720, NOW), tz).map(at), ['18:40', '18:50', '19:00', '19:10', '19:20', '19:30'])
  assert.ok(gridTicks(makeAxis('h6', 720, NOW), tz).map(at).every((x) => x.endsWith(':00')))
  assert.ok(gridTicks(makeAxis('d1', 720, NOW), tz).map(at).every((x) => Number(x.slice(0, 2)) % 3 === 0 && x.endsWith(':00')))
  const w = gridTicks(makeAxis('w1', 720, NOW), tz)
  assert.equal(w.length, 7)
  assert.ok(w.map(at).every((x) => x === '00:00'))
})

test('合并：拉取和推送谁先到都一样，次数只增不减，已有的分钟不被旧数据改小', () => {
  const k = 'claude:s1'
  const a = new LaneStore()
  a.merge(data([{ k, a: '', m: [nowMin - 3, nowMin], c: [4, 2] }]))
  a.push({ m: [{ k, a: '', m: nowMin, c: 3, hc: 9 }], s: [], g: [] })
  const b = new LaneStore()
  b.push({ m: [{ k, a: '', m: nowMin, c: 3, hc: 9 }], s: [], g: [] })
  b.merge(data([{ k, a: '', m: [nowMin - 3, nowMin], c: [4, 2] }])) // 在途的旧拉取结果晚到
  const ax = makeAxis('h1', 720, NOW)
  assert.deepEqual(a.calls(k, [''], ax), b.calls(k, [''], ax))
  assert.equal(a.calls(k, [''], ax).at(-1), 3)
  assert.equal(a.calls(k, [''], ax).at(-4), 4)
})

test('换档只是合并格子：近 6 小时 5 分钟一格，近 1 周用小时级', () => {
  const k = 'claude:s1'
  const st = new LaneStore()
  const ms = Array.from({ length: 30 }, (_, i) => nowMin - i)
  st.merge(data([{ k, a: '', m: ms, c: ms.map(() => 1) }, { k, a: 'x', m: [nowMin], c: [5] }]))
  st.merge(data([{ k, a: '', m: [Math.floor(nowMin / 60) * 60 - 120], c: [40] }], 60))
  const h6 = makeAxis('h6', 720, NOW)
  const c6 = st.calls(k, [''], h6)
  assert.equal(c6.reduce((x, y) => x + y, 0), 30)
  assert.equal(c6.at(-1), (nowMin % 5) + 1) // 当前这一格
  assert.equal(st.calls(k, ['', 'x'], h6).reduce((x, y) => x + y, 0), 35) // 子任务合成一行
  const w1 = makeAxis('w1', 720, NOW)
  assert.equal(st.calls(k, [''], w1).at(-3), 40)
})

test('子任务结束后留到滑出窗口；没收到结束、30 分钟没动作的算已结束', () => {
  const ax = makeAxis('h1', 720, NOW) // 60 分钟
  const g = { k: 'c', a: 'x', label: '', type: '', start: NOW - 50 * 60000, end: NOW - 40 * 60000 }
  assert.ok(inWindow(g, 0, ax, NOW))
  assert.ok(!inWindow({ ...g, start: NOW - 90 * 60000, end: NOW - 70 * 60000 }, 0, ax, NOW))
  const open = { ...g, end: undefined }
  assert.equal(agentEnd(open, NOW - 5 * 60000, NOW), 0) // 还在跑
  assert.equal(agentEnd(open, NOW - 35 * 60000, NOW), NOW - 35 * 60000)
})

test('共用上限、平方根缩放、有动作至少 2px', () => {
  assert.equal(sharedMax([[1, 4], [16, 0]]), 16)
  assert.equal(barHeight(16, 16, 40), 40)
  assert.equal(barHeight(4, 16, 40), 20)
  assert.equal(barHeight(1, 1000, 40), 2)
  assert.equal(barHeight(0, 16, 40), 0)
})

test('性能：近 1 周档 60 条泳道各 168 格、近 1 天档各 144 格，换档算格子远低于 200ms', () => {
  const st = new LaneStore()
  const rows = []
  const hours = []
  for (let s = 0; s < 30; s++)
    for (const a of ['', 'x']) {
      const m: number[] = []
      const c: number[] = []
      for (let i = 0; i < 24 * 60; i++) if ((i * 7 + s) % 10 < 4) (m.push(nowMin - i), c.push(1 + (i % 4)))
      rows.push({ k: `claude:s${s}`, a, m, c })
      const hm: number[] = []
      for (let i = 0; i < 168; i++) hm.push(Math.floor(nowMin / 60) * 60 - i * 60)
      hours.push({ k: `claude:s${s}`, a, m: hm, c: hm.map((_, i) => i % 30) })
    }
  st.merge(data(rows))
  st.merge(data(hours, 60))
  for (const z of ['w1', 'd1', 'h6', 'h1'] as const) {
    const t0 = performance.now()
    const ax = makeAxis(z, 720, NOW)
    const all = []
    for (let s = 0; s < 30; s++) all.push(st.calls(`claude:s${s}`, [''], ax), st.calls(`claude:s${s}`, ['x'], ax))
    sharedMax(all)
    const took = performance.now() - t0
    assert.ok(took < 50, `${z} 用了 ${took.toFixed(1)}ms`)
  }
})

test('共享时间轴：主泳道、子任务行、已结束泳道宽度不同也用同一条轴（R-219）', () => {
  // 各行按自己的宽度算的话，近 1 小时档的格数不同，同一竖线对应的时刻就不同
  assert.notEqual(makeAxis('h1', 236, NOW).n, makeAxis('h1', 240, NOW).n)
  for (const z of ZOOMS) {
    const rows = [
      { w: 0, sub: true }, // 还没量出宽度的行不参与
      { w: 236, sub: false }, // 主泳道
      { w: 240, sub: true }, // 子任务行（差几个像素）
      { w: 180, sub: false },
    ]
    const ax = laneAxis(z, rows, NOW)
    assert.deepEqual(ax, makeAxis(z, 236, NOW), `${z}：取第一条主泳道的宽度`)
    assert.deepEqual(laneAxis(z, [...rows].reverse(), NOW), makeAxis(z, 180, NOW), `${z}：顺序不同只是参照的主泳道不同，轴本身只有一条`)
    // 没有主泳道（只有子任务 / 已结束泳道）时取第一条有宽度的行
    assert.deepEqual(laneAxis(z, [{ w: 0, sub: false }, { w: 300, sub: true }], NOW), makeAxis(z, 300, NOW))
  }
})

test('同一分钟在所有行里的位置：同宽的行完全相同，不同宽的行占整条轴的同一比例', () => {
  for (const z of ZOOMS) {
    const ax = laneAxis(z, [{ w: 236, sub: false }], NOW)
    const t = (ax.start + Math.floor(ax.n / 3) * ax.bucket) * 60000 + 30_000 // 某一格中间
    const a = axisX(ax, 236, t)
    assert.equal(axisX(ax, 236, t), a)
    for (const w of [164, 236, 544, 2754]) assert.ok(Math.abs(axisX(ax, w, t) / w - a / 236) < 1e-9, `${z} 宽 ${w}`)
    // 第 i 格的左边缘就是这一格起始分钟的位置
    for (const i of [0, 1, Math.floor(ax.n / 2), ax.n - 1]) assert.ok(Math.abs(cellX(ax, 236, i) - axisX(ax, 236, (ax.start + i * ax.bucket) * 60000)) < 1e-9)
    // 轴的两端：最左是 0，最右是 w
    assert.equal(axisX(ax, 236, ax.start * 60000), 0)
    assert.ok(Math.abs(axisX(ax, 236, ax.end * 60000) - 236) < 1e-9)
  }
})

test('token 用量（R-206）：累计值取较新的，各模型相加，命中率分档，价格齐全才有金额，按档位合并', async () => {
  const { hitRate, hitTone, priceComplete } = await import('./lanedata.ts')
  const k = 'claude:s1'
  const v = (n: number, inp: number, cw: number, cr: number, out: number) => ({ in: inp, cw, cw1h: 0, cr, out, n })
  const st = new LaneStore()
  st.merge(data([], 1, { tok: [{ k, a: '', model: 'opus', m: [nowMin - 2, nowMin], in: [10, 5], cw: [1000, 0], cw1h: [0, 0], cr: [9000, 100], out: [90, 5], n: [1, 1] }] }))
  // 推送先到、旧拉取晚到：不被改小
  st.push({ m: [], s: [], g: [], t: [{ k, a: '', model: 'opus', m: nowMin, t: v(2, 10, 0, 300, 10), h: v(3, 20, 1000, 9300, 100) }] })
  st.merge(data([], 1, { tok: [{ k, a: '', model: 'opus', m: [nowMin], in: [5], cw: [0], cw1h: [0], cr: [100], out: [5], n: [1] }] }))
  st.push({ m: [], s: [], g: [], t: [{ k, a: 'sub1', model: 'sonnet', m: nowMin, t: v(1, 1, 20000, 0, 1), h: v(1, 1, 20000, 0, 1) }] })
  const ax = makeAxis('h1', 720, NOW)
  const main = st.tokens(k, [''], ax)
  assert.equal(main.at(-1)!.fresh, 20)
  assert.equal(main.at(-3)!.fresh, 1100)
  assert.equal(hitRate(main.at(-3)!), 9000 / 10010)
  assert.equal(hitTone(0.95), 'hi')
  assert.equal(hitTone(0.7), 'mid')
  assert.equal(hitTone(0.2), 'lo')
  assert.equal(hitTone(null), null)
  assert.ok(st.hasTokens(k) && !st.hasTokens('codex:x'))
  // 顶部波形：所有会话含子任务
  assert.equal(st.tokenTotal(ax).at(-1)!.fresh, 20 + 20002)
  // 没填价格：没有金额
  assert.equal(st.tokens(k, [''], ax).at(-1)!.cost, null)
  st.setPrices({ opus: { in: 5, out: 25, cw: 6.25, cr: 0.5 } })
  assert.ok(Math.abs(st.tokens(k, [''], ax).at(-1)!.cost! - (10 * 5 + 300 * 0.5 + 10 * 25) / 1e6) < 1e-12)
  assert.equal(st.tokens(k, ['', 'sub1'], ax).at(-1)!.cost, null) // sonnet 没有价格：整格不算金额
  assert.equal(priceComplete({ in: 1, out: 2, cw: 3 }), false)
  assert.equal(priceComplete({ in: 1, out: 2, cw: 3, cr: 0 }), true)
  // 近 1 周档用小时级
  const w1 = makeAxis('w1', 720, NOW)
  assert.equal(st.tokens(k, [''], w1).at(-1)!.fresh, 1120)
})

test('冷启动：超过阈值、在窗口里的才列出，按时间排', () => {
  const k = 'claude:s1'
  const st = new LaneStore()
  const at = (m: number) => m * 60000 + 5000
  st.merge(data([], 1, { colds: [
    { k, a: '', model: 'opus', at: at(nowMin - 1), tokens: 400000, cause: 'idle', idle_min: 120 },
    { k, a: '', model: 'opus', at: at(nowMin - 30), tokens: 20000, cause: 'compact', idle_min: 1 },
    { k, a: '', model: 'opus', at: at(nowMin - 300), tokens: 20000, cause: 'model', idle_min: 1 },
    { k, a: 'x', model: 'opus', at: at(nowMin - 2), tokens: 28000, cause: 'start', idle_min: 0 },
  ] }))
  const ax = makeAxis('h1', 720, NOW)
  assert.deepEqual(st.coldsOf(k, [''], ax).map((c) => c.cause), ['compact', 'idle'])
  assert.deepEqual(st.coldsOf(k, ['x'], ax).map((c) => c.cause), ['start'])
})

test('后台命令（R-208）：按范围取、整条覆盖、结束了的不被还在跑的旧数据盖掉、不知道结束时间的只算 10 分钟', () => {
  const st = new LaneStore()
  const sh = (ref: string, startAgo: number, extra: object = {}) => ({ k: 'claude:s', ref, kind: 'build', label: ref, cmd: ref, start: NOW - startAgo * 60000, status: 'running', ...extra })
  st.merge(data([], 1, { shells: [sh('a', 30), sh('b', 200, { status: 'failed', end: NOW - 190 * 60000, exit: 1 }), sh('c', 300, { status: 'lost' })] }))
  const h1 = makeAxis('h1', 720, NOW)
  assert.deepEqual(st.shellsOf('claude:s', h1, NOW).map((x) => x.ref), ['a'])
  const h6 = makeAxis('h6', 720, NOW)
  assert.deepEqual(st.shellsOf('claude:s', h6, NOW).map((x) => x.ref), ['c', 'b', 'a'])
  // 推送：a 结束了；随后到的拉取里 a 还在跑（旧数据），不盖掉
  st.push({ m: [], s: [], g: [], sh: [sh('a', 30, { status: 'ok', end: NOW - 60000, exit: 0 })] })
  st.merge(data([], 1, { shells: [sh('a', 30)] }))
  assert.equal(st.shellsOf('claude:s', h1, NOW)[0].status, 'ok')
  // 不知道结束时间的只算开始后 10 分钟：近 1 小时里看不到 5 小时前开始的
  const late = makeAxis('h6', 720, NOW + 2 * 3600e3)
  assert.ok(!st.shellsOf('claude:s', late, NOW + 2 * 3600e3).some((x) => x.ref === 'c'))
})

// 缓存余温（R-207）
const tv = (o: Partial<{ in: number; cw: number; cw1h: number; cr: number; out: number; n: number }> = {}) => ({ in: 0, cw: 0, cw1h: 0, cr: 0, out: 0, n: 1, ...o })

test('缓存余温：剩余 = 档位 − 距最后一次请求，到点后算已冷却', () => {
  const w = { k: 'claude:s', model: 'm', at: NOW - 18 * 60_000, ttl: 3600, ctx: 180000 }
  const v = warmView(w, {}, NOW)!
  assert.equal(v.warm, true)
  assert.equal(Math.round(v.left / 60000), 42)
  assert.equal(v.manual, false)
  const c = warmView({ ...w, ttl: 300 }, {}, NOW)!
  assert.equal(c.warm, false)
  assert.equal(c.left, 0)
  assert.equal(c.ctx, 180000)
  assert.ok(warmView({ ...w, at: NOW - 56 * 60_000 }, {}, NOW)!.left < WARM_SOON)
})

test('缓存余温：拿不到档位的来源默认不显示，手填了才按填的估算并标明', () => {
  const w = { k: 'codex:s', model: 'm', at: NOW - 3 * 60_000, ttl: 0, ctx: 90000 }
  assert.equal(warmView(w, {}, NOW), null)
  assert.equal(warmView(undefined, {}, NOW), null)
  const v = warmView(w, { codex: 10 }, NOW)!
  assert.equal(v.manual, true)
  assert.equal(Math.round(v.left / 60000), 7)
  // 手填只补取不到的：取自用量的档位优先
  assert.equal(warmView({ ...w, k: 'claude:s', ttl: 300 }, { claude: 60 }, NOW)!.manual, false)
})

test('缓存余温：推送里主会话的新请求重新计时，档位这次看不出时沿用之前的；子任务不算', () => {
  const k = 'claude:s'
  const st = new LaneStore()
  st.merge(data([], 1, { warm: [{ k, model: 'm', at: NOW - 30 * 60_000, ttl: 3600, ctx: 100000 }] }))
  st.push({ m: [], s: [], g: [], t: [{ k, a: '', model: 'm', m: nowMin, t: tv({ in: 10, cr: 120000, n: 2 }), h: tv() }] }, NOW)
  const v = st.warmOf(k, NOW)!
  assert.equal(v.ttl, 3600) // 只读缓存、没写：沿用 1 小时档
  assert.equal(v.ctx, 60005)
  assert.ok(v.left > 59 * 60_000) // 命中一次，重新计时
  st.push({ m: [], s: [], g: [], t: [{ k, a: 'sub', model: 'm', m: nowMin, t: tv({ cw: 5000, n: 1 }), h: tv() }] }, NOW + 1000)
  assert.equal(st.warmOf(k, NOW)!.ttl, 3600)
  // 旧的拉取结果晚到，不盖掉新的
  st.merge(data([], 1, { warm: [{ k, model: 'm', at: NOW - 30 * 60_000, ttl: 300, ctx: 1 }] }))
  assert.equal(st.warmOf(k, NOW)!.ttl, 3600)
  // 只有 5 分钟档写入 → 5 分钟档
  st.push({ m: [], s: [], g: [], t: [{ k: 'claude:n', a: '', model: 'm', m: nowMin, t: tv({ cw: 8000, n: 1 }), h: tv() }] }, NOW)
  assert.equal(st.warmOf('claude:n', NOW)!.ttl, 300)
})

test('子任务用过的模型：按用量排序，没用量为空，主会话和别的子任务不混', () => {
  const s = new LaneStore()
  const v = (n: number) => ({ in: n, cw: 0, cw1h: 0, cr: 0, out: 0, n: 1 })
  s.push({ m: null, s: null, g: null, t: [
    { k: 'claude:x', a: 'g1', model: 'claude-sonnet-5-5', m: nowMin, t: v(10), h: v(10) },
    { k: 'claude:x', a: 'g1', model: 'claude-opus-5-5', m: nowMin, t: v(500), h: v(500) },
    { k: 'claude:x', a: 'g2', model: 'gpt-6-astra', m: nowMin, t: v(5), h: v(5) },
    { k: 'claude:x', a: '', model: 'claude-haiku-4-5', m: nowMin, t: v(9000), h: v(9000) },
  ] }, NOW)
  assert.deepEqual(s.modelsOf('claude:x', 'g1').map((x) => x.id), ['claude-opus-5-5', 'claude-sonnet-5-5'])
  assert.deepEqual(s.modelsOf('claude:x', 'g2').map((x) => x.short), ['GPT-6 Astra'])
  assert.deepEqual(s.modelsOf('claude:x', 'g3'), [])
  assert.equal(s.modelsOf('claude:x', 'g1')[0].tokens, 500)
})
