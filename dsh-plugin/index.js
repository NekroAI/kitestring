// 纸鸢 Kitestring 的 DeepSeek Harness Host 插件。
//
// 它做三件事：
//   1. 把 DSH 的会话事件翻译成纸鸢的 hook 事件（SessionStart / UserPromptSubmit / Stop / Tool…）；
//   2. 把 `ks mcp` 的工具原样注册给模型，每次调用用 MCP `_meta` 带上准确会话身份；
//   3. 会话空闲时在 daemon 上挂 /wait，收到纸鸢消息就唤醒同一个会话（空闲 followup，运行中 steer）。
//
// 约束：插件不读任何模型凭证，不另建推理通道；推理始终由 DSH 自己的 Host 完成。
import { randomUUID } from 'node:crypto'
import { execFileSync } from 'node:child_process'
import { appendFileSync, existsSync, mkdirSync } from 'node:fs'
import os from 'node:os'
import path from 'node:path'

import { DaemonClient, defaultSocketPath, ksHome } from './lib/daemon.js'
import { McpClient } from './lib/mcp.js'
import { normalizeToolSchema } from './lib/schema.js'

export const name = 'kitestring-dsh'
// systemPrompt 用来放 ks mcp 的说明；sessions 用来解析子会话的父会话。shellEnv 是可选的，用 ctx.inject 延后拿。
export const inject = ['tools', 'systemPrompt', 'agents', 'sessions', 'llm']

// 给模型 shell 注入的变量（R-251）：ks 在 DSH 里据此认出根会话，子代理里也一样。
// DSH 自带的 DSH_SESSION_ID 在子代理里是子会话；从 Claude Code 终端启动的 DSH 还会继承 CLAUDE_CODE_SESSION_ID。
export const ENV_ROOT_SESSION = 'DSH_KITESTRING_SESSION'
export const ENV_AGENT = 'DSH_KITESTRING_AGENT'

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

const WAIT_TIMEOUT_MS = 12 * 60 * 60 * 1000
const RETRY_MS = 15_000
const RECONCILE_MS = 60_000

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

export function kitestringMessage(text) {
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

function parseArgs(raw) {
  if (typeof raw === 'string' && raw.trim() !== '') {
    try {
      return JSON.parse(raw)
    } catch {
      return {}
    }
  }
  return raw && typeof raw === 'object' ? raw : {}
}

// deliverTo 把一条纸鸢消息交给 agent（R-251）：运行中用 steer（在最近的步骤边界被消费，
// 回合收尾时有新 steer 会再跑一步，不会滞留到下一次唤醒）；空闲用 followup 单独开一轮。
// inject 只是“下一步的上下文”，可能错过已经认领的批次，又不唤醒，所以不用于送达。
export function deliverTo(agent, message) {
  if (agent.status === 'running' && typeof agent.steer === 'function') {
    agent.steer(message)
    return 'steer'
  }
  agent.followup(message)
  return 'followup'
}

// clientKindOf 判断 Host 是桌面版还是 web（R-251）：DSH_PROFILE 只注入模型 shell，Host 进程里没有；
// 以 profile 上下文为准，桌面版的 profile 名固定是 desktop，Electron 里也认。
export function clientKindOf(ctx, env = process.env, versions = process.versions) {
  let profile = ''
  try {
    profile = String(ctx?.get?.('profileContext')?.name ?? '')
  } catch {
    profile = ''
  }
  if (profile.toLowerCase() === 'desktop' || versions?.electron || env.DSH_PROFILE === 'desktop') return 'desktop'
  return 'web'
}

export function apply(ctx, config = {}) {
  createPlugin(ctx, config)
}

// createPlugin 是插件本体；deps 只给测试用（假 daemon、假 MCP、固定的 Host 实例、调快的定时器）。
export function createPlugin(ctx, config = {}, deps = {}) {
  const logger = ctx.logger
  const opts = {
    socketPath: config.socketPath || defaultSocketPath(),
    ksBin: pickKsBin(config.ksBin),
    standby: config.standby !== false,
    events: config.events !== false,
    usage: config.usage !== false,
    attach: config.attach !== 'none',
    debug: config.debug === true,
    logFile: config.logFile || path.join(ksHome(), 'logs', 'dsh-plugin.log'),
  }
  const timing = { retryMs: RETRY_MS, reconcileMs: RECONCILE_MS, waitTimeoutMs: WAIT_TIMEOUT_MS, ...(deps.timing ?? {}) }
  // Host 进程身份：崩溃后 daemon 能据此补发 SessionEnd，两个 Host 开同一会话时也靠它分先后。
  const instance = deps.instance !== undefined ? deps.instance : hostInstance()
  const daemon = deps.daemon ?? new DaemonClient({ socketPath: opts.socketPath, logger })
  const makeMcp = deps.makeMcp ?? ((options) => new McpClient(options))
  const sessions = new Map()
  let mcp = null
  let toolDisposers = []
  let instructionsDisposer = null
  let envDisposer = null
  let reconnectTimer = null
  let connectAttempts = 0
  let reconcileTimer = null
  let stopped = false
  let logDirReady = false

  const log = (level, message) => {
    // 调试时同时落盘到纸鸢的数据目录：Host 的日志在启动终端里，插件出问题时不一定看得到。
    if (opts.debug) {
      try {
        if (!logDirReady) {
          mkdirSync(path.dirname(opts.logFile), { recursive: true })
          logDirReady = true
        }
        appendFileSync(opts.logFile, `${new Date().toISOString()} ${level} ${message}\n`)
      } catch {
        // 落盘失败不影响主流程
      }
    }
    const fn = typeof logger?.[level] === 'function' ? logger[level].bind(logger) : null
    if (fn) fn(`[kitestring] ${message}`)
    else if (level !== 'debug' && !deps.quiet) console.log(`[kitestring] ${message}`)
  }

  log('info', `apply socket=${opts.socketPath} ks=${opts.ksBin} standby=${opts.standby} events=${opts.events}`)

  const clientKind = clientKindOf(ctx)

  function sessionInfo(agent) {
    const header = agent.session?.header ?? {}
    return {
      cwd: typeof header.cwd === 'string' ? header.cwd : '',
      model: String(agent.options?.model ?? ''),
      client: clientKind,
      ...(instance ? { instance } : {}),
    }
  }

  function isSubSession(session) {
    const header = session?.header
    return header?.origin === 'subagent' || (header?.delegationDepth ?? 0) > 0
  }

  function isSubagent(agent) {
    return isSubSession(agent?.session)
  }

  // rootOf 把子代理的会话归到根会话：纸鸢只认识根会话，子代理的工具调用、用量记在它名下。
  function rootOf(session) {
    for (let depth = 0; session && depth < 8; depth++) {
      const header = session.header
      if (!header || !isSubSession(session) || !header.parentSession) break
      const parent = ctx.sessions?.get?.(header.parentSession) ?? ctx.agents?.get?.(header.parentSession)?.session
      if (!parent) break
      session = parent
    }
    return session
  }

  // stateOf 找到事件所属的根会话状态；子代理返回根会话的状态和子会话 ID。
  function stateOf(session) {
    if (!session) return { st: null, agentId: '' }
    const id = String(session.id)
    const own = sessions.get(id)
    if (own) return { st: own, agentId: '' }
    if (!isSubSession(session)) return { st: null, agentId: '' }
    const root = rootOf(session)
    if (!root || String(root.id) === id) return { st: null, agentId: '' }
    return { st: sessions.get(String(root.id)) ?? null, agentId: id }
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
    if (isSubagent(agent)) {
      onSubagentCreated(agent)
      return null
    }
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
      subagents: new Set(),
      waitAbort: null,
      waitGen: 0,
      waitStarting: false,
      retryTimer: null,
      // yielded：同一会话在别的 Host 里挂上了等待（replaced）或继续了（stale），这里不再主动挂等待（R-251）。
      // 值是让出的原因；空串表示没让。
      yielded: '',
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
    log('debug', `接入会话 ${st.key}（${source}）`)
    void resolveContextWindow(st)
    if (opts.standby && agent.status === 'idle') void startWait(st)
    return st
  }

  // 子代理开始 / 结束：在根会话名下记成后台子任务，和 Claude Code 的 SubagentStart / SubagentStop 一样（R-251）。
  function onSubagentCreated(agent) {
    const { st, agentId } = stateOf(agent.session)
    if (!st || st.disposed || !agentId || st.subagents.has(agentId)) return
    st.subagents.add(agentId)
    void sendHook(st, {
      hook_event_name: 'SubagentStart',
      agent_id: agentId,
      agent_type: String(agent.session?.header?.agentPreset ?? ''),
    })
  }

  function onSubagentDisposed(agent) {
    const { st, agentId } = stateOf(agent.session)
    if (!st || !agentId || !st.subagents.delete(agentId)) return
    void sendHook(st, {
      hook_event_name: 'SubagentStop',
      agent_id: agentId,
      agent_type: String(agent.session?.header?.agentPreset ?? ''),
    })
  }

  // resolveContextWindow 让插件加载前就存在的会话也能拿到窗口大小（request/context 事件已经过去了）。
  async function resolveContextWindow(st) {
    const provider = String(st.agent.options?.provider ?? '')
    const model = String(st.agent.options?.model ?? '')
    if (!provider || !model || typeof ctx.llm?.resolveModelInfo !== 'function') return
    try {
      const info = await ctx.llm.resolveModelInfo(provider, model)
      const window = Number(info?.context?.contextWindow)
      if (window > 0) st.contextWindow = window
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
    // 只在还没有等待、也没被别的 Host 接走时补挂；一轮结束时 onTurnEnd 会主动挂。
    if (status === 'idle' && !st.waitAbort && !st.yielded) void startWait(st)
  })

  ctx.on('agent/disposed', ({ agent }) => {
    if (isSubagent(agent)) {
      onSubagentDisposed(agent)
      return
    }
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
    const { st, agentId } = stateOf(session)
    if (!st || st.disposed) return
    try {
      const data = event?.data ?? {}
      if (agentId) {
        onSubagentEvent(st, agentId, event, data)
        return
      }
      if (event.type === 'assistant/message') {
        st.lastReply = textOfBlocks(data.message?.content)
        const usage = data.usage
        if (usage && typeof usage === 'object') {
          // DSH 的 inputTokens 不含缓存命中/写入：上下文要三项相加。
          const context = (Number(usage.inputTokens) || 0) + (Number(usage.cacheReadTokens) || 0) + (Number(usage.cacheWriteTokens) || 0)
          if (context > 0) st.contextTokens = context
          reportUsage(st, usage, event.time, '')
        }
      } else if (event.type === 'request/context') {
        if (Number(data.contextWindow) > 0) st.contextWindow = Number(data.contextWindow)
      } else if (event.type === 'compaction/start') {
        // 纸鸢按 PreCompact 计压缩次数（R-251）。
        void sendHook(st, { hook_event_name: 'PreCompact' })
      } else if (event.type === 'compaction/end') {
        if (!data.error) onCompacted(st)
      } else if (event.type === 'tool/call') {
        onToolCall(st, data, '', session.id)
      } else if (event.type === 'tool/result') {
        onToolResult(st, data, '', session.id)
      } else if (event.type === 'turn/end') {
        onTurnEnd(st, data)
      }
    } catch (error) {
      log('debug', `处理 ${event?.type} 出错：${error.message}`)
    }
  })

  // 子代理只上报工具事件和用量（带 agent_id），不动根会话的回合、上下文和待命（R-251）。
  function onSubagentEvent(st, agentId, event, data) {
    if (event.type === 'assistant/message') {
      if (data.usage && typeof data.usage === 'object') reportUsage(st, data.usage, event.time, agentId)
    } else if (event.type === 'tool/call') {
      onToolCall(st, data, agentId, agentId)
    } else if (event.type === 'tool/result') {
      onToolResult(st, data, agentId, agentId)
    }
  }

  // 压缩完成后补回身份提示词和纸鸢的关键状态（R-251）：一轮还在跑时直接 inject，
  // 剩下的步骤就带着身份；空闲时（手动压缩）留到下一轮的第一步。
  function onCompacted(st) {
    void sendHook(st, { hook_event_name: 'PostCompact', source: 'compact' }).then((context) => {
      if (!context || st.disposed) return
      if (st.agent.status === 'running' && typeof st.agent.inject === 'function') {
        try {
          st.agent.inject(kitestringMessage(context))
          log('debug', `压缩后补上下文 ${st.key}（本轮）`)
          return
        } catch (error) {
          log('debug', `压缩后注入失败，留到下一轮：${error.message}`)
        }
      }
      st.startContext = st.startContext ? `${st.startContext}\n${context}` : context
      log('debug', `压缩后补上下文 ${st.key}（下一轮）`)
    })
  }

  function onToolCall(st, data, agentId, scope) {
    const raw = typeof data.name === 'string' ? data.name : ''
    if (!raw) return
    const name = TOOL_ALIASES[raw] ?? raw
    const input = parseArgs(data.arguments)
    st.calls.set(`${scope}:${data.callId}`, { name, input })
    void sendHook(st, {
      hook_event_name: 'PreToolUse',
      tool_name: name,
      tool_input: input,
      tool_use_id: String(data.callId),
      turn_id: `${st.id}:${data.turn}`,
      ...(agentId ? { agent_id: agentId } : {}),
    })
  }

  function onToolResult(st, data, agentId, scope) {
    const callId = String(data.message?.toolCallId ?? '')
    const k = `${scope}:${callId}`
    const call = st.calls.get(k) ?? { name: 'tool', input: {} }
    st.calls.delete(k)
    void sendHook(st, {
      hook_event_name: 'PostToolUse',
      tool_name: call.name,
      tool_input: call.input,
      tool_use_id: callId,
      turn_id: `${st.id}:${data.turn}`,
      ...(agentId ? { agent_id: agentId } : {}),
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
      // 开着待命时告诉纸鸢：这一轮结束就是待命，不要先记“等你回复”（R-251）。
      ...(opts.standby ? { source: 'standby' } : {}),
    }
    st.lastReply = ''
    st.stopPromise = (async () => {
      await st.ready
      await sendHook(st, payload)
    })()
    // 这个 Host 里刚跑完一轮：要是之前让给过别的 Host，现在由 daemon 的 stale 判断决定谁领活。
    st.yielded = ''
    // 已有活着的等待就不重挂（它跨回合有效）；否则轮末挂上，不依赖真的出现 idle（目标模式可能一轮接一轮）。
    if (!st.waitAbort) void startWait(st)
  }

  // reportUsage 把一次模型回复的 token 用量交给 daemon；子代理的用量记在根会话名下、带子代理 ID。
  function reportUsage(st, usage, at, agentId) {
    if (!opts.usage) return
    const cached = Number(usage.cacheReadTokens) || 0
    const cacheWrite = Number(usage.cacheWriteTokens) || 0
    // DSH 的 inputTokens 就是未命中缓存的输入，不需要再减缓存。
    const input = Math.max(Number(usage.inputTokens) || 0, 0)
    const output = Number(usage.outputTokens) || 0
    if (input + cached + cacheWrite + output === 0) return
    const model = String(st.agent.options?.model ?? '') || 'dsh'
    void daemon
      .usage({
        sessionKey: st.key,
        agentId,
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

  function scheduleWaitRetry(st, delayMs = timing.retryMs) {
    if (stopped || st.disposed || st.retryTimer) return
    st.retryTimer = setTimeout(() => {
      st.retryTimer = null
      void startWait(st)
    }, delayMs)
    st.retryTimer.unref?.()
  }

  // reconcileWaits 是兜底：重载、daemon 重启之后，空闲却没挂等待的会话一分钟内补上。
  // 一轮正在跑时不补（R-251）：轮末会挂；这时补挂会让界面误显待命。
  // 被别的 Host 接管（replaced）的会话不再试，免得两边轮流抢；只是“较早的 Host”（stale）的，
  // 能报出实例时才试——对方还在就照旧是 stale，对方 Host 没了 daemon 才会放行。
  function reconcileWaits() {
    if (stopped || !opts.standby) return
    for (const st of sessions.values()) {
      if (st.disposed || st.waitAbort || st.waitStarting || st.retryTimer) continue
      if (st.agent.status !== 'idle') continue
      if (st.yielded === 'replaced' || (st.yielded === 'stale' && !instance)) continue
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
      // 先等 SessionStart / Stop 上报完：Hub 先记“空闲 / 待命”，再收到挂等待；
      // 否则刚接入的空闲会话会被稍后到的 SessionStart 盖回“空闲”。
      await st.ready
      await st.stopPromise
      if (stopped || st.disposed) return
      gen = ++st.waitGen
      ac = new AbortController()
      st.waitAbort = ac
      if (st.retryTimer) {
        clearTimeout(st.retryTimer)
        st.retryTimer = null
      }
      log('debug', `待命 ${st.key}`)
    } finally {
      st.waitStarting = false
    }
    // 不主动 abort 上一次等待：daemon 会让新等待“接管”旧的，旧请求以 replaced 正常返回。
    let result
    try {
      result = await daemon.wait({ sessionKey: st.key, instance, timeoutMs: timing.waitTimeoutMs, signal: ac.signal })
    } catch (error) {
      if (stopped || st.disposed) return
      if (gen !== st.waitGen) return
      st.waitAbort = null
      log('debug', `待命连接中断：${error.message}`)
      scheduleWaitRetry(st)
      return
    }
    if (stopped || st.disposed) return
    const reason = result?.reason ?? ''
    // 先认 delivered，再看世代：即使这条等待已经被新的一轮接管，daemon 也已经把它标记成送达了；
    // 丢掉文字就等于把用户的消息永久吞掉。
    if (reason === 'delivered' && typeof result?.text === 'string' && result.text.trim() !== '') {
      if (gen === st.waitGen) st.waitAbort = null
      deliver(st, result.text)
      // 运行中 steer 的消息在这一轮里消费，轮末 onTurnEnd 会重挂；空闲 followup 开的新一轮同样。
      return
    }
    if (gen !== st.waitGen) return
    st.waitAbort = null
    switch (reason) {
      case 'timeout':
        void startWait(st)
        return
      case 'replaced':
      case 'stale':
        // 同一会话在更新的 Host 里继续了：让给它，不定时抢回（R-251）。
        // 这个 Host 里再跑完一轮、或兜底检查时 daemon 判定它又是最新的，才会重新挂上。
        st.yielded = reason
        log('debug', `待命让给别的 Host ${st.key}（${reason}）`)
        return
      case 'disabled':
        log('warn', `daemon 拒绝了插件待命：${result?.text ?? ''}`)
        return
      case 'canceled':
        return
      default:
        if (result?.error) log('warn', `待命失败：${result.error}`)
        scheduleWaitRetry(st)
    }
  }

  function deliver(st, text) {
    if (st.disposed) return
    try {
      const how = deliverTo(st.agent, kitestringMessage(text))
      log('debug', `${how === 'steer' ? '送达（运行中）' : '唤醒'} ${st.key}`)
    } catch (error) {
      log('warn', `唤醒 ${st.key} 失败：${error.message}`)
    }
  }

  // ---- shell 环境 -------------------------------------------------------

  // 给模型 shell 注入根会话键（R-251）：ks role / ks wait / ks team tell 在 DSH 里认对会话，子代理里也一样。
  function registerShellEnv(shellEnv) {
    return shellEnv.register({
      name: 'kitestring',
      variables: {
        [ENV_ROOT_SESSION]: { description: '纸鸢（Kitestring）里这个会话的键（子代理里是根会话），ks 命令据此认出会话' },
        [ENV_AGENT]: { description: '子代理的会话 ID（主线程没有），ks team tell 据此标出是哪个子任务发的' },
      },
      resolve(execution) {
        const agent = execution?.agent
        if (!agent?.session) return {}
        const root = rootOf(agent.session)
        if (!root) return {}
        const out = { [ENV_ROOT_SESSION]: `dsh:${root.id}` }
        if (String(root.id) !== String(agent.session.id)) out[ENV_AGENT] = String(agent.session.id)
        return out
      },
    })
  }

  if (typeof ctx.inject === 'function') {
    try {
      ctx.inject(['shellEnv'], (sub) => {
        try {
          envDisposer = registerShellEnv(sub.shellEnv)
        } catch (error) {
          log('warn', `注册 shell 变量失败：${error.message}`)
        }
      })
    } catch (error) {
      log('debug', `等待 shellEnv 失败：${error.message}`)
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
        const root = rootOf(agent.session)
        if (!root) throw new Error('kitestring 工具无法确定所属会话')
        const key = `dsh:${root.id}`
        const agentId = String(root.id) !== String(agent.session.id) ? String(agent.session.id) : ''
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

  function disposeTools() {
    for (const dispose of toolDisposers) {
      try {
        dispose()
      } catch {
        // 旧注册可能已经随插件重载释放
      }
    }
    toolDisposers = []
  }

  async function syncTools(client) {
    const tools = await client.listTools()
    disposeTools()
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
    const delay = Math.min(30_000, (timing.reconnectBaseMs ?? 1000) * 2 ** Math.min(connectAttempts, 5))
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
      client = makeMcp({
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
        disposeTools()
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
      if (mcp === client) {
        mcp = null
        disposeTools()
      }
      client?.close()
      scheduleReconnect()
    }
  }

  // ---- 生命周期 ---------------------------------------------------------

  if (opts.events || opts.standby) {
    const live = ctx.agents.list()
    log('info', `插件加载时已有 ${live.length} 个在线 agent`)
    // 先接根会话，再接子代理（子代理要找得到根会话的状态）。
    for (const agent of live) if (!isSubagent(agent)) attach(agent, 'startup')
    for (const agent of live) if (isSubagent(agent)) attach(agent, 'startup')
  }
  void connectTools()
  reconcileTimer = setInterval(reconcileWaits, timing.reconcileMs)
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
    disposeTools()
    for (const dispose of [instructionsDisposer, envDisposer]) {
      try {
        dispose?.()
      } catch {
        // ignore
      }
    }
    instructionsDisposer = null
    envDisposer = null
    mcp?.close()
    mcp = null
  })

  // 测试用的观察口：不影响插件行为。
  return { sessions, reconcileWaits, get mcp() { return mcp } }
}
