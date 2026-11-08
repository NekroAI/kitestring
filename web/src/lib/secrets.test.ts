import { test } from 'node:test'
import assert from 'node:assert/strict'
import { addCommand, amendKind, secretInputProps, secretRun, logNote, missingReplace, replaceFields, replaceLabels, authDiff, diffEmpty, isAmend, KEEP_TTL, changedPlain, envFromLabel, envOK, expiryOf, filledValues, filterSecrets, groupByDay, missingValues, plainMap, REDACTED, shownValues, splitRedacted, statusCounts, timeLeft, unionCommands, valueHint } from './secrets.ts'

const list = [
  { name: 'Cloudflare 部署', purpose: 'wrangler 部署 Pages', status: 'active', projects: ['p1'], project_names: ['kitestring'] },
  { name: 'OpenAI', purpose: '评测脚本', status: 'active', projects: ['p1', 'p2'], project_names: ['kitestring', 'nekro-nxt'] },
  { name: 'Stripe 测试环境', purpose: '支付回调联调', status: 'pending', projects: ['p2'], project_names: ['nekro-nxt'] },
  { name: 'GitHub 发版', purpose: '', status: 'expired', projects: ['p1'], project_names: ['kitestring'] },
  { name: 'Sentry', purpose: '上传 sourcemap', status: 'revoked', projects: ['p1'], project_names: ['kitestring'] },
]

test('状态筛选、项目、搜索词（名字、用途、项目名）可以叠加', () => {
  assert.deepEqual(
    filterSecrets(list, 'all', '', '').map((x) => x.name),
    list.map((x) => x.name),
  )
  assert.deepEqual(
    filterSecrets(list, 'active', '', '').map((x) => x.name),
    ['Cloudflare 部署', 'OpenAI'],
  )
  assert.deepEqual(
    filterSecrets(list, 'all', 'p2', '').map((x) => x.name),
    ['OpenAI', 'Stripe 测试环境'],
  )
  assert.deepEqual(
    filterSecrets(list, 'all', '', 'nekro 联调').map((x) => x.name),
    ['Stripe 测试环境'],
  )
  assert.deepEqual(
    filterSecrets(list, 'all', '', 'PAGES').map((x) => x.name),
    ['Cloudflare 部署'],
  )
})

test('页签上的数字按状态计', () => {
  assert.deepEqual(statusCounts(list), { all: 5, active: 2, pending: 1, expired: 1, revoked: 1 })
  assert.deepEqual(statusCounts([]), { all: 0, active: 0, pending: 0, expired: 0, revoked: 0 })
})

test('有效期：0 是长期；剩余时间不足 1 天写小时，过期为空', () => {
  const now = Date.parse('2026-11-06T07:20:00Z')
  assert.equal(expiryOf(0, now), undefined)
  assert.equal(expiryOf(7, now)!.toISOString(), '2026-11-13T07:20:00.000Z')
  assert.deepEqual(timeLeft('2026-11-12T08:00:00Z', now), { days: 6 })
  assert.deepEqual(timeLeft('2026-11-06T09:00:00Z', now), { hours: 2 })
  assert.deepEqual(timeLeft('2026-11-06T07:21:00Z', now), { hours: 1 })
  assert.equal(timeLeft('2026-11-06T07:00:00Z', now), undefined)
  assert.equal(timeLeft(undefined, now), undefined)
})

test('命令里的已知值切成「已隐去」小标', () => {
  assert.deepEqual(splitRedacted(`curl -H "Authorization: Bearer ${REDACTED}" https://a`), [
    { text: 'curl -H "Authorization: Bearer ', redacted: false },
    { text: '', redacted: true },
    { text: '" https://a', redacted: false },
  ])
  assert.deepEqual(splitRedacted('wrangler whoami'), [{ text: 'wrangler whoami', redacted: false }])
  assert.deepEqual(splitRedacted(`${REDACTED}${REDACTED}`), [
    { text: '', redacted: true },
    { text: '', redacted: true },
  ])
})

test('取用记录按本地日期分组，组内顺序不变', () => {
  const at = (d: number, h: number) => new Date(2026, 10, d, h).toISOString()
  const g = groupByDay([{ at: at(6, 15) }, { at: at(6, 9) }, { at: at(5, 23) }, { at: at(3, 1) }])
  assert.deepEqual(
    g.map((x) => x.items.length),
    [2, 1, 1],
  )
})

test('允许的命令：去空白、去重；重新申请时并上这次要的', () => {
  assert.deepEqual(addCommand(['wrangler whoami'], '  wrangler   whoami '), ['wrangler whoami'])
  assert.deepEqual(addCommand([], '   '), [])
  assert.deepEqual(unionCommands(['a *'], ['b', 'a  *']), ['a *', 'b'])
})

test('保存前检查：没填过的字段都要有值；只发填了的值', () => {
  const fields = [
    { env: 'A', filled: true },
    { env: 'B', filled: false },
    { env: 'C', filled: false },
  ]
  assert.deepEqual(
    missingValues(fields, { B: 'x', C: '  ' }).map((f) => f.env),
    ['C'],
  )
  assert.deepEqual(filledValues({ A: '', B: 'x', C: ' ' }), { B: 'x' })
})

test('环境变量名：字母数字下划线，不能以数字开头；按显示名推', () => {
  assert.ok(envOK('CLOUDFLARE_API_TOKEN'))
  assert.ok(!envOK('1X'))
  assert.ok(!envOK('A-B'))
  assert.equal(envFromLabel('API Token'), 'API_TOKEN')
  assert.equal(envFromLabel('Account ID'), 'ACCOUNT_ID')
  assert.equal(envFromLabel('账户'), '')
})

test('敏感值的填写提示：少于 6 个字符、常见词（忽略大小写）；不敏感或空值不提示', () => {
  assert.equal(valueHint('admin'), 'short')
  assert.equal(valueHint('甲乙丙丁戊'), 'short')
  assert.equal(valueHint('production'), 'common')
  assert.equal(valueHint(' Production '), 'common')
  assert.equal(valueHint('ADMINISTRATOR'), 'common')
  assert.equal(valueHint('cf-token-AbCdEf0123'), undefined)
  assert.equal(valueHint('production', true), undefined)
  assert.equal(valueHint('admin', true), undefined)
  assert.equal(valueHint('   '), undefined)
})

test('敏感标记：保存时发全部，替换时只发改了的；老数据没有 plain 按敏感', () => {
  const fields = [{ env: 'USER', plain: true }, { env: 'PASS' }, { env: 'ID', plain: false }]
  assert.deepEqual(plainMap(fields), { USER: true, PASS: false, ID: false })
  assert.deepEqual(changedPlain(fields, { USER: true, PASS: true, ID: false }), { PASS: true })
  assert.deepEqual(changedPlain(fields, {}), {})
})

test('凭据页「值」一栏：不敏感的明文列出，有敏感的再画圆点', () => {
  assert.deepEqual(shownValues([{ plain: true, filled: true, value: 'admin' }, { filled: true }]), { plain: ['admin'], masked: true })
  assert.deepEqual(shownValues([{ plain: true, filled: true, value: 'admin' }]), { plain: ['admin'], masked: false })
  assert.deepEqual(shownValues([{ plain: true, filled: false }, { filled: true }]), { plain: [], masked: true })
})

test('修改授权申请（R-320）：改前 → 改后改了哪几项', () => {
  const before = { projects: ['p1'], allowed: ['sh -c ./deploy.sh --prod', 'portal whoami'], disk: false }
  assert.ok(diffEmpty(authDiff(before, { ...before, days: KEEP_TTL })))
  const d = authDiff(before, { projects: ['p1', 'p2'], allowed: ['portal whoami', 'sh -c *'], disk: true, days: 30 })
  assert.deepEqual(d.projects, { added: ['p2'], removed: [] })
  assert.deepEqual(d.allowed, { added: ['sh -c *'], removed: ['sh -c ./deploy.sh --prod'], kept: ['portal whoami'] })
  assert.equal(d.expiry, true)
  assert.equal(d.disk, true)
  assert.ok(!diffEmpty(d))
  // 改成不限命令：去掉全部
  assert.deepEqual(authDiff(before, { ...before, allowed: [], days: KEEP_TTL }).allowed, { added: [], removed: before.allowed, kept: [] })
  assert.ok(isAmend({ ask: { mode: 'amend' } }))
  assert.ok(!isAmend({ ask: { mode: '' } }) && !isAmend(null))
})

test('附言（R-321）：新的在 note，老数据里拒绝的附言在 reason', () => {
  assert.equal(logNote({ kind: 'grant', note: '这个 Token 只读' }), '这个 Token 只读')
  assert.equal(logNote({ kind: 'reject', note: '改用 run', reason: '旧' }), '改用 run')
  assert.equal(logNote({ kind: 'reject', reason: '先别部署' }), '先别部署')
  // 别的类别的 reason 不是附言（被拒原因、改了什么、理由）
  assert.equal(logNote({ kind: 'edit', reason: '允许的命令：加上「a」' }), '')
  assert.equal(logNote({ kind: 'use', reason: '不在允许范围内' }), '')
})

test('替换值（R-325）：要替换的字段、获取方式、没填新值的、时间线的显示名', () => {
  const fields = [
    { env: 'GITHUB_USER', label: '用户名', how: '个人主页', filled: true, plain: true },
    { env: 'GITHUB_TOKEN', label: 'Token', how: 'Settings → Tokens', filled: true },
    { env: 'ORG', label: '组织', filled: true },
  ]
  const rs = replaceFields(fields, [{ env: 'ORG' }, { env: 'GITHUB_TOKEN', how: 'Fine-grained tokens → Generate' }])
  // 按凭据字段的顺序；写了新的获取方式用新的，没写沿用原来的；不改原字段
  assert.deepEqual(rs.map((f) => [f.env, f.how]), [['GITHUB_TOKEN', 'Fine-grained tokens → Generate'], ['ORG', undefined]])
  assert.equal(fields[1].how, 'Settings → Tokens')
  assert.deepEqual(replaceFields(fields, undefined), [])
  // 已填过值的字段也要填新值才能保存
  assert.deepEqual(missingReplace(rs, { GITHUB_TOKEN: 'ghp_x' }).map((f) => f.env), ['ORG'])
  assert.deepEqual(missingReplace(rs, { GITHUB_TOKEN: 'ghp_x', ORG: '  ' }).map((f) => f.env), ['ORG'])
  assert.deepEqual(missingReplace(rs, { GITHUB_TOKEN: 'ghp_x', ORG: 'kite' }), [])
  assert.deepEqual(replaceLabels('Token、用户名'), ['Token', '用户名'])
  assert.deepEqual(replaceLabels(''), [])
  assert.deepEqual(replaceLabels(undefined), [])
})

test('编号与改名（R-326）：待处理的类别词、推荐写法', () => {
  assert.equal(amendKind({ replace: [{ env: 'A' }], name: '新名' }), 'replace')
  assert.equal(amendKind({ replace: [], name: '新名' }), 'rename')
  assert.equal(amendKind({ name: '' }), 'amend')
  assert.equal(amendKind(null), 'amend')
  assert.equal(secretRun('S-3'), 'ks secret run S-3 -- <命令>')
})

test('密钥框（R-327）：能遮盖时不用 password 框，浏览器不弹已存密码', () => {
  const m = secretInputProps(false, true)
  assert.equal(m.type, 'text')
  assert.equal(m.className, 'secret-mask')
  assert.equal(m.autoComplete, 'off')
  assert.equal(m['data-1p-ignore'], '')
  // 显示明文：普通文字框、不遮盖
  assert.deepEqual([secretInputProps(true, true).type, secretInputProps(true, true).className], ['text', ''])
  // 不支持遮盖的浏览器退回 password 框，用 new-password 不填已存密码
  assert.deepEqual([secretInputProps(false, false).type, secretInputProps(false, false).autoComplete], ['password', 'new-password'])
})
