import { useLayoutEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { ChevronRight } from 'lucide-react'
import type { BlockItem, Evidence, Requirement } from '../api/client'
import { t } from '../i18n'
import { absTime, cx, relTime, useNow } from '../lib/util'
import { checklistOf, citesOf, cropLayout, explicitMode, itemNo, otherFiles, proofsOf, shotsOf, splitLegacy, type Box, type Proof, type Shot } from '../lib/proofs'
import { Badge, Tip } from '../ui/base'
import { fileUrl, thumbUrl, ThumbImg, isVideo, Lightbox, Thumbs, type ViewShot } from '../ui/attach'
import { criteriaOf, ResultIcon, Score } from '../ui/blocks'
import { shotTitle } from '../ui/shots'
import { ProtoList } from '../ui/proto'
import { Md } from '../ui/markdown'
import { Linkify } from '../ui/refmark'
import { evidenceKind } from './review'
import { Who } from './who'

// 需求抽屉里的一份证据（R-195）。时间线上每份证据只出现一次：本轮最新的一份展开（EvidenceCard），其余收成一行（EvidenceRow），点开就地展开。
// 展开后从上到下：谁交的 → 一句结论 → 验收清单（每条右边是从截图里裁出的「证据小图」，点这条展开大切片）→ 要你做的
// → 截图图库（缩略图 + 标题，角上标「对比」「几处标注」；抽屉里不画框，框和前后对比都在看图里）→ 顺带改动、技术细节（折叠）。
// 老格式证据（一段文字加几张截图）：第一句当结论，正文太长时收起；截图同样排成图库。

export const hasPayload = (e: Pick<Evidence, 'payload'>) => !!(e.payload && (e.payload.verdict || e.payload.blocks?.length || e.payload.todo?.length))

// useProof：一份证据的截图、清单、每条验收点的证明，以及给看图用的图组（带改前、这张图证明的验收点）。
function useProof(e: Evidence, acceptance?: string) {
  return useMemo(() => {
    const shots = shotsOf(e)
    const items = checklistOf(e)
    const proofs = proofsOf(shots, items)
    const explicit = explicitMode(shots)
    const criteria = criteriaOf(acceptance)
    const group: ViewShot[] = shots.map((s, si) => ({
      ...s.file,
      boxes: s.boxes,
      before: s.before ?? null,
      cites: items.length ? citesOf(si, items, proofs, criteria) : undefined,
      explicit,
    }))
    return {
      shots,
      items,
      proofs,
      criteria,
      group,
      rest: otherFiles(e, shots),
    }
  }, [e, acceptance])
}

// ---------- 证据小图与切片 ----------

// Crop：从截图里裁出一个框（box 为空时显示整张图的顶部）。outline：在切片上把框描出来。
// 小预览用 960 档缩略图（框按比例算，布局不受影响）；点开的大切片按原图像素定「看得清」的缩放，才加载原图（R-246）
function Crop({ src, box, legible, outline, className }: { src: string; box: Box | null; legible?: boolean; outline?: boolean; className?: string }) {
  const ref = useRef<HTMLSpanElement>(null)
  const [nat, setNat] = useState<{ w: number; h: number } | null>(null)
  const [view, setView] = useState({ w: 0, h: 0 })
  useLayoutEffect(() => {
    const el = ref.current
    if (!el) return
    const measure = () => setView({ w: el.clientWidth, h: el.clientHeight })
    measure()
    const ro = new ResizeObserver(measure)
    ro.observe(el)
    return () => ro.disconnect()
  }, [])
  const L = nat && view.w ? cropLayout(box, nat, view, legible) : null
  return (
    <span ref={ref} className={cx('relative block overflow-hidden bg-sunken', className)}>
      <img
        src={legible ? fileUrl(src) : thumbUrl(src, 960)}
        alt=""
        loading="lazy"
        draggable={false}
        onLoad={(ev) =>
          setNat({
            w: ev.currentTarget.naturalWidth,
            h: ev.currentTarget.naturalHeight,
          })
        }
        className="pointer-events-none absolute max-w-none"
        style={L ? { width: L.imgW, left: L.left, top: L.top } : { width: '100%', left: 0, top: 0 }}
      />
      {outline && L?.ol && (
        <i
          aria-hidden
          className="pointer-events-none absolute rounded-[3px] border-2 border-seal shadow-[0_0_0_1px_rgb(255_255_255/0.7)]"
          style={{
            left: L.ol.left,
            top: L.ol.top,
            width: L.ol.w,
            height: L.ol.h,
          }}
        />
      )}
    </span>
  )
}

// ProofItem：验收清单的一条。右边是证明它的小图（宽处两张、窄处一张），点小图直接进看图放大到那个框；
// 点这一条展开大切片（按看得清的比例，框太宽时从开头显示），再点收起。
function ProofItem({ it, k, ps, shots, group, criteria }: { it: BlockItem; k: number; ps: Proof[]; shots: Shot[]; group: ViewShot[]; criteria: Record<number, string> }) {
  const [open, setOpen] = useState(false)
  const n = itemNo(it, k)
  const text = it.text || criteria[n] || t.blocks.criterion(n)
  const can = ps.length > 0
  const boxOf = (q: Proof) => (q.bi == null ? null : shots[q.si].boxes[q.bi])
  const label = (q: Proof) => {
    const b = boxOf(q)
    return b?.label?.trim() ? b.label : shotTitle(shots[q.si].file)
  }
  return (
    <li data-proof-item={n} className={cx('rounded-[10px] transition-colors', open ? 'bg-hover/70' : can && 'hover:bg-hover/60')}>
      <div
        role={can ? 'button' : undefined}
        tabIndex={can ? 0 : undefined}
        aria-expanded={can ? open : undefined}
        onClick={() => can && setOpen((v) => !v)}
        onKeyDown={(ev) => {
          if (can && (ev.key === 'Enter' || ev.key === ' ')) {
            ev.preventDefault()
            setOpen((v) => !v)
          }
        }}
        className={cx('flex items-start gap-2.5 rounded-[10px] px-2 py-2 outline-none focus-visible:ring-2 focus-visible:ring-accent/40', can && 'cursor-pointer')}
      >
        <ResultIcon result={it.result} className="mt-[3px]" />
        <span className="min-w-0 flex-1">
          <span className="block text-sm leading-normal font-medium break-words text-fg">
            {n}. <Linkify text={text} />
          </span>
          {it.why && (
            <span className="mt-0.5 block text-[12.5px] leading-relaxed break-words text-fg-3">
              <Linkify text={it.why} />
            </span>
          )}
        </span>
        {can && !open && (
          <span className="flex shrink-0 gap-1.5">
            {ps.slice(0, 2).map((q, i) => (
              <Lightbox key={i} f={group[q.si]} group={group} focus={q.bi}>
                <button
                  type="button"
                  title={label(q)}
                  aria-label={t.proof.cropLabel(q.si + 1, q.bi == null ? null : q.bi + 1)}
                  className={cx('relative block overflow-hidden rounded-lg border border-line-strong outline-2 outline-offset-1 outline-transparent transition hover:outline-accent', i > 0 && '@max-[520px]:hidden')}
                >
                  <Crop src={shots[q.si].file.id} box={boxOf(q)} className="h-[60px] w-24 @max-[520px]:h-10 @max-[520px]:w-[58px]" />
                  <span className="absolute bottom-[3px] left-[3px] rounded-[4px] bg-black/60 px-1 text-[10.5px] leading-[15px] text-white tabular">{t.proof.cropLabel(q.si + 1, q.bi == null ? null : q.bi + 1)}</span>
                </button>
              </Lightbox>
            ))}
          </span>
        )}
        {can && <ChevronRight aria-hidden className={cx('mt-[3px] size-3.5 shrink-0 text-fg-3 transition-transform', open && 'rotate-90')} />}
      </div>
      {open && (
        <div className="grid grid-cols-[repeat(auto-fit,minmax(240px,1fr))] gap-2.5 px-2 pb-2.5 pl-8.5 @max-[520px]:grid-cols-1 @max-[520px]:pl-2">
          {ps.map((q, i) => (
            <Lightbox key={i} f={group[q.si]} group={group} focus={q.bi}>
              <button type="button" className="group/slice block min-w-0 self-start text-left">
                <span className="relative block overflow-hidden rounded-[10px] border border-line-strong outline-2 outline-offset-1 outline-transparent transition group-hover/slice:outline-accent">
                  <Crop src={shots[q.si].file.id} box={boxOf(q)} legible outline className="h-[190px] @max-[520px]:h-[150px]" />
                  <span className="absolute right-1.5 bottom-1.5 rounded-[5px] bg-black/60 px-1.5 text-[11px] leading-[18px] text-white">{t.proof.zoom}</span>
                </span>
                <span className="mt-1 block text-xs leading-snug break-words text-fg-3">
                  <b className="font-medium text-fg-2">{t.proof.sliceLabel(q.si + 1, q.bi == null ? null : q.bi + 1)}</b> {label(q)}
                </span>
              </button>
            </Lightbox>
          ))}
        </div>
      )}
    </li>
  )
}

// ---------- 图库 ----------

function Gallery({ shots, group }: { shots: Shot[]; group: ViewShot[] }) {
  if (!shots.length) return null
  return (
    <section>
      <div className="mb-1.5 text-xs tracking-[0.04em] text-fg-3">{t.proof.shots(shots.length)}</div>
      <div className="grid grid-cols-[repeat(auto-fill,minmax(200px,1fr))] gap-3 @max-[520px]:grid-cols-2 @max-[520px]:gap-2">
        {shots.map((s, si) => (
          <Lightbox key={s.id} f={group[si]} group={group}>
            <button type="button" className="group/shot block min-w-0 self-start text-left" aria-label={shotTitle(s.file)}>
              <span className="relative block aspect-[16/10] overflow-hidden rounded-[10px] border border-line bg-sunken outline-2 outline-offset-1 outline-transparent transition group-hover/shot:outline-accent">
                {isVideo(s.file.mime) ? (
                  <video src={`${fileUrl(s.file.id)}#t=0.1`} muted playsInline preload="metadata" className="size-full object-cover object-left-top" />
                ) : (
                  <ThumbImg id={s.file.id} alt="" loading="lazy" className="size-full object-cover object-left-top" />
                )}
                <span className="absolute top-1.5 left-1.5 flex flex-wrap gap-1 text-[11px] leading-[17px] text-white">
                  <span className="rounded-[5px] bg-black/60 px-1.5 tabular">{t.proof.figure(si + 1)}</span>
                  {s.before && <span className="rounded-[5px] bg-black/60 px-1.5">{t.proof.compare}</span>}
                  {s.boxes.length > 0 && <span className="rounded-[5px] bg-seal px-1.5 text-seal-fg">{t.proof.marks(s.boxes.length)}</span>}
                </span>
              </span>
              <span className={cx('mt-1.5 line-clamp-2 leading-snug break-words', s.named ? 'text-[12.5px] text-fg-2' : 'font-mono text-[11.5px] text-fg-3')}>{shotTitle(s.file)}</span>
            </button>
          </Lightbox>
        ))}
      </div>
    </section>
  )
}

function Fold({ title, children }: { title: ReactNode; children: ReactNode }) {
  return (
    <details className="group -mx-4 border-t border-line px-4">
      <summary className="flex cursor-pointer list-none items-center gap-1.5 py-2.5 text-[12.5px] text-fg-3 transition hover:text-fg-2 [&::-webkit-details-marker]:hidden">
        <ChevronRight className="size-3.5 shrink-0 transition-transform group-open:rotate-90" />
        {title}
      </summary>
      <div className="space-y-1.5 pb-3 pl-5 text-[13px] leading-relaxed text-fg-2">{children}</div>
    </details>
  )
}

// LegacyBody：老格式证据的正文。第一句当结论，其余正文太长时收起到几行，「展开全文」看完。
function LegacyBody({ text }: { text: string }) {
  const { first, rest } = splitLegacy(text)
  const ref = useRef<HTMLDivElement>(null)
  const [open, setOpen] = useState(false)
  const [long, setLong] = useState(false)
  useLayoutEffect(() => {
    const el = ref.current
    if (el && !open) setLong(el.scrollHeight > el.clientHeight + 4)
  }, [rest, open])
  return (
    <div>
      <p className="font-display text-[17px] leading-normal font-semibold text-balance break-words text-fg @min-[521px]:text-[19px]">
        <Linkify text={first} />
      </p>
      {rest && (
        <>
          <div ref={ref} className={cx('relative mt-1.5', !open && 'max-h-[9.5em] overflow-hidden', !open && long && '[mask-image:linear-gradient(#000_70%,transparent)]')}>
            <Md text={rest} className="text-[13.5px] leading-relaxed text-fg-2" />
          </div>
          {long && (
            <button type="button" onClick={() => setOpen((v) => !v)} className="mt-1 text-[12.5px] text-accent hover:underline">
              {open ? t.proof.less : t.proof.more}
            </button>
          )}
        </>
      )}
    </div>
  )
}

// EvidenceBody：一份证据的内容（不含头部），展开的卡片和作废后点开看原文共用。
export function EvidenceBody({ e, r }: { e: Evidence; r: Requirement }) {
  const { shots, items, proofs, criteria, group, rest } = useProof(e, r.acceptance)
  const p = e.payload ?? {}
  return (
    <div className="space-y-3">
      {hasPayload(e) ? (
        <>
          {p.verdict && (
            <p className="font-display text-[17px] leading-normal font-semibold text-balance break-words text-fg @min-[521px]:text-[19px]">
              <Linkify text={p.verdict} />
            </p>
          )}
          {items.length > 0 && (
            <section>
              <div className="mb-1 flex flex-wrap items-center justify-between gap-2 text-xs tracking-[0.04em] text-fg-3">
                <span>{t.blocks.checklist}</span>
                <Score items={items} />
              </div>
              <ul className="-mx-2 grid gap-0.5">
                {items.map((it, k) => (
                  <ProofItem key={k} it={it} k={k} ps={proofs[k]} shots={shots} group={group} criteria={criteria} />
                ))}
              </ul>
            </section>
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
        </>
      ) : (
        <LegacyBody text={e.summary} />
      )}
      <ProtoList ps={e.prototypes} />
      <Gallery shots={shots} group={group} />
      {rest.length > 0 && (
        <div className="space-y-1.5">
          <span className="block text-xs tracking-[0.04em] text-fg-3">{t.blocks.attachments(rest.length)}</span>
          <Thumbs files={rest} />
        </div>
      )}
      {e.link && <p className="truncate font-mono text-xs text-fg-3">{e.link}</p>}
      {(!!p.aside?.length || p.details) && (
        <div className="pt-1">
          {!!p.aside?.length && (
            <Fold title={t.blocks.aside(p.aside.length)}>
              {p.aside.map((x, k) => (
                <p key={k} className="break-words">
                  <Linkify text={x} />
                </p>
              ))}
            </Fold>
          )}
          {p.details && (
            <Fold title={t.proof.tech}>
              <Md text={p.details} className="text-[13px]" />
            </Fold>
          )}
        </div>
      )}
    </div>
  )
}

const submitter = (e: Evidence) => <Who actor="agent" session={e.session_key} role={e.role} title={e.session_title} className="max-w-[16em]" />
const hasFolds = (e: Evidence) => !!(e.payload?.details || e.payload?.aside?.length)

// EvidenceCard：展开的一份证据。latest：本轮最新（默认展开、不能收起）；其余可以「收起」回一行。
export function EvidenceCard({ e, r, latest, onFold }: { e: Evidence; r: Requirement; latest?: boolean; onFold?: () => void }) {
  const now = useNow()
  return (
    <div data-evidence={e.id} className={cx('@container rounded-[14px] border bg-surface px-4 pt-3.5 shadow-[0_1px_2px_rgb(70_50_18/0.05)]', latest ? 'border-line-strong' : 'border-line', hasFolds(e) ? 'pb-0' : 'pb-3.5')}>
      <div className="mb-2 flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-fg-3">
        {submitter(e)}
        <span>
          {t.proof.submitted} · {evidenceKind(e.kind)}
        </span>
        {latest && <Badge tone="run">{t.proof.latest}</Badge>}
        <Tip content={absTime(e.created_at)}>
          <span className="tabular">{relTime(e.created_at, now)}</span>
        </Tip>
        {onFold && (
          <button type="button" onClick={onFold} className="ml-auto rounded px-1 text-fg-3 transition hover:text-fg">
            {t.proof.collapse}
          </button>
        )}
      </div>
      <EvidenceBody e={e} r={r} />
    </div>
  )
}

// gist：收起时的一行：结构化证据用结论，老格式用第一句。
export const gist = (e: Evidence) => e.payload?.verdict || splitLegacy(e.summary).first

// EvidenceRow：收起的一份证据：谁 · 交了证据 · 较早 / 退回时已作废 · 结论 · 达标 · 几张图 · 时间，点开就地展开。
// 窄处（手机）分两行，结论单独一行。
export function EvidenceRow({ e, onOpen, voidedOnBack }: { e: Evidence; onOpen: () => void; voidedOnBack?: boolean }) {
  const now = useNow()
  const items = checklistOf(e)
  const n = shotsOf(e).length
  return (
    <div className="@container">
      <button
        type="button"
        onClick={onOpen}
        className={cx('flex w-full flex-wrap items-center gap-x-2 gap-y-1 rounded-[10px] border border-line bg-surface-2 px-2.5 py-2 text-left text-[13px] text-fg-2 transition hover:border-line-strong', voidedOnBack && 'opacity-60')}
      >
        <span className="flex w-full min-w-0 items-center gap-2 text-xs @min-[560px]:contents">
          {submitter(e)}
          <span className="shrink-0 text-fg-3">{t.proof.submitted}</span>
          <span className="shrink-0 rounded-[5px] bg-sunken px-1.5 text-[11px] leading-[17px] text-fg-3">{voidedOnBack ? t.proof.voidedOnBack : t.proof.earlier}</span>
          <span className="ml-auto shrink-0 text-xs text-fg-3 tabular @min-[560px]:hidden">{relTime(e.created_at, now)}</span>
        </span>
        <span className="min-w-0 flex-1 truncate">
          <Linkify text={gist(e)} />
        </span>
        {items.length > 0 && <Score items={items} className="shrink-0 text-xs" />}
        {!!e.prototypes?.length && <span className="shrink-0 text-xs text-fg-3">{t.proto.count(e.prototypes.length)}</span>}
        {n > 0 && <span className="shrink-0 text-xs text-fg-3">{t.proof.shotCount(n)}</span>}
        <span className="shrink-0 text-xs text-fg-3 tabular @max-[559px]:hidden">{relTime(e.created_at, now)}</span>
        <ChevronRight aria-hidden className="size-3.5 shrink-0 text-fg-3" />
      </button>
    </div>
  )
}

// VoidedEvidence：Agent 作废的证据（R-189）折成一行「已作废 · 理由」，点开看谁何时作废和原文。
// 验收退回时批量作废的没有理由，不走这里（收成一行，标「退回时已作废」）。
export function VoidedEvidence({ e, r }: { e: Evidence; r: Requirement }) {
  const now = useNow()
  return (
    <details data-void-evidence className="group @container rounded-[10px] border border-line bg-sunken/60 px-2.5 py-2">
      <summary className="flex cursor-pointer list-none flex-wrap items-center gap-x-2 gap-y-1 text-[13px] text-fg-2 [&::-webkit-details-marker]:hidden">
        <ChevronRight className="size-3.5 shrink-0 text-fg-3 transition-transform group-open:rotate-90" />
        <span className="flex min-w-0 items-center gap-2 text-xs">{submitter(e)}</span>
        <span className="min-w-0 flex-1 truncate">{t.review.voidedRow(e.void_reason ?? '')}</span>
        <span className="shrink-0 text-xs text-fg-3 tabular">{relTime(e.created_at, now)}</span>
      </summary>
      <div className="mt-2 space-y-2 opacity-80">
        {e.voided_at && (
          <p className="flex flex-wrap items-center gap-1 text-xs text-fg-3">
            {e.voided_by && <Who actor="agent" session={e.voided_by} role={e.voided_role} />}
            {t.review.voidedBy('', absTime(e.voided_at))}
          </p>
        )}
        <EvidenceBody e={e} r={r} />
      </div>
    </details>
  )
}
