import type { ReactNode } from 'react'
import { ChevronRight } from 'lucide-react'
import type { Decision, Evidence, Requirement } from '../api/client'
import { t } from '../i18n'
import { absTime, relTime, useNow } from '../lib/util'
import { Badge, Tip } from '../ui/base'
import { Linkify } from '../ui/refmark'
import { DecisionComposer } from './DecisionCard'
import { EvidenceView, hasPayload, LegacyEvidence, VoidedEvidence } from './evidence'
import { ReqTimeline } from './reqTimeline'
import { evidenceKind } from './review'

// 验收视图（R-111，原型 ③）：待验收的需求打开时，第一屏只放这一轮的证据——结论、验收清单、放大的截图、要你做的；
// 批准 / 退回在抽屉底部。同一轮里更早的证据、之前几轮（“第 1 轮（已退回：原因）”）和全部记录都折叠在下面。

function Fold({ title, children, hint }: { title: ReactNode; hint?: ReactNode; children: ReactNode }) {
  return (
    <details className="group border-t border-line">
      <summary className="flex cursor-pointer list-none items-center gap-2 py-2.5 text-sm text-fg-2 [&::-webkit-details-marker]:hidden">
        <ChevronRight className="size-3.5 shrink-0 text-fg-3 transition-transform group-open:rotate-90" />
        <span className="min-w-0 flex-1 truncate">{title}</span>
        {hint && <span className="shrink-0 text-xs text-fg-3">{hint}</span>}
      </summary>
      <div className="space-y-4 pb-4 pl-5.5">{children}</div>
    </details>
  )
}

// rounds：按退回意见把证据分轮（与时间线的分轮一致：每次退回开启新的一轮）。
export function rounds(r: Requirement) {
  const revises = [...(r.feedback ?? [])].filter((f) => f.kind === 'feedback').sort((a, b) => (a.answered_at ?? a.created_at).localeCompare(b.answered_at ?? b.created_at))
  const bounds = revises.map((f) => f.answered_at ?? f.created_at)
  const roundOf = (at: string) => 1 + bounds.filter((b) => b <= at).length
  // 退回时那一轮的证据会被作废；作废的仍属于它那一轮（旧轮里照常显示），只是不算本轮的
  const evidence = r.evidence ?? []
  const current = revises.length + 1
  const byRound = (k: number) => evidence.filter((e) => roundOf(e.created_at) === k && (k < current || !e.voided))
  // Agent 主动作废（有理由）的证据，本轮也不当主证据；折叠成一行留在时间线里（R-189）
  return { revises, current, byRound }
}

function OneEvidence({ e, r, wide }: { e: Evidence; r: Requirement; wide?: boolean }) {
  const body = hasPayload(e) ? <EvidenceView e={e} acceptance={r.acceptance} /> : <LegacyEvidence e={e} big={wide} />
  return e.voided && e.void_reason ? <VoidedEvidence e={e}>{body}</VoidedEvidence> : body
}

export function AcceptView({ r, decisions }: { r: Requirement; decisions: Decision[] }) {
  const now = useNow()
  const { revises, current, byRound } = rounds(r)
  const cur = byRound(current)
  const main = cur[cur.length - 1]
  const earlier = cur.slice(0, -1).reverse()
  const pending = decisions.filter((d) => d.kind === 'decision' && d.status === 'pending')
  return (
    <div data-accept className="space-y-3 sm:space-y-4">
      {/* 手机上把第一屏留给证据：轮次在下面“第 n 轮（已退回…）”里也能看出来 */}
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-fg-3 max-sm:hidden">
        <span className="font-display text-[13px] text-fg-2">{t.timeline.round(current)}</span>
        {main && (
          <>
            <Badge tone="run">{evidenceKind(main.kind)}</Badge>
            <Tip content={absTime(main.created_at)}>
              <span className="tabular">{relTime(main.created_at, now)}</span>
            </Tip>
          </>
        )}
      </div>
      {main ? <OneEvidence e={main} r={r} wide /> : <p className="text-sm text-fg-3">{t.review.noEvidence}</p>}
      {main?.link && <p className="truncate font-mono text-xs text-fg-3">{main.link}</p>}

      {pending.map((d) => (
        <DecisionComposer key={d.id} d={d} embedded />
      ))}

      <div>
        {earlier.length > 0 && (
          <Fold title={t.accept.earlier(earlier.length)}>
            {earlier.map((e) => (
              <div key={e.id} className="space-y-1.5">
                <div className="text-xs text-fg-3">
                  {evidenceKind(e.kind)} · {relTime(e.created_at, now)}
                </div>
                <OneEvidence e={e} r={r} />
              </div>
            ))}
          </Fold>
        )}
        {revises
          .map((f, i) => ({ f, k: i + 1 }))
          .reverse()
          .map(({ f, k }) => {
            const evs = byRound(k)
            return (
              <Fold key={f.id} title={t.accept.oldRound(k, firstLine(f.answer))} hint={relTime(f.answered_at ?? f.created_at, now)}>
                {evs.length ? (
                  evs.map((e) => <OneEvidence key={e.id} e={e} r={r} />)
                ) : (
                  <p className="text-sm text-fg-3">{t.review.noEvidence}</p>
                )}
                <div className="rounded-xl bg-reply-soft px-3 py-2 text-sm text-fg">
                  <span className="mr-1.5 text-reply">{t.accept.revisedWith}</span>
                  <Linkify text={f.answer} />
                </div>
              </Fold>
            )
          })}
        <Fold title={t.accept.all}>
          <ReqTimeline r={r} decisions={decisions.filter((d) => !pending.includes(d))} />
        </Fold>
      </div>
    </div>
  )
}

const firstLine = (s: string) => {
  const line = (s || '').trim().split('\n')[0]
  return [...line].length > 40 ? [...line].slice(0, 39).join('') + '…' : line
}
