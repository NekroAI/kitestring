import type { ComposeItem } from './compose.ts'
// 资源库（R-351 第 3 步）的纯函数：筛选、分面、图片墙分组（同一用途的候选成组）、封面图、版本对比、审片队列。
// 界面在 app/assets.tsx；这里不碰 React，便于单测。

export type AssetStatus = 'wip' | 'pending' | 'approved' | 'rework'
export const ASSET_STATUSES: AssetStatus[] = ['wip', 'pending', 'approved', 'rework']

export type FileLite = { id: string; name: string; mime: string; size: number }
// AssetKind：part GLB 零件 / image 独立的图（含 SVG，R-387）/ audio 音效（R-381）/ font 字体（TTF / OTF / WOFF2，R-387）
export type AssetKind = 'part' | 'image' | 'audio' | 'font'
// AudioLite（R-381）：音效的时长、声道、响度等；wave 是均分若干段的峰值（0–100）；declared 是取自 asset.json 声明的字段
export type AudioLite = {
  codec?: string
  duration_s?: number
  channels?: number
  sample_rate?: number
  loudness_lufs?: number | null
  peak_dbfs?: number | null
  wave?: number[] | null
  declared?: string[] | null
}
export type PreviewLite = FileLite & { view?: string }
export type VersionLite = {
  id: string
  version: number
  batch: string
  batches?: string[] | null
  branch: string
  commit: string
  file?: FileLite | null
  previews?: PreviewLite[] | null
  triangles?: number
  budget?: number
  size?: number[] | null
  pixels?: number[] | null
  auto_thumb?: FileLite | null
  audio?: AudioLite | null
  subtype?: string
  name?: string
  variants?: unknown
  created_at: string
  compose?: ComposeItem[] | null // R-407：挂件和比例参照（只在素材详情里有）
}
// AssetRefLite（R-380）：对另一件素材的引用——变体的上级、取代关系。
export type AssetRefLite = { id: string; code: string; uid?: string; key: string; batch?: string; variant?: string; status: AssetStatus }
export type AssetLite = {
  id: string
  code: string
  project_id: string
  batch: string
  batches?: string[] | null
  key: string
  uid?: string
  kind: AssetKind
  category: string
  group?: string
  status: AssetStatus
  version: number
  updated_at: string
  latest?: VersionLite | null
  baseline?: string // R-393：推送时申请标为已在用、等你批准的决策 D-x
  // 变体与取代（R-380）：变体是单独一条素材，parent 指向素材本身；素材本身的 variant 是默认变体名
  variant?: string
  parent?: AssetRefLite | null
  superseded_by?: AssetRefLite | null
}

export type AssetFilter = { project: string; batch: string; category: string; status: string; ftype?: string; q?: string }

// fileTypeOf（R-388）：一个文件的文件类型，取扩展名大写（glb → GLB，jpeg → JPG）；没有扩展名时取 mime 的子类型。
export function fileTypeOf(f: Pick<FileLite, 'name' | 'mime'>): string {
  const m = /\.([A-Za-z0-9]+)$/.exec(f.name)
  let x = m ? m[1].toUpperCase() : ((f.mime.split('/')[1] ?? '').split(/[+;]/)[0] || '').toUpperCase()
  if (x === 'JPEG') x = 'JPG'
  return x
}

// fileTypesOf（R-388）：一件素材最新一版的文件类型，只按主文件算（模型 GLB、图片 PNG / JPG、音效 OGG / WAV）；预览图和自动缩略图不算。
export function fileTypesOf(a: Pick<AssetLite, 'latest'>): string[] {
  const f = a.latest?.file
  const x = f ? fileTypeOf(f) : ''
  return x ? [x] : []
}

// MediaKind（R-397）：文件类型的种类，标签写成「种类 · 文件类型」（模型 · GLB、图片 · PNG），按种类分色。
export type MediaKind = 'model' | 'image' | 'audio' | 'video' | 'font' | 'other'
const MEDIA_EXT: Record<Exclude<MediaKind, 'other'>, string[]> = {
  model: ['GLB', 'GLTF', 'FBX', 'OBJ', 'BLEND', 'USD', 'USDZ', 'USDA', 'USDC', 'STL', 'DAE', 'PLY', '3DS', 'VOX'],
  image: ['PNG', 'JPG', 'WEBP', 'GIF', 'SVG', 'BMP', 'TGA', 'PSD', 'EXR', 'HDR', 'KTX2', 'DDS', 'TIF', 'TIFF', 'AVIF'],
  audio: ['OGG', 'WAV', 'MP3', 'FLAC', 'M4A', 'AAC', 'OPUS'],
  video: ['MP4', 'WEBM', 'MOV', 'MKV', 'AVI', 'M4V'],
  font: ['TTF', 'OTF', 'WOFF2', 'WOFF', 'TTC'],
}

// mediaKindOf：按文件类型（扩展名）定种类；扩展名不认识时看 mime 的大类（model/ image/ audio/ video/），都不是归「其他」。
export function mediaKindOf(type: string, mime = ''): MediaKind {
  const x = type.toUpperCase()
  for (const k of ['model', 'image', 'audio', 'video', 'font'] as const) if (MEDIA_EXT[k].includes(x)) return k
  const m = mime.split('/')[0].toLowerCase()
  return m === 'model' || m === 'image' || m === 'audio' || m === 'video' || m === 'font' ? m : 'other'
}

export type FileTag = { kind: MediaKind; type: string }

// fileTagsOf（R-397）：卡片、详情上的类型标签，和 fileTypesOf 一样只按主文件算。
export function fileTagsOf(a: Pick<AssetLite, 'latest'>): FileTag[] {
  const f = a.latest?.file
  const x = f ? fileTypeOf(f) : ''
  return x ? [{ kind: mediaKindOf(x, f!.mime), type: x }] : []
}

// uniqTags：几件素材的类型标签去重（候选组的卡片用），保持第一次出现的顺序。
export function uniqTags(tags: FileTag[]): FileTag[] {
  const seen = new Set<string>()
  return tags.filter((x) => !seen.has(x.type) && !!seen.add(x.type))
}

// projectsOf：有素材的项目，最近有更新的在前（资源库页的项目切换只列这些）。
export function projectsOf(list: AssetLite[]): string[] {
  const last = new Map<string, string>()
  for (const a of list) if ((last.get(a.project_id) ?? '') < a.updated_at) last.set(a.project_id, a.updated_at)
  return [...last.entries()].sort((a, b) => b[1].localeCompare(a[1]) || a[0].localeCompare(b[0])).map(([p]) => p)
}

// batchesOf：素材带过的全部批次（R-376：按素材 id 推送的素材批次是多值；旧条目只有 batch）。
export function batchesOf(a: Pick<AssetLite, 'batch' | 'batches'>): string[] {
  if (a.batches && a.batches.length) return a.batches
  return a.batch ? [a.batch] : []
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
    batches: naturalSort(new Set(inProj.flatMap(batchesOf))),
    categories: naturalSort(new Set(inProj.map((a) => a.category).filter(Boolean))),
    fileTypes: naturalSort(new Set(inProj.flatMap(fileTypesOf))),
    counts,
    total: inProj.length,
  }
}

// CatNames（R-411）：项目配置 .kitestring/assets.json 里写的类别 / 细类中文名；没写的不在表里。
export type CatNames = Record<string, { name?: string; subtypes?: Record<string, string> }>
export const catNameOf = (n: CatNames | undefined, cat: string) => n?.[cat]?.name ?? ''
export const subNameOf = (n: CatNames | undefined, cat: string, sub: string) => (sub ? (n?.[cat]?.subtypes?.[sub] ?? '') : '')

export function filterAssets(list: AssetLite[], f: AssetFilter, names?: CatNames): AssetLite[] {
  return list.filter((a) => a.project_id === f.project && (!f.batch || batchesOf(a).includes(f.batch)) && (!f.category || a.category === f.category) && (!f.status || a.status === f.status) && (!f.ftype || fileTypesOf(a).includes(f.ftype)) && matchesQuery(a, f.q, names))
}

// matchesQuery（R-400）：关键字按素材 id、key 和中文名找，不分大小写；空关键字都算。
// R-411：传了 names 时，类别和细类的中文名也算（搜「道具」找到 prop 类下的全部素材）。
export function matchesQuery(a: Pick<AssetLite, 'uid' | 'key' | 'latest'> & { category?: string }, q?: string, names?: CatNames): boolean {
  const w = (q ?? '').trim().toLowerCase()
  const cat = a.category ?? ''
  return !w || `${a.uid ?? ''} ${a.key} ${a.latest?.name ?? ''} ${catNameOf(names, cat)} ${subNameOf(names, cat, a.latest?.subtype ?? '')}`.toLowerCase().includes(w)
}

// ---------- 目录层级（R-394，D-2093 方案 C） ----------
// 按素材 id 推送的素材按「类别 → 细类 → 素材」分层（变体先不单独成层，等 R-380 把变体拆开存文件，D-2114）；
// 旧的按批次推送的条目（没有素材 id）单独一栏「按批次（旧）」，按批次分。

// AssetDir：图片墙现在在目录的哪一层。old = 「按批次（旧）」一栏（batch 是其中的批次）；否则 cat / sub 是类别、细类，都空是全部素材；
// asset（R-380）是变体层：这件素材（素材 id）和它的变体，cat / sub 是它所在的类别、细类（面包屑用）。
export type AssetDir = { old: boolean; cat: string; sub: string; batch: string; asset?: string }
export const ROOT_DIR: AssetDir = { old: false, cat: '', sub: '', batch: '' }

export const isLegacy = (a: Pick<AssetLite, 'uid'>) => !a.uid
export const subtypeOf = (a: Pick<AssetLite, 'latest'>) => a.latest?.subtype ?? ''
// assetName：素材在目录里的名字——素材 id 去掉命名空间和类别（core:weapon.carbine → carbine）；旧条目用 key。
// 变体（R-380，素材 id 是 core:weapon.carbine@desert）写成「carbine · desert」。
export function assetName(a: Pick<AssetLite, 'uid' | 'key'> & { variant?: string; parent?: AssetRefLite | null }): string {
  if (!a.uid) return a.key
  const [id, v] = a.parent ? [a.uid.slice(0, a.uid.lastIndexOf('@')), a.variant] : [a.uid, '']
  const rest = id.slice(id.indexOf(':') + 1)
  const name = rest.slice(rest.indexOf('.') + 1) || id
  return v ? `${name} · ${v}` : name
}
// displayNameOf（R-400）：推送方给的中文名；变体沿用素材的，写成「卡宾枪 · desert」；没有写 ''。
export function displayNameOf(a: Pick<AssetLite, 'latest'> & { variant?: string; parent?: AssetRefLite | null }): string {
  const n = a.latest?.name ?? ''
  return n && a.parent && a.variant ? `${n} · ${a.variant}` : n
}
// titleOf（R-400）：素材的主标题——有中文名用中文名，没有照旧用 assetName；有中文名时素材 id 作副标题（subIdOf）。
export const titleOf = (a: Pick<AssetLite, 'uid' | 'key' | 'latest'> & { variant?: string; parent?: AssetRefLite | null }) => displayNameOf(a) || assetName(a)
export const subIdOf = (a: Pick<AssetLite, 'key' | 'latest'>) => (a.latest?.name ? a.key : '')
// familyId（R-380）：素材本身的素材 id——变体取上级的，素材本身取自己的；旧条目为空。
export const familyId = (a: Pick<AssetLite, 'uid'> & { parent?: AssetRefLite | null }) => (a.parent ? (a.parent.uid ?? '') : (a.uid ?? ''))
// variantsOf（R-380）：素材的变体条目，按变体名排。
export function variantsOf(all: AssetLite[], a: Pick<AssetLite, 'id'>): AssetLite[] {
  return all.filter((x) => x.parent?.id === a.id).sort((x, y) => natural.compare(x.variant ?? '', y.variant ?? ''))
}
// variantFamily（R-380）：一件素材和它的变体（素材本身在前），变体层和变体对比用。
export function variantFamily(all: AssetLite[], uid: string): AssetLite[] {
  const main = all.find((a) => !a.parent && a.uid === uid)
  const kids = all.filter((a) => a.parent?.uid === uid).sort((x, y) => natural.compare(x.variant ?? '', y.variant ?? ''))
  return main ? [main, ...kids] : kids
}
// variantLabel：变体在变体层、变体对比里的叫法——变体名；素材本身写默认变体名，没写时用素材名。
export const variantLabel = (a: AssetLite) => a.variant || titleOf(a)
// variantCount：素材的具名变体个数。拆开存了（R-380）就数变体条目（加上素材本身）；还没拆开的旧数据按 asset.json 的 variants 数。
export function variantCount(a: AssetLite, all?: AssetLite[]): number {
  const kids = all ? all.filter((x) => x.parent?.id === a.id).length : 0
  return kids ? kids + 1 : variantNames(a.latest?.variants).length
}
// VARIANT_CMP（R-380）：并排比较一件素材的变体时 ?cmp= 的前缀，后面是素材 id。
export const VARIANT_CMP = 'v:'

export function inDir(a: AssetLite, d: AssetDir): boolean {
  if (d.asset) return familyId(a) === d.asset
  if (d.old) return isLegacy(a) && (!d.batch || a.batch === d.batch)
  if (!d.cat) return true
  return !isLegacy(a) && a.category === d.cat && (!d.sub || subtypeOf(a) === d.sub)
}

// DirCount：一层的件数和待审、返工数。
export type DirCount = { total: number; pending: number; rework: number }
export type DirNode = DirCount & { key: string; name?: string; children: DirNode[] }
// 件数只数素材本身，变体（R-380）不另算一件；待审、返工数连变体一起算（变体各自审核）。
const tally = (c: DirCount, a: Pick<AssetLite, 'status' | 'parent'>) => {
  if (!a.parent) c.total++
  if (a.status === 'pending') c.pending++
  if (a.status === 'rework') c.rework++
}
const zero = (): DirCount => ({ total: 0, pending: 0, rework: 0 })

// dirTree：目录树。list 传项目里按批次、文件类型筛过的素材（状态不筛，待审 / 返工数照样看得到）。
// cats：按素材 id 推送的素材的类别，每个类别下是细类（没写细类的 key 为空，排在最后）；old：旧条目一栏，下面是批次。
// names（R-411）：有类别 / 细类中文名时记在节点的 name 上，同级按显示的名字排（中文名没写的用 key）。
export function dirTree(list: AssetLite[], names?: CatNames): { all: DirCount; cats: DirNode[]; old: DirNode } {
  const all = zero()
  const cats = new Map<string, DirNode>()
  const old: DirNode = { key: '', ...zero(), children: [] }
  const child = (n: DirNode, key: string) => {
    let c = n.children.find((x) => x.key === key)
    if (!c) n.children.push((c = { key, ...zero(), children: [] }))
    return c
  }
  for (const a of list) {
    tally(all, a)
    if (isLegacy(a)) {
      tally(old, a)
      tally(child(old, a.batch), a)
      continue
    }
    let c = cats.get(a.category)
    if (!c) cats.set(a.category, (c = { key: a.category, ...zero(), children: [] }))
    tally(c, a)
    tally(child(c, subtypeOf(a)), a)
  }
  const out = [...cats.values()]
  for (const c of out) {
    const cn = catNameOf(names, c.key)
    if (cn) c.name = cn
    for (const s of c.children) {
      const sn = subNameOf(names, c.key, s.key)
      if (sn) s.name = sn
    }
  }
  const shown = (n: DirNode) => n.name || n.key
  const sortKids = (n: DirNode) => n.children.sort((x, y) => (!x.key ? 1 : !y.key ? -1 : natural.compare(shown(x), shown(y)) || natural.compare(x.key, y.key)))
  out.sort((x, y) => natural.compare(shown(x), shown(y)) || natural.compare(x.key, y.key))
  out.forEach(sortKids)
  sortKids(old)
  return { all, cats: out, old }
}

// dirCount：目录里某一层的统计（面包屑、标签用）；找不到时全是 0。
export function dirCount(tree: ReturnType<typeof dirTree>, d: AssetDir, list?: AssetLite[]): DirCount {
  if (d.asset) {
    // 变体层：件数是素材本身加变体
    const c = zero()
    for (const a of list ?? []) {
      if (familyId(a) !== d.asset) continue
      c.total++
      if (a.status === 'pending') c.pending++
      if (a.status === 'rework') c.rework++
    }
    return c
  }
  if (d.old) return d.batch ? (tree.old.children.find((x) => x.key === d.batch) ?? zero()) : tree.old
  if (!d.cat) return tree.all
  const c = tree.cats.find((x) => x.key === d.cat)
  if (!c) return zero()
  return d.sub ? (c.children.find((x) => x.key === d.sub) ?? zero()) : c
}

// AssetSort（R-410）：资源库排序。recent 最近更新在前（默认）/ name 名称自然排序 / seq 推送先后（素材编号）。
export type AssetSort = 'recent' | 'name' | 'seq'
export const ASSET_SORTS: AssetSort[] = ['recent', 'name', 'seq']
export const parseSort = (v: string | null | undefined): AssetSort => (v === 'name' || v === 'seq' ? v : 'recent')

// pushedAt：素材最新一版的推送时间。不看 updated_at：它在审核改状态时也会动，而改状态不算更新。
const pushedAt = (a: Pick<AssetLite, 'latest' | 'updated_at'>): number => Date.parse(a.latest?.created_at ?? a.updated_at) || 0

// recentStamps：每件素材的「最近更新」时间；变体有新版本也算素材本身更新（取素材和它全部变体里最新的）。
export function recentStamps(list: AssetLite[]): (a: AssetLite) => number {
  const fam = new Map<string, number>()
  const key = (a: AssetLite) => (a.parent ? a.parent.id : a.id)
  for (const a of list) fam.set(key(a), Math.max(fam.get(key(a)) ?? 0, pushedAt(a)))
  return (a) => Math.max(pushedAt(a), fam.get(key(a)) ?? 0)
}

const seqOf = (a: Pick<AssetLite, 'code'>) => Number(a.code.replace(/^A-/, '')) || 0

// sortAssets：组内排序。recent 新的在前；name 有中文名用中文名、否则素材 id（旧条目用 key），自然排序；seq 按素材编号。
export function sortAssets(list: AssetLite[], sort: AssetSort, stamp: (a: AssetLite) => number = recentStamps(list)): AssetLite[] {
  const nm = (a: AssetLite) => a.latest?.name || a.uid || a.key
  const by = {
    recent: (x: AssetLite, y: AssetLite) => stamp(y) - stamp(x) || seqOf(y) - seqOf(x),
    name: (x: AssetLite, y: AssetLite) => natural.compare(nm(x), nm(y)) || seqOf(x) - seqOf(y),
    seq: (x: AssetLite, y: AssetLite) => seqOf(x) - seqOf(y),
  }[sort]
  return [...list].sort(by)
}

// WallSection：图片墙按下一层分的一组。kind 说明 key 是什么：cat 类别 / sub 细类（空 = 未分细类）/ batch 旧条目的批次 / '' 不分组。
export type WallSection = { kind: 'cat' | 'sub' | 'batch' | 'variant' | ''; key: string; name?: string; assets: AssetLite[] }

// wallSections：图片墙按目录的下一层分组——全部素材按类别（旧条目在后面按批次）、类别按细类、「按批次（旧）」按批次；
// 到了细类或某个旧批次就不再分组。只有一组且没有名字时不写组名。
// sort（R-410，不传 = 保持传入顺序）：排序作用在组内；选 recent 时组之间也按组里最新一件的时间排，其他排序下组按名字排。
export function wallSections(list: AssetLite[], d: AssetDir, sort?: AssetSort, names?: CatNames): WallSection[] {
  const stamp = recentStamps(list)
  const ordered = (xs: AssetLite[]) => (d.asset || !sort ? xs : sortAssets(xs, sort, stamp))
  // R-411：类别 / 细类组按显示的名字排（有中文名用中文名）；name 带给组名显示
  const nameFor = (kind: WallSection['kind'], key: string) => (kind === 'cat' ? catNameOf(names, key) : kind === 'sub' ? subNameOf(names, d.cat, key) : '')
  const by = (xs: AssetLite[], kind: WallSection['kind'], keyOf: (a: AssetLite) => string, emptyLast = false) => {
    const shown = (k: string) => nameFor(kind, k) || k
    const m = new Map<string, AssetLite[]>()
    for (const a of xs) m.set(keyOf(a), [...(m.get(keyOf(a)) ?? []), a])
    const top = (as: AssetLite[]) => Math.max(...as.map(stamp))
    return [...m.entries()]
      .sort(([x, xa], [y, ya]) => (emptyLast && !x ? 1 : emptyLast && !y ? -1 : sort === 'recent' ? top(ya) - top(xa) || natural.compare(shown(x), shown(y)) : natural.compare(shown(x), shown(y)) || natural.compare(x, y)))
      .map(([key, assets]) => {
        const name = nameFor(kind, key)
        return name ? { kind, key, name, assets: ordered(assets) } : { kind, key, assets: ordered(assets) }
      })
  }
  if (d.asset) {
    // 变体层（R-380）：素材本身在前，变体按名字排
    const main = list.filter((a) => !a.parent)
    const kids = list.filter((a) => a.parent).sort((x, y) => natural.compare(x.variant ?? '', y.variant ?? ''))
    return [{ kind: 'variant', key: d.asset, assets: [...main, ...kids] }]
  }
  if (d.old) return d.batch ? [{ kind: '', key: '', assets: ordered(list) }] : by(list, 'batch', (a) => a.batch)
  if (d.sub) return [{ kind: '', key: '', assets: ordered(list) }]
  if (d.cat) {
    const s = by(list, 'sub', subtypeOf, true)
    return s.length === 1 && !s[0].key ? [{ kind: '', key: '', assets: ordered(list) }] : s
  }
  return [...by(list.filter((a) => !isLegacy(a)), 'cat', (a) => a.category), ...by(list.filter(isLegacy), 'batch', (a) => a.batch)]
}

// 图片墙上的一格：单个素材，或同一用途的一组候选（概念图 A / B 版）。
// variants（R-380）：变体层以外，变体收进素材本身这一格（卡片上标「N 个变体」），审片队列照样逐个过。
export type WallTile = { type: 'one'; asset: AssetLite; variants?: AssetLite[] } | { type: 'group'; key: string; group: string; batch: string; assets: AssetLite[] }

// groupKey：候选分组只在同一项目、同一批次里成立。
export const groupKey = (a: Pick<AssetLite, 'project_id' | 'batch' | 'group'>) => `${a.batch}/${a.group ?? ''}`

// wallTiles：按列表顺序排，同组候选（两张及以上）收成一格，放在组里第一张的位置；只剩一张的照常单独一格。
export function wallTiles(all: AssetLite[], fold = true): WallTile[] {
  // 变体收进素材本身（R-380）：素材本身也在这一组里时才收，否则（比如按状态筛掉了素材本身）变体单独一格
  const here = new Set(all.map((a) => a.id))
  const kids = new Map<string, AssetLite[]>()
  if (fold) for (const a of all) if (a.parent && here.has(a.parent.id)) kids.set(a.parent.id, [...(kids.get(a.parent.id) ?? []), a])
  const list = fold ? all.filter((a) => !(a.parent && here.has(a.parent.id))) : all
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
      const vs = kids.get(a.id)
      out.push(vs ? { type: 'one', asset: a, variants: vs } : { type: 'one', asset: a })
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

// 封面能用的图：PNG / JPEG / GIF / WebP，和 SVG（R-387，只用 <img> 加载，里面的脚本不执行）。
const isImg = (f?: FileLite | null) => !!f && /^image\/(png|jpeg|gif|webp|svg\+xml)/i.test(f.mime)

// isSvg（R-387）：SVG 文件。界面上一律用 <img> 加载（图片模式不执行脚本），不内联进页面；铺在棋盘格上看得出透明处。
export const isSvg = (f?: Pick<FileLite, 'mime'> | null) => f?.mime === 'image/svg+xml'

// coverOf：图片墙和大图用哪张图。独立的图就是它自己；零件、音效优先用特写（文件名带 closeup），否则第一张预览图；都没有返回 null（音效画波形）。
export function coverOf(kind: AssetKind, v?: VersionLite | null): FileLite | null {
  if (!v) return null
  if (kind === 'image') return isImg(v.file) ? v.file! : null
  const ps = (v.previews ?? []).filter(isImg)
  return ps.find((p) => /closeup/i.test(p.name)) ?? ps[0] ?? null
}

// wallCover（R-383）：图片墙、胶片条、版本列表用的封面。有预览图用预览图；只有 GLB 的零件用自动生成的缩略图（auto = true，界面上标明）。
export function wallCover(kind: AssetKind, v?: VersionLite | null): { file: FileLite; auto: boolean } | null {
  const c = coverOf(kind, v)
  if (c) return { file: c, auto: false }
  if (kind === 'part' && v?.file && v.auto_thumb && isImg(v.auto_thumb)) return { file: v.auto_thumb, auto: true }
  return null
}

// needsAutoThumb（R-383）：这一版要不要在浏览器里把模型渲染成缩略图——零件、有 GLB、没有预览图、还没有自动缩略图。
export const needsAutoThumb = (kind: AssetKind, v?: VersionLite | null) => kind === 'part' && !!v?.file && !coverOf(kind, v) && !v.auto_thumb

// viewGroups（R-376）：预览图按机位分组，机位按第一次出现的顺序；没有机位的（旧数据）归在空机位一组。
export function viewGroups<P extends { view?: string }>(previews: P[]): { view: string; items: P[] }[] {
  const out: { view: string; items: P[] }[] = []
  for (const p of previews) {
    const v = p.view ?? ''
    const g = out.find((x) => x.view === v)
    if (g) g.items.push(p)
    else out.push({ view: v, items: [p] })
  }
  return out
}

// 纸鸢认识的通用字段（asset.json 与旧 manifest），项目字段里不再重复列出。
// budget、size 等写成对象时（里面常有项目自己的检查项）照样列出，只有数字 / 数组的已在面数、尺寸里显示。
const GENERIC = new Set(['format', 'id', 'file', 'category', 'subtype', 'previews', 'batch', 'batches', 'variants', 'default_variant', 'source', 'sources', 'license', 'group', 'triangles', 'budget_max', 'pixels', 'dimensions_px', 'audio', 'display'])
const SHOWN_UNLESS_OBJECT = new Set(['budget', 'size', 'bounds_m', 'bounds_local_m', 'dimensions_m'])
const isObj = (v: unknown) => !!v && typeof v === 'object' && !Array.isArray(v)

// metaRows（R-376）：项目特有字段，原样展示为「字段 · 值」；值不是字符串、数字、布尔时写成紧凑的 JSON。
export function metaRows(meta: unknown): [string, string][] {
  if (!meta || typeof meta !== 'object' || Array.isArray(meta)) return []
  return Object.entries(meta as Record<string, unknown>)
    .filter(([k, v]) => !GENERIC.has(k) && !(SHOWN_UNLESS_OBJECT.has(k) && !isObj(v)) && v !== null && v !== undefined && v !== '')
    .map(([k, v]) => [k, typeof v === 'string' ? v : typeof v === 'number' || typeof v === 'boolean' ? String(v) : JSON.stringify(v)])
}

// metaView（R-401）：项目字段分三类显示——单值和短列表一行一条；对象数组（部件、插槽、动作等）和对象写成小表格；
// 原始数据保留整份 meta 的格式化 JSON。表格列取各项键的并集，最多 META_COLS 列，其余见原始数据。
export const META_COLS = 8
export type MetaTable = { key: string; columns: string[]; rows: string[][]; cut: boolean }
export type MetaView = { scalars: [string, string][]; tables: MetaTable[]; raw: string }
const isPrim = (v: unknown) => v === null || ['string', 'number', 'boolean'].includes(typeof v)
const cell = (v: unknown): string => (v === null || v === undefined ? '' : isPrim(v) ? String(v) : Array.isArray(v) && v.every(isPrim) ? v.join(', ') : JSON.stringify(v))

export function metaView(meta: unknown): MetaView {
  const out: MetaView = { scalars: [], tables: [], raw: '' }
  if (!isObj(meta)) return out
  out.raw = JSON.stringify(meta, null, 2)
  for (const [k, v] of Object.entries(meta as Record<string, unknown>)) {
    if (GENERIC.has(k) || (SHOWN_UNLESS_OBJECT.has(k) && !isObj(v)) || v === null || v === undefined || v === '') continue
    if (Array.isArray(v) && v.length > 0 && v.every(isObj)) {
      const cols: string[] = []
      for (const o of v as Record<string, unknown>[]) for (const c of Object.keys(o)) if (!cols.includes(c)) cols.push(c)
      const columns = cols.slice(0, META_COLS)
      out.tables.push({ key: k, columns, rows: (v as Record<string, unknown>[]).map((o) => columns.map((c) => cell(o[c]))), cut: cols.length > META_COLS })
    } else if (isObj(v)) {
      const ents = Object.entries(v as Record<string, unknown>)
      out.tables.push({ key: k, columns: [], rows: ents.map(([a, b]) => [a, cell(b)]), cut: false })
    } else if (Array.isArray(v) && v.every(isPrim)) out.scalars.push([k, v.map(cell).join('、')])
    else out.scalars.push([k, typeof v === 'string' ? v : isPrim(v) ? String(v) : JSON.stringify(v)])
  }
  return out
}

// variantNames（R-376）：asset.json 里具名变体的名字——对象写法取键，数组写法取 name。
export function variantNames(variants: unknown): string[] {
  if (Array.isArray(variants)) return variants.map((x) => (isObj(x) ? String((x as { name?: unknown }).name ?? '') : '')).filter(Boolean)
  if (isObj(variants)) return Object.keys(variants as object)
  return []
}

// glbOf：零件的 GLB；独立的图没有。
export const glbOf = (kind: AssetKind, v?: VersionLite | null) => (kind === 'part' && v?.file ? v.file : null)

// defaultView（R-395）：详情打开时先看什么——有 GLB 的零件先看 3D，其余看大图。
// 音效（R-381）先听：详情打开时是试听。
// 字体（R-387）先看字形。
export const defaultView = (kind: AssetKind, v?: VersionLite | null): 'model' | 'image' | 'audio' | 'font' => (fontOf(kind, v) ? 'font' : audioOf(kind, v) ? 'audio' : glbOf(kind, v) ? 'model' : 'image')

// detailViews（R-395）：详情的视图切换，有 GLB 时 3D 排在最前（默认看它），其后大图、对比、3D 对比。
// 音效（R-381）：试听排最前；有封面图时才有大图、对比。
// 字体（R-387）：字形排最前；有封面图时才有大图。
export function detailViews(o: { glb: boolean; pair: boolean; pair3d: boolean; audio?: boolean; font?: boolean; image?: boolean }): ('image' | 'model' | 'audio' | 'font' | 'compare' | 'compare3d')[] {
  return [
    ...(o.font ? (['font'] as const) : []),
    ...(o.audio ? (['audio'] as const) : []),
    ...(o.glb ? (['model'] as const) : []),
    ...(!(o.audio || o.font) || o.image ? (['image'] as const) : []),
    ...(o.pair ? (['compare'] as const) : []),
    ...(o.pair3d ? (['compare3d'] as const) : []),
  ]
}

// fontOf（R-387）：字体的主文件；别的素材没有。
export const fontOf = (kind: AssetKind, v?: VersionLite | null) => (kind === 'font' && v?.file ? v.file : null)

// FONT_SIZES（R-387）：字形预览的几档字号（px），从正文到标题。
export const FONT_SIZES = [12, 16, 24, 36, 56]

// audioOf（R-381）：音效的主文件；别的素材没有。
export const audioOf = (kind: AssetKind, v?: VersionLite | null) => (kind === 'audio' && v?.file ? v.file : null)

// isDeclared（R-381）：这个音频字段是不是取自 asset.json 声明（文件里读不出）。
export const isDeclared = (a: AudioLite | null | undefined, field: string) => !!a?.declared?.includes(field)

// waveBars（R-381）：把波形重采样成 n 根柱子，按最高的一段归一（安静的音效也看得出形状），每根至少 4%。
export function waveBars(wave: number[] | null | undefined, n: number): number[] {
  if (!wave?.length || n <= 0) return []
  const top = Math.max(...wave, 1)
  return Array.from({ length: n }, (_, i) => {
    const a = Math.floor((i * wave.length) / n)
    const b = Math.max(a + 1, Math.floor(((i + 1) * wave.length) / n))
    const v = Math.max(...wave.slice(a, b))
    return Math.max(0.04, v / top)
  })
}

// 宽屏分栏（R-396）：视口不小于 SPLIT_MIN 时资源库右侧固定一栏详情，点素材直接在栏里切换；更窄时仍用抽屉。
// 1920px 起：去掉侧边栏、目录和详情栏后图片墙还能放三列以上；21:9 带鱼屏（2560 / 3440）都在分栏范围里，1440 这类普通宽度仍是抽屉。
export const SPLIT_MIN = 1920
// SPLIT_W：详情栏宽度，视口的 26%，夹在 560～720px 之间（560 和抽屉一样能放下大图和判断栏，720 起再宽只是留白）。
export const SPLIT_W = { min: 560, vw: 26, max: 720 }
export const splitWidth = (viewport: number) => Math.round(Math.min(SPLIT_W.max, Math.max(SPLIT_W.min, (viewport * SPLIT_W.vw) / 100)))
export const splitWidthCss = `clamp(${SPLIT_W.min}px, ${SPLIT_W.vw}vw, ${SPLIT_W.max}px)`

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
// 申请标为已在用的（R-393）等你批准那条决策，不逐个审，不进队列。
export function reviewQueue(tiles: WallTile[]): string[] {
  const out: string[] = []
  for (const t of tiles) for (const a of t.type === 'one' ? [t.asset, ...(t.variants ?? [])] : t.assets) if (a.status === 'pending' && !a.baseline) out.push(a.code)
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

// reviewProgress（R-366）：审片顶栏的「已审 n / 共几项」和审完后的小结——队列里已不是待审的个数，其中通过、返工各几个。
// statusOf 传撤销期里按审核后的状态（pendingStatus），结果立刻到位。
export function reviewProgress(queue: string[], statusOf: (code: string) => string | undefined) {
  let done = 0
  let approved = 0
  let rework = 0
  for (const c of queue) {
    const s = statusOf(c)
    if (!s || s === 'pending') continue
    done++
    if (s === 'approved') approved++
    if (s === 'rework') rework++
  }
  return { done, approved, rework }
}
