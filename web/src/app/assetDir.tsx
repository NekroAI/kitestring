import { useCallback, useMemo, useState, useSyncExternalStore, type ReactNode } from 'react'
import { Archive, ArrowDownUp, Boxes, ChevronRight } from 'lucide-react'
import { t } from '../i18n'
import { cx } from '../lib/util'
import { Button } from '../ui/base'
import { Menu } from '../ui/overlay'
import { ASSET_SORTS, parseSort, titleOf, dirCount, dirTree, filterAssets, inDir, ROOT_DIR, variantFamily, wallSections, wallTiles, type AssetDir, type AssetLite, type AssetSort, type DirCount, type DirNode, type WallSection, type WallTile } from '../lib/assets'

// 资源库的目录层级（R-394，D-2093 方案 C）：桌面左侧窄目录只到「类别 → 细类」两层，顶部面包屑写到素材；
// 手机上不放目录，面包屑下面一行可横滑的标签列出下一层。所有者页和分享页（只有分享范围内的素材）共用。
// URL：?c=类别、?sub=细类、?old=1 是「按批次（旧）」一栏、?ob=其中的批次；和状态、批次、文件类型筛选一起生效。
// 变体层（R-380）：?va=素材 id，图片墙列出这件素材和它的变体，各自的封面和状态；面包屑写到素材。

export function dirFromParams(p: URLSearchParams): AssetDir {
  if (p.get('old') === '1') return { ...ROOT_DIR, old: true, batch: p.get('ob') ?? '' }
  const d = { ...ROOT_DIR, cat: p.get('c') ?? '', sub: p.get('c') ? (p.get('sub') ?? '') : '' }
  const va = p.get('va')
  return va ? { ...d, asset: va } : d
}

// dirParams：进到某一层要改的 URL 参数（关掉抽屉和并排比较）。
export const dirParams = (d: AssetDir): Record<string, string | null> => ({
  c: !d.old && d.cat ? d.cat : null,
  sub: !d.old && d.cat && d.sub ? d.sub : null,
  old: d.old ? '1' : null,
  ob: d.old && d.batch ? d.batch : null,
  va: !d.old && d.asset ? d.asset : null,
  asset: null,
  cmp: null,
})

// 排序（R-410）：选择记在本机（localStorage），所有者页和分享页共用一份；读写都包在 try 里，拿不到时只在本页内存里记。
const SORT_KEY = 'ks.assets.sort'
let sortMem: AssetSort | null = null
const sortSubs = new Set<() => void>()
const readSort = (): AssetSort => {
  if (sortMem) return sortMem
  try {
    return parseSort(localStorage.getItem(SORT_KEY))
  } catch {
    return 'recent'
  }
}
export function useAssetSort(): [AssetSort, (s: AssetSort) => void] {
  const sort = useSyncExternalStore(
    (cb) => {
      sortSubs.add(cb)
      return () => sortSubs.delete(cb)
    },
    readSort,
    () => 'recent' as AssetSort,
  )
  const set = useCallback((s: AssetSort) => {
    sortMem = s
    try {
      localStorage.setItem(SORT_KEY, s)
    } catch {
      /* 拿不到本机存储：只在本页记 */
    }
    sortSubs.forEach((f) => f())
  }, [])
  return [sort, set]
}

// SortMenu：资源库工具栏的排序选项。
export function SortMenu({ value, onChange }: { value: AssetSort; onChange: (s: AssetSort) => void }) {
  return (
    <Menu
      align="start"
      trigger={
        <Button size="sm" data-asset-sort={value} icon={<ArrowDownUp className="size-4" />}>
          <span className="text-fg-3">{t.asset.sort.label}</span>
          {t.asset.sort[value]}
        </Button>
      }
      items={ASSET_SORTS.map((s) => ({ label: t.asset.sort[s], checked: s === value, onSelect: () => onChange(s) }))}
    />
  )
}

export type Section = WallSection & { tiles: WallTile[] }

// useBrowse：按目录和筛选算出图片墙。目录的件数不受状态筛选影响（待审 / 返工数照样看得到），受批次、文件类型筛选影响；
// 状态分段的数字是当前这一层里的。
export function useBrowse(all: AssetLite[], project: string, p: URLSearchParams, sort: AssetSort = 'recent') {
  const batch = p.get('b') ?? ''
  const status = p.get('st') ?? ''
  const ftype = p.get('ft') ?? ''
  const q = p.get('q') ?? ''
  const dir = dirFromParams(p)
  const key = JSON.stringify(dir)
  return useMemo(() => {
    const scoped = filterAssets(all, { project, batch, category: '', status: '', ftype, q })
    const tree = dirTree(scoped)
    const here = scoped.filter((a) => inDir(a, dir))
    const counts: Record<string, number> = {}
    for (const a of here) counts[a.status] = (counts[a.status] ?? 0) + 1
    const shown = status ? here.filter((a) => a.status === status) : here
    const sections: Section[] = wallSections(shown, dir, sort).map((s) => ({ ...s, tiles: wallTiles(s.assets, !dir.asset) }))
    // 变体层的素材本身（面包屑写它的名字）
    const family = dir.asset ? variantFamily(scoped, dir.asset)[0] : undefined
    return { dir, tree, counts, sections, here, family, tiles: sections.flatMap((s) => s.tiles), filtered: !!(batch || status || ftype || q.trim() || dir.old || dir.cat || dir.asset) }
  }, [all, project, batch, status, ftype, q, key, sort]) // eslint-disable-line react-hooks/exhaustive-deps
}
export type Browse = ReturnType<typeof useBrowse>

// sectionLabel：图片墙分组的组名；全部素材里的旧批次写明「按批次（旧）」。
export function sectionLabel(s: WallSection, d: AssetDir): string {
  if (s.kind === 'variant') return t.asset.dir.variantsOf
  if (s.kind === 'sub') return s.key || t.asset.dir.noSub
  if (s.kind === 'batch' && !d.old) return `${t.asset.dir.old}${t.common.sep}${s.key}`
  return s.key
}
// sectionDir：点组名进到那一层。
export function sectionDir(s: WallSection, d: AssetDir): AssetDir | null {
  if (s.kind === 'cat') return { ...ROOT_DIR, cat: s.key }
  if (s.kind === 'sub') return s.key ? { ...d, sub: s.key } : null
  if (s.kind === 'batch') return { ...ROOT_DIR, old: true, batch: s.key }
  return null
}

// CountPills：件数（灰）、待审（黄）、返工（红），为 0 的不显示。
export function CountPills({ c, className }: { c: DirCount; className?: string }) {
  return (
    <span className={cx('flex shrink-0 items-center gap-1 text-xs tabular', className)}>
      <span className="text-fg-3">{c.total}</span>
      {c.pending > 0 && (
        <span className="rounded bg-wait-soft px-1 font-medium text-wait" title={t.asset.dir.pending(c.pending)}>
          {c.pending}
        </span>
      )}
      {c.rework > 0 && (
        <span className="rounded bg-danger-soft px-1 font-medium text-danger" title={t.asset.dir.rework(c.rework)}>
          {c.rework}
        </span>
      )}
    </span>
  )
}

const same = (a: AssetDir, b: AssetDir) => a.old === b.old && a.cat === b.cat && a.sub === b.sub && a.batch === b.batch && (a.asset ?? '') === (b.asset ?? '')
const onlyBlank = (n: DirNode) => n.children.length === 1 && !n.children[0].key

// DirTree：桌面左侧的窄目录——全部素材、类别（选中或展开时列出细类）、「按批次（旧）」和它的批次。
function DirTree({ b, go }: { b: Browse; go: (d: AssetDir) => void }) {
  const { tree, dir } = b
  const [open, setOpen] = useState<Map<string, boolean>>(() => new Map())
  const row = (active: boolean, depth: number) =>
    cx('flex h-8 w-full min-w-0 items-center gap-1.5 rounded-lg pr-2 text-left text-[13.5px] transition', depth ? 'pl-8' : 'pl-2', active ? 'bg-accent-soft font-medium text-accent' : 'text-fg-2 hover:bg-hover hover:text-fg')
  return (
    <nav className="grid gap-0.5" aria-label={t.asset.dir.nav} data-dir-tree="">
      <button className={row(same(dir, ROOT_DIR), 0)} onClick={() => go(ROOT_DIR)} data-dir="">
        <Boxes className="size-4 shrink-0" />
        <span className="min-w-0 flex-1 truncate">{t.asset.dir.all}</span>
        <CountPills c={tree.all} />
      </button>
      {tree.cats.length > 0 && <div className="mt-2 mb-0.5 px-2 text-xs text-fg-3">{t.asset.dir.cats}</div>}
      {tree.cats.map((c) => {
        const kids = onlyBlank(c) ? [] : c.children
        const expanded = kids.length > 0 && (open.get(c.key) ?? (!dir.old && dir.cat === c.key))
        return (
          <div key={c.key} className="grid gap-0.5">
            <div className="relative">
              <button className={row(!dir.old && dir.cat === c.key && !dir.sub, 0)} onClick={() => go({ ...ROOT_DIR, cat: c.key })} data-dir={c.key}>
                <span className="size-4 shrink-0" />
                <span className="min-w-0 flex-1 truncate">{c.key}</span>
                <CountPills c={c} />
              </button>
              {kids.length > 0 && (
                <button
                  className="absolute top-1.5 left-1 flex size-5 items-center justify-center rounded text-fg-3 hover:bg-hover hover:text-fg"
                  aria-label={expanded ? t.asset.dir.collapse : t.asset.dir.expand}
                  aria-expanded={expanded}
                  onClick={() => setOpen((m) => new Map(m).set(c.key, !expanded))}
                >
                  <ChevronRight className={cx('size-3.5 transition-transform', expanded && 'rotate-90')} />
                </button>
              )}
            </div>
            {expanded &&
              kids.map((s) => (
                <button key={s.key} className={row(!dir.old && dir.cat === c.key && dir.sub === s.key && !!s.key, 1)} disabled={!s.key} onClick={() => s.key && go({ ...ROOT_DIR, cat: c.key, sub: s.key })} data-dir={`${c.key}/${s.key}`}>
                  <span className={cx('min-w-0 flex-1 truncate', !s.key && 'text-fg-3')}>{s.key || t.asset.dir.noSub}</span>
                  <CountPills c={s} />
                </button>
              ))}
          </div>
        )
      })}
      {tree.old.total > 0 && (
        <>
          <div className={cx('grid gap-0.5', tree.cats.length > 0 && 'mt-2 border-t border-line pt-2')}>
            <button className={row(dir.old && !dir.batch, 0)} onClick={() => go({ ...ROOT_DIR, old: true })} data-dir="old">
              <Archive className="size-4 shrink-0" />
              <span className="min-w-0 flex-1 truncate">{t.asset.dir.old}</span>
              <CountPills c={tree.old} />
            </button>
            {tree.old.children.map((x) => (
              <button key={x.key} className={row(dir.old && dir.batch === x.key, 1)} onClick={() => go({ ...ROOT_DIR, old: true, batch: x.key })} data-dir={`old/${x.key}`}>
                <span className="min-w-0 flex-1 truncate">{x.key}</span>
                <CountPills c={x} />
              </button>
            ))}
          </div>
        </>
      )}
    </nav>
  )
}

// Crumbs：面包屑——全部素材 › 类别 › 细类（或 按批次（旧） › 批次），打开素材时再写上素材名；右边是这一层的件数。
function Crumbs({ b, go, openName }: { b: Browse; go: (d: AssetDir) => void; openName?: string }) {
  const { dir, tree } = b
  const parts: { label: string; to: AssetDir }[] = [{ label: t.asset.dir.all, to: ROOT_DIR }]
  if (dir.old) {
    parts.push({ label: t.asset.dir.old, to: { ...ROOT_DIR, old: true } })
    if (dir.batch) parts.push({ label: dir.batch, to: dir })
  } else if (dir.cat) {
    parts.push({ label: dir.cat, to: { ...ROOT_DIR, cat: dir.cat } })
    if (dir.sub) parts.push({ label: dir.sub, to: { ...ROOT_DIR, cat: dir.cat, sub: dir.sub } })
  }
  // 变体层（R-380）：面包屑写到素材，再打开某个变体时接着写变体名
  if (dir.asset) parts.push({ label: b.family ? titleOf(b.family) : dir.asset, to: dir })
  return (
    <div className="mb-3 flex min-w-0 items-center gap-3" data-dir-crumbs="">
      <ol className="flex min-w-0 flex-1 items-center gap-1 overflow-hidden text-[14px]">
        {parts.map((p, i) => {
          const last = i === parts.length - 1 && !openName
          return (
            <li key={i} className={cx('flex min-w-0 items-center gap-1', !last && 'shrink-0')}>
              {i > 0 && <ChevronRight className="size-3.5 shrink-0 text-fg-3" />}
              {last ? (
                <span className="truncate font-semibold text-fg" aria-current="page">
                  {p.label}
                </span>
              ) : (
                <button className="truncate text-fg-2 hover:text-accent" onClick={() => go(p.to)}>
                  {p.label}
                </button>
              )}
            </li>
          )
        })}
        {openName && (
          <li className="flex min-w-0 items-center gap-1">
            <ChevronRight className="size-3.5 shrink-0 text-fg-3" />
            <span className="truncate font-semibold text-fg" aria-current="page">
              {openName}
            </span>
          </li>
        )}
      </ol>
      <CountPills c={dirCount(tree, dir, b.here)} />
    </div>
  )
}

// DirTags：手机上面包屑下面一行横滑的标签——这一层有下一层时列下一层，到底了列同级（当前的高亮）。
function DirTags({ b, go }: { b: Browse; go: (d: AssetDir) => void }) {
  const { dir, tree } = b
  let tags: { key: string; label: string; c: DirCount; to: AssetDir; icon?: ReactNode }[] = []
  if (dir.old) {
    tags = tree.old.children.map((x) => ({ key: x.key, label: x.key, c: x, to: { ...ROOT_DIR, old: true, batch: x.key } }))
  } else if (!dir.cat) {
    tags = tree.cats.map((x) => ({ key: x.key, label: x.key, c: x, to: { ...ROOT_DIR, cat: x.key } }))
    if (tree.old.total) tags.push({ key: 'old', label: t.asset.dir.old, c: tree.old, to: { ...ROOT_DIR, old: true }, icon: <Archive className="size-3.5" /> })
  } else {
    const c = tree.cats.find((x) => x.key === dir.cat)
    if (c && !onlyBlank(c)) tags = c.children.filter((x) => x.key).map((x) => ({ key: x.key, label: x.key, c: x, to: { ...ROOT_DIR, cat: dir.cat, sub: x.key } }))
  }
  if (!tags.length) return null
  return (
    <div className="-mx-4 mb-3 flex gap-2 overflow-x-auto px-4 pb-1 [scrollbar-width:none] lg:hidden" data-dir-tags="">
      {tags.map((x) => {
        const on = same(x.to, dir)
        return (
          <button
            key={x.key}
            onClick={() => go(on && !dir.old ? { ...ROOT_DIR, cat: dir.cat } : on ? { ...ROOT_DIR, old: true } : x.to)}
            aria-pressed={on}
            className={cx('flex h-8 shrink-0 items-center gap-1.5 rounded-full border px-3 text-[13px] transition active:scale-[.98]', on ? 'border-accent bg-accent-soft text-accent' : 'border-line bg-surface text-fg-2')}
          >
            {x.icon}
            {x.label}
            <CountPills c={x.c} />
          </button>
        )
      })}
    </div>
  )
}

// DirBrowser：桌面左目录 + 中间（面包屑、图片墙）+ 宽屏时右侧详情栏（detail，R-396）；手机面包屑 + 横滑标签 + 图片墙。
// height 给了时（所有者页铺满一屏）目录和图片墙各自滚动；不给时（分享页整页滚动）目录贴在顶部。
export function DirBrowser({ b, go, openName, height, detail, children }: { b: Browse; go: (d: AssetDir) => void; openName?: string; height?: number; detail?: ReactNode; children: ReactNode }) {
  return (
    <div className="lg:flex lg:gap-5" style={height ? { height } : undefined}>
      <aside
        className={cx(
          'hidden w-[248px] shrink-0 overflow-y-auto overscroll-contain rounded-2xl border border-line bg-surface p-2 lg:block',
          height ? 'h-full' : 'sticky top-4 max-h-[calc(100dvh-2rem)] self-start',
        )}
      >
        <DirTree b={b} go={go} />
      </aside>
      <div className={cx('min-w-0 flex-1', height && 'lg:-mx-2 lg:overflow-y-auto lg:overscroll-contain lg:px-2 lg:pb-4')} data-asset-wall="">
        <Crumbs b={b} go={go} openName={openName} />
        <DirTags b={b} go={go} />
        {children}
      </div>
      {detail}
    </div>
  )
}
