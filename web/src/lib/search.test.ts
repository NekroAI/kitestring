import { test } from 'node:test'
import assert from 'node:assert/strict'
import { flatten, parseQuery, scoreDoc, search, type SearchDoc } from './search.ts'

const docs: SearchDoc[] = [
  { id: 'r12', kind: 'req', code: 'R-12', title: '登录页加通行密钥', fields: ['kitestring'] },
  { id: 'r120', kind: 'req', code: 'R-120', title: '需求池盘面', fields: ['kitestring'] },
  { id: 'r7', kind: 'req', code: 'R-7', title: '命令面板快捷键', fields: ['demo'], boost: 20 },
  { id: 'd12', kind: 'dec', code: 'D-12', title: '面板放哪里' },
  { id: 'p3', kind: 'plan', code: 'P-3', title: '第二阶段：命令面板' },
  { id: 's1', kind: 'session', title: '修 hub 的登录', fields: ['~/Projects/kitestring', 'main'] },
  { id: 'pj', kind: 'project', title: 'kitestring' },
  { id: 'c1', kind: 'cmd', title: '新建需求', keywords: ['new', 'create'] },
]

test('parseQuery 认出编号与纯数字', () => {
  assert.deepEqual(parseQuery(' R-12 ').code, { prefix: 'R', num: '12' })
  assert.deepEqual(parseQuery('r12').code, { prefix: 'R', num: '12' })
  assert.equal(parseQuery('12').digits, '12')
  assert.equal(parseQuery('登录 页').tokens.length, 2)
})

test('R-12 精确命中排第一，R-120 只算前缀', () => {
  const g = search(docs, 'R-12')
  assert.equal(g[0].kind, 'req')
  assert.equal(g[0].items[0].id, 'r12')
  assert.equal(g[0].items[1].id, 'r120')
  assert.ok(!flatten(g).some((d) => d.id === 'd12'), 'R- 开头不命中决策')
})

test('纯数字跨类型命中同号编号', () => {
  const ids = flatten(search(docs, '12')).map((d) => d.id)
  assert.ok(ids.includes('r12') && ids.includes('d12'))
  assert.equal(ids[0], 'r12')
})

test('关键词：每个词都要命中，标题优先于次要字段', () => {
  const ids = flatten(search(docs, '命令 面板')).map((d) => d.id)
  assert.deepEqual(ids.sort(), ['p3', 'r7'])
  assert.equal(flatten(search(docs, '登录 不存在')).length, 0)
  // 项目名命中排在标题命中之后
  const k = search(docs, 'kitestring')
  assert.equal(k[0].kind, 'project')
})

test('命令可以用别名找到，大小写不敏感', () => {
  assert.equal(flatten(search(docs, 'NEW'))[0].id, 'c1')
})

test('scoreDoc：空输入都命中，boost 加分', () => {
  const q = parseQuery('')
  assert.ok(scoreDoc(docs[2], q) > scoreDoc(docs[0], q))
})

test('每组限量', () => {
  const many: SearchDoc[] = Array.from({ length: 20 }, (_, i) => ({ id: `x${i}`, kind: 'req', code: `R-${i + 1}`, title: `需求 ${i}` }))
  assert.equal(search(many, '需求', 6)[0].items.length, 6)
  assert.equal(search(many, '需求', 0)[0].items.length, 20)
})
