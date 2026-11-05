// 分享（R-281）的勾选规则：所有者新建 / 编辑分享时的分层勾选列表（计划 → 阶段 → 需求 → 会话）。
// 挂载项（items）和后端 store.ResolveShare 同一套规则，这里在网页上算出勾选状态，不必每点一下都问后端：
// - 明确挂上的项按自己的「包含以后新增的」（auto）；上级开着 auto 时，下面现在和以后的项都算进来，并带着 auto 往下传；
// - 单独取消的项（excluded）连同只经由它带进来的下级都不算，上级的 auto 不会把它带回来；
// - 会话按会话键算：在一条需求下取消，所有需求下都不出现。

export type ShareKind = 'plan' | 'stage' | 'req' | 'session'

export type ShareNode = {
  kind: ShareKind
  ref: string
  code?: string
  seq?: number
  title: string
  status?: string
  role?: string
  parents?: string[] | null
}

export type ShareItem = { kind: ShareKind; ref: string; auto: boolean; excluded: boolean }

export const keyOf = (kind: string, ref: string) => `${kind}|${ref}`
const splitKey = (key: string) => {
  const i = key.indexOf('|')
  return { kind: key.slice(0, i) as ShareKind, ref: key.slice(i + 1) }
}

const upKind: Record<ShareKind, ShareKind | null> = { plan: null, stage: 'plan', req: 'stage', session: 'req' }

export type ShareTree = {
  nodes: Map<string, ShareNode>
  kids: Map<string, string[]> // 下级，按 nodes 的顺序
  ups: Map<string, string[]> // 上级（会话可能挂在几条需求下）
  plans: string[]
  loose: string[] // 不在计划里的需求
  byCode: Map<string, string> // P-x / R-x → 键
}

// buildTree 把后端给的节点列表（parents 连上级）排成树。
export function buildTree(list: ShareNode[]): ShareTree {
  const tr: ShareTree = { nodes: new Map(), kids: new Map(), ups: new Map(), plans: [], loose: [], byCode: new Map() }
  for (const n of list) {
    const k = keyOf(n.kind, n.ref)
    tr.nodes.set(k, n)
    if (n.code && (n.kind === 'plan' || n.kind === 'req')) tr.byCode.set(n.code, k)
  }
  for (const n of list) {
    const k = keyOf(n.kind, n.ref)
    const uk = upKind[n.kind]
    const ups = uk ? (n.parents ?? []).map((p) => keyOf(uk, p)).filter((p) => tr.nodes.has(p)) : []
    tr.ups.set(k, ups)
    for (const p of ups) tr.kids.set(p, [...(tr.kids.get(p) ?? []), k])
    if (n.kind === 'plan') tr.plans.push(k)
    else if (n.kind === 'req' && !ups.length) tr.loose.push(k)
  }
  return tr
}

export type Resolved = { inc: Set<string>; auto: Map<string, boolean> }

// resolve：按挂载项算出实际包含哪些节点、各自是否「包含以后新增的」。和后端 ResolveShare 一一对应。
export function resolve(tr: ShareTree, items: ShareItem[]): Resolved {
  const rows = new Map(items.map((it) => [keyOf(it.kind, it.ref), it]))
  const inc = new Set<string>()
  const auto = new Map<string, boolean>()
  const level = (kind: ShareKind, fromAuto: string[]) => {
    const cand = new Map<string, boolean>() // 键 → 经由开着 auto 的上级带进来
    for (const p of fromAuto) for (const c of tr.kids.get(p) ?? []) cand.set(c, true)
    for (const it of items) {
      const k = keyOf(it.kind, it.ref)
      if (it.kind === kind && tr.nodes.has(k) && !cand.has(k)) cand.set(k, false)
    }
    const next: string[] = []
    for (const [k, inherited] of cand) {
      const it = rows.get(k)
      const on = it ? !it.excluded : inherited
      if (!on) continue
      const a = it ? it.auto : true
      inc.add(k)
      auto.set(k, a)
      if (a) next.push(k)
    }
    return next
  }
  const plans: string[] = []
  for (const it of items) {
    const k = keyOf(it.kind, it.ref)
    if (it.kind !== 'plan' || it.excluded || !tr.nodes.has(k)) continue
    inc.add(k)
    auto.set(k, it.auto)
    if (it.auto) plans.push(k)
  }
  level('session', level('req', level('stage', plans)))
  return { inc, auto }
}

// descendants：一个节点下面的全部节点（不含自己）。
export function descendants(tr: ShareTree, key: string): Set<string> {
  const out = new Set<string>()
  const walk = (k: string) => {
    for (const c of tr.kids.get(k) ?? []) {
      if (out.has(c)) continue
      out.add(c)
      walk(c)
    }
  }
  walk(key)
  return out
}

// inherited：有开着 auto 的上级把它带进来（不看它自己的挂载项）。
const inherited = (tr: ShareTree, r: Resolved, key: string) => (tr.ups.get(key) ?? []).some((p) => r.inc.has(p) && r.auto.get(p))

const toList = (rows: Map<string, ShareItem>) => [...rows.values()]
const mk = (key: string, auto: boolean, excluded = false): ShareItem => ({ ...splitKey(key), auto, excluded })

// check：勾上一项，连带它下面的全部内容。勾计划时默认开「包含以后新增的」；
// 没开时把下面现有的内容逐条写上（以后新增的不带）。
export function check(tr: ShareTree, items: ShareItem[], key: string): ShareItem[] {
  const rows = new Map(items.map((it) => [keyOf(it.kind, it.ref), it]))
  const r = resolve(tr, items)
  if (r.inc.has(key)) return items
  const it = rows.get(key)
  if (inherited(tr, r, key)) rows.delete(key)
  else rows.set(key, it ? { ...it, excluded: false } : mk(key, splitKey(key).kind === 'plan'))
  const r2 = resolve(tr, toList(rows))
  const fill = (k: string) => {
    if (r2.auto.get(k)) return // 开着 auto：下面的自动带上
    for (const c of tr.kids.get(k) ?? []) {
      if (!r2.inc.has(c)) rows.set(c, mk(c, false))
      fill(c)
    }
  }
  fill(key)
  return toList(rows)
}

// uncheck：取消一项，连同它下面的内容。上级开着 auto 时记成「单独取消」，以后不会被带回来；
// 会话还挂在别的、仍在分享里的需求下时不动它。
export function uncheck(tr: ShareTree, items: ShareItem[], key: string): ShareItem[] {
  const rows = new Map(items.map((it) => [keyOf(it.kind, it.ref), it]))
  const r = resolve(tr, items)
  const sub = descendants(tr, key)
  if (inherited(tr, r, key)) rows.set(key, mk(key, false, true))
  else rows.delete(key)
  for (const d of sub) {
    if (!rows.has(d)) continue
    if (splitKey(d).kind === 'session' && (tr.ups.get(d) ?? []).some((p) => p !== key && !sub.has(p) && r.inc.has(p))) continue
    rows.delete(d)
  }
  // 还被别的路径带进来（例如同一会话挂在另一条开着 auto 的需求下）：明确记成单独取消
  if (resolve(tr, toList(rows)).inc.has(key)) rows.set(key, mk(key, false, true))
  return toList(rows)
}

// toggle：勾上 / 取消。
export const toggle = (tr: ShareTree, items: ShareItem[], key: string) => (resolve(tr, items).inc.has(key) ? uncheck(tr, items, key) : check(tr, items, key))

// setAuto：打开 / 关掉「包含以后新增的」，现在勾着的内容保持不变。
// 打开时，现在没勾的下级记成单独取消（不会因此被带回来）；关掉时，现在由它带进来的下级逐条写上。
export function setAuto(tr: ShareTree, items: ShareItem[], key: string, on: boolean): ShareItem[] {
  const rows = new Map(items.map((it) => [keyOf(it.kind, it.ref), it]))
  const r = resolve(tr, items)
  if (!r.inc.has(key) || !!r.auto.get(key) === on) return items
  for (const c of tr.kids.get(key) ?? []) {
    const it = rows.get(c)
    if (on && !r.inc.has(c) && !it) rows.set(c, mk(c, false, true))
    if (!on && r.inc.has(c) && !it) rows.set(c, mk(c, !!r.auto.get(c)))
  }
  rows.set(key, { ...(rows.get(key) ?? mk(key, on)), auto: on, excluded: false })
  return toList(rows)
}

// tidy：去掉多余的「单独取消」（没有开着 auto 的上级，本来就不会被带进来），提交前用。
export function tidy(tr: ShareTree, items: ShareItem[]): ShareItem[] {
  const r = resolve(tr, items)
  return items.filter((it) => !it.excluded || !tr.nodes.has(keyOf(it.kind, it.ref)) || inherited(tr, r, keyOf(it.kind, it.ref)))
}

// missing：勾着的一项下面有几项被取消（「已取消 n 项」）。
export function missing(tr: ShareTree, r: Resolved, key: string): number {
  if (!r.inc.has(key)) return 0
  let n = 0
  for (const d of descendants(tr, key)) if (!r.inc.has(d)) n++
  return n
}

export type ShareCounts = { plans: number; stages: number; reqs: number; sessions: number }
export function counts(tr: ShareTree, r: Resolved): ShareCounts {
  const c: ShareCounts = { plans: 0, stages: 0, reqs: 0, sessions: 0 }
  for (const k of r.inc) {
    const kind = splitKey(k).kind
    if (!tr.nodes.has(k)) continue
    if (kind === 'plan') c.plans++
    else if (kind === 'stage') c.stages++
    else if (kind === 'req') c.reqs++
    else c.sessions++
  }
  return c
}

// preset：从需求 / 计划的「分享」进入新建时预先勾好它和它下面的内容（包含以后新增的）。
export function preset(tr: ShareTree, code: string): ShareItem[] {
  const k = tr.byCode.get(code)
  return k ? [mk(k, true)] : []
}

export type GraphCol = { kind: ShareKind; keys: string[] }
export type GraphEdge = { from: string; to: string; direct: boolean }

// graph：只读关系图的列和连线。上级没勾时连到最近一个勾上的上级（虚线）；会话只连到勾上的需求。
export function graph(tr: ShareTree, inc: Set<string>): { cols: GraphCol[]; edges: GraphEdge[] } {
  const order: ShareKind[] = ['plan', 'stage', 'req', 'session']
  const cols = order.map((kind) => ({ kind, keys: [...tr.nodes.keys()].filter((k) => inc.has(k) && splitKey(k).kind === kind) }))
  const edges: GraphEdge[] = []
  const nearest = (k: string): { key: string; direct: boolean } | null => {
    let direct = true
    let cur = k
    for (;;) {
      const up = (tr.ups.get(cur) ?? [])[0]
      if (!up) return null
      if (inc.has(up)) return { key: up, direct }
      direct = false
      cur = up
    }
  }
  for (const k of inc) {
    if (!tr.nodes.has(k)) continue
    if (splitKey(k).kind === 'session') {
      for (const p of tr.ups.get(k) ?? []) if (inc.has(p)) edges.push({ from: p, to: k, direct: true })
      continue
    }
    const a = nearest(k)
    if (a) edges.push({ from: a.key, to: k, direct: a.direct })
  }
  return { cols, edges }
}

// expiry：有效期选项换算成到期时间；永久为 null。自定义日期按当天结束算。
export type ExpiryChoice = '7' | '30' | 'forever' | 'custom'
export function expiryAt(choice: ExpiryChoice, date: string, now = Date.now()): Date | null {
  if (choice === 'forever') return null
  if (choice === 'custom') {
    const [y, m, d] = date.split('-').map(Number)
    if (!y || !m || !d) return null
    return new Date(y, m - 1, d, 23, 59, 59)
  }
  return new Date(now + Number(choice) * 86_400_000)
}

// linkURL：访客链接的完整地址。设置里填了对外地址时后端已拼好（url），没填时用浏览器当前地址。
export const linkURL = (l: { url?: string; path: string }, origin: string) => l.url || origin + l.path
