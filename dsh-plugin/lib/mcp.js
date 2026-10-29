// 一个只够用来连 `ks mcp` 的最小 MCP stdio 客户端。不引第三方依赖：
// 行分隔的 JSON-RPC、initialize / tools/list / tools/call，以及取消通知，就是插件需要的全部。
//
// 协商 2025-11-25：这个版本的 tools/call 允许在 params 里直接带 `_meta`，正好用来传纸鸢的会话身份；
// 2026-07-28 那套每请求 _meta 信封对这里没有额外价值。
import { spawn } from 'node:child_process'
import { createInterface } from 'node:readline'

const PROTOCOL_VERSION = '2025-11-25'

export class McpClient {
  constructor({ command, args = [], cwd, env = process.env, logger, clientName = 'kitestring-dsh', clientVersion = '0.1.0', spawnFn = spawn }) {
    this.spawnFn = spawnFn
    this.command = command
    this.args = args
    this.cwd = cwd
    this.env = env
    this.logger = logger
    this.clientName = clientName
    this.clientVersion = clientVersion
    this.child = null
    this.pending = new Map()
    this.nextId = 1
    this.closed = false
    this.instructions = ''
    this.onToolsChanged = null
    this.onExit = null
  }

  log(level, message) {
    const logger = this.logger
    if (!logger) return
    const fn = typeof logger[level] === 'function' ? logger[level].bind(logger) : null
    if (fn) fn(`[kitestring] ${message}`)
  }

  async start() {
    if (this.child) throw new Error('MCP 客户端已经启动')
    const child = this.spawnFn(this.command, this.args, { cwd: this.cwd, env: this.env, stdio: ['pipe', 'pipe', 'pipe'] })
    this.child = child

    const exited = new Promise((resolve) => {
      child.once('exit', (code, signal) => resolve({ code, signal }))
    })
    // error 可能来不止一次（启动失败、kill 失败）：用 on，避免第二次成了未处理的 error 把 Host 带崩。
    child.on('error', (error) => {
      this.failAll(new Error(`启动 ${this.command} 失败：${error.message}`))
    })
    // ks mcp 退出后再写 stdin 会得到 EPIPE；不监听的话这个 error 会让整个 Host 崩溃（R-251）。
    // 这里只记日志并让挂着的请求失败，退出本身由 exit 事件接着处理（重连）。
    child.stdin?.on('error', (error) => {
      this.log('debug', `ks mcp stdin 出错：${error.message}`)
      this.failAll(new Error(`ks mcp 连接已断开：${error.message}`))
    })
    child.stdout?.on('error', (error) => this.log('debug', `ks mcp stdout 出错：${error.message}`))
    child.stderr?.on('error', (error) => this.log('debug', `ks mcp stderr 出错：${error.message}`))
    child.once('exit', (code, signal) => {
      const wasClosed = this.closed
      this.failAll(new Error(`ks mcp 已退出（code=${code}, signal=${signal ?? ''}）`))
      if (!wasClosed) this.onExit?.(code, signal)
    })

    createInterface({ input: child.stdout }).on('line', (line) => this.handleLine(line))
    createInterface({ input: child.stderr }).on('line', (line) => {
      if (line.trim() !== '') this.log('debug', `ks mcp stderr: ${line}`)
    })

    const init = await Promise.race([
      this.request('initialize', {
        protocolVersion: PROTOCOL_VERSION,
        capabilities: {},
        clientInfo: { name: this.clientName, version: this.clientVersion },
      }),
      exited.then(({ code, signal }) => {
        throw new Error(`ks mcp 在初始化前退出（code=${code}, signal=${signal ?? ''}）`)
      }),
    ])
    this.instructions = typeof init?.instructions === 'string' ? init.instructions : ''
    this.notify('notifications/initialized', {})
    return init
  }

  handleLine(line) {
    const text = line.trim()
    if (text === '') return
    let msg
    try {
      msg = JSON.parse(text)
    } catch {
      this.log('debug', `ks mcp 输出了非 JSON：${text.slice(0, 200)}`)
      return
    }
    if (msg.id !== undefined && msg.id !== null) {
      const entry = this.pending.get(msg.id)
      if (entry) {
        this.pending.delete(msg.id)
        clearTimeout(entry.timer)
        if (msg.error) entry.reject(new Error(`${msg.error.message ?? 'MCP 错误'}（code=${msg.error.code ?? '?'}）`))
        else entry.resolve(msg.result)
        return
      }
    }
    if (typeof msg.method === 'string') {
      if (msg.id !== undefined && msg.id !== null) {
        // 服务端发起的请求（sampling 等）本插件不支持，明确拒绝而不是挂着。
        this.write({ jsonrpc: '2.0', id: msg.id, error: { code: -32601, message: `不支持 ${msg.method}` } })
        return
      }
      if (msg.method === 'notifications/tools/list_changed') this.onToolsChanged?.()
    }
  }

  // alive：子进程还在、stdin 还能写。写之前先查，进程已退出时直接报错而不是往断掉的管道里写。
  alive() {
    const child = this.child
    if (!child || this.closed) return false
    if (child.exitCode !== null && child.exitCode !== undefined) return false
    if (child.signalCode !== null && child.signalCode !== undefined) return false
    const stdin = child.stdin
    return Boolean(stdin && !stdin.destroyed && stdin.writable !== false && !stdin.writableEnded)
  }

  write(msg) {
    if (!this.alive()) throw new Error('MCP 连接已关闭')
    this.child.stdin.write(`${JSON.stringify(msg)}\n`, (error) => {
      if (error) this.log('debug', `写入 ks mcp 失败：${error.message}`)
    })
  }

  notify(method, params) {
    try {
      this.write({ jsonrpc: '2.0', method, params })
    } catch (error) {
      this.log('debug', `发送通知失败：${error.message}`)
    }
  }

  request(method, params, { timeoutMs = 120_000, signal } = {}) {
    if (!this.alive()) return Promise.reject(new Error('MCP 连接已关闭'))
    const id = this.nextId++
    return new Promise((resolve, reject) => {
      let settled = false
      const cleanup = () => {
        signal?.removeEventListener?.('abort', onAbort)
      }
      const finish = (fn, value) => {
        if (settled) return
        settled = true
        cleanup()
        fn(value)
      }
      const timer =
        timeoutMs > 0
          ? setTimeout(() => {
              this.pending.delete(id)
              finish(reject, new Error(`MCP 请求超时：${method}`))
            }, timeoutMs)
          : null
      const onAbort = () => {
        this.pending.delete(id)
        if (timer) clearTimeout(timer)
        this.notify('notifications/cancelled', { requestId: id, reason: 'aborted' })
        finish(reject, new Error('MCP 请求已取消'))
      }
      this.pending.set(id, {
        resolve: (value) => {
          if (timer) clearTimeout(timer)
          finish(resolve, value)
        },
        reject: (error) => {
          if (timer) clearTimeout(timer)
          finish(reject, error)
        },
        timer,
      })
      if (signal) {
        if (signal.aborted) {
          onAbort()
          return
        }
        signal.addEventListener('abort', onAbort, { once: true })
      }
      try {
        this.write({ jsonrpc: '2.0', id, method, params })
      } catch (error) {
        this.pending.delete(id)
        if (timer) clearTimeout(timer)
        finish(reject, error)
      }
    })
  }

  async listTools() {
    const tools = []
    let cursor
    for (let page = 0; page < 20; page++) {
      const res = await this.request('tools/list', cursor === undefined ? {} : { cursor })
      if (Array.isArray(res?.tools)) tools.push(...res.tools)
      cursor = typeof res?.nextCursor === 'string' ? res.nextCursor : undefined
      if (cursor === undefined || cursor === '') break
    }
    return tools
  }

  // callTool 把纸鸢的会话身份放进 _meta；ks mcp 优先读它，其次才走 Claude / Codex 的推断。
  callTool(name, args, { sessionKey, agentId, signal, timeoutMs = 180_000 } = {}) {
    const meta = { 'kitestring/session': sessionKey }
    if (agentId) meta['kitestring/agent'] = agentId
    return this.request('tools/call', { name, arguments: args ?? {}, _meta: meta }, { signal, timeoutMs })
  }

  failAll(error) {
    for (const [, entry] of this.pending) {
      clearTimeout(entry.timer)
      entry.reject(error)
    }
    this.pending.clear()
  }

  close() {
    if (this.closed) return
    this.closed = true
    this.failAll(new Error('MCP 连接已关闭'))
    const child = this.child
    this.child = null
    if (child) {
      try {
        child.stdin.end()
      } catch {
        // 已经关了
      }
      try {
        child.kill('SIGTERM')
      } catch {
        // 已经退出
      }
    }
  }
}
