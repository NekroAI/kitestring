import { useState } from 'react'
import { ArrowRightLeft, Ban } from 'lucide-react'
import type { Decision } from '../api/client'
import { useRequirement } from '../api/queries'
import { t } from '../i18n'
import { cx } from '../lib/util'
import { Badge } from '../ui/base'
import { Md, plainText } from '../ui/markdown'
import { Linkify, RefChip } from '../ui/refmark'

// Agent 申请改需求状态（R-188）：请求类审批（request_kind = status）。「要你处理」的卡片和审批抽屉都要让你一眼看到
// 是哪条需求（编号、标题、一句话说明）、从什么改成什么、为什么；抽屉里可以展开需求原文，不用离开去需求池找。
// 用词按 R-176 请求类场景：批准 / 拒绝（拒绝 = 需求保持原状态）。批准前状态被改动过的申请自动作废，抽屉里显示已作废。

// 状态小标签的色调（与需求时间线 R-153 一致）
export const statusTone: Record<string, 'neutral' | 'accent' | 'run' | 'wait' | 'danger'> = {
  draft: 'neutral',
  ready: 'accent',
  in_progress: 'run',
  blocked: 'danger',
  review: 'wait',
  done: 'run',
  shelved: 'neutral',
}

export const isStatusReq = (d?: Decision | null) => d?.request_kind === 'status'

// firstLine：一句话说明——需求描述的第一行（去掉 Markdown）。
export const firstLine = (s?: string) =>
  plainText(s ?? '')
    .split('\n')
    .map((x) => x.trim())
    .find(Boolean) ?? ''

// StatusArrow：「草稿 → 待开始」两枚小标签。
export function StatusArrow({ from, to, className }: { from?: string; to?: string; className?: string }) {
  return (
    <span className={cx('inline-flex shrink-0 items-center gap-1 font-normal', className)}>
      {from && <Badge tone={statusTone[from]}>{t.reqStatus[from] ?? from}</Badge>}
      {from && <span className="text-fg-3">→</span>}
      {to && <Badge tone={statusTone[to]}>{t.reqStatus[to] ?? to}</Badge>}
    </span>
  )
}

// StatusReqLines：「要你处理」卡片的下两行——一句话说明；当前状态 → 目标状态 + 理由。
export function StatusReqLines({ d }: { d: Decision }) {
  const q = useRequirement(d.requirement_code ?? null)
  const brief = firstLine(q.data?.detail)
  return (
    <>
      <span className="min-w-0 truncate text-[12.5px] leading-5 text-fg-3">{q.data ? brief || t.ask.status.noDetail : '…'}</span>
      <span className="flex min-w-0 items-center gap-1.5 text-[12.5px] leading-5 text-fg-2">
        <StatusArrow from={d.request_from} to={d.request_to} className="[&>span]:py-0 [&>span]:text-[11px]" />
        <span className="min-w-0 flex-1 truncate">
          <span className="text-fg-3">{t.ask.status.why}</span>
          {d.context}
        </span>
      </span>
    </>
  )
}

// StatusReqNote：审批抽屉里的申请内容——需求（编号、标题、一句话说明，可展开原文）、状态变化、理由、批准 / 拒绝的后果。
export function StatusReqNote({ d }: { d: Decision }) {
  const q = useRequirement(d.requirement_code ?? null)
  const r = q.data
  const [open, setOpen] = useState(false)
  const brief = firstLine(r?.detail)
  const from = t.reqStatus[d.request_from ?? ''] ?? d.request_from
  const to = t.reqStatus[d.request_to ?? ''] ?? d.request_to
  return (
    <div className="space-y-3">
      <div data-status-req className="space-y-2.5 rounded-xl border border-line bg-surface px-3.5 py-3">
        <div className="flex min-w-0 items-center gap-2">
          {d.requirement_code && <RefChip code={d.requirement_code} />}
          <span className="min-w-0 flex-1 truncate text-[15px] font-semibold text-fg">{r?.title ?? d.requirement_title}</span>
        </div>
        {r && <p className="text-sm leading-relaxed text-fg-2">{brief || t.ask.status.noDetail}</p>}
        {r && (r.detail || r.acceptance) && (
          <div>
            <button type="button" onClick={() => setOpen(!open)} className="text-[13px] font-medium text-accent hover:underline">
              {open ? t.ask.status.hideFull : t.ask.status.showFull}
            </button>
            {open && (
              <div className="mt-2 space-y-2 rounded-lg bg-sunken/60 px-3 py-2.5 text-sm leading-relaxed text-fg-2">
                {r.detail && <Md text={r.detail} className="break-words" />}
                {r.acceptance && (
                  <div>
                    <div className="mb-0.5 text-xs font-medium text-fg-3">{t.ask.status.acceptance}</div>
                    <Md text={r.acceptance} className="break-words" />
                  </div>
                )}
              </div>
            )}
          </div>
        )}
        <div className="flex flex-wrap items-center gap-2 border-t border-line pt-2.5 text-sm">
          <ArrowRightLeft className="size-4 shrink-0 text-fg-3" />
          <span className="text-fg-3">{t.ask.status.change}</span>
          <StatusArrow from={d.request_from} to={d.request_to} />
          {r && r.status !== d.request_from && !d.voided && <span className="text-xs text-danger">{t.ask.status.changedNow(t.reqStatus[r.status] ?? r.status)}</span>}
        </div>
        {d.context && (
          <p className="text-sm leading-relaxed break-words text-fg">
            <span className="mr-1.5 text-fg-3">{t.ask.status.why}</span>
            <Linkify text={d.context} />
          </p>
        )}
      </div>
      {d.voided ? (
        <VoidedNote d={d} />
      ) : (
        <p className="text-[13px] leading-relaxed text-fg-3">{t.ask.status.rule(from ?? '', to ?? '')}</p>
      )}
    </div>
  )
}

// VoidedNote：申请已作废（批准前需求状态被改动过）。
export function VoidedNote({ d }: { d: Decision }) {
  return (
    <div data-voided className="flex items-start gap-2.5 rounded-xl border border-line-strong bg-sunken/70 px-3 py-2.5 text-sm">
      <Ban className="mt-0.5 size-4 shrink-0 text-fg-3" />
      <div className="min-w-0 flex-1 leading-relaxed">
        <div className="font-medium text-fg">{t.ask.status.voided}</div>
        <div className="text-[13px] text-fg-2">{t.ask.status.voidedWhy(t.reqStatus[d.request_from ?? ''] ?? d.request_from ?? '')}</div>
      </div>
    </div>
  )
}
