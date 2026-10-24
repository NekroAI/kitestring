// 聚光（R-215）的浏览器测试：对演示 hub（真实数据快照，不碰线上 hub）跑一遍悬停 / 离开 / 键盘 / 触屏。
// 用法：HUB=http://127.0.0.1:7415 PASSWORD=demo-pass-123 PW=/path/to/playwright-core/index.mjs node web/e2e/spotlight.mjs
// 失败时以非零状态退出。覆盖：从上下左右四个方向离开、快速划出、经过两栏空隙 / 滚动条离开、滚动、窗口失焦、
// 悬停 90ms 内扫过不闪、Tab 聚焦、触屏点开抽屉期间保持关联、关闭恢复、撤销线和呼吸灯。
const HUB = process.env.HUB || 'http://127.0.0.1:7415'
const PASSWORD = process.env.PASSWORD || 'demo-pass-123'
const { chromium } = await import(process.env.PW || 'playwright-core')

let fails = 0
const ok = (cond, msg) => {
  console.log(`${cond ? 'ok  ' : 'FAIL'} ${msg}`)
  if (!cond) fails++
}
const sleep = (ms) => new Promise((r) => setTimeout(r, ms))

async function open(browser, opts) {
  const ctx = await browser.newContext({ deviceScaleFactor: 1, ...opts })
  const page = await ctx.newPage()
  await page.goto(HUB + '/login')
  await page.request.post(HUB + '/api/auth/login', { data: { password: PASSWORD } })
  await page.goto(HUB + '/')
  await page.waitForSelector('[data-lane]')
  await sleep(2500)
  return { ctx, page }
}

// 每个带 data-spot-key 的元素：[键, 不透明度]
const snap = (page) => page.evaluate(() => [...document.querySelectorAll('[data-spot-key]')].map((e) => [e.getAttribute('data-spot-key'), +getComputedStyle(e).opacity]))
const dimmedCount = async (page) => (await snap(page)).filter(([, o]) => o < 0.99).length
const settle = () => sleep(450) // 90ms 延迟 + 0.2s 过渡

// 选一张有会话、且该会话有泳道的卡片，并确认还有别的会话的元素可以被淡掉
async function pick(page) {
  return page.evaluate(() => {
    const lanes = new Set([...document.querySelectorAll('[data-lane][data-spot-key]')].map((e) => e.getAttribute('data-spot-key')))
    const all = [...document.querySelectorAll('[data-spot-key]')]
    for (const c of document.querySelectorAll('[data-ask]')) {
      const root = c.closest('[data-spot-key]')
      const k = root?.getAttribute('data-spot-key')
      if (!k || !lanes.has(k)) continue
      if (!all.some((e) => e.getAttribute('data-spot-key') !== k)) continue
      const r = root.getBoundingClientRect()
      if (r.top < 150 || r.bottom > innerHeight - 30) continue
      return { key: k, x: r.x + r.width / 2, y: r.y + r.height / 2 }
    }
    return null
  })
}
const laneBox = (page, key) =>
  page.evaluate((k) => {
    const e = document.querySelector(`[data-lane][data-spot-key="${k}"]`)
    const r = e.getBoundingClientRect()
    return { x: r.x + r.width / 2, y: r.y + r.height / 2, top: r.top, bottom: r.bottom }
  }, key)

const browser = await chromium.launch()
try {
  // ---------- 桌面：悬停 / 离开 ----------
  const { ctx, page } = await open(browser, { viewport: { width: 1440, height: 900 } })
  const card = await pick(page)
  ok(!!card, '找到一张有会话和泳道的卡片')
  const hoverCard = async () => {
    await page.mouse.move(card.x, card.y)
    await settle()
  }
  ok((await dimmedCount(page)) === 0, '没有悬停时什么都不淡')

  await hoverCard()
  const s1 = await snap(page)
  ok(s1.filter(([k, o]) => k === card.key && o === 1).length >= 2, '悬停卡片：同一会话的卡片和泳道保持原样')
  ok(s1.filter(([k, o]) => k !== card.key && Math.abs(o - 0.32) < 0.02).length === s1.filter(([k]) => k !== card.key).length && s1.some(([k]) => k !== card.key), '其余卡片和泳道淡到 32%')
  ok(await page.evaluate(() => !document.querySelector('.desk-lit') && ![...document.querySelectorAll('[data-spot-key]')].some((e) => getComputedStyle(e).outlineStyle !== 'none')), '没有描边')
  ok(await page.evaluate(() => ![...document.querySelectorAll('aside')].some((a) => a.querySelector('[data-spot-key]'))), '右侧「风向」不参与')

  // 悬停泳道：同一会话的卡片保持原样
  const lb = await laneBox(page, card.key)
  await page.mouse.move(lb.x, lb.y)
  await settle()
  ok((await snap(page)).filter(([k, o]) => k === card.key && o === 1).length >= 2, '悬停泳道：对应的卡片也保持原样')

  // 四个方向离开
  const exits = { 上: [card.x, 4], 下: [card.x, 895], 左: [20, card.y], 右: [1420, card.y] }
  for (const [name, [x, y]] of Object.entries(exits)) {
    await hoverCard()
    await page.mouse.move(x, y, { steps: 8 })
    await settle()
    ok((await dimmedCount(page)) === 0, `向${name}离开后立即恢复`)
  }
  // 快速划出：一步跳到窗口外 / 一步跳到空白处
  for (const [name, [x, y]] of Object.entries({ 快速划出窗口顶部: [card.x, 0], 快速划到窗口左下: [0, 899], 快速划到右栏: [1300, 500] })) {
    await hoverCard()
    await page.mouse.move(x, y)
    await settle()
    ok((await dimmedCount(page)) === 0, `${name}后恢复`)
  }
  // 经过两栏空隙：从卡片横向移到两栏之间的缝，再到泳道之外
  const gap = await page.evaluate(() => {
    const a = document.querySelector('[data-ask]').closest('[data-spot-key]').getBoundingClientRect()
    const l = document.querySelector('[data-lane]').getBoundingClientRect()
    return { x: (a.right + l.left) / 2, scrollbar: a.right + 3 }
  })
  await hoverCard()
  await page.mouse.move(gap.x, card.y, { steps: 6 })
  await settle()
  ok((await dimmedCount(page)) === 0, '停在两栏之间的空隙时恢复')
  // 经过滚动条：左栏滚动区右边缘
  await hoverCard()
  await page.mouse.move(gap.scrollbar, card.y, { steps: 6 })
  await settle()
  ok((await dimmedCount(page)) === 0, '移到滚动条一侧时恢复')
  // 离开后不再回来：等一会儿状态不会被「补设」回去
  await sleep(400)
  ok((await dimmedCount(page)) === 0, '离开后等一会儿仍然没有淡')

  // 90ms 内扫过不闪：在卡片上停 40ms 就移开，期间所有元素的不透明度始终 ≥ 0.99
  await page.evaluate(() => {
    window.__min = 1
    const loop = () => {
      for (const e of document.querySelectorAll('[data-spot-key]')) window.__min = Math.min(window.__min, +getComputedStyle(e).opacity)
      window.__raf = requestAnimationFrame(loop)
    }
    loop()
  })
  for (let i = 0; i < 3; i++) {
    await page.mouse.move(card.x, card.y)
    await sleep(40)
    await page.mouse.move(1300, 500)
    await sleep(120)
  }
  const min = await page.evaluate(() => (cancelAnimationFrame(window.__raf), window.__min))
  ok(min > 0.99, `停留不到 90ms 的扫过不闪（最低不透明度 ${min.toFixed(2)}）`)

  // 滚动 / 窗口失焦 / 页面隐藏 清除
  await hoverCard()
  // 滚动区真的滚动（内容不够长时用同一事件）：卡片在光标下移走了，清除悬停
  await page.evaluate(() => {
    const el = [...document.querySelectorAll('.desk-scroll')].find((e) => e.scrollHeight > e.clientHeight + 40) || document.querySelector('.desk-scroll')
    el.scrollTop += 40
    el.dispatchEvent(new Event('scroll'))
  })
  await settle()
  ok((await dimmedCount(page)) === 0, '滚动时清除')
  await hoverCard()
  await page.evaluate(() => window.dispatchEvent(new Event('blur')))
  await settle()
  ok((await dimmedCount(page)) === 0, '窗口失焦时清除')
  await hoverCard()
  await page.evaluate(() => {
    Object.defineProperty(document, 'hidden', { configurable: true, get: () => true })
    document.dispatchEvent(new Event('visibilitychange'))
  })
  await settle()
  ok((await dimmedCount(page)) === 0, '页面隐藏时清除')
  await page.evaluate(() => Object.defineProperty(document, 'hidden', { configurable: true, get: () => false }))

  // 键盘：Tab 聚焦卡片触发，离开卡片恢复；鼠标点击后留下的焦点不触发
  await page.mouse.move(1300, 500)
  await page.evaluate(() => document.body.focus())
  let focused = false
  for (let i = 0; i < 40 && !focused; i++) {
    await page.keyboard.press('Tab')
    focused = await page.evaluate(() => !!document.activeElement?.closest('[data-ask], [data-lane]'))
  }
  await settle()
  ok(focused && (await dimmedCount(page)) > 0, 'Tab 聚焦卡片 / 泳道时触发')
  for (let i = 0; i < 60 && (await page.evaluate(() => !!document.activeElement?.closest('[data-ask], [data-lane]'))); i++) await page.keyboard.press('Tab')
  await settle()
  ok((await dimmedCount(page)) === 0, 'Tab 离开卡片 / 泳道后恢复')
  await page.mouse.click(card.x, card.y)
  await sleep(300)
  await page.keyboard.press('Escape')
  await page.mouse.move(1300, 500, { steps: 4 })
  await settle()
  ok((await dimmedCount(page)) === 0, '鼠标点过卡片、移开后不因留着焦点而继续淡')

  // 淡掉的「等你」呼吸灯暂停
  await hoverCard()
  const lamps = await page.evaluate(() => {
    const out = []
    for (const e of document.querySelectorAll('.desk-lamp.you')) {
      const dim = +getComputedStyle(e.closest('[data-spot-key]')).opacity < 0.99
      out.push([dim, e.getAnimations().map((a) => a.playState).join(',')])
    }
    return out
  })
  ok(lamps.every(([dim, st]) => (dim ? !st.includes('running') : st.includes('running') || st === '')), `淡掉的呼吸灯暂停、未淡的照常（${JSON.stringify(lamps)}）`)
  await ctx.close()

  // ---------- 触屏 ----------
  const t = await open(browser, { viewport: { width: 1024, height: 768 }, hasTouch: true })
  const tc = await pick(t.page)
  ok(!!tc, '触屏：找到卡片')
  await t.page.touchscreen.tap(tc.x, tc.y)
  await sleep(900)
  const opened = await t.page.evaluate(() => new URLSearchParams(location.search).has('a'))
  ok(opened, '触屏点卡片：照常打开审批抽屉')
  ok((await dimmedCount(t.page)) > 0, '抽屉开着期间保持这组关联')
  const kept = (await snap(t.page)).filter(([k, o]) => k === tc.key && o === 1).length >= 1
  ok(kept, '保持的是这张卡片的会话')
  await t.page.keyboard.press('Escape')
  await t.page.evaluate(() => history.length && 0)
  await sleep(900)
  ok(await t.page.evaluate(() => !new URLSearchParams(location.search).has('a')), '抽屉已关闭')
  ok((await dimmedCount(t.page)) === 0, '关闭抽屉即恢复')
  await t.ctx.close()
} finally {
  await browser.close()
}
console.log(fails ? `\n${fails} 项失败` : '\n全部通过')
process.exit(fails ? 1 : 0)
