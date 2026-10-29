// 插件测试用的假 DSH 上下文、假 agent、假 daemon、假 ks mcp。只模拟插件实际用到的那部分接口。

export const flush = async (n = 6) => {
  for (let i = 0; i < n; i++) await new Promise((resolve) => setImmediate(resolve))
}

export function deferred() {
  let resolve
  let reject
  const promise = new Promise((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

export function fakeAgent(id, { status = 'idle', origin, parentSession, model = 'deepseek-v4' } = {}) {
  const calls = { steer: [], followup: [], inject: [] }
  const header = { id, cwd: '/tmp/proj', ...(origin ? { origin, delegationDepth: 1 } : {}), ...(parentSession ? { parentSession } : {}) }
  return {
    calls,
    status,
    options: { model },
    session: { id, header },
    steer(message) {
      calls.steer.push(message)
    },
    followup(message) {
      calls.followup.push(message)
    },
    inject(message) {
      calls.inject.push(message)
    },
  }
}

export function fakeCtx({ profile = 'web', agents = [] } = {}) {
  const handlers = new Map()
  const disposers = []
  const registered = []
  const live = new Map(agents.map((a) => [a.session.id, a]))
  let shellContributor = null
  const ctx = {
    logger: { debug() {}, info() {}, warn() {} },
    on(name, fn) {
      if (!handlers.has(name)) handlers.set(name, [])
      handlers.get(name).push(fn)
    },
    effect(fn) {
      disposers.push(fn())
    },
    agents: {
      list: () => [...live.values()],
      get: (id) => live.get(id),
    },
    sessions: {
      get: (id) => live.get(id)?.session,
    },
    tools: {
      register(def) {
        registered.push(def)
        return () => {
          const i = registered.indexOf(def)
          if (i >= 0) registered.splice(i, 1)
        }
      },
    },
    systemPrompt: {
      getSectionOrder: () => 3100,
      section: () => () => {},
    },
    llm: {},
    inject(names, cb) {
      if (names.includes('shellEnv')) {
        cb({
          shellEnv: {
            register(c) {
              shellContributor = c
              return () => {
                shellContributor = null
              }
            },
          },
        })
      }
    },
    get(name) {
      return name === 'profileContext' ? { name: profile } : undefined
    },
  }
  return {
    ctx,
    registered,
    live,
    get shellContributor() {
      return shellContributor
    },
    add(agent) {
      live.set(agent.session.id, agent)
    },
    async emit(name, ...args) {
      const out = []
      for (const fn of handlers.get(name) ?? []) out.push(await fn(...args))
      return out
    },
    dispose() {
      for (const d of disposers) d?.()
    },
  }
}

// fakeDaemon：记下每条 hook 与用量；wait 返回一个由测试决定何时、以什么结束的 promise。
export function fakeDaemon({ hookContext = () => '' } = {}) {
  const hooks = []
  const usages = []
  const waits = []
  const log = []
  return {
    hooks,
    usages,
    waits,
    log,
    async hook(body) {
      hooks.push(body)
      log.push(`hook:${body.hook_event_name}`)
      return hookContext(body)
    },
    wait(args) {
      const d = deferred()
      const entry = { args, ...d, settled: false }
      args.signal?.addEventListener('abort', () => {
        if (!entry.settled) d.reject(new Error('aborted'))
      })
      waits.push(entry)
      log.push(`wait:${args.sessionKey}`)
      return d.promise.finally(() => {
        entry.settled = true
      })
    },
    async usage(body) {
      usages.push(body)
      return {}
    },
    async status() {
      return { connected: true }
    },
    pendingWaits() {
      return waits.filter((w) => !w.settled)
    },
  }
}

export function fakeMcpFactory({ failStarts = 0, tools = [{ name: 'req_get', inputSchema: { type: 'object' } }] } = {}) {
  const made = []
  let starts = 0
  const make = (options) => {
    const client = {
      options,
      instructions: '纸鸢说明',
      closed: false,
      onExit: null,
      onToolsChanged: null,
      async start() {
        starts++
        if (starts <= failStarts) throw new Error('ks mcp 启动失败')
      },
      async listTools() {
        return tools
      },
      async callTool(name, args, meta) {
        client.lastCall = { name, args, meta }
        return { content: [{ type: 'text', text: 'ok' }] }
      },
      close() {
        client.closed = true
      },
    }
    made.push(client)
    return client
  }
  return { make, made, get starts() { return starts } }
}

export const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))
