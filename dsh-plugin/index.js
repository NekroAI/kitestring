// 纸鸢 Kitestring 的 DeepSeek Harness Host 插件。
//
// 它做三件事：
//   1. 把 DSH 的会话事件翻译成纸鸢的 hook 事件（SessionStart / UserPromptSubmit / Stop / Tool…）；
//   2. 把 `ks mcp` 的 28 个工具原样注册给模型，每次调用用 MCP `_meta` 带上准确会话身份；
//   3. 会话空闲时在 daemon 上挂 /wait，收到纸鸢消息就用 agent.followup() 唤醒同一个会话。
//
// 约束：插件不读任何模型凭证，不另建推理通道；推理始终由 DSH 自己的 Host 完成。
import { randomUUID } from 'node:crypto'
import { execFileSync } from 'node:child_process'
import { appendFileSync, existsSync } from 'node:fs'
import os from 'node:os'
import path from 'node:path'

import { DaemonClient, defaultSocketPath } from './lib/daemon.js'
import { McpClient } from './lib/mcp.js'
import { normalizeToolSchema } from './lib/schema.js'

export const name = 'kitestring-dsh'
// systemPrompt 用来放 ks mcp 的说明；sessions 用来解析子会话的父会话。
export const inject = ['tools', 'systemPrompt', 'agents', 'sessions', 'llm']

// 工具返回值对模型的投影：只保留文本，结构化值原样留给程序化调用方。
// 形状与 DSH 自带 MCP 桥一致，这样 output schema 能通过注册表的子集校验。
const OUTPUT_SCHEMA = {
  type: 'object',
  properties: {
    content: { type: 'array', items: {} },
    structuredContent: {},
  },
  required: ['content'],
  additionalProperties: false,
}

// DSH 工具名 → 纸鸢已有 hook 习惯里的工具名（影响动作简述、shell 跟踪、是否在向用户提问）。
const TOOL_ALIASES = {
  bash: 'Bash',
  pwsh: 'shell',
  read: 'Read',
  write: 'Write',
  edit: 'Edit',
  glob: 'Glob',
  grep: 'Grep',
  web_search: 'WebSearch',
  web_fetch: 'WebFetch',
  todo_write: 'TodoWrite',
  ask_user_question: 'AskUserQuestion',
  subagent: 'Task',
  workflow: 'Task',
}

// 本地 ks 通常装在 ~/.local/bin；Host 从桌面启动时 PATH 里不一定有它。
function pickKsBin(configured) {
  if (configured) return configured
  const local = path.join(os.homedir(), '.local', 'bin', 'ks')
  if (existsSync(local)) return local
  return 'ks'
}

// hostInstance 返回 "<pid>@<启动时间>"，格式和 daemon 的 instanceAlive 一致。
// 这样 Host 崩掉时 daemon 能在一个心跳周期内补发 SessionEnd，而不是让会话一直挂着“待命”。
// 取不到启动时间就不报：宁可退化成旧行为，也不要被误判成“进程已消失”。
function hostInstance() {
  try {
    const out = execFileSync('ps', ['-o', 'lstart=', '-p', String(process.pid)], { encoding: 'utf8', env: { ...process.env, LC_ALL: 'C' } }).trim()
    if (out) return `${process.pid}@${out.replace(/\s+/g, ' ')}`
  } catch {
    // ps 不可用时忽略
  }
  return ''
}

function kitestringMessage(text) {
  return {
    id: randomUUID(),
    role: 'user',
    content: [{ type: 'text', text }],
    source: { kind: 'kitestring', form: 'notice', summary: '纸鸢消息' },
  }
}

function textOfBlocks(blocks) {
  if (!Array.isArray(blocks)) return ''
  return blocks
    .filter((b) => b && b.type === 'text' && typeof b.text === 'string')
    .map((b) => b.text)
    .join('\n')
    .trim()
}

function userPromptText(messages) {
  if (!Array.isArray(messages)) return ''
  const parts = []
  for (const message of messages) {
    if (!message || message.source?.kind !== 'user') continue
    const text = textOfBlocks(message.content)
    if (text) parts.push(text)
  }
  return parts.join('\n').trim()
}

function renderToolContent(value) {
  const parts = []
  if (value && Array.isArray(value.content)) {
    for (const block of value.content) {
      if (block?.type === 'text' && typeof block.text === 'string') parts.push(block.text)
      else if (block?.type === 'image') parts.push(`[图片 ${block.mimeType ?? ''}]`)
      else if (block?.type === 'resource') parts.push(block.resource?.text ?? block.resource?.uri ?? '[资源]')
    }
  }
  if (parts.length === 0 && value?.structuredContent !== undefined) parts.push(JSON.stringify(value.structuredContent))
  return [{ type: 'text', text: parts.join('\n') || '（无返回内容）' }]
}

export function apply(ctx, config = {}) {
  const logger = ctx.logger
  const opts = {
    socketPath: config.socketPath || defaultSocketPath(),
    ksBin: pickKsBin(config.ksBin),
    standby: config.standby !== false,
    events: config.events !== false,
    usage: config.usage !== false,
    attach: config.attach !== 'none',
    debug: config.debug === true,
  }
  // Host 进程身份：崩溃后 daemon 能据此补发 SessionEnd。
  const instance = hostInstance()
  const daemon = new DaemonClient({ socketPath: opts.socketPath, logger })
  const sessions = new Map()
  let mcp = null
  let toolDisposers = []
  let instructionsDisposer = null
  let reconnectTimer = null
  let connectAttempts = 0
  let reconcileTimer = null
  let stopped = false

  const log = (level, message) => {
    // 调试时同时落盘：Host 的日志在启动终端里，插件出问题时不一定看得到。
    if (opts.debug) {
      try {
        appendFileSync('/tmp/kitestring-dsh.log', `${new Date().toISOString()} ${level} ${message}\n`)
      } catch {
        // 落盘失败不影响主流程
      }
    }
    const fn = typeof logger?.[level] === 'function' ? logger[level].bind(logger) : null
    if (fn) fn(`[kitestring] ${message}`)
    else if (level !== 'debug') console.log(`[kitestring] ${message}`)
  }

  log('info', `apply socket=${opts.socketPath} ks=${opts.ksBin} standby=${opts.standby} events=${opts.events}`)

  const clientKind = () => (process.env.DSH_PROFILE === 'desktop' ? 'desktop' : 'web')

  function sessionInfo(agent) {
    const header = agent.session?.header ?? {}
    return {
      cwd: typeof header.cwd === 'string' ? header.cwd : '',
      model: String(agent.options?.model ?? ''),
      client: clientKind(),
      ...(instance ? { instance } : {}),
    }
  }

  function isSubagent(agent) {
    const header = agent.session?.header
    return header?.origin === 'subagent' || (header?.delegationDepth ?? 0) > 0
  }

  // rootSession 把子代理的会话归到根会话：纸鸢只认识根会话，子代理的工具调用记在它名下。
  function rootSession(agent) {
    let session = agent?.session
    for (let depth = 0; session && depth < 8; depth++) {
      const header = session.header
      if (!header || header.origin !== 'subagent' || !header.parentSession) break
      const parent = ctx.sessions?.get?.(header.parentSession) ?? ctx.agents?.get?.(header.parentSession)?.session
      if (!parent) break
      session = parent
    }
    return session
  }

  // sendHook 是所有事件上报的唯一入口；任何失败都只记日志，不影响 DSH 本身。
  async function sendHook(st, payload, { quiet = true } = {}) {
    if (!opts.events) return ''
    try {
      const body = { session_id: st.id, ...sessionInfo(st.agent), ...payload }
      const context = await daemon.hook(body)
      log('debug', `已上报 ${payload.hook_event_name} ${st.key}${context ? '（有注入）' : ''}`)
      return context
    } catch (error) {
      if (!quiet) log('warn', `上报 ${payload.hook_event_name} 失败：${error.message}`)
      else log('debug', `上报 ${payload.hook_event_name} 失败：${error.message}`)
      return ''
    }
  }

  function attach(agent, source) {
    if (!opts.attach || !agent?.session) return null
    if (isSubagent(agent)) return null
    const id = String(agent.session.id)
    const existing = sessions.get(id)
    if (existing) return existing
    const st = {
      agent,
      id,
      key: `dsh:${id}`,
      source,
      startContext: '',
      lastReply: '',
      contextTokens: 0,
      contextWindow: 0,
      injectedTurn: null,
      calls: new Map(),
      waitAbort: null,
      waitGen: 0,
      waitStarting: false,
      retryTimer: null,
      stopPromise: Promise.resolve(),
      ready: Promise.resolve(),
      disposed: false,
    }
    sessions.set(id, st)
    st.ready = (async () => {
      const context = await sendHook(st, { hook_event_name: 'SessionStart', source: normalizeSource(source) })
      if (context) st.startContext = context
      log('debug', `会话就绪 ${st.key}（${source}）`)
    })()
    log('debug', `接入会话 ${st.key}（${source}）inbox turn=${agent.inbox?.nextTurn?.length ?? '?'} step=${agent.inbox?.nextStep?.length ?? '?'}`)
    void resolveContextWindow(st)
    if (opts.standby && agent.status === 'idle') void startWait(st)
    return st
  }

  // resolveContextWindow 让插件加载前就存在的会话也能拿到窗口大小（request/context 事件已经过去了）。
  async function resolveContextWindow(st) {
    const provider = String(st.agent.options?.provider ?? '')
    const model = String(st.agent.options?.model ?? '')
    if (!provider || !model) return
    try {
      const info = await ctx.llm.resolveModelInfo(provider, model)
      const window = Number(info?.context?.contextWindow)
      if (window > 0) {
        st.contextWindow = window
        log('debug', `上下文窗口 ${st.key}: ${window}`)
      }
    } catch (error) {
      log('debug', `读取上下文窗口失败：${error.message}`)
    }
  }

  function normalizeSource(source) {
    // DSH 的 clear 在纸鸢里没有对应语义，按新开会话处理。
    return source === 'resume' || source === 'compact' ? source : 'startup'
  }

  // ---- 事件翻译 ---------------------------------------------------------

  ctx.on('agent/created', ({ agent, source }) => {
    attach(agent, source)
  })

  ctx.on('agent/status', ({ agent, status }) => {
    if (!opts.standby) return
    const st = sessions.get(String(agent.session?.id))
    if (!st || st.disposed) return
    log('debug', `状态 ${st.key} -> ${status} inbox turn=${agent.inbox?.nextTurn?.length ?? '?'} step=${agent.inbox?.nextStep?.length ?? '?'}`)
    // 只在还没有等待时补挂；一轮结束时 onTurnEnd 会主动重挂（目标模式的会话可能没有真正的 idle 间隙）。
    if (status === 'idle' && !st.waitAbort) void startWait(st)
  })

  ctx.on('agent/disposed', ({ agent }) => {
    const id = String(agent.session?.id)
    const st = sessions.get(id)
    if (!st) return
    st.disposed = true
    st.waitGen++
    st.waitAbort?.abort()
    if (st.retryTimer) clearTimeout(st.retryTimer)
    sessions.delete(id)
    void sendHook(st, { hook_event_name: 'SessionEnd', reason: 'exit' })
    log('debug', `会话结束 ${st.key}`)
  })

  ctx.on('session/event', (session, event) => {
    const st = sessions.get(String(session.id))
    if (!st || st.disposed) return
    try {
      const data = event?.data ?? {}
      if (event.type === 'assistant/message') {
        st.lastReply = textOfBlocks(data.message?.content)
        const usage = data.usage
        if (usage && typeof usage === 'object') {
          if (Number(usage.inputTokens) > 0) st.contextTokens = Number(usage.inputTokens)
          void reportUsage(st, usage, event.time)
        }
      } else if (event.type === 'request/context') {
        if (Number(data.contextWindow) > 0) st.contextWindow = Number(data.contextWindow)
      } else if (event.type === 'tool/call') {
        onToolCall(st, data)
      } else if (event.type === 'tool/result') {
        onToolResult(st, data)
      } else if (event.type === 'turn/end') {
        onTurnEnd(st, data)
      }
    } catch (error) {
      log('debug', `处理 ${event?.type} 出错：${error.message}`)
    }
  })

  function onToolCall(st, data) {
    const raw = typeof data.name === 'string' ? data.name : ''
    if (!raw) return
    const name = TOOL_ALIASES[raw] ?? raw
    let input = {}
    if (typeof data.arguments === 'string' && data.arguments.trim() !== '') {
      try {
        input = JSON.parse(data.arguments)
      } catch {
        input = {}
      }
    } else if (data.arguments && typeof data.arguments === 'object') {
      input = data.arguments
    }
    st.calls.set(String(data.callId), { name, input })
    void sendHook(st, {
      hook_event_name: 'PreToolUse',
      tool_name: name,
      tool_input: input,
      tool_use_id: String(data.callId),
      turn_id: `${st.id}:${data.turn}`,
    })
  }

  function onToolResult(st, data) {
    const callId = String(data.message?.toolCallId ?? '')
    const call = st.calls.get(callId) ?? { name: 'tool', input: {} }
    st.calls.delete(callId)
    void sendHook(st, {
      hook_event_name: 'PostToolUse',
      tool_name: call.name,
      tool_input: call.input,
      tool_use_id: callId,
      turn_id: `${st.id}:${data.turn}`,
    })
  }

  function onTurnEnd(st, data) {
    const aborted = data.reason?.kind === 'aborted'
    const payload = {
      hook_event_name: aborted ? 'Interrupt' : 'Stop',
      turn_id: `${st.id}:${data.turn}`,
      last_assistant_message: st.lastReply,
      context: st.contextTokens,
      context_max: st.contextWindow,
    }
    st.lastReply = ''
    st.stopPromise = (async () => {
      await st.ready
      await sendHook(st, payload)
    })()
    // 不依赖真的出现 idle：目标 / 后台任务这类会话可能一轮接一轮地跑。
    // 挂上等待不影响它们继续工作，纸鸢的消息会排成下一轮。
    void startWait(st)
  }

  // reportUsage 把一次模型回复的 token 用量交给 daemon；DSH 没有 transcript，不能等 usageScanner 去读文件。
  function reportUsage(st, usage, at) {
    if (!opts.usage) return
    const cached = Number(usage.cacheReadTokens) || 0
    const cacheWrite = Number(usage.cacheWriteTokens) || 0
    const totalInput = Number(usage.inputTokens) || 0
    const input = Math.max(totalInput - cached - cacheWrite, 0)
    const output = Number(usage.outputTokens) || 0
    if (input + cached + cacheWrite + output === 0) return
    const model = String(st.agent.options?.model ?? '') || 'dsh'
    void daemon
      .usage({
        sessionKey: st.key,
        model,
        at: typeof at === 'number' ? at : Date.now(),
        input,
        cacheRead: cached,
        cacheWrite,
        output,
        requests: 1,
      })
      .catch((error) => log('debug', `上报用量失败：${error.message}`))
  }

  // ---- 上下文注入 -------------------------------------------------------

  ctx.on('agent/pre-step', async (payload, next) => {
    const decision = await next()
    if (!decision || decision.kind !== 'enter') return decision
    const st = sessions.get(String(payload.agent?.session?.id))
    if (!st || st.disposed) return decision
    if (st.injectedTurn === payload.turn) return decision
    st.injectedTurn = payload.turn
    try {
      await st.ready
      const extra = []
      if (st.startContext) {
        extra.push(kitestringMessage(st.startContext))
        st.startContext = ''
      }
      const prompt = userPromptText(decision.messages)
      if (prompt) {
        const context = await sendHook(st, {
          hook_event_name: 'UserPromptSubmit',
          prompt,
          turn_id: `${st.id}:${payload.turn}`,
        })
        if (context) extra.push(kitestringMessage(context))
      }
      if (extra.length === 0) return decision
      return { ...decision, messages: [...decision.messages, ...extra] }
    } catch (error) {
      log('debug', `注入上下文失败：${error.message}`)
      return decision
    }
  })

  // ---- 待命与唤醒 -------------------------------------------------------

  function scheduleWaitRetry(st, delayMs = 15_000) {
    if (stopped || st.disposed || st.retryTimer) return
    st.retryTimer = setTimeout(() => {
      st.retryTimer = null
      void startWait(st)
    }, delayMs)
    st.retryTimer.unref?.()
  }

  function reconcileWaits() {
    if (stopped || !opts.standby) return
    for (const st of sessions.values()) {
      if (st.disposed || st.waitAbort || st.waitStarting) continue
      void startWait(st)
    }
  }

  async function startWait(st) {
    if (stopped || st.disposed || !opts.standby) return
    // 同一会话同一时刻只建立一次等待：轮末事件和 status=idle 可能同时到达，
    // 两次 /wait 会互相接管，谁拿到 delivered 都可能被另一条覆盖。
    if (st.waitStarting) return
    st.waitStarting = true
    let gen
    let ac
    try {
      await st.stopPromise
      if (stopped || st.disposed) return
      gen = ++st.waitGen
      ac = new AbortController()
      st.waitAbort = ac
      log('debug', `待命 ${st.key}`)
    } finally {
      st.waitStarting = false
    }
    // 不主动 abort 上一次等待：daemon 会让新等待“接管”旧的，旧请求以 replaced 正常返回，
    // 这样 Hub 不会收到一串 canceled 把状态打回“等你回复”。
    let result
    try {
      result = await daemon.wait({ sessionKey: st.key, timeoutMs: 12 * 60 * 60 * 1000, signal: ac.signal })
    } catch (error) {
      if (stopped || st.disposed) return
      if (gen === st.waitGen) st.waitAbort = null
      log('debug', `待命连接中断：${error.message}`)
      if (gen === st.waitGen) scheduleWaitRetry(st)
      return
    }
    if (stopped || st.disposed) return
    const reason = result?.reason ?? ''
    // 先认 delivered，再看世代：即使这条等待已经被新的一轮接管，daemon 也已经把它标记成送达了；
    // 丢掉文字就等于把用户的消息永久吞掉。
    if (reason === 'delivered' && typeof result?.text === 'string' && result.text.trim() !== '') {
      if (gen === st.waitGen) st.waitAbort = null
      deliver(st, result.text)
      return
    }
    if (gen !== st.waitGen) return
    st.waitAbort = null
    if (reason === 'timeout') {
      void startWait(st)
      return
    }
    if (reason === 'replaced' || reason === 'stale' || reason === 'disabled') {
      // 同一会话在别的 Host / 终端里也有等待：让给它，隔一会儿再确认自己是否空着。
      scheduleWaitRetry(st, 30_000)
      return
    }
    if (reason === 'canceled') return
    if (result?.error) log('warn', `待命失败：${result.error}`)
    scheduleWaitRetry(st)
  }

  function deliver(st, text) {
    if (st.disposed) return
    const message = kitestringMessage(text)
    try {
      // 忙的时候注入到最近的下一步（和官方 jobs 插件同一套语义）：目标 / 后台任务这类会话会一直跑，
      // 排成下一轮可能被目标轮次压住；inject 能让模型在下一步就看到纸鸢的消息。
      if (st.agent.status === 'running') {
        st.agent.inject(message)
        log('debug', `注入 ${st.key}（运行中）`)
      } else {
        st.agent.followup(message)
        log('debug', `唤醒 ${st.key}`)
      }
    } catch (error) {
      log('warn', `唤醒 ${st.key} 失败：${error.message}`)
    }
  }

  // ---- 工具桥 -----------------------------------------------------------

  function toolDefinition(tool, client) {
    return {
      name: tool.name,
      description: typeof tool.description === 'string' ? tool.description : '',
      parameters: normalizeToolSchema(tool.inputSchema),
      output: { schema: OUTPUT_SCHEMA, render: (_args, value) => renderToolContent(value) },
      async execute(args, exec) {
        const agent = exec?.agent
        if (!agent) throw new Error('kitestring 工具需要在 agent 会话里调用')
        const root = rootSession(agent)
        if (!root) throw new Error('kitestring 工具无法确定所属会话')
        const key = `dsh:${root.id}`
        const agentId = isSubagent(agent) ? String(agent.session.id) : ''
        const result = await client.callTool(tool.name, args ?? {}, {
          sessionKey: key,
          agentId,
          signal: exec.signal,
        })
        if (result?.isError === true) {
          const text = textOfBlocks(result.content) || 'Kitestring 工具调用失败'
          throw new Error(text)
        }
        const value = { content: Array.isArray(result?.content) ? result.content : [] }
        if (result?.structuredContent !== undefined) value.structuredContent = result.structuredContent
        return value
      },
    }
  }

  async function syncTools(client) {
    const tools = await client.listTools()
    for (const dispose of toolDisposers) {
      try {
        dispose()
      } catch {
        // 旧注册可能已经随插件重载释放
      }
    }
    toolDisposers = []
    for (const tool of tools) {
      if (!tool || typeof tool.name !== 'string' || tool.name === '') continue
      try {
        toolDisposers.push(ctx.tools.register(toolDefinition(tool, client)))
      } catch (error) {
        log('warn', `注册工具 ${tool.name} 失败：${error.message}`)
      }
    }
    if (!instructionsDisposer && typeof client.instructions === 'string' && client.instructions.trim() !== '') {
      const order = (() => {
        try {
          return ctx.systemPrompt.getSectionOrder('MCP_SERVERS')
        } catch {
          return 3100
        }
      })()
      instructionsDisposer = ctx.systemPrompt.section({ name: 'kitestring', order, text: client.instructions })
    }
    log('info', `已注册 ${toolDisposers.length} 个纸鸢工具`)
  }

  function scheduleReconnect() {
    if (stopped || reconnectTimer) return
    const delay = Math.min(30_000, 1000 * 2 ** Math.min(connectAttempts, 5))
    connectAttempts++
    reconnectTimer = setTimeout(() => {
      reconnectTimer = null
      void connectTools()
    }, delay)
    reconnectTimer.unref?.()
  }

  async function connectTools() {
    if (stopped) return
    let client = null
    try {
      client = new McpClient({
        command: opts.ksBin,
        args: ['mcp', '--client', 'dsh'],
        logger,
        clientVersion: '0.1.0',
      })
      client.onToolsChanged = () => {
        if (mcp === client) void syncTools(client).catch((error) => log('warn', `同步工具失败：${error.message}`))
      }
      client.onExit = () => {
        if (stopped || mcp !== client) return
        mcp = null
        for (const dispose of toolDisposers) {
          try {
            dispose()
          } catch {
            // ignore
          }
        }
        toolDisposers = []
        log('warn', 'ks mcp 已断开，准备重连')
        scheduleReconnect()
      }
      await client.start()
      if (stopped) {
        client.close()
        return
      }
      mcp = client
      connectAttempts = 0
      await syncTools(client)
      const st = await daemon.status().catch(() => null)
      if (!st) log('warn', `daemon 未响应（${opts.socketPath}），事件和待命会在它启动后自动恢复`)
    } catch (error) {
      log('warn', `连接 ks mcp 失败：${error.message}`)
      client?.close()
      scheduleReconnect()
    }
  }

  // ---- 生命周期 ---------------------------------------------------------

  if (opts.events || opts.standby) {
    const live = ctx.agents.list()
    log('info', `插件加载时已有 ${live.length} 个在线 agent`)
    for (const agent of live) {
      attach(agent, 'startup')
    }
  }
  void connectTools()
  // 兜底：重载、daemon 重启、被别的 Host 接管之后，任何会话只要没有活着的等待就在一分钟内补上。
  reconcileTimer = setInterval(reconcileWaits, 60_000)
  reconcileTimer.unref?.()

  ctx.effect(() => () => {
    stopped = true
    if (reconnectTimer) clearTimeout(reconnectTimer)
    if (reconcileTimer) clearInterval(reconcileTimer)
    connectAttempts = 0
    for (const st of sessions.values()) {
      st.disposed = true
      st.waitGen++
      st.waitAbort?.abort()
      if (st.retryTimer) clearTimeout(st.retryTimer)
    }
    sessions.clear()
    for (const dispose of toolDisposers) {
      try {
        dispose()
      } catch {
        // ignore
      }
    }
    toolDisposers = []
    try {
      instructionsDisposer?.()
    } catch {
      // ignore
    }
    instructionsDisposer = null
    mcp?.close()
    mcp = null
  })
}
