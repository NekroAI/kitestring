// 资源库（R-351 第 3 步）的纯函数：筛选、分面、图片墙分组（同一用途的候选成组）、封面图、版本对比、审片队列。
// 界面在 app/assets.tsx；这里不碰 React，便于单测。

export type AssetStatus = 'wip' | 'pending' | 'approved' | 'rework'
export const ASSET_STATUSES: AssetStatus[] = ['wip', 'pending', 'approved', 'rework']

export type FileLite = { id: string; name: string; mime: string; size: number }
export type VersionLite = {
  id: string
  version: number
  batch: string
  branch: string
  commit: string
  file?: FileLite | null
  previews?: FileLite[] | null
  triangles?: number
  budget?: number
  size?: number[] | null
  pixels?: number[] | null
  created_at: string
}
export type AssetLite = {
  id: string
  code: string
  project_id: string
  batch: string
  key: string
  kind: 'part' | 'image'
  category: string
  group?: string
  status: AssetStatus
  version: number
  updated_at: string
  latest?: VersionLite | null
}

export type AssetFilter = { project: string; batch: string; category: string; status: string }

// projectsOf：有素材的项目，最近有更新的在前（资源库页的项目切换只列这些）。
export function projectsOf(list: AssetLite[]): string[] {
  const last = new Map<string, string>()
  for (const a of list) if ((last.get(a.project_id) ?? '') < a.updated_at) last.set(a.project_id, a.updated_at)
  return [...last.entries()].sort((a, b) => b[1].localeCompare(a[1]) || a[0].localeCompare(b[0])).map(([p]) => p)
}

// 批次名按自然顺序排（G2 在 G11 前面，G11e1 在 G11e2 前面）。
const natural = new Intl.Collator('zh', { numeric: true, sensitivity: 'base' })
export const naturalSort = (xs: Iterable<string>) => [...xs].sort(natural.compare)

// facetsOf：一个项目里全部素材的批次、类别和各状态数量；不受批次 / 类别 / 状态筛选影响。
export function facetsOf(list: AssetLite[], project: string) {
  const inProj = list.filter((a) => a.project_id === project)
  const counts: Record<string, number> = Object.fromEntries(ASSET_STATUSES.map((s) => [s, 0]))
  for (const a of inProj) counts[a.status] = (counts[a.status] ?? 0) + 1
  return {
    batches: naturalSort(new Set(inProj.map((a) => a.batch))),
    categories: naturalSort(new Set(inProj.map((a) => a.category).filter(Boolean))),
    counts,
    total: inProj.length,
  }
}

export function filterAssets(list: AssetLite[], f: AssetFilter): AssetLite[] {
  return list.filter((a) => a.project_id === f.project && (!f.batch || a.batch === f.batch) && (!f.category || a.category === f.category) && (!f.status || a.status === f.status))
}

// 图片墙上的一格：单个素材，或同一用途的一组候选（概念图 A / B 版）。
export type WallTile = { type: 'one'; asset: AssetLite } | { type: 'group'; key: string; group: string; batch: string; assets: AssetLite[] }

// groupKey：候选分组只在同一项目、同一批次里成立。
export const groupKey = (a: Pick<AssetLite, 'project_id' | 'batch' | 'group'>) => `${a.batch}/${a.group ?? ''}`

// wallTiles：按列表顺序排，同组候选（两张及以上）收成一格，放在组里第一张的位置；只剩一张的照常单独一格。
export function wallTiles(list: AssetLite[]): WallTile[] {
  const groups = new Map<string, AssetLite[]>()
  for (const a of list) {
    if (!a.group) continue
    const k = groupKey(a)
    groups.set(k, [...(groups.get(k) ?? []), a])
  }
  const out: WallTile[] = []
  const done = new Set<string>()
  for (const a of list) {
    const k = a.group ? groupKey(a) : ''
    const g = k ? groups.get(k) : undefined
    if (!g || g.length < 2) {
      out.push({ type: 'one', asset: a })
      continue
    }
    if (done.has(k)) continue
    done.add(k)
    out.push({ type: 'group', key: k, group: a.group!, batch: a.batch, assets: [...g].sort((x, y) => natural.compare(x.key, y.key)) })
  }
  return out
}

// groupMembers：并排比较时取同组全部候选（不受状态筛选影响，否则 A 通过后 B 就看不到对照了）。
export function groupMembers(list: AssetLite[], key: string): AssetLite[] {
  return list.filter((a) => a.group && groupKey(a) === key).sort((x, y) => natural.compare(x.key, y.key))
}

// candidateLabel：候选在组里的叫法，utility_4x4_A → A；取不到时用素材 ID。
export function candidateLabel(a: Pick<AssetLite, 'key' | 'group'>): string {
  if (a.group && a.key.startsWith(a.group)) {
    const rest = a.key.slice(a.group.length).replace(/^[_\-. ]+/, '')
    if (rest) return rest
  }
  return a.key
}

// groupStatus：一组候选的状态标记取最需要处理的那个：待审 > 返工 > 制作中 > 通过。
const URGENT: AssetStatus[] = ['pending', 'rework', 'wip', 'approved']
export function groupStatus(assets: Pick<AssetLite, 'status'>[]): AssetStatus {
  for (const s of URGENT) if (assets.some((a) => a.status === s)) return s
  return 'pending'
}

const isImg = (f?: FileLite | null) => !!f && /^image\/(png|jpeg|gif|webp)/i.test(f.mime)

// coverOf：图片墙和大图用哪张图。独立的图就是它自己；零件优先用特写（文件名带 closeup），否则第一张预览图；都没有返回 null。
export function coverOf(kind: 'part' | 'image', v?: VersionLite | null): FileLite | null {
  if (!v) return null
  if (kind === 'image') return isImg(v.file) ? v.file! : null
  const ps = (v.previews ?? []).filter(isImg)
  return ps.find((p) => /closeup/i.test(p.name)) ?? ps[0] ?? null
}

// glbOf：零件的 GLB；独立的图没有。
export const glbOf = (kind: 'part' | 'image', v?: VersionLite | null) => (kind === 'part' && v?.file ? v.file : null)

// comparePair：版本对比的两版。shown 是正在看的版本（默认最新），和它前一版比；只有一版时返回 null。
// versions 新的在前（与 Hub 一致），但这里不依赖顺序。
export function comparePair<V extends { version: number }>(versions: V[], shown?: number): { before: V; after: V } | null {
  if (versions.length < 2) return null
  const sorted = [...versions].sort((a, b) => b.version - a.version)
  const after = sorted.find((v) => v.version === shown) ?? sorted[0]
  const before = sorted.find((v) => v.version < after.version)
  if (!before) {
    // 正在看最早的一版：和它后一版比，左右仍是旧 → 新
    const next = [...sorted].reverse().find((v) => v.version > after.version)
    return next ? { before: after, after: next } : null
  }
  return { before, after }
}

// fmtSize：尺寸（米），保留三位小数。
export function fmtSize(size?: number[] | null): string {
  if (!size || !size.length) return ''
  return `${size.map((x) => x.toFixed(3)).join(' × ')} m`
}

// fmtTris：面数，有上限时写成「1496 / 1500」。
export function fmtTris(n?: number, budget?: number): string {
  if (!n) return ''
  return budget ? `${n} / ${budget}` : String(n)
}
export const overBudget = (n?: number, budget?: number) => !!n && !!budget && n > budget

export const fmtPixels = (p?: number[] | null) => (p && p.length >= 2 ? `${p[0]} × ${p[1]}` : '')

// reviewQueue（给第 4 步的审片模式用）：当前筛选下待审的素材，按图片墙顺序；同组候选按组内顺序展开。
export function reviewQueue(tiles: WallTile[]): string[] {
  const out: string[] = []
  for (const t of tiles) for (const a of t.type === 'one' ? [t.asset] : t.assets) if (a.status === 'pending') out.push(a.code)
  return out
}

// ---------- 审核（R-351 第 4 步） ----------

// reviewKey：素材审核在撤销期调度器（app/verdict.tsx 的 schedule）里的键。
export const reviewKey = (code: string) => `asset:${code}`

// pendingStatus：撤销期里结果立刻到位（docs/principles.md 第 8 条）——通过 / 返工还没生效时，图片墙和抽屉先按审核后的状态显示。
export function pendingStatus(status: AssetStatus, act?: string): AssetStatus {
  if (act === 'pass') return 'approved'
  if (act === 'back') return 'rework'
  return status
}

// canReview：这个状态能不能下这个结论（与 Hub 的 AssetCanMove 一致：制作中不能审；已通过只能改判返工，已返工只能改判通过）。
export function canReview(status: AssetStatus, verdict: 'approved' | 'rework'): boolean {
  if (status === 'pending') return true
  if (status === 'approved') return verdict === 'rework'
  if (status === 'rework') return verdict === 'approved'
  return false
}

export type Recipient = { kind: 'submitter'; name: string } | { kind: 'pm' } | { kind: 'unknown' }

// recipientOf：意见会送到哪里（审核前在判断栏旁提示；实际收件人以 Hub 为准）。提交者是会话且还没结束 → 送它；
// 已结束 → 送同项目主调度；会话不在列表里（已清理）或提交者不是会话 → 不确定，只写「会话」。
export function recipientOf(sub: { kind: string; ref?: string; name?: string } | undefined, sessions: { key: string; state: string; title?: string }[]): Recipient {
  if (sub?.kind !== 'session' || !sub.ref) return { kind: 'unknown' }
  const x = sessions.find((s) => s.key === sub.ref)
  if (!x) return { kind: 'unknown' }
  if (x.state === 'ended') return { kind: 'pm' }
  return { kind: 'submitter', name: x.title || sub.name || '' }
}

// stepQueue：审片模式里 ← / → 在队列里前后切换（不跳过已审的，回头还能看）；到头返回 null。
export function stepQueue(queue: string[], cur: string, d: 1 | -1): string | null {
  const i = queue.indexOf(cur)
  if (i < 0) return queue[0] ?? null
  const j = i + d
  return j >= 0 && j < queue.length ? queue[j] : null
}

// nextInQueue（第 4 步）：from 之后下一个仍待审的；后面没有就从头找；都审完返回 null。
export function nextInQueue(queue: string[], from: number, statusOf: (code: string) => string | undefined): number | null {
  const n = queue.length
  for (let k = 1; k <= n; k++) {
    const i = (from + k) % n
    if (i !== from && statusOf(queue[i]) === 'pending') return i
  }
  return null
}
