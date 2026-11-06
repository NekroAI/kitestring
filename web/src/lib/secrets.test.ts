import { test } from 'node:test'
import assert from 'node:assert/strict'
import { addCommand, envFromLabel, envOK, expiryOf, filledValues, filterSecrets, groupByDay, missingValues, REDACTED, splitRedacted, statusCounts, timeLeft, unionCommands } from './secrets.ts'

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
