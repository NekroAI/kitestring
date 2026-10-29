import assert from 'node:assert/strict'
import test from 'node:test'

import { clientKindOf, createPlugin, deliverTo, ENV_AGENT, ENV_ROOT_SESSION, kitestringMessage } from '../index.js'
import { fakeAgent, fakeCtx, fakeDaemon, fakeMcpFactory, flush, sleep } from './helpers.js'

const HOST = '4242@Thu Oct 29 10:00:00 2026'

function setup({ agents = [], daemonOpts = {}, timing = {}, instance = HOST, mcp = fakeMcpFactory(), config = {} } = {}) {
  const env = fakeCtx({ agents })
  const daemon = fakeDaemon(daemonOpts)
  const plugin = createPlugin(env.ctx, { standby: true, ...config }, {
    daemon,
    instance,
    makeMcp: mcp.make,
    quiet: true,
    timing: { retryMs: 20, reconcileMs: 60_000, reconnectBaseMs: 5, ...timing },
  })
  return { env, daemon, plugin, mcp }
}

const names = (daemon) => daemon.hooks.map((h) => h.hook_event_name)

// ---- 待命状态机 ----------------------------------------------------------

test('一轮结束先报带 standby 的 Stop，再挂等待（不经过“等你回复”）', async () => {
  const agent = fakeAgent('s1', { status: 'running' })
  const { env, daemon } = setup({ agents: [agent] })
  await flush()
  assert.deepEqual(names(daemon), ['SessionStart'])
  assert.equal(daemon.waits.length, 0, '运行中不挂等待')

  await env.emit('session/event', agent.session, { type: 'assistant/message', data: { message: { content: [{ type: 'text', text: '做完了' }] } } })
  await env.emit('session/event', agent.session, { type: 'turn/end', data: { turn: 1, reason: { kind: 'completed' } } })
  await flush()
  const stop = daemon.hooks.find((h) => h.hook_event_name === 'Stop')
  assert.equal(stop.source, 'standby')
  assert.equal(stop.last_assistant_message, '做完了')
  assert.equal(stop.instance, HOST)
  assert.ok(daemon.log.indexOf('hook:Stop') < daemon.log.indexOf('wait:dsh:s1'), 'Stop 要先于等待')
  assert.equal(daemon.waits[0].args.instance, HOST, '等待带上 Host 实例')
  env.dispose()
})

test('空闲会话接入：SessionStart 报完才挂等待（否则会被盖回“空闲”）', async () => {
  const agent = fakeAgent('s1')
  const { env, daemon } = setup({ agents: [agent], daemonOpts: { hookContext: () => sleep(30).then(() => '') } })
  await flush()
  assert.equal(daemon.waits.length, 0, 'SessionStart 还没报完')
  await sleep(50)
  await flush()
  assert.equal(daemon.waits.length, 1)
  env.dispose()
})

test('关了待命时 Stop 不带 standby，也不挂等待',async () => {
  const agent = fakeAgent('s1', { status: 'running' })
  const { env, daemon } = setup({ agents: [agent], config: { standby: false } })
  await env.emit('session/event', agent.session, { type: 'turn/end', data: { turn: 1 } })
  await flush()
  assert.equal(daemon.hooks.find((h) => h.hook_event_name === 'Stop').source, undefined)
  assert.equal(daemon.waits.length, 0)
  env.dispose()
})

test('中断的一轮报 Interrupt，同样带 standby', async () => {
  const agent = fakeAgent('s1', { status: 'running' })
  const { env, daemon } = setup({ agents: [agent] })
  await env.emit('session/event', agent.session, { type: 'turn/end', data: { turn: 1, reason: { kind: 'aborted' } } })
  await flush()
  const intr = daemon.hooks.find((h) => h.hook_event_name === 'Interrupt')
  assert.equal(intr.source, 'standby')
  env.dispose()
})

test('等待还活着时轮末不重挂；超时立即续挂', async () => {
  const agent = fakeAgent('s1')
  const { env, daemon } = setup({ agents: [agent] })
  await flush()
  assert.equal(daemon.waits.length, 1, '空闲会话接入即待命')
  agent.status = 'running'
  await env.emit('session/event', agent.session, { type: 'turn/end', data: { turn: 1 } })
  await flush()
  assert.equal(daemon.waits.length, 1, '等待跨回合有效，不重挂')
  daemon.waits[0].resolve({ reason: 'timeout' })
  await flush()
  assert.equal(daemon.waits.length, 2, '超时后续挂')
  env.dispose()
})

test('replaced 后让出，不定时抢回；本 Host 再跑完一轮才重新挂', async () => {
  const agent = fakeAgent('s1')
  const { env, daemon, plugin } = setup({ agents: [agent] })
  await flush()
  daemon.waits[0].resolve({ reason: 'replaced' })
  await flush()
  await sleep(50) // 比重试间隔长
  plugin.reconcileWaits()
  await env.emit('agent/status', { agent, status: 'idle' })
  await flush()
  assert.equal(daemon.waits.length, 1, 'replaced 后不应再挂')
  await env.emit('session/event', agent.session, { type: 'turn/end', data: { turn: 2 } })
  await flush()
  assert.equal(daemon.waits.length, 2, '本 Host 跑完一轮后重新挂')
  env.dispose()
})

test('stale 的 Host：兜底检查会再试（daemon 按实例判 stale，不会抢）；报不出实例就不试', async () => {
  const agent = fakeAgent('s1')
  const { env, daemon, plugin } = setup({ agents: [agent] })
  await flush()
  daemon.waits[0].resolve({ reason: 'stale' })
  await flush()
  plugin.reconcileWaits()
  await flush()
  assert.equal(daemon.waits.length, 2)
  env.dispose()

  const agent2 = fakeAgent('s2')
  const b = setup({ agents: [agent2], instance: '' })
  await flush()
  b.daemon.waits[0].resolve({ reason: 'stale' })
  await flush()
  b.plugin.reconcileWaits()
  await flush()
  assert.equal(b.daemon.waits.length, 1)
  b.env.dispose()
})

test('兜底检查不在一轮进行中补挂等待', async () => {
  const agent = fakeAgent('s1')
  const { env, daemon, plugin } = setup({ agents: [agent] })
  await flush()
  daemon.waits[0].resolve({ reason: 'canceled' })
  await flush()
  agent.status = 'running'
  plugin.reconcileWaits()
  await flush()
  assert.equal(daemon.waits.length, 1, '运行中不补')
  agent.status = 'idle'
  plugin.reconcileWaits()
  await flush()
  assert.equal(daemon.waits.length, 2, '空闲时补上')
  env.dispose()
})

// ---- 重连 ----------------------------------------------------------------

test('daemon 连不上：隔一会儿重试等待', async () => {
  const agent = fakeAgent('s1')
  const { env, daemon } = setup({ agents: [agent] })
  await flush()
  daemon.waits[0].reject(new Error('ECONNREFUSED'))
  await flush()
  assert.equal(daemon.waits.length, 1)
  await sleep(40)
  await flush()
  assert.equal(daemon.waits.length, 2, '重试间隔后重新挂上')
  env.dispose()
})

test('ks mcp 启动失败会退避重连；断开时撤下工具并重连', async () => {
  const mcp = fakeMcpFactory({ failStarts: 2 })
  const { env } = setup({ mcp })
  await sleep(80)
  await flush()
  assert.equal(mcp.starts, 3)
  assert.equal(env.registered.length, 1, '连上后注册工具')
  mcp.made.at(-1).onExit()
  assert.equal(env.registered.length, 0, '断开后撤下工具')
  await sleep(30)
  await flush()
  assert.equal(mcp.starts, 4)
  assert.equal(env.registered.length, 1, '重连后重新注册')
  env.dispose()
})

// ---- 送达 ----------------------------------------------------------------

test('送达：空闲用 followup 开新一轮，运行中用 steer', async () => {
  const agent = fakeAgent('s1')
  const { env, daemon } = setup({ agents: [agent] })
  await flush()
  daemon.waits[0].resolve({ reason: 'delivered', text: '用户答复：先做 A' })
  await flush()
  assert.equal(agent.calls.followup.length, 1)
  assert.equal(agent.calls.followup[0].content[0].text, '用户答复：先做 A')
  assert.equal(agent.calls.steer.length, 0)

  agent.status = 'running'
  await env.emit('agent/status', { agent, status: 'idle' })
  await flush()
  daemon.waits.at(-1).resolve({ reason: 'delivered', text: '用户留言' })
  await flush()
  assert.equal(agent.calls.steer.length, 1, '运行中走 steer')
  assert.equal(agent.calls.inject.length, 0, '不用 inject 送达')
  env.dispose()
})

test('deliverTo 按状态选 steer / followup', () => {
  const running = fakeAgent('a', { status: 'running' })
  assert.equal(deliverTo(running, kitestringMessage('x')), 'steer')
  const idle = fakeAgent('b')
  assert.equal(deliverTo(idle, kitestringMessage('x')), 'followup')
})

// ---- 压缩 ----------------------------------------------------------------

test('一轮内压缩：报 PreCompact / PostCompact，身份提示词立即 inject', async () => {
  const agent = fakeAgent('s1', { status: 'running' })
  const { env, daemon } = setup({
    agents: [agent],
    daemonOpts: { hookContext: (b) => (b.hook_event_name === 'PostCompact' ? '你是本项目的主调度' : '') },
  })
  await flush()
  await env.emit('session/event', agent.session, { type: 'compaction/start', data: { turn: 1 } })
  await env.emit('session/event', agent.session, { type: 'compaction/end', data: { turn: 1 } })
  await flush()
  assert.deepEqual(names(daemon), ['SessionStart', 'PreCompact', 'PostCompact'])
  assert.equal(agent.calls.inject.length, 1)
  assert.equal(agent.calls.inject[0].content[0].text, '你是本项目的主调度')
  env.dispose()
})

test('空闲时压缩：身份提示词留到下一轮第一步；压缩失败不补', async () => {
  const agent = fakeAgent('s1')
  const { env, daemon } = setup({
    agents: [agent],
    daemonOpts: { hookContext: (b) => (b.hook_event_name === 'PostCompact' ? '身份' : '') },
  })
  await flush()
  await env.emit('session/event', agent.session, { type: 'compaction/end', data: { turn: null, error: 'boom' } })
  await flush()
  assert.ok(!names(daemon).includes('PostCompact'))
  await env.emit('session/event', agent.session, { type: 'compaction/end', data: { turn: null } })
  await flush()
  assert.equal(agent.calls.inject.length, 0)
  const [decision] = await env.emit('agent/pre-step', { agent, turn: 2, step: 1, messages: [] }, async () => ({ kind: 'enter', messages: [] }))
  assert.equal(decision.messages.length, 1)
  assert.equal(decision.messages[0].content[0].text, '身份')
  env.dispose()
})

// ---- 子代理 --------------------------------------------------------------

test('子代理：开始 / 结束、工具事件、用量都记在根会话名下并带 agent_id；子代理的轮末不报 Stop', async () => {
  const root = fakeAgent('root', { status: 'running' })
  const { env, daemon } = setup({ agents: [root] })
  await flush()
  const child = fakeAgent('child', { status: 'running', origin: 'subagent', parentSession: 'root' })
  env.add(child)
  await env.emit('agent/created', { agent: child, source: 'startup' })
  await env.emit('session/event', child.session, { type: 'tool/call', data: { name: 'bash', callId: 'c1', turn: 1, arguments: '{"command":"ls"}' } })
  await env.emit('session/event', child.session, { type: 'tool/result', data: { turn: 1, message: { toolCallId: 'c1' } } })
  await env.emit('session/event', child.session, { type: 'assistant/message', data: { usage: { inputTokens: 10, outputTokens: 5 } } })
  await env.emit('session/event', child.session, { type: 'turn/end', data: { turn: 1 } })
  await env.emit('agent/disposed', { agent: child })
  await flush()
  const sub = daemon.hooks.filter((h) => h.agent_id === 'child')
  assert.deepEqual(sub.map((h) => h.hook_event_name), ['SubagentStart', 'PreToolUse', 'PostToolUse', 'SubagentStop'])
  assert.ok(sub.every((h) => h.session_id === 'root'))
  assert.equal(sub[1].tool_name, 'Bash')
  assert.deepEqual(sub[1].tool_input, { command: 'ls' })
  assert.ok(!names(daemon).includes('Stop'), '子代理的轮末不算根会话的 Stop')
  assert.deepEqual(daemon.usages.map((u) => [u.sessionKey, u.agentId, u.input, u.output]), [['dsh:root', 'child', 10, 5]])
  assert.ok(!names(daemon).includes('SessionEnd'), '子代理结束不结束根会话')
  env.dispose()
})

test('shell 变量：主线程与子代理都给根会话键，子代理另给自己的会话 ID', async () => {
  const root = fakeAgent('root')
  const child = fakeAgent('child', { origin: 'subagent', parentSession: 'root' })
  const { env } = setup({ agents: [root, child] })
  await flush()
  const c = env.shellContributor
  assert.ok(c.variables[ENV_ROOT_SESSION] && c.variables[ENV_AGENT])
  assert.deepEqual(c.resolve({ agent: root }), { [ENV_ROOT_SESSION]: 'dsh:root' })
  assert.deepEqual(c.resolve({ agent: child }), { [ENV_ROOT_SESSION]: 'dsh:root', [ENV_AGENT]: 'child' })
  assert.deepEqual(c.resolve({}), {})
  env.dispose()
  assert.equal(env.shellContributor, null, '插件卸载时撤下')
})

test('工具调用带根会话键；子代理里调用另带 agent', async () => {
  const root = fakeAgent('root')
  const child = fakeAgent('child', { origin: 'subagent', parentSession: 'root' })
  const mcp = fakeMcpFactory()
  const { env } = setup({ agents: [root, child], mcp })
  await flush()
  const tool = env.registered[0]
  await tool.execute({}, { agent: child })
  assert.deepEqual([mcp.made[0].lastCall.meta.sessionKey, mcp.made[0].lastCall.meta.agentId], ['dsh:root', 'child'])
  await tool.execute({}, { agent: root })
  assert.deepEqual([mcp.made[0].lastCall.meta.sessionKey, mcp.made[0].lastCall.meta.agentId], ['dsh:root', ''])
  env.dispose()
})

// ---- 其它 ----------------------------------------------------------------

test('一个 Host 多个会话：每个会话各自上报同一个 Host 实例、各挂一个等待', async () => {
  const a = fakeAgent('a')
  const b = fakeAgent('b')
  const { env, daemon } = setup({ agents: [a, b] })
  await flush()
  const starts = daemon.hooks.filter((h) => h.hook_event_name === 'SessionStart')
  assert.deepEqual(starts.map((h) => [h.session_id, h.instance]), [['a', HOST], ['b', HOST]])
  assert.deepEqual(daemon.waits.map((w) => w.args.sessionKey), ['dsh:a', 'dsh:b'])
  env.dispose()
})

test('客户端形态以 profile 上下文为准，不看 Host 进程里不存在的 DSH_PROFILE', () => {
  assert.equal(clientKindOf({ get: () => ({ name: 'desktop' }) }, {}, {}), 'desktop')
  assert.equal(clientKindOf({ get: () => ({ name: 'web' }) }, {}, {}), 'web')
  assert.equal(clientKindOf({ get: () => undefined }, {}, { electron: '30' }), 'desktop')
  assert.equal(clientKindOf({}, {}, {}), 'web')
})
