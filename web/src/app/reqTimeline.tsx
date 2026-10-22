import { Fragment, type ReactNode } from 'react'
import { CheckCheck, FileCheck2, HelpCircle, Sparkles, Undo2 } from 'lucide-react'
import type { Decision, Requirement } from '../api/client'
import { t } from '../i18n'
import { absTime, cx, relTime, useNow } from '../lib/util'
import { Badge, Tip } from '../ui/base'
import { Thumbs } from '../ui/attach'
import { evidenceKind } from './review'

// 需求进展：创建、每一轮的证据、你的修改意见、相关决策、验收，按时间排成一条线。
// 每次退回修改开启新的一轮，复杂需求反复几轮也能看清来龙去脉。

type Entry = { at: string; key: string; round: number; node: ReactNode }

const tone = {
  create: 'bg-idle-soft text-idle',
  evidence: 'bg-run-soft text-run',
  revise: 'bg-reply-soft text-reply',
  decision: 'bg-accent-soft text-accent',
  done: 'bg-run text-seal-fg',
}

function Row({ icon, tone: cls, title, at, now, muted, children }: { icon: ReactNode; tone: string; title: ReactNode; at: string; now: number; muted?: boolean; children?: ReactNode }) {
  return (
    <li className="relative flex gap-3 pb-5 last:pb-0 [&:last-child>.tl-line]:hidden">
      <span aria-hidden className="tl-line absolute top-8 bottom-0 left-[13px] w-px bg-line" />
      <span className={cx('relative z-[1] mt-0.5 flex size-7 shrink-0 items-center justify-center rounded-full ring-4 ring-surface [&_svg]:size-3.5', cls)}>{icon}</span>
      <div className={cx('min-w-0 flex-1', muted && 'opacity-60')}>
        <div className="flex items-baseline justify-between gap-3">
          <div className="min-w-0 text-[13px] font-medium text-fg-2">{title}</div>
          <Tip content={absTime(at)}>
            <span className="shrink-0 text-xs text-fg-3 tabular">{relTime(at, now)}</span>
          </Tip>
        </div>
        {children && <div className="mt-1.5 space-y-2">{children}</div>}
      </div>
    </li>
  )
}

export function ReqTimeline({ r, decisions }: { r: Requirement; decisions: Decision[] }) {
  const now = useNow()
  const feedback = [...(r.feedback ?? [])].sort((a, b) => (a.answered_at ?? a.created_at).localeCompare(b.answered_at ?? b.created_at))
  const bounds = feedback.map((f) => f.answered_at ?? f.created_at)
  const roundOf = (at: string) => 1 + bounds.filter((b) => b <= at).length

  const entries: Entry[] = [
    {
      at: r.created_at,
      key: 'create',
      round: 1,
      node: <Row icon={<Sparkles />} tone={tone.create} title={r.created_by === 'agent' ? t.timeline.createdAgent : t.timeline.createdUser} at={r.created_at} now={now} />,
    },
  ]
  for (const e of r.evidence ?? []) {
    entries.push({
      at: e.created_at,
      key: e.id,
      round: roundOf(e.created_at),
      node: (
        <Row icon={<FileCheck2 />} tone={tone.evidence} title={<span className="flex items-center gap-2">{t.timeline.evidence}<Badge tone="run">{evidenceKind(e.kind)}</Badge></span>} at={e.created_at} now={now} muted={e.voided}>
          <p className="text-sm leading-relaxed break-words text-fg">{e.summary}</p>
          {e.link && <p className="truncate font-mono text-xs text-fg-3">{e.link}</p>}
          {!!e.files?.length && <Thumbs files={e.files} />}
        </Row>
      ),
    })
  }
  for (const f of feedback) {
    const at = f.answered_at ?? f.created_at
    entries.push({
      at,
      key: f.id,
      // 修改意见收尾它所在的那一轮
      round: roundOf(at) - 1,
      node: (
        <Row
          icon={<Undo2 />}
          tone={tone.revise}
          title={
            <span className="flex items-center gap-2">
              {t.timeline.revise}
              <Badge tone={f.status === 'delivered' ? 'run' : 'reply'}>{t.decision.status[f.status]}</Badge>
            </span>
          }
          at={at}
          now={now}
        >
          {f.answer && <p className="rounded-xl bg-reply-soft/60 px-3 py-2 text-sm leading-relaxed whitespace-pre-wrap text-fg">{f.answer}</p>}
          {!!f.answer_files?.length && <Thumbs files={f.answer_files} size="sm" />}
        </Row>
      ),
    })
  }
  for (const d of decisions.filter((x) => x.status !== 'pending' && x.kind !== 'feedback')) {
    entries.push({
      at: d.created_at,
      key: d.id,
      round: roundOf(d.created_at),
      node: (
        <Row icon={<HelpCircle />} tone={tone.decision} title={<span className="flex items-center gap-2">{t.timeline.decision}<span className="font-mono text-[11px] text-fg-3">{d.code}</span></span>} at={d.created_at} now={now}>
          <p className="text-sm text-fg">{d.short_title || d.question}</p>
          {d.answer && (
            <p className="text-sm text-fg-2">
              <span className="mr-1.5 text-fg-3">{t.timeline.answer}</span>
              {d.answer}
            </p>
          )}
        </Row>
      ),
    })
  }
  if (r.status === 'done') {
    entries.push({ at: r.updated_at, key: 'done', round: 1 + feedback.length, node: <Row icon={<CheckCheck />} tone={tone.done} title={t.timeline.done} at={r.updated_at} now={now} /> })
  }
  entries.sort((a, b) => a.at.localeCompare(b.at))

  const rounds = feedback.length > 0
  const groups: { round: number; items: Entry[] }[] = []
  for (const e of entries) {
    const g = groups[groups.length - 1]
    if (g && g.round === e.round) g.items.push(e)
    else groups.push({ round: e.round, items: [e] })
  }

  return (
    <div className="space-y-4">
      {groups.map((g, i) => (
        <div key={i}>
          {rounds && (
            <div className="mb-3 flex items-center gap-2 text-xs font-medium text-fg-3">
              <span className="font-display text-sm text-fg-2">{t.timeline.round(g.round)}</span>
              {g.round === 1 + feedback.length && r.status !== 'done' && <Badge tone="accent">{t.timeline.current}</Badge>}
              <span aria-hidden className="h-0 flex-1 border-t border-dashed border-line-strong/80" />
            </div>
          )}
          <ol>
            {g.items.map((e) => (
              <Fragment key={e.key}>{e.node}</Fragment>
            ))}
          </ol>
        </div>
      ))}
    </div>
  )
}
