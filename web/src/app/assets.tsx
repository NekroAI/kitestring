import { createContext, lazy, Suspense, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { useSearchParams } from 'react-router'
import { useQueryClient } from '@tanstack/react-query'
import { Box as BoxIcon, Check, ChevronDown, ChevronLeft, Columns2, Image as ImageIcon, Images, ListChecks, Maximize2, SplitSquareHorizontal, X } from 'lucide-react'
import { api, unwrap } from '../api/client'
import { useAsset, useAssets, useProjects } from '../api/queries'
import { t } from '../i18n'
import { cx, relTime, useDesktop, useNow } from '../lib/util'
import { batchesOf, candidateLabel, comparePair, metaRows, variantNames, viewGroups, coverOf, facetsOf, fileTypesOf, filterAssets, fmtPixels, fmtSize, fmtTris, glbOf, groupMembers, groupStatus, needsAutoThumb, nextInQueue, overBudget, pendingStatus, projectsOf, reviewKey, reviewProgress, reviewQueue, stepQueue, wallCover, wallTiles, type AssetLite, type AssetStatus, type FileLite, type VersionLite, type WallTile } from '../lib/assets'
import { Badge, Button, Card, Empty, ErrorState, Segmented, Skeleton, Tip } from '../ui/base'
import { Menu, Panel } from '../ui/overlay'
import { fileUrl, Lightbox, ThumbImg, Thumbs, type ViewShot } from '../ui/attach'
import { ReviewBar, ReviewHistory, ReviewNav } from './assetReview'
import { KeyCaps } from './command'
import { useFillHeight } from './pool'
import { AnimBar, FrameCtx, type FrameCtxValue, type FrameMark } from './animBar'
import { createAnimSync, type AnimSync, type Frame } from '../lib/anim'
import { useAdvance, usePending, usePendingMap } from './verdict'

// 资源库页（R-351 第 3 步，D-1713 方案 B 的图片墙部分）：按项目浏览会话推送的素材。
// 图片墙按批次 / 类别 / 状态筛选；点一格在右侧抽屉里看大图、3D（three.js 按需加载）、版本对比；
// 同一用途的候选（概念图 A / B 版）在墙上收成一格，点开并排比较。
// 审核（第 4 步）：抽屉底部是通过 / 返工的判断栏（app/assetReview.tsx），页头「审片」把当前筛选下的待审素材排成队列逐个过：
// ← → 切换、P 通过、R 返工；下了判断停在这一件，3 秒撤销期走完才翻到下一个待审的（docs/principles.md 第 8 条）。
//
// URL：?p=项目 ID、?b=批次、?c=类别、?st=状态，?asset=A-x 打开素材抽屉，?cmp=批次/组名 打开并排比较，?rv=1 审片模式。

const Viewer3D = lazy(() => import('./viewer3d'))

// AssetExtras：抽屉的数据来源和底栏。所有者用 /api/assets 和判断栏；访客页（R-365）换成 /api/share/<token>/assets，
// 底栏换成访客的留言、上传、下载，不显示审核记录。
export type AssetExtras = { useDetail: typeof useAsset; footer: (a: Detail, shown: number) => ReactNode }
export const AssetExtrasContext = createContext<AssetExtras | null>(null)

const tone: Record<AssetStatus, 'neutral' | 'wait' | 'run' | 'danger'> = { wip: 'neutral', pending: 'wait', approved: 'run', rework: 'danger' }
// AssetBadge：素材状态；传了 code 时撤销期里先按审核后的状态显示（结果立刻到位）。
export function AssetBadge({ status, code, className }: { status: AssetStatus; code?: string; className?: string }) {
  const e = usePending(code ? reviewKey(code) : undefined)
  const st = pendingStatus(status, e?.act)
  return (
    <Badge tone={tone[st]} className={className}>
      {t.asset.status[st]}
    </Badge>
  )
}

// Review：审片模式。queue 是进入时冻结的待审队列（之后审过的留在原位，← → 还能翻回去看）。
// exit(true) 退出后回到图片墙（桌面的审片舞台）；不带参数时留在正在看的这一件（手机的抽屉）。
type Review = { queue: string[]; finished: boolean; go: (code: string) => void; advance: () => void; exit: (toWall?: boolean) => void }

function useAssetParams() {
  const [params, setParams] = useSearchParams()
  const set = (kv: Record<string, string | null>) =>
    setParams((p) => {
      const n = new URLSearchParams(p)
      for (const [k, v] of Object.entries(kv)) {
        if (v) n.set(k, v)
        else n.delete(k)
      }
      return n
    })
  return { params, set }
}

export function AssetsPage() {
  const list = useAssets()
  const projects = useProjects()
  const desktop = useDesktop()
  const { params, set } = useAssetParams()
  const all = useMemo(() => (list.data?.assets ?? []) as AssetLite[], [list.data])
  const projIds = useMemo(() => projectsOf(all), [all])
  const project = projIds.includes(params.get('p') ?? '') ? params.get('p')! : (projIds[0] ?? '')
  const batch = params.get('b') ?? ''
  const category = params.get('c') ?? ''
  const status = params.get('st') ?? ''
  const ftype = params.get('ft') ?? ''
  const facets = useMemo(() => facetsOf(all, project), [all, project])
  const shown = useMemo(() => filterAssets(all, { project, batch, category, status, ftype }), [all, project, batch, category, status, ftype])
  const tiles = useMemo(() => wallTiles(shown), [shown])
  const nameOf = (id: string) => (projects.data?.projects ?? []).find((p) => p.id === id)?.name ?? id
  const filtered = !!(batch || category || status || ftype)
  const review = useReviewMode(all, tiles, params.get('rv') === '1', params.get('asset'), set)
  const pendingN = reviewQueue(tiles).length
  // 桌面上审片是全宽的舞台（R-366，D-1713 方案 B）；手机上仍是底部抽屉里逐个翻
  const stage = review.on && desktop

  return (
    <>
      <div className="mb-4 flex flex-wrap items-center gap-x-3 gap-y-2 lg:mb-5">
        <h1 className="font-display text-[24px] font-semibold tracking-wide text-fg lg:text-[28px]">{t.asset.title}</h1>
        {projIds.length > 0 && (
          <Menu
            align="start"
            trigger={
              <Button size="sm" variant="ghost" className="flex-row-reverse text-fg-2" icon={<ChevronDown className="size-4" />}>
                {nameOf(project)}
              </Button>
            }
            items={projIds.map((p) => ({ label: nameOf(p), checked: p === project, onSelect: () => set({ p, b: null, c: null, ft: null, st: null, asset: null, cmp: null }) }))}
          />
        )}
        {project && (
          <div className="flex flex-wrap items-center gap-2 max-lg:order-last max-lg:w-full lg:ml-2">
            <Segmented
              size="sm"
              value={status || 'all'}
              onChange={(v) => set({ st: v === 'all' ? null : v })}
              options={[{ value: 'all', label: t.asset.all }, ...(list.data?.statuses ?? []).map((s) => ({ value: s, label: t.asset.status[s] ?? s, count: facets.counts[s] }))]}
            />
            <FacetMenu label={t.asset.batch} allLabel={t.asset.allBatches} value={batch} options={facets.batches} onChange={(v) => set({ b: v || null })} />
            <FacetMenu label={t.asset.category} allLabel={t.asset.allCategories} value={category} options={facets.categories} onChange={(v) => set({ c: v || null })} />
            <FacetMenu label={t.asset.fileType} allLabel={t.asset.allFileTypes} value={ftype} options={facets.fileTypes} onChange={(v) => set({ ft: v || null })} />
          </div>
        )}
        <span className="flex-1" />
        {project && <span className="text-[13px] text-fg-3 tabular">{t.asset.total(facets.total)}</span>}
        {project && (
          <Tip content={pendingN ? t.asset.review.modeTip(pendingN) : t.asset.review.noQueue}>
            <span>
              <Button size="sm" variant={pendingN ? 'primary' : 'secondary'} disabled={!pendingN} icon={<ListChecks className="size-4" />} onClick={review.start} data-review-start="">
                {t.asset.review.mode}
                {pendingN > 0 && <span className="tabular">{pendingN}</span>}
              </Button>
            </span>
          </Tip>
        )}
      </div>

      <WallArea>
        {list.isPending ? (
          <WallGrid>
            {Array.from({ length: 12 }, (_, i) => (
              <Skeleton key={i} className="h-[232px] rounded-2xl" />
            ))}
          </WallGrid>
        ) : list.isError ? (
          <ErrorState error={list.error} onRetry={() => list.refetch()} />
        ) : !project ? (
          <Card>
            <Empty icon={<Images />} title={t.asset.empty}>
              {t.asset.emptyHint}
            </Empty>
          </Card>
        ) : !tiles.length ? (
          <Card>
            <Empty
              icon={<Images />}
              title={t.asset.noMatch}
              action={
                filtered && (
                  <Button size="sm" onClick={() => set({ b: null, c: null, ft: null, st: null })}>
                    {t.asset.clearFilter}
                  </Button>
                )
              }
            />
          </Card>
        ) : (
          <AssetWall tiles={tiles} active={params.get('asset')} activeGroup={params.get('cmp')} onOpen={(code) => set({ asset: code, cmp: null })} onGroup={(k) => set({ cmp: k, asset: null })} />
        )}
      </WallArea>

      {stage && <ReviewStage review={review} code={params.get('asset')} all={all} />}
      <AssetDrawer code={stage ? null : params.get('asset')} review={review.on ? review : undefined} onClose={() => set({ asset: null, rv: null })} />
      <CompareDrawer gkey={params.get('cmp')} all={all} onClose={() => set({ cmp: null })} onOpen={(code) => set({ asset: code, cmp: null })} />
    </>
  )
}

// WallArea：桌面上图片墙铺满页面剩下的高度、在区域里滚动，页头和筛选不动（R-383，和工作台一样一屏）；手机上整页照常滚动。
function WallArea({ children }: { children: ReactNode }) {
  const desktop = useDesktop()
  const [el, setEl] = useState<HTMLDivElement | null>(null)
  const h = useFillHeight(el, desktop)
  return (
    <div ref={setEl} style={desktop && h ? { height: h } : undefined} className="lg:-mx-2 lg:overflow-y-auto lg:overscroll-contain lg:px-2 lg:pb-4" data-asset-wall="">
      {children}
    </div>
  )
}

// 图片墙的格子：每格固定高度（设计原则第 3 条），列数随宽度增加，宽屏上一行放得更多。
export function WallGrid({ children }: { children: ReactNode }) {
  return <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-[repeat(auto-fill,minmax(200px,1fr))] lg:gap-3.5">{children}</div>
}

// AssetWall：图片墙（所有者页和访客页共用，R-365）。
export function AssetWall({ tiles, active, activeGroup, onOpen, onGroup }: { tiles: WallTile[]; active: string | null; activeGroup: string | null; onOpen: (code: string) => void; onGroup: (key: string) => void }) {
  return (
    <WallGrid>
      {tiles.map((tile) =>
        tile.type === 'one' ? (
          <AssetCard key={tile.asset.id} a={tile.asset} active={active === tile.asset.code} onOpen={() => onOpen(tile.asset.code)} />
        ) : (
          <GroupCard key={tile.key} tile={tile} active={activeGroup === tile.key} onOpen={() => onGroup(tile.key)} />
        ),
      )}
    </WallGrid>
  )
}

// useReviewMode：审片模式。进入时把当前筛选下的待审素材排成队列（图片墙顺序，候选组展开）；
// 下了判断后由抽屉里的 useAdvance 在撤销期走完时翻到下一个仍待审的，都审完就停在这里写「已全部审完」。
function useReviewMode(all: AssetLite[], tiles: WallTile[], on: boolean, code: string | null, set: (kv: Record<string, string | null>) => void) {
  const pend = usePendingMap()
  const [queue, setQueue] = useState<string[]>([])
  const [finished, setFinished] = useState(false)
  // 带着 ?rv=1 刷新回来：按当前筛选重新排队，正在看的那个排在最前
  useEffect(() => {
    if (!on || queue.length || !all.length) return
    const q = reviewQueue(tiles)
    if (code && !q.includes(code)) q.unshift(code)
    if (q.length) setQueue(q)
  }, [on, all.length]) // eslint-disable-line react-hooks/exhaustive-deps
  const statusOf = (c: string) => {
    const a = all.find((x) => x.code === c)
    return a ? pendingStatus(a.status, pend.get(reviewKey(c))?.act) : undefined
  }
  const go = (c: string) => {
    setFinished(false)
    set({ asset: c, cmp: null })
  }
  return {
    on: on && queue.length > 0,
    queue,
    finished,
    go,
    start: () => {
      const q = reviewQueue(tiles)
      if (!q.length) return
      setQueue(q)
      setFinished(false)
      set({ asset: q[0], rv: '1', cmp: null })
    },
    advance: () => {
      const i = code ? queue.indexOf(code) : -1
      const j = nextInQueue(queue, Math.max(0, i), statusOf)
      if (j === null) setFinished(true)
      else go(queue[j])
    },
    exit: (toWall?: boolean) => {
      setQueue([])
      setFinished(false)
      set(toWall ? { rv: null, asset: null } : { rv: null })
    },
  }
}

export function FacetMenu({ label, allLabel, value, options, onChange }: { label: string; allLabel: string; value: string; options: string[]; onChange: (v: string) => void }) {
  if (options.length < 2 && !value) return null
  return (
    <Menu
      align="start"
      trigger={
        <Button size="sm" className={cx(value && 'border-accent/40 text-accent')}>
          <span className="text-fg-3">{label}</span>
          {value || t.asset.all}
          <ChevronDown className="size-4 text-fg-3" />
        </Button>
      }
      items={[{ label: allLabel, checked: !value, onSelect: () => onChange('') }, ...options.map((o) => ({ label: o, checked: o === value, onSelect: () => onChange(o) }))]}
    />
  )
}

// 图片墙的一格：高度固定（设计原则第 3 条），名字截断。
const cardCls = (active: boolean) =>
  cx(
    'group relative flex h-[232px] flex-col overflow-hidden rounded-2xl border bg-surface text-left shadow-card transition hover:border-line-strong active:scale-[.99]',
    active ? 'border-accent ring-2 ring-accent/30' : 'border-line',
  )
const metaLine = (...xs: (string | number | undefined)[]) => xs.filter((x) => x !== undefined && x !== '').join(t.common.sep)

// ---------- 封面（R-383：只有 GLB 的零件自动生成缩略图） ----------

// 浏览器里渲染好的缩略图（素材编号:版本 → 本页的图片地址），同一页里翻来翻去不重复渲染；所有者页渲染完回传 Hub，之后列表里直接带上。
const localThumbs = new Map<string, string>()
const thumbJobs = new Map<string, Promise<string | null>>()

async function saveAutoThumb(code: string, version: number, blob: Blob) {
  const fd = new FormData()
  fd.append('file', blob, `${code}-v${version}-auto.png`)
  const r = await fetch('/api/files', { method: 'POST', body: fd, credentials: 'same-origin' })
  if (!r.ok) throw new Error(String(r.status))
  const f = (await r.json()) as { id: string }
  await unwrap(api.POST('/api/assets/{ref}/auto-thumb', { params: { path: { ref: code } }, body: { version, file: f.id } }))
}

function autoThumb(k: string, glb: string, save: { code: string; version: number } | null): Promise<string | null> {
  let job = thumbJobs.get(k)
  if (!job) {
    job = import('./autoThumb')
      .then((m) => m.renderGlbThumb(fileUrl(glb)))
      .then(async (blob) => {
        const url = URL.createObjectURL(blob)
        localThumbs.set(k, url)
        if (save) await saveAutoThumb(save.code, save.version, blob).catch(() => undefined) // 回传失败下次打开再渲染，不打扰
        return url
      })
      .catch(() => null)
    thumbJobs.set(k, job)
  }
  return job
}

type CoverSrc = { id?: string; url?: string; auto: boolean } | null

// useCover：图片墙这一格用哪张图。有预览图用预览图；只有 GLB 的零件用 Hub 存的自动缩略图，还没有时滚到眼前才在浏览器里渲染。
// 访客页（R-365）只在本页渲染、不回传。
function useCover(a: Pick<AssetLite, 'code' | 'kind'>, v: VersionLite | null | undefined, el: HTMLElement | null): CoverSrc {
  const ex = useContext(AssetExtrasContext)
  const qc = useQueryClient()
  const c = wallCover(a.kind, v)
  const need = needsAutoThumb(a.kind, v)
  const k = `${a.code}:${v?.version ?? 0}`
  const [url, setUrl] = useState<string | null>(() => localThumbs.get(k) ?? null)
  const glb = v?.file?.id ?? ''
  const ver = v?.version ?? 0
  useEffect(() => {
    if (!need || !el || !glb) return
    const ready = localThumbs.get(k)
    if (ready) return setUrl(ready)
    let alive = true
    const io = new IntersectionObserver(
      (es) => {
        if (!es.some((e) => e.isIntersecting)) return
        io.disconnect()
        void autoThumb(k, glb, ex ? null : { code: a.code, version: ver }).then((u) => {
          if (!alive || !u) return
          setUrl(u)
          if (!ex) void qc.invalidateQueries({ queryKey: ['assets'] })
        })
      },
      { rootMargin: '240px' },
    )
    io.observe(el)
    return () => {
      alive = false
      io.disconnect()
    }
  }, [need, el, k, glb]) // eslint-disable-line react-hooks/exhaustive-deps
  if (c) return { id: c.file.id, auto: c.auto }
  if (need && url) return { url, auto: true }
  return null
}

// AssetCover：一张封面，自动生成的在角上标明。tag = false 时不标（胶片条这类很小的格子只在悬停说明里写）。
export function AssetCover({ a, v, className, tag = true }: { a: Pick<AssetLite, 'code' | 'kind'>; v?: VersionLite | null; className?: string; tag?: boolean }) {
  const [el, setEl] = useState<HTMLDivElement | null>(null)
  const c = useCover(a, v, el)
  return (
    <div ref={setEl} className={cx('relative overflow-hidden', c?.auto ? 'bg-[radial-gradient(ellipse_at_50%_40%,var(--color-surface-2),var(--color-sunken))]' : 'bg-sunken', className)} title={c?.auto ? t.asset.autoThumbTip : undefined} data-auto-thumb={c?.auto ? (c.id ? 'saved' : 'local') : undefined}>
      {!c ? (
        <div className="flex h-full w-full items-center justify-center text-fg-3">
          <BoxIcon className="size-8" strokeWidth={1.4} />
        </div>
      ) : c.id ? (
        <ThumbImg id={c.id} alt="" loading="lazy" draggable={false} className={cx('h-full w-full', c.auto ? 'object-contain p-2' : 'object-cover')} />
      ) : (
        <img src={c.url} alt="" draggable={false} className="h-full w-full object-contain p-2" />
      )}
      {c?.auto && tag && <span className="pointer-events-none absolute right-1.5 bottom-1.5 rounded bg-black/45 px-1.5 text-[11px] leading-[18px] text-white">{t.asset.autoThumb}</span>}
    </div>
  )
}

// FileTypeTags（R-388）：卡片上的文件类型小标签，一件素材有几种文件就列几个。
function FileTypeTags({ types }: { types: string[] }) {
  if (!types.length) return null
  return (
    <div className="mt-1 flex gap-1 overflow-hidden" data-file-types="">
      {types.map((x) => (
        <span key={x} className="rounded bg-sunken px-1.5 font-mono text-[11px] leading-[16px] text-fg-2">
          {x}
        </span>
      ))}
    </div>
  )
}

export function AssetCard({ a, active, onOpen }: { a: AssetLite; active: boolean; onOpen: () => void }) {
  return (
    <button onClick={onOpen} className={cardCls(active)} data-asset={a.code}>
      <AssetCover a={a} v={a.latest} className="h-[164px] w-full shrink-0" />
      <AssetBadge status={a.status} code={a.code} className="absolute top-2 left-2 shadow-xs" />
      <div className="flex min-h-0 flex-1 flex-col justify-center px-3">
        <div className="truncate text-sm font-medium text-fg" title={a.key}>
          {a.key}
        </div>
        <div className="mt-0.5 truncate text-xs text-fg-3">{metaLine(batchesOf(a).join(t.asset.listSep), a.category, t.asset.ver(a.version))}</div>
        <FileTypeTags types={fileTypesOf(a)} />
      </div>
    </button>
  )
}

export function GroupCard({ tile, active, onOpen }: { tile: Extract<WallTile, { type: 'group' }>; active: boolean; onOpen: () => void }) {
  const shown = tile.assets.slice(0, 3)
  const pend = usePendingMap()
  const st = groupStatus(tile.assets.map((a) => ({ status: pendingStatus(a.status, pend.get(reviewKey(a.code))?.act) })))
  return (
    <button onClick={onOpen} className={cardCls(active)} data-group={tile.key}>
      <div className="flex h-[164px] w-full shrink-0 gap-px bg-line">
        {shown.map((a) => (
          <div key={a.id} className="relative min-w-0 flex-1">
            <AssetCover a={a} v={a.latest} tag={false} className="h-full w-full" />
            <span className="absolute bottom-1.5 left-1.5 rounded bg-black/55 px-1.5 text-[11px] font-semibold text-white">{candidateLabel(a)}</span>
          </div>
        ))}
      </div>
      <AssetBadge status={st} className="absolute top-2 left-2 shadow-xs" />
      <Columns2 className="absolute top-2 right-2 size-6 rounded-md bg-black/45 p-1 text-white" />
      <div className="flex min-h-0 flex-1 flex-col justify-center px-3">
        <div className="truncate text-sm font-medium text-fg" title={tile.group}>
          {tile.group}
        </div>
        <div className="mt-0.5 truncate text-xs text-fg-3">{metaLine(tile.batch, tile.assets[0].category, t.asset.candidates(tile.assets.map(candidateLabel)))}</div>
        <FileTypeTags types={[...new Set(tile.assets.flatMap(fileTypesOf))]} />
      </div>
    </button>
  )
}

// ---------- 素材抽屉 ----------

type View = 'image' | 'model' | 'compare' | 'compare3d'

const inField = (el: HTMLElement) => !!el.closest?.('input, textarea, [contenteditable=true], [role=menu]')
// 有别的弹层（看大图、别的抽屉）盖在上面时不拦截按键
const covered = (shell: HTMLElement | null) => [...document.querySelectorAll('[data-ks-modal]')].some((m) => !shell || !m.contains(shell))

// useReviewNav：审片时 ← → 在队列里切换（输入时、看大图时不拦截）；下了判断停在这一件，撤销期走完盖印后翻到下一个待审的，
// 撤销期内撤回不翻，自己翻走则取消这次自动跳转。手机抽屉和桌面舞台共用。
function useReviewNav(code: string | null, review: Review | undefined, shell: React.RefObject<HTMLDivElement | null>) {
  useAdvance(code ? reviewKey(code) : undefined, !!review && !review.finished, () => review?.advance(), () => code && review?.go(code))
  const prev = review && code ? stepQueue(review.queue, code, -1) : null
  const next = review && code ? stepQueue(review.queue, code, 1) : null
  const nav = useRef({ prev, next, go: review?.go })
  nav.current = { prev, next, go: review?.go }
  useEffect(() => {
    if (!review) return
    const onKey = (e: KeyboardEvent) => {
      if (e.defaultPrevented || e.metaKey || e.ctrlKey || e.altKey || e.isComposing) return
      if (inField(e.target as HTMLElement) || covered(shell.current)) return
      const to = e.key === 'ArrowLeft' ? nav.current.prev : e.key === 'ArrowRight' ? nav.current.next : null
      if (to) {
        e.preventDefault()
        nav.current.go?.(to)
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [!!review]) // eslint-disable-line react-hooks/exhaustive-deps
  return { prev, next, pos: review && code ? review.queue.indexOf(code) + 1 : 0 }
}

export function AssetDrawer({ code, review, onClose }: { code: string | null; review?: Review; onClose: () => void }) {
  const ex = useContext(AssetExtrasContext)
  const q = (ex?.useDetail ?? useAsset)(code)
  const a = q.data
  const shell = useRef<HTMLDivElement>(null)
  const { prev, next, pos } = useReviewNav(code, review, shell)
  return (
    <Panel open={!!code} onClose={onClose} wide bare title={a?.key ?? t.asset.title}>
      <div ref={shell} className="flex min-h-0 flex-1 flex-col">
        {review && (
          <ReviewNav
            pos={pos}
            total={review.queue.length}
            prev={!!prev}
            next={!!next}
            finished={review.finished}
            onPrev={() => prev && review.go(prev)}
            onNext={() => next && review.go(next)}
            onExit={() => review.exit()}
          />
        )}
        {q.isError ? (
          <div className="p-6">
            <ErrorState error={q.error} />
          </div>
        ) : !a ? (
          <div className="space-y-3 px-5 py-5">
            <Skeleton className="h-6 w-1/2" />
            <Skeleton className="aspect-[4/3] w-full rounded-xl" />
            <Skeleton className="h-24 w-full" />
          </div>
        ) : (
          <AssetBody key={a.code} a={a} onClose={onClose} keys={!!review} shell={shell} />
        )}
      </div>
    </Panel>
  )
}

// ---------- 审片舞台（R-366，D-1713 方案 B） ----------

// ReviewStage：桌面上的审片——顶栏（返回、进度、按键）、全宽大图 / 3D 舞台、右侧固定的审核栏、底部待审胶片条。
// 盖住侧边栏右边的整块区域，侧边导航保留；Esc 或「资源库」回到图片墙。
function ReviewStage({ review, code, all }: { review: Review; code: string | null; all: AssetLite[] }) {
  const q = useAsset(code)
  const a = q.data
  const shell = useRef<HTMLDivElement>(null)
  const { prev, next } = useReviewNav(code, review, shell)
  const pend = usePendingMap()
  const R = t.asset.review
  const byCode = useMemo(() => new Map(all.map((x) => [x.code, x])), [all])
  const statusOf = (c: string) => {
    const x = byCode.get(c)
    return x ? pendingStatus(x.status, pend.get(reviewKey(c))?.act) : undefined
  }
  const prog = reviewProgress(review.queue, statusOf)
  const exit = useRef(review.exit)
  exit.current = review.exit
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== 'Escape' || e.defaultPrevented || inField(e.target as HTMLElement) || covered(shell.current)) return
      e.preventDefault()
      exit.current(true)
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])
  const film = useRef<HTMLDivElement>(null)
  useEffect(() => {
    film.current?.querySelector('[aria-current=true]')?.scrollIntoView({ inline: 'center', block: 'nearest', behavior: 'smooth' })
  }, [code])
  const navBtn =
    'flex size-8 items-center justify-center rounded-lg text-fg-2 transition hover:bg-hover hover:text-fg active:scale-95 disabled:pointer-events-none disabled:opacity-30'
  return (
    <div ref={shell} className="fixed inset-y-0 right-0 left-[var(--sidebar-w,232px)] z-30 flex flex-col bg-bg transition-[left] duration-200 ease-out" data-review-stage="">
      <div className="flex shrink-0 items-center gap-3 border-b border-line bg-surface-2 px-5 py-2.5">
        <Button size="sm" variant="ghost" icon={<ChevronLeft className="size-4" />} onClick={() => review.exit(true)}>
          {t.asset.title}
        </Button>
        <b className="font-display text-[17px] font-semibold tracking-wide text-fg">{R.mode}</b>
        <span className="h-1.5 w-40 overflow-hidden rounded-full bg-sunken" aria-hidden>
          <i className="block h-full rounded-full bg-run transition-[width] duration-200" style={{ width: `${review.queue.length ? (prog.done / review.queue.length) * 100 : 0}%` }} />
        </span>
        <span className="text-[13px] text-fg-3 tabular" data-review-progress="">
          {R.reviewed(prog.done, review.queue.length)}
        </span>
        <span className="flex-1" />
        <span className="flex items-center gap-3 text-xs text-fg-3">
          <span className="flex items-center gap-1">
            <KeyCaps keys={['←', '→']} />
            {R.keySwitch}
          </span>
          <span className="flex items-center gap-1">
            <KeyCaps keys={['P']} />
            {t.verdict.pass('asset')}
          </span>
          <span className="flex items-center gap-1">
            <KeyCaps keys={['R']} />
            {t.verdict.back('asset')}
          </span>
          <span className="flex items-center gap-1">
            <KeyCaps keys={['Esc']} />
            {R.keyExit}
          </span>
        </span>
        <span className="flex items-center">
          <button className={navBtn} disabled={!prev} aria-label={R.prev} onClick={() => prev && review.go(prev)}>
            <ChevronLeft className="size-[18px]" />
          </button>
          <button className={cx(navBtn, 'rotate-180')} disabled={!next} aria-label={R.next} onClick={() => next && review.go(next)}>
            <ChevronLeft className="size-[18px]" />
          </button>
        </span>
      </div>

      <div className="flex min-h-0 flex-1">
        {review.finished ? (
          <div className="flex flex-1 flex-col items-center justify-center gap-2" data-review-done="">
            <span className="flex size-12 items-center justify-center rounded-full bg-run-soft text-run">
              <Check className="size-6" />
            </span>
            <h2 className="mt-2 font-display text-[22px] font-semibold text-fg">{R.allDone}</h2>
            <p className="mb-3 text-sm text-fg-3">{R.summary(review.queue.length, prog.approved, prog.rework)}</p>
            <Button variant="primary" onClick={() => review.exit(true)}>
              {R.backToWall}
            </Button>
          </div>
        ) : q.isError ? (
          <div className="flex-1 p-6">
            <ErrorState error={q.error} />
          </div>
        ) : !a ? (
          <div className="flex min-w-0 flex-1 gap-0">
            <div className="flex-1 p-5">
              <Skeleton className="h-full w-full rounded-xl" />
            </div>
            <div className="w-[360px] space-y-3 border-l border-line p-5">
              <Skeleton className="h-6 w-1/2" />
              <Skeleton className="h-40 w-full" />
            </div>
          </div>
        ) : (
          <AssetBody key={a.code} a={a} onClose={() => review.exit(true)} keys shell={shell} layout="stage" pos={R.position(review.queue.indexOf(a.code) + 1, review.queue.length)} />
        )}
      </div>

      <div ref={film} className="flex h-[112px] shrink-0 gap-2.5 overflow-x-auto border-t border-line bg-surface-2 px-5 py-3" aria-label={R.queue} data-film="">
        {review.queue.map((c, i) => {
          const x = byCode.get(c)
          const cur = c === code && !review.finished
          const st = statusOf(c)
          return (
            <button
              key={c}
              onClick={() => review.go(c)}
              aria-current={cur}
              title={x?.key ?? c}
              className={cx('relative h-[86px] w-[132px] shrink-0 overflow-hidden rounded-lg border-2 bg-sunken transition', cur ? 'border-accent ring-3 ring-accent/30' : 'border-transparent hover:border-line-strong')}
              data-film-item={c}
            >
              {x && <AssetCover a={x} v={x.latest} tag={false} className="h-full w-full" />}
              <span className="absolute top-1 left-1 rounded bg-black/60 px-1 text-[10px] font-semibold text-white tabular">{i + 1}</span>
              {st && <AssetBadge status={st as AssetStatus} className="absolute bottom-1 left-1 h-[17px] px-1.5 text-[10px] shadow-xs" />}
            </button>
          )
        })}
      </div>
    </div>
  )
}

export type Detail = NonNullable<ReturnType<typeof useAsset>['data']>
type Ver = Detail['versions'][number]

// AssetBody：一条素材的详情。layout = drawer（抽屉：大图在上、信息在下，一起滚动）/ stage（审片舞台：大图占满左边，信息和审核栏固定在右侧）。
function AssetBody({ a, onClose, keys, shell, layout = 'drawer', pos }: { a: Detail; onClose: () => void; keys: boolean; shell: React.RefObject<HTMLDivElement | null>; layout?: 'drawer' | 'stage'; pos?: string }) {
  const ex = useContext(AssetExtrasContext)
  const now = useNow()
  const versions = a.versions
  const [shownN, setShownN] = useState(a.version)
  const v = versions.find((x) => x.version === shownN) ?? versions[0]
  const pair = comparePair(versions.filter((x) => coverOf(a.kind, x)), shownN)
  const glb = glbOf(a.kind, v)
  // 3D 对比（R-392）：两版都有 GLB 时并排看，动作同步播放
  const pair3d = comparePair(versions.filter((x) => glbOf(a.kind, x)), shownN)
  const views: View[] = ['image', ...(glb ? (['model'] as View[]) : []), ...(pair ? (['compare'] as View[]) : []), ...(pair3d ? (['compare3d'] as View[]) : [])]
  // 没有预览图的零件（例如访客交的新版本，R-365）打开时直接看 3D
  const [view, setView] = useState<View>(() => (glb && !coverOf(a.kind, v) ? 'model' : 'image'))
  const cur = views.includes(view) ? view : 'image'
  const previews = a.kind === 'part' ? (v?.previews ?? []).filter((p) => p.mime.startsWith('image/')) : []
  const cover = coverOf(a.kind, v)
  const [pick, setPick] = useState<string | null>(null)
  // 只有 GLB 的零件：大图看自动生成的缩略图（R-383），标明是自动生成的
  const auto = !cover && a.kind === 'part' && v?.auto_thumb ? v.auto_thumb : null
  const main = previews.find((p) => p.id === pick) ?? cover ?? auto
  // 按机位分组（R-376）：预览图按机位排在一起，缩略图下标出机位
  const groups = viewGroups(previews)
  const ordered = groups.flatMap((g) => g.items)
  const variants = variantNames(v?.variants)
  const meta: [string, ReactNode][] = [
    [t.asset.meta.code, a.code],
    [t.asset.meta.batch, batchesOf(a.uid ? a : (v ?? a)).join(t.asset.listSep)],
    [t.asset.meta.category, a.category],
    [t.asset.meta.fileType, fileTypesOf({ latest: v }).join(t.asset.listSep)],
    [t.asset.meta.previews, v?.previews?.length ? t.asset.previewCount(v.previews.length) : ''],
    [t.asset.meta.branch, v?.branch],
    [t.asset.meta.commit, v?.commit ? <span className="font-mono text-[12px]">{v.commit.slice(0, 10)}</span> : ''],
    [t.asset.meta.triangles, v && fmtTris(v.triangles, v.budget) ? <span className={cx(overBudget(v.triangles, v.budget) && 'text-danger')} title={overBudget(v.triangles, v.budget) ? t.asset.overBudget : undefined}>{fmtTris(v.triangles, v.budget)}</span> : ''],
    [t.asset.meta.size, fmtSize(v?.size)],
    [t.asset.meta.pixels, fmtPixels(v?.pixels)],
    [t.asset.meta.source, v?.source],
    [t.asset.meta.license, v?.license],
    [t.asset.meta.variants, variants.join(t.asset.listSep)],
    [t.asset.meta.submitter, v ? submitterName(v.submitter) : ''],
  ]
  // 项目特有字段（R-376）：asset.json 里纸鸢不认识的字段，原样展示
  const fields = metaRows(v?.meta)
  const rows = meta.filter(([, val]) => val !== undefined && val !== '' && val !== null)
  const onStage = layout === 'stage'

  // 动作动画（R-392）：一件素材一个时钟，切版本、切 3D / 3D 对比时动作和进度不丢；动作名、循环标记以这一版的 asset.json 为准
  const sync = useMemo<AnimSync>(() => createAnimSync(), [])
  useEffect(() => sync.setMeta(v?.meta), [sync, v?.meta])
  const [sink, setSinkState] = useState<((m: FrameMark) => void) | null>(null)
  const setSink = useCallback((f: ((m: FrameMark) => void) | null) => setSinkState(() => f), [])
  const jump = useCallback(
    (f: Frame) => {
      if (f.v && f.v !== shownN && versions.some((x) => x.version === f.v)) setShownN(f.v) // 旧版本上的意见：先切到那一版
      setView('model')
      sync.select(f.anim, f.t, false)
    },
    [sync, shownN, versions],
  )
  const frameCtx = useMemo<FrameCtxValue>(() => ({ sync, version: v?.version ?? a.version, jump, sink, setSink }), [sync, v?.version, a.version, jump, sink, setSink])
  const animated = (cur === 'model' && !!glb) || (cur === 'compare3d' && !!pair3d)

  const viewSwitch = views.length > 1 && (
    <div className={onStage ? '' : 'mb-3'}>
      <Segmented
        size="sm"
        value={cur}
        onChange={(x) => setView(x)}
        options={views.map((x) => ({
          value: x,
          label: (
            <span className="flex items-center gap-1.5">
              {x === 'image' ? <ImageIcon className="size-3.5" /> : x === 'model' ? <BoxIcon className="size-3.5" /> : x === 'compare3d' ? <Columns2 className="size-3.5" /> : <SplitSquareHorizontal className="size-3.5" />}
              {x === 'compare' && pair ? `${t.asset.view.compare} ${t.asset.ver(pair.before.version)}` : x === 'compare3d' && pair3d ? `${t.asset.view.compare3d} ${t.asset.ver(pair3d.before.version)}` : t.asset.view[x]}
            </span>
          ),
        }))}
      />
    </div>
  )

  const stageBox = (
    <div className={cx('relative w-full overflow-hidden rounded-xl border border-line bg-[radial-gradient(ellipse_at_50%_40%,var(--color-surface-2),var(--color-sunken))]', onStage ? 'min-h-0 flex-1' : 'aspect-[4/3]')} data-stage={cur}>
      {cur === 'model' && glb ? (
        <Suspense fallback={null}>
          <Viewer3D key={glb.id} url={fileUrl(glb.id)} className="absolute inset-0" sync={sync} main />
        </Suspense>
      ) : cur === 'compare3d' && pair3d ? (
        <Suspense fallback={null}>
          <div className="absolute inset-0 grid grid-cols-2 divide-x divide-line" data-compare3d="">
            {[pair3d.before, pair3d.after].map((x) => (
              <Viewer3D key={x.id} url={fileUrl(glbOf(a.kind, x)!.id)} className="relative min-w-0" sync={sync} label={t.asset.ver(x.version)} main={x === pair3d.after} />
            ))}
          </div>
        </Suspense>
      ) : cur === 'compare' && pair ? (
        <CompareSlider kind={a.kind} before={pair.before} after={pair.after} />
      ) : main ? (
        <Lightbox f={main} group={ordered.length > 1 ? ordered : undefined}>
          <button className="absolute inset-0 cursor-zoom-in">
            <img src={fileUrl(main.id)} alt="" className={cx('h-full w-full object-contain', main === auto && 'p-6')} draggable={false} />
            {main === auto && <span className="pointer-events-none absolute right-2 bottom-2 rounded bg-black/45 px-1.5 py-0.5 text-[11px] text-white">{t.asset.autoThumbTip}</span>}
          </button>
        </Lightbox>
      ) : (
        <div className="absolute inset-0 flex flex-col items-center justify-center gap-2 text-sm text-fg-3">
          <BoxIcon className="size-8" strokeWidth={1.4} />
          {t.asset.noPreview}
        </div>
      )}
    </div>
  )

  const animBar = animated && <AnimBar sync={sync} className={onStage ? 'shrink-0' : 'mt-2'} />

  const strip = cur === 'image' && previews.length > 1 && (
    <div className={cx('flex gap-3 overflow-x-auto', onStage ? 'shrink-0' : 'mt-2')} aria-label={t.asset.previews} data-views={groups.length}>
      {groups.map((g) => (
        <div key={g.view} className="flex shrink-0 flex-col gap-1">
          <div className="flex gap-2">
            {g.items.map((p) => (
              <button key={p.id} onClick={() => setPick(p.id)} title={p.name} className={cx('h-14 w-20 shrink-0 overflow-hidden rounded-lg border', (main?.id ?? '') === p.id ? 'border-accent ring-2 ring-accent/30' : 'border-line')}>
                <ThumbImg id={p.id} alt="" className="h-full w-full object-cover" />
              </button>
            ))}
          </div>
          {g.view && <span className="truncate text-center text-[12px] text-fg-3">{g.view}</span>}
        </div>
      ))}
    </div>
  )

  const info = (
    <>
      <dl className={cx('overflow-hidden rounded-xl border border-line text-[13px]', !onStage && 'mt-4')}>
        {rows.map(([k, val]) => (
          <div key={k} className="flex min-w-0 gap-3 border-b border-line px-3 py-2 last:border-b-0">
            <dt className="w-[4.5em] shrink-0 text-fg-3">{k}</dt>
            <dd className="min-w-0 truncate text-fg">{val}</dd>
          </div>
        ))}
      </dl>

      {fields.length > 0 && (
        <section className="mt-4" data-fields>
          <h3 className="mb-2 text-[13px] font-medium text-fg-3">{t.asset.projectFields}</h3>
          <dl className="overflow-hidden rounded-xl border border-line text-[13px]">
            {fields.map(([k, val]) => (
              <div key={k} className="flex min-w-0 gap-3 border-b border-line px-3 py-2 last:border-b-0">
                <dt className="w-24 shrink-0 truncate font-mono text-[12px] text-fg-3" title={k}>
                  {k}
                </dt>
                <dd className="min-w-0 break-all text-fg">{val}</dd>
              </div>
            ))}
          </dl>
        </section>
      )}

      {versions.length > 0 && (
        <section className="mt-5">
          <h3 className="mb-2 text-[13px] font-medium text-fg-3">{t.asset.versions}</h3>
          <div className="overflow-hidden rounded-xl border border-line">
            {versions.map((x) => (
              <VersionRow key={x.id} a={a} x={x} latest={x.version === a.version} active={x.version === (v?.version ?? a.version)} now={now} onPick={() => setShownN(x.version)} />
            ))}
          </div>
        </section>
      )}
      <Attachments list={a.attachments} now={now} />
      {!ex && <ReviewHistory reviews={a.reviews} />}
      {pos && <p className="mt-4 text-xs text-fg-3 tabular">{pos}</p>}
    </>
  )

  const head = (
    <div className="flex items-start gap-3 border-b border-line px-5 py-4 max-lg:pt-1">
      <div className="min-w-0 flex-1">
        <div className="truncate font-display text-[17px] font-semibold text-fg" title={a.key}>
          {a.key}
        </div>
        <div className="mt-1 text-[13px] text-fg-3">{metaLine(a.code, t.asset.ver(a.version), relTime(a.updated_at, now))}</div>
      </div>
      <AssetBadge status={a.status as AssetStatus} code={a.code} className="mt-1" />
      {!onStage && (
        <Button variant="ghost" size="icon-sm" aria-label={t.common.close} onClick={onClose} className="max-lg:hidden">
          <X className="size-4" />
        </Button>
      )}
    </div>
  )
  const footer = ex ? ex.footer(a, v?.version ?? a.version) : <ReviewBar a={a} shown={v?.version ?? a.version} keys={keys} barRef={shell} />

  if (onStage)
    return (
      <FrameCtx.Provider value={frameCtx}>
        <div className="flex min-w-0 flex-1 flex-col gap-3 p-5" data-review-main="">
          {viewSwitch}
          {stageBox}
          {animBar}
          {strip}
        </div>
        <aside className="flex w-[360px] shrink-0 flex-col border-l border-line bg-surface xl:w-[380px]" data-review-side="">
          {head}
          <div className="min-h-0 flex-1 overflow-y-auto overscroll-contain px-5 py-4">{info}</div>
          {footer}
        </aside>
      </FrameCtx.Provider>
    )

  return (
    <FrameCtx.Provider value={frameCtx}>
      <div className="flex min-h-0 flex-1 flex-col">
        {head}
        <div className="min-h-0 flex-1 overflow-y-auto overscroll-contain px-5 py-4">
          {viewSwitch}
          {stageBox}
          {animBar}
          {strip}
          {info}
        </div>
        {footer}
      </div>
    </FrameCtx.Provider>
  )
}

// Attachments：素材的附件（R-365：访客上传、你收下为附件的文件），图片和录屏能直接看，其它显示为文件块。
function Attachments({ list, now }: { list: Detail['attachments']; now: number }) {
  if (!list.length) return null
  return (
    <section className="mt-5" data-attachments="">
      <h3 className="mb-2 text-[13px] font-medium text-fg-3">{t.asset.attachments}</h3>
      <ul className="space-y-2">
        {list.map((x) => (
          <li key={x.id} className="rounded-xl border border-line px-3 py-2.5">
            <Thumbs files={[x.file]} size="sm" />
            <div className="mt-1.5 truncate text-xs text-fg-3">{metaLine(submitterName(x.submitter), t.asset.ver(x.version), relTime(x.created_at, now))}</div>
            {x.note && <p className="mt-1 text-[13px] whitespace-pre-wrap text-fg-2">{x.note}</p>}
          </li>
        ))}
      </ul>
    </section>
  )
}

// 提交者（D-1782）：会话写会话标题（没有标题时写「会话」），访客写名字；guest 给 R-365 资源库分享预留。
function submitterName(x?: { kind: string; ref?: string; name?: string }): string {
  if (!x?.kind) return ''
  if (x.kind === 'user') return t.asset.submitter.user
  if (x.kind === 'guest') return t.asset.submitter.guest(x.name || x.ref || '')
  return x.name || t.asset.submitter.session // 会话键不上界面（copywriting 2.5）
}

function VersionRow({ a, x, latest, active, now, onPick }: { a: Pick<AssetLite, 'code' | 'kind'>; x: Ver; latest: boolean; active: boolean; now: number; onPick: () => void }) {
  return (
    <button onClick={onPick} aria-pressed={active} className={cx('flex w-full items-center gap-3 border-b border-line px-3 py-2 text-left text-[13px] last:border-b-0 transition', active ? 'bg-accent-soft/60' : 'hover:bg-hover')}>
      <AssetCover a={a} v={x} tag={false} className="h-9 w-12 shrink-0 rounded-md border border-line [&_svg]:size-4" />
      <b className="w-7 shrink-0 font-mono text-fg">{t.asset.ver(x.version)}</b>
      <span className="min-w-0 flex-1 truncate text-fg-3">{metaLine(latest ? t.asset.current : undefined, relTime(x.created_at, now), x.commit ? x.commit.slice(0, 7) : undefined)}</span>
      {active && !latest && <span className="shrink-0 text-xs text-accent">{t.asset.viewing}</span>}
    </button>
  )
}

// 版本对比：左边旧版、右边新版，拖动分隔线（或方向键）调整；点图进看图，空格切改前 / 改后。
function CompareSlider({ kind, before, after }: { kind: 'part' | 'image'; before: Ver; after: Ver }) {
  const [pos, setPos] = useState(50)
  const b = coverOf(kind, before)!
  const a = coverOf(kind, after)!
  const shot: ViewShot = { ...a, before: b }
  return (
    <div className="absolute inset-0 touch-none select-none" data-no-drag="">
      <img src={fileUrl(b.id)} alt="" className="absolute inset-0 h-full w-full object-contain" draggable={false} />
      <img src={fileUrl(a.id)} alt="" className="absolute inset-0 h-full w-full object-contain" style={{ clipPath: `inset(0 0 0 ${pos}%)` }} draggable={false} />
      <div className="pointer-events-none absolute inset-y-0 w-0.5 -translate-x-1/2 bg-white shadow-[0_0_0_1px_rgb(0_0_0/0.25)]" style={{ left: `${pos}%` }}>
        <span className="absolute top-1/2 left-1/2 flex size-7 -translate-x-1/2 -translate-y-1/2 items-center justify-center rounded-full bg-white text-[11px] font-semibold text-fg shadow-pop">⇆</span>
      </div>
      <input
        type="range"
        min={0}
        max={100}
        value={pos}
        onChange={(e) => setPos(+e.target.value)}
        aria-label={t.asset.compareSlider(before.version, after.version)}
        className="absolute inset-0 h-full w-full cursor-ew-resize opacity-0"
      />
      <span className="pointer-events-none absolute top-2 left-2 rounded bg-black/55 px-1.5 py-0.5 font-mono text-[11px] text-white">{t.asset.ver(before.version)}</span>
      <span className="pointer-events-none absolute top-2 right-2 rounded bg-black/55 px-1.5 py-0.5 font-mono text-[11px] text-white">{t.asset.ver(after.version)}</span>
      <div className="absolute right-2 bottom-2 flex items-center gap-2">
        <span className="pointer-events-none text-[11px] text-fg-3">{t.asset.compareHint}</span>
        <Lightbox f={shot}>
          <button aria-label={t.attach.zoom} className="flex size-8 items-center justify-center rounded-lg bg-black/45 text-white backdrop-blur transition hover:bg-black/65">
            <Maximize2 className="size-4" />
          </button>
        </Lightbox>
      </div>
    </div>
  )
}

// ---------- 并排比较（同一用途的候选） ----------

export function CompareDrawer({ gkey, all, onClose, onOpen }: { gkey: string | null; all: AssetLite[]; onClose: () => void; onOpen: (code: string) => void }) {
  const members = gkey ? groupMembers(all, gkey) : []
  const shots: ViewShot[] = members.map((m) => coverOf(m.kind, m.latest)).filter((f): f is FileLite => !!f)
  const first = members[0]
  // 候选都有 GLB 时可切到 3D 并排（R-392）：共用一个动画时钟，同步播放同一动作
  const glbs = members.filter((m) => glbOf(m.kind, m.latest))
  const [by, setBy] = useState<'image' | 'model'>('image')
  const sync = useMemo<AnimSync>(() => createAnimSync(), [gkey])
  const in3d = by === 'model' && glbs.length >= 2
  return (
    <Panel open={!!gkey} onClose={onClose} wide="approval" title={first ? `${t.asset.compareTitle}${t.common.sep}${first.group}` : t.asset.compareTitle} subtitle={first ? t.asset.compareSub(first.batch, members.length) : undefined}>
      {glbs.length >= 2 && (
        <div className="mb-3">
          <Segmented
            size="sm"
            value={by}
            onChange={setBy}
            options={[
              { value: 'image', label: <span className="flex items-center gap-1.5"><ImageIcon className="size-3.5" />{t.asset.compareBy.image}</span> },
              { value: 'model', label: <span className="flex items-center gap-1.5"><BoxIcon className="size-3.5" />{t.asset.compareBy.model}</span> },
            ]}
          />
        </div>
      )}
      {in3d ? (
        <div data-compare-3d="">
          <div className={cx('grid gap-4', glbs.length === 2 ? 'sm:grid-cols-2' : 'sm:grid-cols-2 lg:grid-cols-3')}>
            {glbs.map((m, i) => (
              <div key={m.id} className="min-w-0" data-candidate={candidateLabel(m)}>
                <div className="relative aspect-[4/3] overflow-hidden rounded-xl border border-line bg-[radial-gradient(ellipse_at_50%_40%,var(--color-surface-2),var(--color-sunken))]">
                  <Suspense fallback={null}>
                    <Viewer3D url={fileUrl(glbOf(m.kind, m.latest)!.id)} className="absolute inset-0" sync={sync} label={candidateLabel(m)} main={i === 0} />
                  </Suspense>
                </div>
                <div className="mt-2 flex items-center gap-2">
                  <b className="text-sm text-fg">{candidateLabel(m)}</b>
                  <AssetBadge status={m.status} code={m.code} />
                  <span className="min-w-0 flex-1 truncate text-xs text-fg-3">{metaLine(m.code, t.asset.ver(m.version))}</span>
                  <Button size="sm" variant="ghost" onClick={() => onOpen(m.code)}>
                    {t.asset.openOne}
                  </Button>
                </div>
              </div>
            ))}
          </div>
          <AnimBar sync={sync} className="mt-3" />
        </div>
      ) : !members.length ? (
        <Empty icon={<Images />} title={t.asset.notFound} />
      ) : (
        <div className={cx('grid gap-4', members.length === 2 ? 'sm:grid-cols-2' : 'sm:grid-cols-2 lg:grid-cols-3')}>
          {members.map((m) => {
            const f = coverOf(m.kind, m.latest)
            return (
              <div key={m.id} className="min-w-0" data-candidate={candidateLabel(m)}>
                <div className="relative aspect-[4/3] overflow-hidden rounded-xl border border-line bg-sunken">
                  {f ? (
                    <Lightbox f={f} group={shots}>
                      <button className="absolute inset-0 cursor-zoom-in">
                        <img src={fileUrl(f.id)} alt="" className="h-full w-full object-contain" draggable={false} />
                      </button>
                    </Lightbox>
                  ) : (
                    <div className="absolute inset-0 flex items-center justify-center text-sm text-fg-3">{t.asset.noPreview}</div>
                  )}
                </div>
                <div className="mt-2 flex items-center gap-2">
                  <b className="text-sm text-fg">{candidateLabel(m)}</b>
                  <AssetBadge status={m.status} code={m.code} />
                  <span className="min-w-0 flex-1 truncate text-xs text-fg-3">{metaLine(m.code, t.asset.ver(m.version))}</span>
                  <Button size="sm" variant="ghost" onClick={() => onOpen(m.code)}>
                    {t.asset.openOne}
                  </Button>
                </div>
              </div>
            )
          })}
        </div>
      )}
    </Panel>
  )
}
