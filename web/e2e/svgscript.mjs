// SVG 素材的脚本隔离（R-387）：在真浏览器里证明资源库和分享页显示 SVG 时，SVG 里的脚本、事件属性、javascript: 链接都不执行。
// 用法：HUB=http://127.0.0.1:7417 PASSWORD=demo-pass-123 PW=/path/to/playwright-core/index.mjs CHANNEL=chrome \
//   node web/e2e/svgscript.mjs <素材 id> [结果截图路径]
// 素材 id 指向一件主文件是「执行了就把页面标题改成 PWNED 并请求 /pwned」的 SVG（如 internal/assetfmt 的 ScriptSVG）。
// 逐项核对：① 对照组——同一段 SVG 内联进页面时脚本确实会执行（证明探针有效）；② 资源库详情（<img> 加载）不执行；
// ③ 分享页同样不执行；④ 直接打开文件地址按下载处理；⑤ 假如浏览器无视下载、把它当页面打开，Hub 带的 CSP（sandbox）也拦住脚本。
// 任一项不符以非零状态退出。
const HUB = process.env.HUB || 'http://127.0.0.1:7417'
const PASSWORD = process.env.PASSWORD || 'demo-pass-123'
const { chromium } = await import(process.env.PW || 'playwright-core')
const id = process.argv[2]
const shot = process.argv[3] || ''
if (!id) throw new Error('要给素材 id')

const b = await chromium.launch(process.env.CHANNEL ? { channel: process.env.CHANNEL } : {})
const ctx = await b.newContext({ viewport: { width: 1440, height: 900 }, deviceScaleFactor: 1, acceptDownloads: true })
let pwned = 0
ctx.on('request', (r) => {
  if (r.url().includes('/pwned')) pwned++
})
const p = await ctx.newPage()
p.on('dialog', (d) => {
  pwned++
  void d.dismiss()
})
await p.goto(HUB + '/login')
await p.request.post(HUB + '/api/auth/login', { data: { password: PASSWORD } })
const list = await (await p.request.get(HUB + '/api/assets?id=' + encodeURIComponent(id))).json()
const a = list.assets?.[0]
if (!a) throw new Error('找不到素材 ' + id)
const fid = a.latest.file.id
const svgText = await (await p.request.get(HUB + '/api/files/' + fid)).text()
const results = []
const check = (name, ok, note) => results.push({ name, ok, note })
const titleOf = (pg) => pg.evaluate(() => document.title)

// ① 对照组：内联进页面（不该这样做）时脚本会执行
{
  const q = await ctx.newPage()
  const before = pwned
  await q.setContent(`<!doctype html><title>inline</title>${svgText.replace(/^<\?xml[^>]*>/, '')}`)
  await q.waitForTimeout(800)
  const t = await titleOf(q)
  check('对照：内联进页面时探针生效', t === 'PWNED' || pwned > before, `标题「${t}」，/pwned 请求 ${pwned - before} 次`)
  await q.close()
}
pwned = 0

// ② 资源库详情：<img> 加载
await p.goto(`${HUB}/assets?p=${a.project_id}&c=${encodeURIComponent(a.category)}${a.latest.subtype ? '&sub=' + encodeURIComponent(a.latest.subtype) : ''}&asset=${a.code}`)
await p.waitForSelector(`[data-svg="${fid}"] img`, { timeout: 15000 })
await p.waitForTimeout(1500)
const loaded = await p.evaluate((fid) => [...document.querySelectorAll(`[data-svg="${fid}"] img`)].map((i) => i.complete && i.naturalWidth > 0), fid)
const t2 = await titleOf(p)
check('资源库详情：SVG 当图片显示', loaded.length > 0 && loaded.every(Boolean), `${loaded.length} 处 <img> 加载完成`)
check('资源库详情：脚本未执行', t2 !== 'PWNED' && pwned === 0, `标题「${t2}」，/pwned 请求 ${pwned} 次`)
const inlined = await p.evaluate(() => document.querySelectorAll('svg script, svg [onload]').length)
check('页面里没有内联 SVG 原文', inlined === 0, `页面里带 script / onload 的 SVG 元素 ${inlined} 个`)
if (shot) await p.screenshot({ path: shot.replace(/\.png$/, '-detail.png') })

// ③ 分享页
const share = await (await p.request.post(HUB + '/api/shares', { data: { name: 'R-387 SVG 验证', links: ['验证'], items: [{ kind: 'asset', ref: a.id }] } })).json()
const tok = share.links?.[0]?.token
if (tok) {
  const g = await b.newContext({ viewport: { width: 1440, height: 900 }, deviceScaleFactor: 1 })
  let gp = 0
  g.on('request', (r) => {
    if (r.url().includes('/pwned')) gp++
  })
  const gpg = await g.newPage()
  await gpg.goto(`${HUB}/s/${tok}/a?asset=${a.code}`)
  await gpg.waitForSelector(`[data-svg="${fid}"] img`, { timeout: 15000 })
  await gpg.waitForTimeout(1500)
  const t3 = await titleOf(gpg)
  check('分享页：脚本未执行', t3 !== 'PWNED' && gp === 0, `标题「${t3}」，/pwned 请求 ${gp} 次`)
  await g.close()
} else check('分享页：脚本未执行', false, '没建成分享：' + JSON.stringify(share).slice(0, 120))

// ④ 直接打开文件地址：按下载处理
{
  const q = await ctx.newPage()
  const dl = q.waitForEvent('download', { timeout: 8000 }).catch(() => null)
  await q.goto(HUB + '/api/files/' + fid).catch(() => {})
  const d = await dl
  check('直接打开文件地址：按下载处理，不在本站打开', !!d, d ? `下载为 ${d.suggestedFilename()}` : '没有触发下载')
  await q.close()
}

// ⑤ 去掉「按下载」后强行当页面打开：只剩 Hub 的 CSP 兜底
{
  const q = await ctx.newPage()
  let csp = ''
  await q.route('**/api/files/' + fid, async (route) => {
    const r = await route.fetch()
    const h = { ...r.headers() }
    csp = h['content-security-policy'] || ''
    delete h['content-disposition']
    await route.fulfill({ response: r, headers: h })
  })
  const before = pwned
  await q.goto(HUB + '/api/files/' + fid)
  await q.waitForTimeout(1200)
  const t = await titleOf(q).catch(() => '')
  check('强行当页面打开：CSP 拦住脚本', t !== 'PWNED' && pwned === before && /sandbox/.test(csp) && !/allow-scripts/.test(csp), `CSP「${csp}」，标题「${t}」，/pwned 请求 ${pwned - before} 次`)
  await q.close()
}

const bad = results.filter((r) => !r.ok)
for (const r of results) console.log(`${r.ok ? '通过' : '不符'}  ${r.name} —— ${r.note}`)
if (shot) {
  const q = await ctx.newPage()
  await q.setViewportSize({ width: 960, height: 120 + results.length * 56 })
  const rows = results.map((r) => `<tr><td class="${r.ok ? 'ok' : 'bad'}">${r.ok ? '通过' : '不符'}</td><td>${r.name}</td><td class="n">${r.note.replace(/</g, '&lt;')}</td></tr>`).join('')
  await q.setContent(`<!doctype html><meta charset="utf-8"><style>body{font:14px -apple-system,"PingFang SC",sans-serif;margin:24px;color:#222}h1{font-size:17px;margin:0 0 12px}table{border-collapse:collapse;width:100%}td{border-bottom:1px solid #e5e5e5;padding:9px 8px;vertical-align:top}.ok{color:#1d7a4c;font-weight:600;white-space:nowrap}.bad{color:#b3261e;font-weight:600}.n{color:#666;font-size:12px;word-break:break-all}</style><h1>SVG 脚本隔离验证（Chrome ${b.version()}，素材 ${id}）</h1><table>${rows}</table>`)
  await q.screenshot({ path: shot })
}
await b.close()
process.exit(bad.length ? 1 : 0)
