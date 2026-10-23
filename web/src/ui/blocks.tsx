import { lazy, Suspense, useRef, useState, type ReactNode } from 'react'
import { Maximize2 } from 'lucide-react'
import type { Block, BlockItem } from '../api/client'
import { t } from '../i18n'
import { cx } from '../lib/util'
import { fileUrl, isVideo, Lightbox, type FileRef } from './attach'
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

function Media({ f, className }: { f: FileRef; className?: string }) {
  if (isVideo(f.mime)) return <video src={fileUrl(f.id)} controls playsInline preload="metadata" className={cx('block w-full rounded-[10px] border border-line bg-sunken', className)} />
  return (
    <Lightbox f={f}>
      <button type="button" aria-label={f.name} className="group relative block w-full cursor-zoom-in">
        <img src={fileUrl(f.id)} alt={f.name} loading="lazy" className={cx('block h-auto w-full rounded-[10px] border border-line bg-sunken', className)} />
        <span className="absolute top-2 right-2 flex size-7 items-center justify-center rounded-lg bg-black/45 text-white opacity-0 backdrop-blur transition group-hover:opacity-100 pointer-coarse:opacity-100">
          <Maximize2 className="size-3.5" />
        </span>
      </button>
    </Lightbox>
  )
}

// Compare：前后对比。拖动图片移动分界线；下面的“之前 / 对半 / 之后”一点切换（D-131）。
// 图上的“之前 / 之后”标签默认隐藏，点一下图片显示（半透明，不挡内容），再点一下隐藏（R-111 退回意见）。
// 拖动和点按用指针事件自己分：移动超过 4px 算拖动，否则算点按；键盘用一个看不见的 range 调。
export function Compare({ before, after }: { before: FileRef; after: FileRef }) {
  const [cut, setCut] = useState(50)
  const [jump, setJump] = useState(false) // 点按切换时带过渡，拖动时跟手
  const [tags, setTags] = useState(false)
  const [ratio, setRatio] = useState<number | null>(null)
  const drag = useRef<{ x: number; moved: boolean } | null>(null)
  const go = (v: number) => {
    setJump(true)
    setCut(v)
  }
  const at = (e: React.PointerEvent<HTMLDivElement>) => {
    const r = e.currentTarget.getBoundingClientRect()
    return Math.min(100, Math.max(0, ((e.clientX - r.left) / r.width) * 100))
  }
  const ease = jump ? 'clip-path .28s ease, left .28s ease' : undefined
  const tag = 'pointer-events-none absolute top-2 rounded-[5px] bg-black/35 px-1.5 py-px text-[11px] font-semibold text-white/90 backdrop-blur-[2px] transition-opacity duration-200'
  const seg = [
    { v: 100, label: t.blocks.before },
    { v: 50, label: t.blocks.half },
    { v: 0, label: t.blocks.after },
  ]
  return (
    <div className="grid gap-1.5">
      <div
        className="relative cursor-ew-resize touch-pan-y overflow-hidden rounded-[10px] border border-line bg-sunken select-none"
        style={{ aspectRatio: ratio ?? 16 / 10 }}
        onPointerDown={(e) => {
          if (e.button !== 0) return
          drag.current = { x: e.clientX, moved: false }
          e.currentTarget.setPointerCapture(e.pointerId)
        }}
        onPointerMove={(e) => {
          const d = drag.current
          if (!d) return
          if (!d.moved && Math.abs(e.clientX - d.x) < 4) return
          d.moved = true
          setJump(false)
          setCut(at(e))
        }}
        onPointerUp={() => {
          if (drag.current && !drag.current.moved) setTags((v) => !v) // 点按：显示 / 隐藏标签
          drag.current = null
        }}
        onPointerCancel={() => (drag.current = null)}
      >
        <img src={fileUrl(before.id)} alt={`${t.blocks.before}：${before.name}`} draggable={false} onLoad={(e) => setRatio(e.currentTarget.naturalWidth / e.currentTarget.naturalHeight || null)} className="pointer-events-none absolute inset-0 size-full object-cover object-top-left" />
        <img src={fileUrl(after.id)} alt={`${t.blocks.after}：${after.name}`} draggable={false} className="pointer-events-none absolute inset-0 size-full object-cover object-top-left" style={{ clipPath: `inset(0 0 0 ${cut}%)`, transition: ease }} />
        <span className="pointer-events-none absolute inset-y-0 w-0.5 bg-seal" style={{ left: `${cut}%`, transition: ease, opacity: cut === 0 || cut === 100 ? 0 : 1 }} />
        <span aria-hidden className={cx(tag, 'left-2', tags ? 'opacity-100' : 'opacity-0')}>
          {t.blocks.before}
        </span>
        <span aria-hidden className={cx(tag, 'right-2', tags ? 'opacity-100' : 'opacity-0')}>
          {t.blocks.after}
        </span>
        <input
          type="range"
          min={0}
          max={100}
          value={cut}
          onChange={(e) => {
            setJump(false)
            setCut(Number(e.target.value))
          }}
          aria-label={t.blocks.dragCompare}
          className="sr-only"
        />
      </div>
      <div className="flex justify-center">
        <div role="radiogroup" aria-label={t.blocks.dragCompare} className="inline-flex rounded-lg border border-line bg-surface-2 p-0.5 text-xs">
          {seg.map((x) => (
            <button
              key={x.v}
              type="button"
              role="radio"
              aria-checked={cut === x.v}
              onClick={() => go(x.v)}
              className={cx('rounded-md px-3 py-1 font-medium transition-colors pointer-coarse:px-4 pointer-coarse:py-1.5', cut === x.v ? 'bg-surface text-fg shadow-xs' : 'text-fg-3 hover:text-fg')}
            >
              {x.label}
            </button>
          ))}
        </div>
      </div>
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

// BlockView：画一个块。figNo 是截图的编号；lit 时加高亮框（清单项点过来时）。
export function BlockView({ block, figNo, lit, criteria, className }: { block: Block; figNo?: number; lit?: boolean; criteria?: Record<number, string>; className?: string }) {
  const b = block
  const typeName = b.type === 'flow' ? t.blocks.flow : b.type === 'metrics' ? t.blocks.metrics : b.type === 'scope' ? t.blocks.scope : b.type === 'options' ? t.blocks.options : b.type === 'table' ? t.blocks.table : b.type === 'checklist' ? t.blocks.checklist : ''
  const cap = b.type === 'figure' || b.type === 'compare' ? b.title : [typeName, b.title].filter(Boolean).join(' · ')
  let body: ReactNode = null
  switch (b.type) {
    case 'figure':
      body = b.image && <Media f={b.image} />
      break
    case 'compare':
      body = b.before && b.after && <Compare before={b.before} after={b.after} />
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
  const caption = b.type === 'figure' ? [figNo ? t.blocks.figure(figNo) : '', b.caption].filter(Boolean).join(' · ') : b.caption
  return (
    <figure
      data-block={b.id}
      className={cx('grid min-w-0 scroll-mt-20 gap-1.5 rounded-xl outline-offset-4 transition-[outline-color] duration-300', lit ? 'outline-2 outline-accent' : 'outline-2 outline-transparent', className)}
    >
      {cap && <Cap>{cap}</Cap>}
      {body}
      {caption && <figcaption className="text-xs break-words text-fg-3">{caption}</figcaption>}
    </figure>
  )
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
