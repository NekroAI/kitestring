import { lazy, Suspense, useRef, useState, type ReactNode } from 'react'
import { Maximize2 } from 'lucide-react'
import type { Block, BlockItem } from '../api/client'
import { t } from '../i18n'
import { cx } from '../lib/util'
import { fileUrl, isVideo, Lightbox, ZoomButton, type FileRef, type ViewShot } from './attach'
import { BoxLayer, BoxLegend, BoxToggle, shotTitle } from './shots'
import { Linkify } from './refmark'

// 表达块（R-97）：Agent 交证据、提决策、交阶段小结时附的结构化图示，种类固定（见 internal/blocks）。
// 这里只管画：每种块一个渲染器，样式与印记统一；不渲染任何 Agent 给的 HTML。验收、决策、工作台都复用这些件。

const MermaidView = lazy(() => import('./flow').then((m) => ({ default: m.MermaidView })))

// ---------- 判定图标与清单 ----------

export function ResultIcon({ result, className }: { result?: string; className?: string }) {
  const tone = result === 'pass' ? 'text-run' : result === 'fail' ? 'text-danger' : 'text-seal'
  return (
    <svg viewBox="0 0 16 16" className={cx('size-4 shrink-0', tone, className)} aria-label={t.blocks.result[result ?? 'ask']}>
      <circle cx="8" cy="8" r="7" fill="currentColor" opacity=".16" />
      {result === 'pass' ? (
        <path d="M4.8 8.2l2.1 2.1 4.3-4.5" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" />
      ) : result === 'fail' ? (
        <path d="M5.4 5.4l5.2 5.2M10.6 5.4l-5.2 5.2" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" />
      ) : (
        <>
          <path d="M8 4.4v4.3" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" />
          <circle cx="8" cy="11.3" r="1.1" fill="currentColor" />
        </>
      )}
    </svg>
  )
}

// Score：达标小方块（绿 = 达标，朱 = 要你看 / 未达标）加“3 / 4 达标”。
export function Score({ items, className, label = true }: { items: BlockItem[]; className?: string; label?: boolean }) {
  const pass = items.filter((i) => i.result === 'pass').length
  return (
    <span className={cx('inline-flex items-center gap-1 text-[13px] text-fg-2', className)}>
      {items.map((i, k) => (
        <i key={k} className={cx('inline-block size-2.5 rounded-[3px]', i.result === 'pass' ? 'bg-run' : i.result === 'fail' ? 'bg-danger' : 'bg-seal')} />
      ))}
      {label && <span className="ml-1 tabular-nums">{t.blocks.score(pass, items.length)}</span>}
    </span>
  )
}

// ScoreCount：只有达标数与总数时（阶段验收汇总）画同样的小方块。
export function ScoreCount({ pass, total, className }: { pass: number; total: number; className?: string }) {
  const items = Array.from({ length: total }, (_, k) => ({ result: k < pass ? 'pass' : 'ask' }) as BlockItem)
  return <Score items={items} className={className} label={false} />
}

// criteriaOf：从需求的验收标准里按编号取出各条（“1. …”“2、…”），清单项只写了 criterion 时用来补文字。
export function criteriaOf(acceptance?: string): Record<number, string> {
  const out: Record<number, string> = {}
  for (const m of (acceptance ?? '').matchAll(/(?:^|[\s；;。])(\d{1,2})[.、．)）]\s*([^\n；;]+)/g)) {
    const n = Number(m[1])
    if (!out[n]) out[n] = m[2].trim().replace(/[。，,]$/, '')
  }
  return out
}

export function Checklist({
  block,
  criteria,
  refLabel,
  active,
  onSee,
}: {
  block: Block
  criteria?: Record<number, string>
  refLabel?: (id: string) => string | undefined
  active?: number | null
  onSee?: (id: string, index: number) => void
}) {
  const items = block.items ?? []
  return (
    <div className="grid gap-0.5">
      {items.map((it, k) => {
        const text = it.text || (it.criterion ? (criteria?.[it.criterion] ?? t.blocks.criterion(it.criterion)) : '')
        const tag = it.see ? refLabel?.(it.see) : undefined
        const can = !!(it.see && onSee)
        return (
          <button
            key={k}
            type="button"
            disabled={!can}
            onClick={() => can && onSee!(it.see!, k)}
            className={cx(
              'grid w-full grid-cols-[18px_1fr_auto] items-start gap-2.5 rounded-lg px-2.5 py-2 text-left @max-[680px]:py-1.5 transition-colors disabled:cursor-default',
              can && 'cursor-pointer hover:bg-hover',
              active === k && 'bg-hover',
            )}
          >
            <ResultIcon result={it.result} className="mt-0.5" />
            <span className="min-w-0">
              <span className="block text-sm break-words text-fg">
                {it.criterion && it.text ? <span className="mr-1 font-mono text-xs text-fg-3">{it.criterion}.</span> : null}
                <Linkify text={text} />
              </span>
              {it.why && (
                <span className="mt-0.5 block text-[13px] break-words text-fg-3 @max-[680px]:line-clamp-1">
                  <Linkify text={it.why} />
                </span>
              )}
            </span>
            {tag && <span className="mt-0.5 rounded-[5px] border border-line px-1.5 py-px font-mono text-[11px] font-semibold whitespace-nowrap text-accent">{tag}</span>}
          </button>
        )
      })}
    </div>
  )
}

// ---------- 截图与对比 ----------

// Media：一张截图（或录屏）。改动框叠在图上，与图同尺寸；limit 时限高 50dvh（R-181），框随图一起被裁。
function Media({ f, className, limit, hideBoxes, group }: { f: FileRef; className?: string; limit?: boolean; hideBoxes?: boolean; group?: FileRef[] }) {
  const box = useRef<HTMLSpanElement>(null)
  const [cut, setCut] = useState(false) // 图被限高裁掉了一截（R-181）
  const measure = () => {
    const el = box.current
    if (el) setCut(el.scrollHeight - el.clientHeight > 4)
  }
  if (isVideo(f.mime)) return <video src={fileUrl(f.id)} controls playsInline preload="metadata" className={cx('block w-full rounded-[10px] border border-line bg-sunken', className)} />
  return (
    <Lightbox f={f} group={group}>
      <button type="button" aria-label={shotTitle(f)} className="group relative block w-full cursor-zoom-in">
        <span ref={box} className={cx('block overflow-hidden rounded-[10px] border border-line bg-sunken', limit && 'relative max-h-[50dvh]')}>
          <span className="relative block">
            <img src={fileUrl(f.id)} alt={shotTitle(f)} loading="lazy" onLoad={limit ? measure : undefined} className={cx('block h-auto w-full', className)} />
            <BoxLayer boxes={f.boxes} hidden={hideBoxes} />
          </span>
        </span>
        {limit && cut && <span aria-hidden className="pointer-events-none absolute inset-x-px bottom-px h-16 rounded-b-[10px] bg-gradient-to-t from-bg via-bg/70 to-transparent" />}
        <span className="absolute top-2 right-2 flex size-7 items-center justify-center rounded-lg bg-black/45 text-white opacity-0 backdrop-blur transition group-hover:opacity-100 pointer-coarse:opacity-100">
          <Maximize2 className="size-3.5" />
        </span>
      </button>
    </Lightbox>
  )
}

// Compare：前后对比（R-183，D-304）。默认显示「改后 + 改动框」，点一下图切到改前同一位置，再点切回；不再拖分界线。
// 两张图叠在同一格里，切换只换透明度，不闪、不跳；左上角的小标签写明现在看的是哪张。右上角看大图（改后、改前两页）。
export function Compare({ before, after, title, limit, hideBoxes }: { before: FileRef; after: FileRef; title?: string; limit?: boolean; hideBoxes?: boolean }) {
  const [old, setOld] = useState(false)
  const tag = 'pointer-events-none absolute top-2 left-2 rounded-[5px] px-1.5 py-px text-[11px] font-semibold backdrop-blur-[2px]'
  // 看大图：一张图，改后 / 改前在看图里切换（R-195）
  const shot: ViewShot = { ...after, title: title || after.title, before: { ...before, boxes: null } }
  return (
    <div className="grid gap-1.5">
      <div className="group relative">
        <button
          type="button"
          aria-pressed={old}
          aria-label={t.blocks.compareToggle}
          onClick={() => setOld((v) => !v)}
          className={cx('relative block w-full cursor-pointer overflow-hidden rounded-[10px] border border-line bg-sunken select-none', limit && 'max-h-[50dvh]')}
        >
          <span className="grid [&>*]:[grid-area:1/1]">
            <img src={fileUrl(before.id)} alt={`${t.blocks.before}：${title || before.name}`} draggable={false} className={cx('block h-auto w-full transition-opacity duration-200', old ? 'opacity-100' : 'opacity-0')} />
            <span className={cx('relative block self-start transition-opacity duration-200', old ? 'opacity-0' : 'opacity-100')}>
              <img src={fileUrl(after.id)} alt={`${t.blocks.after}：${title || after.name}`} draggable={false} className="block h-auto w-full" />
              <BoxLayer boxes={after.boxes} hidden={hideBoxes} />
            </span>
          </span>
          <span aria-hidden className={cx(tag, old ? 'bg-black/55 text-white' : 'bg-seal text-seal-fg')}>
            {old ? t.blocks.before : t.blocks.after}
          </span>
        </button>
        <ZoomButton f={shot} className="absolute top-2 right-2 opacity-0 group-hover:opacity-100 focus-visible:opacity-100 pointer-coarse:opacity-100" />
      </div>
      <span className="text-center text-xs text-fg-3">{old ? t.blocks.toAfter : t.blocks.toBefore}</span>
    </div>
  )
}

// ---------- 指标、改动范围、选项、表格 ----------

function Spark({ data }: { data: number[] }) {
  const min = Math.min(0, ...data)
  const max = Math.max(...data)
  const span = max - min || 1
  return (
    <div className="mt-1 flex h-[22px] items-end gap-0.5" aria-label={t.blocks.trend}>
      {data.map((v, k) => (
        <i key={k} className="block min-h-px flex-1 rounded-[1px] bg-accent opacity-75" style={{ height: `${Math.max(4, ((v - min) / span) * 100)}%` }} />
      ))}
    </div>
  )
}

export function Metrics({ items }: { items: BlockItem[] }) {
  return (
    <div className="grid grid-cols-[repeat(auto-fit,minmax(min(100%,140px),1fr))] gap-2.5">
      {items.map((m, k) => (
        <div key={k} className="grid min-w-0 content-start gap-0.5 rounded-[10px] border border-line bg-surface-2 p-3">
          <span className="font-display text-[22px] leading-tight font-semibold break-words text-fg tabular-nums">
            {m.value}
            {m.unit && <small className="ml-1 font-sans text-[13px] font-medium text-fg-3">{m.unit}</small>}
          </span>
          <span className="text-xs break-words text-fg-3">
            {m.label}
            {m.note && <> · {m.note}</>}
          </span>
          {!!m.trend?.length && m.trend.length > 1 && <Spark data={m.trend} />}
        </div>
      ))}
    </div>
  )
}

export function Scope({ items }: { items: BlockItem[] }) {
  const max = Math.max(1, ...items.map((i) => i.count ?? 0))
  return (
    <div className="grid gap-1.5">
      {items.map((m, k) => (
        <div key={k} className="grid grid-cols-[minmax(4.5rem,8rem)_1fr_auto] items-center gap-2.5 text-[13px]">
          <span className="truncate text-fg" title={m.note ? `${m.label} · ${m.note}` : m.label}>
            {m.label}
          </span>
          <span className="h-2 overflow-hidden rounded-full bg-sunken">
            <i className="block h-full rounded-full bg-accent" style={{ width: `${((m.count ?? 0) / max) * 100}%` }} />
          </span>
          <span className="min-w-[3em] text-right text-fg-3 tabular-nums">{t.blocks.scopeCount(m.count ?? 0)}</span>
        </div>
      ))}
    </div>
  )
}

// Options：决策的选项（只读展示：标出推荐、可附效果图）。可点选的多点决策在决策卡里做（R-112）。
export function Options({ items }: { items: BlockItem[] }) {
  return (
    <div className="flex flex-wrap gap-2">
      {items.map((o, k) => (
        <div key={k} className={cx('grid min-w-0 flex-[1_1_200px] content-start gap-1.5 rounded-[10px] border bg-surface px-3 py-2.5', o.recommended ? 'border-run/50' : 'border-line-strong')}>
          {o.image && <Media f={o.image} className="max-h-48 object-cover object-top" />}
          <span className="flex items-center gap-1.5 text-sm font-semibold text-fg">
            <span className="min-w-0 break-words">{o.label}</span>
            {o.recommended && <span className="shrink-0 rounded px-1.5 py-px text-[11px] font-bold text-run bg-run-soft">{t.blocks.recommended}</span>}
          </span>
          {o.desc && (
            <span className="text-[13px] break-words text-fg-3">
              <Linkify text={o.desc} />
            </span>
          )}
        </div>
      ))}
    </div>
  )
}

export function Table({ columns, rows }: { columns: string[]; rows: string[][] }) {
  return (
    <div className="overflow-x-auto rounded-[10px] border border-line">
      <table className="w-full border-collapse text-[13px]">
        <thead>
          <tr className="bg-surface-2">
            {columns.map((c, k) => (
              <th key={k} className="border-b border-line px-2.5 py-1.5 text-left text-xs font-semibold whitespace-nowrap text-fg-3">
                {c}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.map((r, i) => (
            <tr key={i} className="border-b border-line last:border-0">
              {columns.map((_, k) => (
                <td key={k} className="px-2.5 py-1.5 align-top break-words text-fg-2">
                  <Linkify text={r[k] ?? ''} />
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

// ---------- 单个块与块列表 ----------

// figureNumbers：截图块按出现顺序编号（图 1、图 2…），清单项的引用标签和图注共用。
export function figureNumbers(blocks: Block[]) {
  const n: Record<string, number> = {}
  let k = 0
  for (const b of blocks) if (b.type === 'figure') n[b.id] = ++k
  return n
}

// refLabeler：清单项右侧的引用标签（图 1 / 流程 / 指标 …）。
export function refLabeler(blocks: Block[]) {
  const figs = figureNumbers(blocks)
  return (id: string) => {
    const b = blocks.find((x) => x.id === id)
    if (!b) return undefined
    return b.type === 'figure' ? t.blocks.refFigure(figs[id]) : t.blocks.refLabel[b.type]
  }
}

function Cap({ children }: { children: ReactNode }) {
  return <span className="block text-xs font-medium tracking-[0.06em] text-fg-2">{children}</span>
}

// figureTitle：截图 / 对比块的标题（R-183）：块的 title，没有时用图注，再没有用图片自己的标题，最后用文件名兜底。
export function figureTitle(b: Block) {
  const f = b.type === 'compare' ? b.after : b.image
  return b.title?.trim() || b.caption?.trim() || (f ? shotTitle(f) : '')
}

// BlockView：画一个块。figNo 是截图的编号；lit 时加高亮框（清单项点过来时）。group：看大图时可以左右翻的同组图片。
export function BlockView({ block, figNo, lit, criteria, className, limit, group }: { block: Block; figNo?: number; lit?: boolean; criteria?: Record<number, string>; className?: string; limit?: boolean; group?: FileRef[] }) {
  const b = block
  const [hideBoxes, setHideBoxes] = useState(false)
  const shot = b.type === 'figure' || b.type === 'compare'
  const typeName = b.type === 'flow' ? t.blocks.flow : b.type === 'metrics' ? t.blocks.metrics : b.type === 'scope' ? t.blocks.scope : b.type === 'options' ? t.blocks.options : b.type === 'table' ? t.blocks.table : b.type === 'checklist' ? t.blocks.checklist : ''
  const title = shot ? figureTitle(b) : ''
  const boxes = b.type === 'compare' ? b.after?.boxes : b.type === 'figure' ? b.image?.boxes : null
  let body: ReactNode = null
  switch (b.type) {
    case 'figure':
      body = b.image && <Media f={shotFile(b)!} limit={limit} hideBoxes={hideBoxes} group={group} />
      break
    case 'compare':
      body = b.before && b.after && <Compare before={b.before} after={b.after} title={title} limit={limit} hideBoxes={hideBoxes} />
      break
    case 'flow':
      body = (
        <div className="rounded-[10px] border border-line bg-surface p-2.5">
          <Suspense fallback={<div className="flex h-24 items-center justify-center text-xs text-fg-3">{t.blocks.flowLoading}</div>}>
            <MermaidView source={b.mermaid ?? ''} />
          </Suspense>
        </div>
      )
      break
    case 'metrics':
      body = <Metrics items={b.items ?? []} />
      break
    case 'scope':
      body = <Scope items={b.items ?? []} />
      break
    case 'options':
      body = <Options items={b.items ?? []} />
      break
    case 'table':
      body = <Table columns={b.columns ?? []} rows={(b.rows ?? []).map((r) => r ?? [])} />
      break
    case 'checklist':
      body = (
        <div className="space-y-1.5">
          <Score items={b.items ?? []} />
          <Checklist block={b} criteria={criteria} />
        </div>
      )
      break
  }
  if (!body) return null
  // 截图的图注：标题已经用了图注时不再重复
  const caption = shot ? (b.caption?.trim() && b.caption.trim() !== title ? b.caption : '') : b.caption
  return (
    <figure
      data-block={b.id}
      className={cx('grid min-w-0 scroll-mt-20 gap-1.5 rounded-xl outline-offset-4 transition-[outline-color] duration-300', lit ? 'outline-2 outline-accent' : 'outline-2 outline-transparent', className)}
    >
      {shot ? (
        <div className="flex min-w-0 items-start gap-2">
          <span className="min-w-0 flex-1 text-[13px] leading-snug font-medium break-words text-fg">
            {figNo ? <span className="mr-1.5 text-xs font-normal text-fg-3 tabular">{t.blocks.figure(figNo)}</span> : null}
            {title}
          </span>
          {!!boxes?.length && <BoxToggle hidden={hideBoxes} onToggle={() => setHideBoxes((v) => !v)} className="-mt-0.5 -mr-1.5" />}
        </div>
      ) : (
        (typeName || b.title) && <Cap>{[typeName, b.title].filter(Boolean).join(' · ')}</Cap>
      )}
      {body}
      {shot && <BoxLegend boxes={boxes} />}
      {caption && <figcaption className="text-xs break-words text-fg-3">{caption}</figcaption>}
    </figure>
  )
}

// shotFile：截图块的图片，带上块的标题（看大图时显示）。
export function shotFile(b: Block): FileRef | undefined {
  const f = b.type === 'compare' ? b.after : b.image
  return f ? { ...f, title: figureTitle(b) } : undefined
}

// BlockList：按顺序画一组块（决策说明、阶段小结里用）。
export function BlockList({ blocks, className, criteria }: { blocks?: Block[] | null; className?: string; criteria?: Record<number, string> }) {
  if (!blocks?.length) return null
  const figs = figureNumbers(blocks)
  return (
    <div className={cx('grid gap-5', className)}>
      {blocks.map((b) => (
        <BlockView key={b.id} block={b} figNo={figs[b.id]} criteria={criteria} />
      ))}
    </div>
  )
}
