// 通讯页（R-249）的筛选：按会话、身份、需求、要动手 / 只告知 / 广播、没送到、关键词过滤消息；纸鸢提醒另由开关控制。

export type CommsRange = 'h1' | 'h6' | 'today' | 'week'
export type CommsKind = 'all' | 'action' | 'fyi' | 'cast'

export type CommsMsgLike = {
  system: boolean
  broadcast: boolean
  fyi: boolean
  requirement?: string
  text: string
  full: string
  from?: { key: string; role: string; title?: string; agent_name?: string }
  recipients: { to: string; role?: string; title?: string; status: string }[] | null
}

export type CommsFilter = {
  session?: string // 会话键：发的或收的
  role?: string // 身份；'none' = 无身份；空 = 不限
  req?: string // 需求编号
  kind: CommsKind
  unsent: boolean
  q: string
}

const rcpts = (m: CommsMsgLike) => m.recipients ?? []

// involves：会话 key 是这条消息的发送方或收件人之一。
export const involves = (m: CommsMsgLike, key: string) => m.from?.key === key || rcpts(m).some((r) => r.to === key)

// unsent：有收件人还没送到（等对方醒来或对方不在线）。
export const unsent = (m: CommsMsgLike) => rcpts(m).some((r) => r.status !== 'delivered')

const roleOf = (r: string | undefined) => r || 'none'

export function matches(m: CommsMsgLike, f: CommsFilter): boolean {
  if (f.session && !involves(m, f.session)) return false
  if (f.role && roleOf(m.from?.role) !== f.role && !rcpts(m).some((r) => roleOf(r.role) === f.role)) return false
  if (f.req && m.requirement !== f.req) return false
  if (f.kind === 'action' && m.fyi) return false
  if (f.kind === 'fyi' && !m.fyi) return false
  if (f.kind === 'cast' && !m.broadcast) return false
  if (f.unsent && !unsent(m)) return false
  const q = f.q.trim().toLowerCase()
  if (q) {
    const hay = [m.full, m.text, m.requirement, m.from?.title, m.from?.agent_name, ...rcpts(m).map((r) => r.title)].filter(Boolean).join('\n').toLowerCase()
    if (!hay.includes(q)) return false
  }
  return true
}

// rangeStart：时间范围的起点（本地时间）。1 小时 / 6 小时按整分钟取，免得每次刷新都换一个查询。
export function rangeStart(range: CommsRange, now: number): Date {
  const minute = Math.floor(now / 60_000) * 60_000
  switch (range) {
    case 'h1':
      return new Date(minute - 3_600_000)
    case 'h6':
      return new Date(minute - 6 * 3_600_000)
    case 'week': {
      const d = dayStart(now)
      d.setDate(d.getDate() - 6)
      return d
    }
    default:
      return dayStart(now)
  }
}

// dayStart：本地今天零点。
export function dayStart(now: number): Date {
  const d = new Date(now)
  d.setHours(0, 0, 0, 0)
  return d
}
