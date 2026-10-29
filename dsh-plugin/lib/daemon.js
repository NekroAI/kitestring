// 访问本机 ks daemon 的 unix socket 客户端。插件不读 Hub 凭证，也不直连 Hub：
// 所有事件、待命和工具调用都走 daemon 已有的接口，和 ks hook / ks mcp 是同一条路。
import http from 'node:http'
import os from 'node:os'
import path from 'node:path'

// ksHome 与 daemon.Home() 的规则保持一致：KS_HOME 优先，否则 ~/.kitestring。
export function ksHome() {
  return process.env.KS_HOME || path.join(os.homedir(), '.kitestring')
}

export function defaultSocketPath() {
  return path.join(ksHome(), 'daemon.sock')
}

export class DaemonClient {
  constructor({ socketPath = defaultSocketPath(), logger } = {}) {
    this.socketPath = socketPath
    this.logger = logger
  }

  // request 发一个 HTTP 请求到 unix socket。body 为 undefined 时不带请求体。
  request(method, urlPath, body, { signal, timeoutMs = 5000 } = {}) {
    return new Promise((resolve, reject) => {
      const payload = body === undefined ? undefined : JSON.stringify(body)
      const req = http.request(
        {
          socketPath: this.socketPath,
          path: urlPath,
          method,
          headers: payload === undefined ? {} : { 'content-type': 'application/json', 'content-length': Buffer.byteLength(payload) },
          signal,
        },
        (res) => {
          let text = ''
          res.setEncoding('utf8')
          res.on('data', (chunk) => {
            text += chunk
          })
          res.on('end', () => {
            if (res.statusCode === 204 || text.trim() === '') {
              resolve({})
              return
            }
            try {
              resolve(JSON.parse(text))
            } catch {
              reject(new Error(`daemon 返回了非 JSON（HTTP ${res.statusCode}）：${text.slice(0, 200)}`))
            }
          })
        },
      )
      if (timeoutMs > 0) {
        req.setTimeout(timeoutMs, () => req.destroy(new Error(`daemon 请求超时（${method} ${urlPath}）`)))
      }
      req.on('error', reject)
      if (payload !== undefined) req.write(payload)
      req.end()
    })
  }

  // hook 上报一条事件，返回要注入模型上下文的文字（通常为空）。
  async hook(payload, options) {
    const res = await this.request('POST', '/hook', { tool: 'dsh', ppid: 0, payload }, options)
    return typeof res?.context === 'string' ? res.context : ''
  }

  // wait 以插件身份挂起等待（via=plugin）。daemon 侧只把这类请求当作 DSH 的待命，不拦截模型自己跑的 ks wait。
  // instance 是 Host 的“进程号@启动时间”：同一会话开在两个 Host 时，daemon 据此让较早的那个直接返回 stale（R-251）。
  wait({ sessionKey, instance, timeoutMs, signal }) {
    const q = new URLSearchParams({ session_key: sessionKey, via: 'plugin', timeout: `${Math.max(1, Math.floor(timeoutMs / 1000))}s` })
    if (instance) q.set('instance', instance)
    // 客户端比 daemon 的上限再多等一点，避免两端同时到期时把正常超时误判成连接错误。
    return this.request('GET', `/wait?${q.toString()}`, undefined, { signal, timeoutMs: timeoutMs + 60_000 })
  }

  // status 用于插件启动时判断 daemon 是否在线（只做诊断，失败不阻塞）。
  status() {
    return this.request('GET', '/status', undefined, { timeoutMs: 2000 })
  }

  // usage 上报一次模型回复的 token 用量（绝对值）。daemon 按分钟累加，随原有批次送到 Hub。
  // agentId 是子代理的会话 ID（主线程为空）：子代理的用量记在根会话名下（R-251）。
  usage({ sessionKey, agentId, model, at, input, cacheRead, cacheWrite, output, requests }) {
    return this.request(
      'POST',
      '/usage',
      {
        session_key: sessionKey,
        ...(agentId ? { agent_id: agentId } : {}),
        model,
        at,
        input,
        cache_read: cacheRead,
        cache_write: cacheWrite,
        output,
        requests,
      },
      { timeoutMs: 3000 },
    )
  }
}
