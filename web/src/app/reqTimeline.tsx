import type { ReactNode } from 'react'
import { CheckCheck, FileCheck2, HelpCircle, MessageSquareText, Send, Sparkles, Undo2 } from 'lucide-react'
import type { Decision, Requirement } from '../api/client'
import { t } from '../i18n'
import { absTime, cx, relTime, useNow } from '../lib/util'
import { Badge, Tip } from '../ui/base'
import { Thumbs } from '../ui/attach'
import { Item, List } from '../ui/motion'
import { DecisionComposer } from './DecisionCard'
import { evidenceKind } from './review'
import { useSessions } from '../api/queries'
import { sessionTitle } from './SessionCard'

// 需求进展：一条竖向时间线，从上往下按时间排，最新在最下面（紧挨底部的输入区）。
// 每次退回修改开启新的一轮，复杂需求反复几轮也能看清来龙去脉；留言不开新一轮。

type Entry = { at: string; key: string; round: number; node: ReactNode }

const tone = {
  create: 'bg-sunken text-fg-3',
  evidence: 'bg-run-soft text-run',
  revise: 'bg-reply-soft text-reply',
  note: 'bg-accent-soft text-accent',
  decision: 'bg-accent-soft text-accent',
  ask: 'bg-reply text-seal-fg',
  done: 'bg-run text-seal-fg',
}

// Node：时间线上的一个节点。圆点在左侧竖线上，内容占满右侧。
function Node({ icon, tone: cls, title, at, now, muted, children }: { icon: ReactNode; tone: string; title: ReactNode; at?: string; now: number; muted?: boolean; children?: ReactNode }) {
  return (
    <div className="relative flex gap-3">
      <span className={cx('relative z-[1] mt-0.5 flex size-7 shrink-0 items-center justify-center rounded-full ring-4 ring-surface [&_svg]:size-3.5', cls)}>{icon}</span>
      <div className={cx('min-w-0 flex-1 pt-1', muted && 'opacity-55')}>
        <div className="flex items-baseline justify-between gap-3">
          <div className="flex min-w-0 flex-wrap items-center gap-1.5 text-[13px] font-medium text-fg-2">{title}</div>
          {at && (
            <Tip content={absTime(at)}>
              <span className="shrink-0 text-xs text-fg-3 tabular">{relTime(at, now)}</span>
            </Tip>
          )}
        </div>
        {children && <div className="mt-1.5 space-y-2">{children}</div>}
      </div>
    </div>
  )
}

function Quote({ children, cls }: { children: ReactNode; cls: string }) {
  return <p className={cx('rounded-xl px-3 py-2 text-sm leading-relaxed whitespace-pre-wrap text-fg', cls)}>{children}</p>
}

export function ReqTimeline({ r, decisions }: { r: Requirement; decisions: Decision[] }) {
  const now = useNow()
  const sessions = useSessions()
  const who = (key?: string | null) => {
    const s = (sessions.data?.sessions ?? []).find((x) => x.key === key)
    return s ? [sessionTitle(s), s.device_name].filter(Boolean).join(t.common.sep) : ''
  }
  const mine = [...(r.feedback ?? [])].sort((a, b) => (a.answered_at ?? a.created_at).localeCompare(b.answered_at ?? b.created_at))
  const revises = mine.filter((f) => f.kind === 'feedback')
  const bounds = revises.map((f) => f.answered_at ?? f.created_at)
  const roundOf = (at: string) => 1 + bounds.filter((b) => b <= at).length
  const current = 1 + revises.length

  const entries: Entry[] = [
    {
      at: r.created_at,
      key: 'create',
      round: 1,
      node: <Node icon={<Sparkles />} tone={tone.create} title={r.created_by === 'agent' ? t.timeline.createdAgent : t.timeline.createdUser} at={r.created_at} now={now} />,
    },
  ]
  for (const e of r.evidence ?? []) {
    entries.push({
      at: e.created_at,
      key: e.id,
      round: roundOf(e.created_at),
      node: (
        <Node
          icon={<FileCheck2 />}
          tone={tone.evidence}
          title={
            <>
              {t.timeline.evidence}
              <Badge tone="run">{evidenceKind(e.kind)}</Badge>
            </>
          }
          at={e.created_at}
          now={now}
          muted={e.voided}
        >
          <p className="text-sm leading-relaxed break-words text-fg">{e.summary}</p>
          {e.link && <p className="truncate font-mono text-xs text-fg-3">{e.link}</p>}
          {!!e.files?.length && <Thumbs files={e.files} />}
        </Node>
      ),
    })
  }
  for (const f of mine) {
    const at = f.answered_at ?? f.created_at
    if (f.kind === 'assign') {
      entries.push({
        at,
        key: f.id,
        round: roundOf(at),
        node: (
          <Node
            icon={<Send />}
            tone={tone.note}
            title={
              <>
                {t.assign.timeline}
                <span className="truncate font-normal text-fg-3">{who(f.session_key) || f.device_name}</span>
                <Badge tone={f.status === 'delivered' ? 'run' : 'neutral'}>{t.decision.status[f.status]}</Badge>
              </>
            }
            at={at}
            now={now}
          />
        ),
      })
      continue
    }
    const note = f.kind === 'note'
    entries.push({
      at,
      key: f.id,
      round: note ? roundOf(at) : roundOf(at) - 1, // 修改意见收尾它所在的那一轮
      node: (
        <Node
          icon={note ? <MessageSquareText /> : <Undo2 />}
          tone={note ? tone.note : tone.revise}
          title={
            <>
              {note ? t.timeline.note : t.timeline.revise}
              <Badge tone={f.status === 'delivered' ? 'run' : 'neutral'}>{t.decision.status[f.status]}</Badge>
            </>
          }
          at={at}
          now={now}
        >
          {f.answer && <Quote cls={note ? 'bg-accent-soft/60' : 'bg-reply-soft/60'}>{f.answer}</Quote>}
          {!!f.answer_files?.length && <Thumbs files={f.answer_files} size="sm" />}
        </Node>
      ),
    })
  }
  for (const d of decisions.filter((x) => x.kind === 'decision')) {
    if (d.status === 'pending') {
      entries.push({
        at: '￿', // 等你回答的问题放在最后，紧挨着底部的输入区
        key: d.id,
        round: current,
        node: (
          <Node icon={<HelpCircle />} tone={tone.ask} title={t.timeline.ask} now={now}>
            <DecisionComposer d={d} embedded />
          </Node>
        ),
      })
      continue
    }
    entries.push({
      at: d.created_at,
      key: d.id,
      round: roundOf(d.created_at),
      node: (
        <Node
          icon={<HelpCircle />}
          tone={tone.decision}
          title={
            <>
              {t.timeline.decision}
              <span className="font-mono text-[11px] text-fg-3">{d.code}</span>
            </>
          }
          at={d.answered_at ?? d.created_at}
          now={now}
        >
          <p className="text-sm text-fg">{d.short_title || d.question}</p>
          {d.answer && (
            <Quote cls="bg-sunken/70">
              <span className="mr-1.5 text-fg-3">{t.timeline.answer}</span>
              {d.answer}
            </Quote>
          )}
          {!!d.answer_files?.length && <Thumbs files={d.answer_files} size="sm" />}
        </Node>
      ),
    })
  }
  if (r.status === 'done') {
    entries.push({ at: r.updated_at, key: 'done', round: current, node: <Node icon={<CheckCheck />} tone={tone.done} title={t.timeline.done} at={r.updated_at} now={now} /> })
  }
  entries.sort((a, b) => a.at.localeCompare(b.at))

  // 竖线画在每项左侧（最后一项不画）；AnimatePresence 只认直接子元素，这里展开成一维列表。
  const showRounds = revises.length > 0
  const items: ReactNode[] = []
  let last = 0
  entries.forEach((e, i) => {
    if (showRounds && e.round !== last) {
      items.push(
        <Item key={'round' + e.round}>
          <div className="flex items-center gap-2 pt-1 pb-4 text-xs text-fg-3">
            <span className="font-display text-[13px] text-fg-2">{t.timeline.round(e.round)}</span>
            {e.round === current && r.status !== 'done' && <Badge tone="accent">{t.timeline.current}</Badge>}
            <span aria-hidden className="h-0 flex-1 border-t border-dashed border-line-strong/80" />
          </div>
        </Item>,
      )
    }
    last = e.round
    const end = i === entries.length - 1
    items.push(
      <Item key={e.key}>
        <div className={cx('relative', !end && 'pb-5')}>
          {!end && <span aria-hidden className="absolute top-8 bottom-0 left-[13px] w-px bg-line" />}
          {e.node}
        </div>
      </Item>,
    )
  })
  return <List>{items}</List>
}
