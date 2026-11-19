// 资源库批量审核（R-393）的纯函数：图片墙多选（点选、Shift 连选、全选当前筛选结果）、个数估算、跳过原因汇总、
// 一次批量请求被多个撤销期共用。界面在 app/assetBatch.tsx；这里不碰 React，便于单测。

import { canReview, type AssetStatus, type WallTile } from './assets.ts'

// MAX_BATCH：一次最多审多少个（与 Hub 的 store.MaxAssetBatch 一致）。
export const MAX_BATCH = 1000

export type Verdict = 'approved' | 'rework'

// tileCodes：图片墙上的素材编号，按墙上的顺序；候选组展开成组里的每一个。
export function tileCodes(tiles: WallTile[]): string[] {
  const out: string[] = []
  for (const t of tiles) for (const a of t.type === 'one' ? [t.asset] : t.assets) out.push(a.code)
  return out
}

// toggleCodes：勾上 / 取消一格（候选组是组里的几个）：都已勾上时全部取消，否则全部勾上。
export function toggleCodes(sel: ReadonlySet<string>, codes: string[]): Set<string> {
  const n = new Set(sel)
  const all = codes.every((c) => n.has(c))
  for (const c of codes) {
    if (all) n.delete(c)
    else n.add(c)
  }
  return n
}

// rangeSel：Shift 连选——把上次点的（anchor）到这次点的之间（含两头，按墙上顺序）都勾上；anchor 不在墙上时只勾这一个。
export function rangeSel(order: string[], sel: ReadonlySet<string>, anchor: string | null, code: string): Set<string> {
  const n = new Set(sel)
  const i = anchor ? order.indexOf(anchor) : -1
  const j = order.indexOf(code)
  if (i < 0 || j < 0) {
    n.add(code)
    return n
  }
  for (let k = Math.min(i, j); k <= Math.max(i, j); k++) n.add(order[k])
  return n
}

// pruneSel：筛选、目录变了以后，只留下仍在墙上的；没有变化时原样返回（不触发重渲染）。
export function pruneSel(sel: ReadonlySet<string>, order: string[]): ReadonlySet<string> {
  if (!sel.size) return sel
  const on = new Set(order)
  let drop = false
  for (const c of sel) if (!on.has(c)) drop = true
  if (!drop) return sel
  return new Set([...sel].filter((c) => on.has(c)))
}

// movable：选中的素材里能下这个结论的有几个（按钮上的个数；以 Hub 预检为准）。
export function movable(list: { status: AssetStatus }[], verdict: Verdict): number {
  return list.filter((a) => a.status !== verdict && canReview(a.status, verdict)).length
}

// skipGroups：预检跳过的素材按原因汇总，多的在前。
export function skipGroups(skipped: { why: string }[]): { why: string; n: number }[] {
  const m = new Map<string, number>()
  for (const s of skipped) m.set(s.why, (m.get(s.why) ?? 0) + 1)
  return [...m.entries()].map(([why, n]) => ({ why, n })).sort((a, b) => b.n - a.n)
}

// sharedCommit：一次批量审核在撤销期里是每个素材各一条（卡片上立刻按审核后的状态显示），提交时只发一个请求。
// 第一个到点的那条决定这次提交哪些：仍在撤销期里的（live）加上它自己；撤销了的不在里面。之后到点的都等同一个结果。
export function sharedCommit(codes: string[], live: (code: string) => boolean, send: (codes: string[]) => Promise<boolean>) {
  let p: Promise<boolean> | null = null
  return (code: string) => {
    if (!p) p = send(codes.filter((c) => c === code || live(c)))
    return p
  }
}
