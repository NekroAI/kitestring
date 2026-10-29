// 等你的事都进「要你处理」（R-243，设计原则第 12 条）：清空「要你处理」，进行中的工作就能一直推进。
// 这里只从明确的记录算（原则第 11 条），不唤醒任何会话：
// - 受阻指向（blocked_on，R-x / D-x）：顺着等待关系往下走，路过的每一条都记下「谁在等它」；
//   走到草稿，那条草稿以「R-210 在等它定稿」进来（普通草稿不进）；走到待验收 / 待答决策，原本就在列表里，标上谁在等。
// - 受阻但没写等什么、或等的那条已经结束（决策已答、需求已完成 / 搁置 / 不存在）仍标受阻的，这条受阻需求本身进来。
// - 进行中、没有待答决策、没有会话在跑，而且这条需求和关联会话都很久没动静（项目设置，默认 2 小时）：算停滞，本身进来。

export type WReq = {
  code: string
  status: string
  blocked_on?: string
  blocked_reason?: string
  sessions: string[]
  updated_at: string
  project_id?: string
  pending_decisions?: number
}
export type WDec = { code: string; status: string; requirement_code?: string }
export type WSes = { key: string; state: string; last_active_at: string }

// 受阻需求本身进来的原因：bare 没写等什么；gone 等的那条已经结束（仍标受阻）；missing 指向的编号不存在
export type BlockedWhy = 'bare' | 'gone' | 'missing'

export type Attention = {
  waitedBy: Map<string, string[]> // R-x / D-x → 在等它的受阻需求（按编号排）
  blocked: { r: WReq; why: BlockedWhy }[]
  drafts: WReq[] // 被受阻需求等着定稿的草稿
  stalled: { r: WReq; since: string }[] // since：最后一次动静
}

// 会话在跑：正在干活或在后台干活；等你授权的另算一件（会话本身在「要你处理」里）
const busy = new Set(['running', 'background', 'waiting_approval'])

export const STALL_DEFAULT_MS = 120 * 60_000

export function attention(reqs: WReq[], decs: WDec[], sessions: WSes[], now: number, stallMs: (projectId: string) => number = () => STALL_DEFAULT_MS): Attention {
  const R = new Map(reqs.map((r) => [r.code, r]))
  const D = new Map(decs.map((d) => [d.code, d]))
  const S = new Map(sessions.map((s) => [s.key, s]))
  const pendingOn = new Map<string, string[]>() // 需求 → 挂在上面的待答决策
  for (const d of decs) if (d.status === 'pending' && d.requirement_code) pendingOn.set(d.requirement_code, [...(pendingOn.get(d.requirement_code) ?? []), d.code])

  const waitedBy = new Map<string, string[]>()
  const mark = (code: string, by: string) => {
    const list = waitedBy.get(code) ?? []
    if (!list.includes(by)) waitedBy.set(code, [...list, by].sort(byCode))
  }
  const blocked: Attention['blocked'] = []
  const drafts = new Map<string, WReq>()

  for (const b of reqs) {
    if (b.status !== 'blocked') continue
    if (!b.blocked_on) {
      blocked.push({ r: b, why: 'bare' })
      continue
    }
    const seen = new Set([b.code])
    let cur = b.blocked_on
    for (;;) {
      if (seen.has(cur)) break // 绕成圈（Hub 设置时已拒绝，老数据兜底）
      seen.add(cur)
      if (cur.startsWith('D-')) {
        const d = D.get(cur)
        if (!d) {
          if (cur === b.blocked_on) blocked.push({ r: b, why: 'missing' })
        } else if (d.status === 'pending') mark(cur, b.code)
        else if (cur === b.blocked_on) blocked.push({ r: b, why: 'gone' })
        break
      }
      const r = R.get(cur)
      if (!r) {
        if (cur === b.blocked_on) blocked.push({ r: b, why: 'missing' })
        break
      }
      if (r.status === 'done' || r.status === 'shelved') {
        if (cur === b.blocked_on) blocked.push({ r: b, why: 'gone' })
        break
      }
      mark(cur, b.code)
      for (const dc of pendingOn.get(cur) ?? []) mark(dc, b.code)
      if (r.status === 'draft') drafts.set(r.code, r)
      if (r.status === 'blocked' && r.blocked_on) {
        cur = r.blocked_on
        continue
      }
      break
    }
  }

  const stalled: Attention['stalled'] = []
  for (const r of reqs) {
    if (r.status !== 'in_progress' || (pendingOn.get(r.code)?.length ?? 0) > 0) continue
    const linked = r.sessions.map((k) => S.get(k)).filter((x): x is WSes => !!x)
    if (linked.some((s) => busy.has(s.state))) continue
    let last = r.updated_at
    for (const s of linked) if (s.last_active_at > last) last = s.last_active_at
    if (now - Date.parse(last) >= stallMs(r.project_id ?? '')) stalled.push({ r, since: last })
  }

  return { waitedBy, blocked: blocked.sort((a, b) => byCode(a.r.code, b.r.code)), drafts: [...drafts.values()].sort((a, b) => byCode(a.code, b.code)), stalled }
}

function byCode(a: string, b: string) {
  return Number(a.slice(2)) - Number(b.slice(2)) || a.localeCompare(b)
}

// stallLimit：项目设置的停滞时长（分钟，0 = 默认 2 小时）。
export function stallLimit(minutes: number | undefined) {
  return minutes && minutes > 0 ? minutes * 60_000 : STALL_DEFAULT_MS
}
