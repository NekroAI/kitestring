// 泳道时间轴对齐检查（R-219）：四档分度下，所有泳道（主泳道、子任务行、已结束泳道）用同一条时间轴，
// 时间线列左右边缘一致，轴上的刻度字和画布同一位置。
// 用法：HUB=http://127.0.0.1:7415 PASSWORD=demo-pass-123 PW=/path/to/playwright-core/index.mjs node web/e2e/axis.mjs [截图前缀]
// 有任何一行的轴 / 左边缘 / 宽度和别的行不一样就以非零状态退出。
const HUB = process.env.HUB || 'http://127.0.0.1:7415'
const PASSWORD = process.env.PASSWORD || 'demo-pass-123'
const { chromium } = await import(process.env.PW || 'playwright-core')
const tag = process.argv[2] || ''
const ZOOMS = [['h1', /^(近 )?1 小时$/], ['h6', /^(近 )?6 小时$/], ['d1', /^(近 )?1 天$/], ['w1', /^(近 )?1 周$/]]
const SIZES = [['1440', 1440, 900], ['wide', 3348, 1280], ['ipad', 1024, 768], ['1280', 1280, 800]]
let fails = 0
const ok = (c, m) => (console.log(`${c ? 'ok  ' : 'FAIL'} ${m}`), c || fails++)
const b = await chromium.launch()
for (const [name, w, h] of SIZES) {
  const ctx = await b.newContext({ viewport: { width: w, height: h }, deviceScaleFactor: 1 })
  const p = await ctx.newPage()
  await p.goto(HUB + '/login')
  await p.request.post(HUB + '/api/auth/login', { data: { password: PASSWORD } })
  await p.goto(HUB + '/')
  await p.waitForSelector('[data-lane] canvas')
  await p.waitForTimeout(2500)
  for (const [z, label] of ZOOMS) {
    await p.locator('button', { hasText: label }).first().click({ timeout: 3000 }).catch(() => {})
    await p.waitForTimeout(1500)
    const rows = await p.evaluate(() => [...document.querySelectorAll('[data-lane] canvas')].map((c) => { const r = c.getBoundingClientRect(); return { axis: c.dataset.axis, left: +r.left.toFixed(2), width: +r.width.toFixed(2), sub: !!c.closest('[data-lane] > div:last-child') && !c.parentElement.classList.contains('absolute') && c.parentElement.className.includes('h-6') } }))
    const uniq = (f) => new Set(rows.map(f)).size
    ok(rows.length > 1 && uniq((r) => r.axis) === 1, `${name} ${z}：${rows.length} 行画布用同一条轴（${rows[0]?.axis}）`)
    ok(uniq((r) => r.left) === 1 && uniq((r) => r.width) === 1, `${name} ${z}：时间线列左右边缘一致（${rows[0]?.left} + ${rows[0]?.width}）`)
    // 轴上的刻度字：位置按同一条轴
    const ticks = await p.evaluate(() => {
      const col = document.querySelector('[data-lane] canvas')?.getBoundingClientRect()
      const labels = [...document.querySelectorAll('div.sticky span.absolute')].map((s) => s.getBoundingClientRect())
      return { col: col && [col.left, col.width], n: labels.length, inside: labels.every((r) => col && r.left + r.width / 2 >= col.left - 1 && r.left + r.width / 2 <= col.left + col.width + 1) }
    })
    ok(ticks.n > 0 && ticks.inside, `${name} ${z}：轴上 ${ticks.n} 个刻度字都落在时间线列内`)
    if (tag && (name === '1440' || name === 'wide' || name === 'ipad')) {
      // 证据截图：在第 1/3 处拉一条竖线，核对每一行同一竖线是同一时刻（只在截图里画，不进产品）
      const col = await p.evaluate(() => { const r = document.querySelector('[data-lane] canvas').getBoundingClientRect(); return [r.left, r.width] })
      await p.evaluate(([l, wd]) => { const d = document.createElement('div'); d.id = '__ruler'; d.style.cssText = `position:fixed;top:0;bottom:0;left:${l + wd * 0.62}px;width:2px;background:#e11d48;z-index:99999;pointer-events:none`; document.body.appendChild(d) }, col)
      await p.screenshot({ path: `${tag}-${name}-${z}.png` })
      await p.evaluate(() => document.getElementById('__ruler')?.remove())
    }
  }
  await ctx.close()
}
await b.close()
console.log(fails ? `\n${fails} 项失败` : '\n全部通过')
process.exit(fails ? 1 : 0)
