import { useState, type ReactNode } from 'react'
import { ChevronRight } from 'lucide-react'
import type { Decision } from '../api/client'
import { t } from '../i18n'
import { cx } from '../lib/util'
import { fileUrl, ZoomButton } from '../ui/attach'
import { Linkify } from '../ui/refmark'

// 多点决策（R-112，原型 ④）：一个方案里有几个点要决策时，每个点单独一组选项（R-97 的 options 块），默认选推荐，可以分别选。

// currentPicks：没动过的点默认选推荐（没有推荐时为 -1，要用户自己选）。
export function currentPicks(d: Decision, picks?: number[]) {
  return (d.points ?? []).map((p, i) => picks?.[i] ?? (p.items ?? []).findIndex((o) => o.recommended))
}

// picksText：发送前遮罩上显示的一行（批准 · 无身份 / 2 次 · 附言）。
export function picksText(d: Decision, picks: number[], note: string) {
  const labels = (d.points ?? []).map((p, i) => p.items?.[picks[i]]?.label ?? '?').join(' / ')
  return [d.approval ? t.decision.approve : '', labels, note.trim()].filter(Boolean).join(' · ')
}

export function Points({ d, picks, onPick, disabled }: { d: Decision; picks: number[]; onPick: (point: number, option: number) => void; disabled?: boolean }) {
  return (
    <div className="grid gap-2.5">
      {(d.points ?? []).map((p, i) => (
        <fieldset key={p.id} className="m-0 grid min-w-0 gap-2 rounded-xl border border-line bg-surface-2 p-3 sm:p-3.5">
          <legend className="float-left mb-0.5 w-full p-0 text-sm font-semibold text-fg">
            <span className="mr-1 text-fg-3 tabular">{i + 1}.</span>
            <Linkify text={p.title ?? ''} />
          </legend>
          <div className="flex flex-wrap gap-2" role="radiogroup" aria-label={p.title}>
            {(p.items ?? []).map((o, k) => {
              const on = picks[i] === k
              return (
                <div
                  key={k}
                  role="radio"
                  aria-checked={on}
                  aria-disabled={disabled}
                  tabIndex={0}
                  onClick={() => !disabled && onPick(i, k)}
                  onKeyDown={(e) => (e.key === 'Enter' || e.key === ' ') && (e.preventDefault(), !disabled && onPick(i, k))}
                  className={cx(
                    'group/pt relative grid min-w-0 flex-[1_1_200px] cursor-pointer content-start gap-0.5 overflow-hidden rounded-[10px] border bg-surface px-3 py-2.5 text-left transition-[border,box-shadow] duration-150 outline-none focus-visible:ring-2 focus-visible:ring-accent/40',
                    on ? 'border-accent shadow-[inset_0_0_0_1px_var(--accent)]' : 'border-line-strong hover:border-fg-3/50',
                  )}
                >
                  {o.image && (
                    <div className="relative -mx-3 -mt-2.5 mb-2 aspect-[16/10] overflow-hidden border-b border-line bg-sunken">
                      <img src={fileUrl(o.image.id)} alt={o.label} loading="lazy" className="size-full object-cover object-top" />
                      <span className="absolute top-2 right-2 opacity-0 transition group-hover/pt:opacity-100 pointer-coarse:opacity-100" onClick={(e) => e.stopPropagation()}>
                        <ZoomButton f={o.image} />
                      </span>
                    </div>
                  )}
                  <span className="flex min-w-0 items-center gap-1.5 text-sm font-semibold text-fg">
                    <span className={cx('flex size-4 shrink-0 items-center justify-center rounded-full border transition-colors', on ? 'border-accent bg-accent' : 'border-line-strong')}>
                      {on && <span className="size-1.5 rounded-full bg-accent-fg" />}
                    </span>
                    <span className="min-w-0 break-words">{o.label}</span>
                    {o.recommended && <span className="shrink-0 rounded bg-run-soft px-1.5 py-px text-[11px] font-bold text-run">{t.blocks.recommended}</span>}
                  </span>
                  {o.desc && (
                    <span className="pl-5.5 text-[13px] break-words text-fg-3">
                      <Linkify text={o.desc} />
                    </span>
                  )}
                </div>
              )
            })}
          </div>
        </fieldset>
      ))}
    </div>
  )
}

// PlanSummary：方案要点（每条需求一行，编号自动画成印记），完整说明折叠在下面。
export function PlanSummary({ lines, full }: { lines: string[]; full?: ReactNode }) {
  const [open, setOpen] = useState(false)
  return (
    <div className="space-y-1">
      {lines.map((x, k) => (
        <p key={k} className="text-sm leading-relaxed break-words text-fg-2">
          <Linkify text={x} />
        </p>
      ))}
      {full && (
        <div>
          <button onClick={() => setOpen(!open)} className="inline-flex items-center gap-1 text-[13px] font-medium text-accent hover:underline">
            <ChevronRight className={cx('size-3.5 transition-transform', open && 'rotate-90')} />
            {t.decision.fullPlan}
          </button>
          {open && <div className="mt-1.5 rounded-xl bg-surface-2 px-3 py-2.5">{full}</div>}
        </div>
      )}
    </div>
  )
}
