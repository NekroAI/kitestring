import assert from 'node:assert/strict'
import { EventEmitter } from 'node:events'
import { PassThrough } from 'node:stream'
import test from 'node:test'

import { McpClient } from '../lib/mcp.js'
import { normalizeToolSchema } from '../lib/schema.js'

// ---- schema 收敛 ---------------------------------------------------------

test('可空类型收敛成单个 type，未知关键字丢掉，object 补 properties', () => {
  const out = normalizeToolSchema({
    type: 'object',
    $schema: 'x',
    properties: {
      title: { type: ['null', 'string'], description: '标题', format: 'uri' },
      tags: { type: ['null', 'array'], items: { type: 'string' } },
      tuple: { type: 'array', items: [{ type: 'number' }, { type: 'string' }] },
      any: { type: ['null'] },
      obj: { type: 'object' },
    },
    required: ['title', 3],
    additionalProperties: false,
  })
  assert.deepEqual(out, {
    type: 'object',
    properties: {
      title: { type: 'string', description: '标题' },
      tags: { type: 'array', items: { type: 'string' } },
      tuple: { type: 'array', items: { type: 'number' } },
      any: { type: 'string' },
      obj: { type: 'object', properties: {} },
    },
    required: ['title'],
    additionalProperties: false,
  })
})

test('非对象 schema 退化成空对象参数', () => {
  assert.deepEqual(normalizeToolSchema(null), { type: 'object', properties: {} })
  assert.deepEqual(normalizeToolSchema([1]), { type: 'object', properties: {} })
})

// ---- ks mcp 子进程 -------------------------------------------------------

function fakeChild() {
  const child = new EventEmitter()
  child.stdin = new PassThrough()
  child.stdout = new PassThrough()
  child.stderr = new PassThrough()
  child.exitCode = null
  child.signalCode = null
  child.kill = () => true
  // 回应 initialize
  child.stdin.on('data', (buf) => {
    for (const line of String(buf).split('\n')) {
      if (!line.trim()) continue
      const msg = JSON.parse(line)
      if (msg.method === 'initialize') child.stdout.write(`${JSON.stringify({ jsonrpc: '2.0', id: msg.id, result: { instructions: 'hi' } })}\n`)
    }
  })
  return child
}

test('stdin 出错（EPIPE）不抛到进程，挂着的请求失败；进程退出后写入直接报错', async () => {
  const child = fakeChild()
  const client = new McpClient({ command: 'ks', spawnFn: () => child })
  await client.start()
  assert.equal(client.instructions, 'hi')
  const pending = client.request('tools/list', {})
  const epipe = Object.assign(new Error('write EPIPE'), { code: 'EPIPE' })
  child.stdin.emit('error', epipe) // 没有监听的话这里会抛出，带崩 Host
  await assert.rejects(pending, /连接已断开/)
  let exited = false
  client.onExit = () => {
    exited = true
  }
  child.exitCode = 0
  child.emit('exit', 0, null)
  assert.ok(exited, '退出要通知插件去重连')
  await assert.rejects(client.request('tools/list', {}), /连接已关闭/)
  client.notify('notifications/cancelled', {}) // 通知静默失败，不抛
})

test('子进程 error 事件来两次也不会成为未处理的 error', async () => {
  const child = fakeChild()
  const client = new McpClient({ command: 'ks', spawnFn: () => child })
  await client.start()
  child.emit('error', new Error('kill ESRCH'))
  child.emit('error', new Error('again'))
  client.close()
})

test('真实子进程：初始化前就退出，start 报错而不是让 Host 崩溃', async () => {
  const client = new McpClient({ command: process.execPath, args: ['-e', 'process.stdin.destroy(); process.exit(0)'] })
  await assert.rejects(client.start(), /退出|连接/)
  client.close()
})
