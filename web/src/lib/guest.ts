// 访客页（R-281 第 3 段）的纯逻辑：路径、编号索引、目录树、状态分布、新动态。
// 访客页只调 /api/share/<token>/…，不碰所有者的接口；这里不依赖 React，方便单测。

import { buildTree, keyOf, type ShareNode, type ShareTree } from './share.ts'

export type GuestRef = { kind: 'req' | 'plan' | 'dec'; code: string; title: string; status: string; turn: false; reqCode?: string }

type DecRef = { code: string; req: string; title: string; status: string }

// guestPath：访客页里的地址。会话键里有冒号，整段编码。
export function guestPath(token: string, kind?: 'req' | 'plan' | 'session', ref?: string): string {
  const base = `/s/${encodeURIComponent(token)}`
  if (!kind || !ref) return base
  const seg = kind === 'req' ? 'r' : kind === 'plan' ? 'p' : 's'
  return `${base}/${seg}/${encodeURIComponent(ref)}`
}

// refIndex：分享里的计划、需求和需求上的决策，按编号建索引。不在索引里的编号只显示编号、不能点开。
export function refIndex(nodes: ShareNode[], decs: DecRef[]): Map<string, GuestRef> {
  const m = new Map<string, GuestRef>()
  for (const n of nodes) {
    if (!n.code) continue
    if (n.kind === 'req') m.set(n.code, { kind: 'req', code: n.code, title: n.title, status: n.status ?? '', turn: false })
    else if (n.kind === 'plan') m.set(n.code, { kind: 'plan', code: n.code, title: n.title, status: n.status ?? '', turn: false })
  }
  for (const d of decs) if (m.has(d.req)) m.set(d.code, { kind: 'dec', code: d.code, title: d.title, status: d.status, turn: false, reqCode: d.req })
  return m
}

export type NavRow = { key: string; depth: number; node: ShareNode }

// navRows：目录树摊平成行：计划 → 阶段 → 需求 → 会话；不在计划里的需求接在后面，没挂在需求下的会话放最后。
// 同一个会话挂在几条需求下时，每条下面都出现一次。
export function navRows(tr: ShareTree): NavRow[] {
  const out: NavRow[] = []
  const seen = new Set<string>()
  const add = (k: string, depth: number) => {
    const node = tr.nodes.get(k)
    if (!node) return
    out.push({ key: k, depth, node })
    seen.add(k)
    for (const c of tr.kids.get(k) ?? []) add(c, depth + 1)
  }
  for (const p of tr.plans) add(p, 0)
  for (const r of tr.loose) add(r, 0)
  // 计划不在分享里、只挂了阶段时，阶段没有上级
  for (const [k, n] of tr.nodes) if (!seen.has(k) && n.kind === 'stage' && !(tr.ups.get(k) ?? []).length) add(k, 0)
  for (const [k, n] of tr.nodes) if (!seen.has(k) && n.kind === 'session') add(k, 0)
  return out
}

export const guestTree = (nodes: ShareNode[]) => buildTree(nodes)
export const sessionKey = (ref: string) => keyOf('session', ref)

// statusBar：需求状态分布，按「还在流转的在前」排好，只留有数的。
const order = ['in_progress', 'review', 'blocked', 'ready', 'draft', 'done', 'shelved']
export function statusBar(counts: Record<string, number>): [string, number][] {
  const known = order.filter((k) => counts[k] > 0).map((k): [string, number] => [k, counts[k]])
  const rest = Object.entries(counts).filter(([k, n]) => n > 0 && !order.includes(k))
  return [...known, ...rest]
}

type Act = { at: string; kind: string; req: string; dec?: string; to?: string; actor: string; session?: string }

// actKey：一条动态的键（动态没有 ID：时间 + 类别 + 对象 + 谁做的）。
export const actKey = (a: Act) => [a.at, a.kind, a.req, a.dec ?? '', a.to ?? '', a.actor, a.session ?? ''].join('|')

// freshOf：和上次看到的相比新出现的动态。第一次加载（prev 为空）不算新。
export function freshOf(prev: Set<string> | null, list: Act[]): Set<string> {
  if (!prev) return new Set()
  return new Set(list.map(actKey).filter((k) => !prev.has(k)))
}
