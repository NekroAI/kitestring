// 工作台三栏溢出检查（R-220）：在多种屏幕比例下，逐个元素量「被祖先裁剪后看得见的部分」有没有伸出所在栏的左右边界。
// 用法：HUB=http://127.0.0.1:7415 PASSWORD=demo-pass-123 PW=/path/to/playwright-core/index.mjs node web/e2e/overflow.mjs [截图前缀]
// 有溢出时以非零状态退出，并列出哪一栏的哪个元素、伸出多少像素；第二个参数是截图前缀（可省，不给就不截图）。
const HUB = process.env.HUB || 'http://127.0.0.1:7415'
const PASSWORD = process.env.PASSWORD || 'demo-pass-123'
const { chromium } = await import(process.env.PW || 'playwright-core')
const H = HUB
const tag = process.argv[2] || ''
const VPS = [['1280x800', 1280, 800, false], ['1440x900', 1440, 900, false], ['1920x1080', 1920, 1080, false], ['2560x1440', 2560, 1440, false], ['wide3348', 3348, 1280, false], ['tall1180x1600', 1180, 1600, false], ['ipad-land', 1366, 1024, true], ['ipad-land2', 1180, 820, true], ['ipad-port', 820, 1180, true], ['ipad-port2', 1024, 1366, true], ['short1440x600', 1440, 600, false], ['short1920x700', 1920, 700, false], ['1100x700', 1100, 700, false], ['phone', 390, 844, true]]
const b = await chromium.launch()
const res = {}
for (const [name, w, h, touch] of VPS) {
  const ctx = await b.newContext({ viewport: { width: w, height: h }, deviceScaleFactor: 1, hasTouch: touch })
  const p = await ctx.newPage()
  await p.goto(H + '/login'); await p.request.post(H + '/api/auth/login', { data: { password: PASSWORD } })
  await p.goto(H + '/'); await p.waitForTimeout(3500)
  const r = await p.evaluate(() => {
    const clipOf = (el, stop) => {
      let r = { l: -1e9, t: -1e9, r: 1e9, b: 1e9 }
      for (let a = el.parentElement; a && a !== stop?.parentElement; a = a.parentElement) {
        const cs = getComputedStyle(a)
        if (cs.overflowX !== 'visible' || cs.overflowY !== 'visible') {
          const b = a.getBoundingClientRect()
          r = { l: Math.max(r.l, b.left), t: Math.max(r.t, b.top), r: Math.min(r.r, b.right), b: Math.min(r.b, b.bottom) }
        }
      }
      return r
    }
    const panes = [...document.querySelectorAll('section, aside')].filter((e) => e.parentElement && /grid|block/.test(getComputedStyle(e.parentElement).display) && e.getBoundingClientRect().width > 150 && e.getBoundingClientRect().height > 200)
    const out = []
    const seen = new Set()
    for (const pane of panes) {
      const pr = pane.getBoundingClientRect()
      for (const el of pane.querySelectorAll('*')) {
        if (el.closest('[data-skel-skip]') && false) continue
        const cs = getComputedStyle(el)
        if (cs.display === 'none' || cs.visibility === 'hidden' || cs.position === 'fixed') continue
        const rc = el.getBoundingClientRect()
        if (!rc.width || !rc.height) continue
        const c = clipOf(el, pane)
        const vis = { l: Math.max(rc.left, c.l), r: Math.min(rc.right, c.r), t: Math.max(rc.top, c.t), b: Math.min(rc.bottom, c.b) }
        if (vis.r <= vis.l || vis.b <= vis.t) continue
        if (vis.r > pr.right + 1.5 || vis.l < pr.left - 1.5) {
          const key = el.tagName + '.' + (el.className?.toString().slice(0, 50) || '')
          if (seen.has(key)) continue
          seen.add(key)
          out.push({ pane: pane.tagName + ':' + (pane.querySelector('h2')?.textContent || pane.className.toString().slice(0, 30)), el: key, text: (el.textContent || '').slice(0, 30), over: Math.round(Math.max(vis.r - pr.right, pr.left - vis.l)) })
        }
      }
    }
    // 页面横向滚动
    const sx = document.documentElement.scrollWidth - innerWidth
    return { n: panes.length, out: out.slice(0, 12), sx }
  })
  res[name] = r
  if (tag) await p.screenshot({ path: `${tag}-${name}.png` })
  await ctx.close()
}
let bad = 0
for (const [name, r] of Object.entries(res)) {
  const n = r.out.length + (r.sx > 0 ? 1 : 0)
  bad += n
  console.log(`${n ? 'FAIL' : 'ok  '} ${name}（${r.n} 栏${r.sx > 0 ? `，页面横向多出 ${r.sx}px` : ''}）`)
  for (const o of r.out) console.log(`       ${o.pane} 的 ${o.el}「${o.text}」伸出 ${o.over}px`)
}
await b.close()
process.exit(bad ? 1 : 0)
