import { useRef, useState, type ReactNode } from 'react'
import { ChevronRight } from 'lucide-react'
import type { Block, Evidence, Payload } from '../api/client'
import { t } from '../i18n'
import { cx } from '../lib/util'
import { fileUrl, Lightbox, Thumbs } from '../ui/attach'
import { BlockView, Checklist, criteriaOf, figureNumbers, refLabeler, Score } from '../ui/blocks'
import { Md } from '../ui/markdown'
import { Linkify } from '../ui/refmark'

// 结构化证据（R-97，原型 ③）：一句结论 → 验收清单（点一项跳到右边对应的块）→ 要你做的 → 折叠的细节；
// 右边是截图、对比、流程、指标等表达块。窄处（抽屉、手机）上下堆叠。旧证据没有 payload，由调用方按纯文本显示。

const resultWord: Record<string, string> = { pass: '达标', fail: '未达标', ask: '要你看' }

// payloadText 与 hub 的 blocks.Payload.Text 一致：没写 summary 时 hub 用它拼正文。相同就不再折叠显示“原始证据全文”。
function payloadText(p: Payload) {
  const lines: string[] = []
  if (p.verdict) lines.push(p.verdict)
  const c = p.blocks?.find((b) => b.type === 'checklist')
  for (const it of c?.items ?? []) {
    let s = `- ${it.text || `验收标准 ${it.criterion}`}：${resultWord[it.result ?? 'ask']}`
    if (it.why) s += '，' + it.why
    lines.push(s)
  }
  for (const x of p.todo ?? []) lines.push('要你做的：' + x)
  for (const x of p.aside ?? []) lines.push('顺带改动：' + x)
  if (p.details) lines.push('实现细节：' + p.details)
  return lines.join('\n')
}

function Fold({ title, children }: { title: ReactNode; children: ReactNode }) {
  return (
    <details className="group border-t border-line">
      <summary className="flex cursor-pointer list-none items-center gap-2 px-0.5 py-2.5 text-sm text-fg-2 [&::-webkit-details-marker]:hidden">
        <ChevronRight className="size-3.5 shrink-0 text-fg-3 transition-transform group-open:rotate-90" />
        {title}
      </summary>
      <div className="space-y-1.5 pb-3 pl-5.5 text-sm text-fg-2">{children}</div>
    </details>
  )
}

export const hasPayload = (e: Pick<Evidence, 'payload'>) => !!(e.payload && (e.payload.verdict || e.payload.blocks?.length || e.payload.todo?.length))

export function EvidenceView({ e, acceptance }: { e: Evidence; acceptance?: string }) {
  const p = e.payload ?? {}
  const blocks: Block[] = p.blocks ?? []
  const checklist = blocks.find((b) => b.type === 'checklist')
  const side = blocks.filter((b) => b !== checklist)
  const figs = figureNumbers(blocks)
  const criteria = criteriaOf(acceptance)
  const box = useRef<HTMLDivElement>(null)
  const [lit, setLit] = useState<{ id: string; k: number } | null>(null)

  const used = new Set(blocks.flatMap((b) => [b.image?.id, b.before?.id, b.after?.id, ...(b.items ?? []).map((i) => i.image?.id)]).filter(Boolean))
  const extra = (e.files ?? []).filter((f) => !used.has(f.id))
  const raw = e.summary && e.summary.trim() !== payloadText(p).trim() ? e.summary : ''

  // 窄处（抽屉、手机）截图排成一条横向胶片，紧跟在结论下面，第一屏就能看到；宽处放在右栏。
  const shots = side.filter((b) => b.type === 'figure' && b.image)

  const see = (id: string, k: number) => {
    if (lit?.k === k) return setLit(null)
    setLit({ id, k })
    // 同一个块在胶片和右栏各有一份，跳到当前看得见的那份
    const el = [...(box.current?.querySelectorAll<HTMLElement>(`[data-block="${CSS.escape(id)}"]`) ?? [])].find((x) => x.getClientRects().length > 0)
    el?.scrollIntoView({ behavior: matchMedia('(prefers-reduced-motion: reduce)').matches ? 'auto' : 'smooth', block: 'nearest', inline: 'nearest' })
  }

  const main = (
    <div className="min-w-0 space-y-3 [grid-area:main]">
      {checklist && (
        <div className="space-y-1">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <span className="text-xs tracking-[0.06em] text-fg-3">{checklist.title || t.blocks.checklist}</span>
            <Score items={checklist.items ?? []} />
          </div>
          <Checklist block={checklist} criteria={criteria} refLabel={refLabeler(blocks)} active={lit?.k ?? null} onSee={see} />
        </div>
      )}
      {!!p.todo?.length && (
        <div className="grid gap-1.5 rounded-[10px] bg-reply-soft px-3.5 py-3">
          <b className="text-[13px] text-seal">{t.blocks.todo(p.todo.length)}</b>
          {p.todo.map((x, k) => (
            <span key={k} className="text-sm break-words text-fg">
              <Linkify text={x} />
            </span>
          ))}
        </div>
      )}
    </div>
  )
  const folds = (p.details || !!p.aside?.length || raw) && (
        <div className="min-w-0 [grid-area:folds]">
          {p.details && (
            <Fold title={t.blocks.details}>
              <Md text={p.details} className="text-sm" />
            </Fold>
          )}
          {!!p.aside?.length && (
            <Fold title={t.blocks.aside(p.aside.length)}>
              {p.aside.map((x, k) => (
                <p key={k} className="break-words">
                  <Linkify text={x} />
                </p>
              ))}
            </Fold>
          )}
          {raw && (
            <Fold title={t.blocks.raw([...raw].length)}>
              <Md text={raw} className="text-sm" />
            </Fold>
          )}
        </div>
  )
  const right = (side.length > 0 || extra.length > 0) && (
    <div className="min-w-0 space-y-6 [grid-area:side] @min-[680px]:sticky @min-[680px]:top-5 @min-[680px]:max-h-[100cqh] @min-[680px]:self-start @min-[680px]:overflow-y-auto @min-[680px]:overscroll-contain @min-[680px]:pr-1 @min-[680px]:[scrollbar-width:thin]">
      {side.map((b) => (
        <BlockView key={b.id} block={b} figNo={figs[b.id]} lit={lit?.id === b.id} criteria={criteria} limit className={shots.includes(b) ? '@max-[680px]:hidden' : undefined} />
      ))}
      {extra.length > 0 && (
        <div className="space-y-1.5">
          <span className="block text-xs tracking-[0.06em] text-fg-3">{t.blocks.attachments(extra.length)}</span>
          <Thumbs files={extra} />
        </div>
      )}
    </div>
  )
  return (
    <div ref={box} className="@container space-y-3">
      {p.verdict && (
        <p className="font-display text-[17px] leading-normal font-semibold text-balance break-words text-fg @min-[680px]:text-[19px]">
          <Linkify text={p.verdict} />
        </p>
      )}
      {shots.length > 0 && (
        <div className="-mx-1 flex snap-x gap-2 overflow-x-auto px-1 pb-1 @min-[680px]:hidden">
          {shots.map((b) => (
            <Lightbox key={b.id} f={b.image!} group={shots.map((x) => x.image!)}>
              <button type="button" data-block={b.id} className={cx('relative shrink-0 snap-start overflow-hidden rounded-[10px] border bg-sunken outline-2 outline-offset-2 transition-[outline-color]', lit?.id === b.id ? 'border-accent outline-accent' : 'border-line outline-transparent')}>
                <img src={fileUrl(b.image!.id)} alt={b.caption || b.image!.name} className={cx('block h-28 w-auto max-w-[78cqw] object-cover object-top-left', shots.length === 1 && 'max-w-full')} />
                <span className="absolute bottom-1.5 left-1.5 rounded-[5px] bg-black/55 px-1.5 py-px text-[11px] font-semibold text-white">{t.blocks.figure(figs[b.id])}</span>
              </button>
            </Lightbox>
          ))}
        </div>
      )}
      <div
        className={cx(
          "grid gap-4 [grid-template-areas:'main'_'side'_'folds']",
          right && "@min-[680px]:grid-cols-[minmax(0,1.05fr)_minmax(0,1fr)] @min-[680px]:grid-rows-[auto_1fr] @min-[680px]:gap-x-5 @min-[680px]:[grid-template-areas:'main_side'_'folds_side']",
        )}
      >
        {main}
        {right}
        {folds}
      </div>
    </div>
  )
}

// EvidenceGist：一行要点（验收卡、列表里用）：结论加达标小方块。
export function EvidenceGist({ e }: { e: Evidence }) {
  const p = e.payload ?? {}
  const c = p.blocks?.find((b) => b.type === 'checklist')
  return (
    <span className="inline">
      <Linkify text={p.verdict || e.summary} />
      {c && <Score items={c.items ?? []} className="ml-2 align-[-1px]" />}
    </span>
  )
}

// LegacyEvidence：没有结构化内容的证据，照原文（Markdown）显示。big 时（验收视图）截图放大：宽处在右栏，窄处排成横向胶片。
export function LegacyEvidence({ e, big }: { e: Evidence; big?: boolean }) {
  const files = e.files ?? []
  const imgs = files.filter((f) => f.mime.startsWith('image/'))
  const rest = files.filter((f) => !imgs.includes(f))
  if (!big || !imgs.length)
    return (
      <div className="space-y-2">
        <Md text={e.summary} className="text-sm" />
        <Thumbs files={files} />
      </div>
    )
  return (
    <div className="@container">
      <div className="grid gap-4 @min-[680px]:grid-cols-[minmax(0,1.05fr)_minmax(0,1fr)] @min-[680px]:gap-x-5">
        <div className="-mx-1 flex snap-x gap-2 overflow-x-auto px-1 pb-1 @min-[680px]:hidden">
          {imgs.map((f, k) => (
            <Lightbox key={f.id} f={f} group={imgs}>
              <button type="button" className="relative shrink-0 snap-start overflow-hidden rounded-[10px] border border-line bg-sunken">
                <img src={fileUrl(f.id)} alt={f.name} className={cx('block h-28 w-auto max-w-[78cqw] object-cover object-top-left', imgs.length === 1 && 'max-w-full')} />
                <span className="absolute bottom-1.5 left-1.5 rounded-[5px] bg-black/55 px-1.5 py-px text-[11px] font-semibold text-white">{t.blocks.figure(k + 1)}</span>
              </button>
            </Lightbox>
          ))}
        </div>
        <div className="min-w-0 space-y-2">
          <Md text={e.summary} className="text-sm" />
          <Thumbs files={rest} />
        </div>
        <div className="min-w-0 space-y-4 @max-[680px]:hidden">
          {imgs.map((f, k) => (
            <BlockView key={f.id} block={{ id: f.id, type: 'figure', image: f }} figNo={k + 1} limit />
          ))}
        </div>
      </div>
    </div>
  )
}
