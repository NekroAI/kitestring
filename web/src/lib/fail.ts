// 失败提示（R-236）：中文，说清哪件事没成功、为什么、现在是什么状态，不直接显示英文报错原文（如 Failed to fetch）。
// 用词按 docs/copywriting.md 3.8：「{动作}未成功：{原因}。未做改动，可重试。」
// 全站写操作的失败提示都从这里出（app/actions.ts 的 run、各页自己 try/catch 的地方用 api/client.ts 的 errMsg）。

export type Failure =
  | { kind: 'network' } // 请求没发出去或没有回音：断网、纸鸢在重启、代理断开
  | { kind: 'timeout' } // 等太久没回应（写操作 30 秒）
  | { kind: 'http'; status: number; detail?: string } // 纸鸢回了错误
  | { kind: 'other'; message?: string }

const cjk = /[一-鿿]/

// reasonOf：失败的原因，一句白话。
export function reasonOf(f: Failure): string {
  switch (f.kind) {
    case 'network':
      return '无法连接纸鸢（网络已断开，或纸鸢正在重启）'
    case 'timeout':
      return '纸鸢长时间未响应'
    case 'http': {
      // 纸鸢自己写的中文说明最准，直接用；英文原文（框架默认的 Not Found、validation failed 之类）按状态码换成白话
      const d = (f.detail ?? '').trim().replace(/[。.]+$/, '')
      if (d && cjk.test(d)) return d
      if (f.status === 401) return '登录已过期，需重新登录'
      if (f.status === 403) return '没有权限执行此操作'
      if (f.status === 404) return '记录不存在，可能已被删除'
      if (f.status === 409) return '状态已变化，刷新后查看'
      if (f.status === 413) return '内容过大'
      if (f.status === 400 || f.status === 422) return '提交的内容有误'
      if (f.status >= 500) return `纸鸢内部错误（${f.status}）`
      return `纸鸢拒绝了请求（${f.status}）`
    }
    default: {
      const m = (f.message ?? '').trim().replace(/[。.]+$/, '')
      return m && cjk.test(m) ? m : '未知错误'
    }
  }
}

// failText：完整的一句提示。what 是这件事（「关闭 D-964」「提交验收」），不给时只说原因。
// 失败时什么都没改（撤销期里的事回到原处、按钮恢复可点），所以都写「未做改动，可重试」；登录过期的除外。
export function failText(f: Failure, what?: string): string {
  const reason = reasonOf(f)
  const after = f.kind === 'http' && f.status === 401 ? '' : '未做改动，可重试。'
  if (what) return `${what}${/[A-Za-z0-9]$/.test(what) ? ' ' : ''}未成功：${reason}。${after}`
  return `${reason}。${after}`
}

// classify：把抛出来的错误归类。ApiError 由 api/client.ts 的 unwrap 抛出（带 status）；fetch 断网是 TypeError；超时是 TimeoutError / AbortError。
export function classify(e: unknown): Failure {
  if (e && typeof e === 'object') {
    const x = e as { name?: string; message?: string; status?: number; detail?: string }
    if (typeof x.status === 'number' && x.status > 0) return { kind: 'http', status: x.status, detail: x.detail ?? x.message }
    if (x.name === 'TimeoutError' || x.name === 'AbortError') return { kind: 'timeout' }
    if (x.name === 'TypeError' && /fetch|network|load failed/i.test(x.message ?? '')) return { kind: 'network' }
    return { kind: 'other', message: x.message }
  }
  return { kind: 'other' }
}
