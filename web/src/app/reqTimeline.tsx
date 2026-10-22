import type { ReactNode } from 'react'
import { Bot, CheckCheck, Sparkles } from 'lucide-react'
import type { Decision, Requirement } from '../api/client'
import { t } from '../i18n'
import { absTime, cx, relTime, useNow } from '../lib/util'
import { Badge, Tip } from '../ui/base'
import { Thumbs } from '../ui/attach'
import { Item, List } from '../ui/motion'
import { DecisionComposer } from './DecisionCard'
import { evidenceKind } from './review'

// 需求进展：像聊天记录一样按时间往下排——Agent 在左（提交验收、提问），你在右（退回修改、答复），
// 系统事件居中。每次退回修改开启新的一轮，复杂需求反复几轮也能看清来龙去脉。

type Entry = { at: string; key: string; round: number; node: ReactNode }

function When({ at, now, right }: { at: string; now: number; right?: boolean }) {
  return (
    <Tip content={absTime(at)}>
      <span className={cx('mt-1 block text-[11px] text-fg-3 tabular', right && 'text-right')}>{relTime(at, now)}</span>
    </Tip>
  )
}

function Agent({ title, at, now, muted, children }: { title: ReactNode; at: string; now: number; muted?: boolean; children?: ReactNode }) {
  return (
    <div className={cx('flex max-w-[92%] gap-2.5', muted && 'opacity-55')}>
      <span className="mt-0.5 flex size-7 shrink-0 items-center justify-center rounded-full bg-sunken text-fg-3">
        <Bot className="size-4" />
      </span>
      <div className="min-w-0">
        <div className="rounded-2xl rounded-tl-md border border-line bg-surface-2 px-3.5 py-2.5">
          <div className="mb-1 flex flex-wrap items-center gap-1.5 text-xs font-medium text-fg-3">{title}</div>
          <div className="space-y-2">{children}</div>
        </div>
        <When at={at} now={now} />
      </div>
    </div>
  )
}

function Mine({ title, at, now, children }: { title: ReactNode; at: string; now: number; children?: ReactNode }) {
  return (
    <div className="ml-auto flex max-w-[88%] flex-col items-end">
      <div className="rounded-2xl rounded-tr-md bg-reply-soft px-3.5 py-2.5">
        <div className="mb-1 flex flex-wrap items-center justify-end gap-1.5 text-xs font-medium text-reply">{title}</div>
        <div className="space-y-2">{children}</div>
      </div>
      <When at={at} now={now} right />
    </div>
  )
}

function System({ icon, children, at, now }: { icon: ReactNode; children: ReactNode; at: string; now: number }) {
  return (
    <div className="flex items-center justify-center gap-1.5 text-xs text-fg-3 [&_svg]:size-3.5">
      {icon}
      <span>{children}</span>
      <span aria-hidden>·</span>
      <Tip content={absTime(at)}>
        <span className="tabular">{relTime(at, now)}</span>
      </Tip>
    </div>
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
      node: (
        <System icon={<Sparkles />} at={r.created_at} now={now}>
          {r.created_by === 'agent' ? t.timeline.createdAgent : t.timeline.createdUser}
        </System>
      ),
    },
  ]
  for (const e of r.evidence ?? []) {
    entries.push({
      at: e.created_at,
      key: e.id,
      round: roundOf(e.created_at),
      node: (
        <Agent
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
        </Agent>
      ),
    })
  }
  for (const f of feedback) {
    const at = f.answered_at ?? f.created_at
    entries.push({
      at,
      key: f.id,
      round: roundOf(at) - 1, // 修改意见收尾它所在的那一轮
      node: (
        <Mine
          title={
            <>
              <Badge tone={f.status === 'delivered' ? 'run' : 'neutral'}>{t.decision.status[f.status]}</Badge>
              {t.timeline.revise}
            </>
          }
          at={at}
          now={now}
        >
          {f.answer && <p className="text-sm leading-relaxed whitespace-pre-wrap text-fg">{f.answer}</p>}
          {!!f.answer_files?.length && <Thumbs files={f.answer_files} size="sm" />}
        </Mine>
      ),
    })
  }
  for (const d of decisions.filter((x) => x.kind !== 'feedback')) {
    entries.push({
      at: d.created_at,
      key: d.id,
      round: d.status === 'pending' ? 1 + feedback.length : roundOf(d.created_at),
      node:
        d.status === 'pending' ? (
          <div className="flex gap-2.5">
            <span className="mt-3 flex size-7 shrink-0 items-center justify-center rounded-full bg-sunken text-fg-3">
              <Bot className="size-4" />
            </span>
            <div className="min-w-0 flex-1">
              <DecisionComposer d={d} embedded />
            </div>
          </div>
        ) : (
          <Agent
            title={
              <>
                {t.timeline.decision}
                <span className="font-mono text-[11px]">{d.code}</span>
              </>
            }
            at={d.created_at}
            now={now}
          >
            <p className="text-sm text-fg">{d.short_title || d.question}</p>
          </Agent>
        ),
    })
    if (d.status !== 'pending' && d.answer && d.answered_at) {
      entries.push({
        at: d.answered_at,
        key: d.id + '.a',
        round: roundOf(d.answered_at),
        node: (
          <Mine title={t.timeline.answer} at={d.answered_at} now={now}>
            <p className="text-sm leading-relaxed whitespace-pre-wrap text-fg">{d.answer}</p>
            {!!d.answer_files?.length && <Thumbs files={d.answer_files} size="sm" />}
          </Mine>
        ),
      })
    }
  }
  if (r.status === 'done') {
    entries.push({
      at: r.updated_at,
      key: 'done',
      round: 1 + feedback.length,
      node: (
        <System icon={<CheckCheck className="text-run" />} at={r.updated_at} now={now}>
          {t.timeline.done}
        </System>
      ),
    })
  }
  // 等你回答的问题放在最后，紧挨着底部的输入区。
  const pendingKeys = new Set(decisions.filter((d) => d.status === 'pending').map((d) => d.id))
  entries.sort((a, b) => Number(pendingKeys.has(a.key)) - Number(pendingKeys.has(b.key)) || a.at.localeCompare(b.at))

  const showRounds = feedback.length > 0
  // AnimatePresence 只认直接子元素，这里展开成一维列表（不能用 Fragment 包）。
  const items: ReactNode[] = []
  let last = 0
  for (const e of entries) {
    if (showRounds && e.round !== last) {
      items.push(
        <Item key={'round' + e.round} space={16}>
          <div className="flex items-center gap-3 text-xs text-fg-3">
            <span aria-hidden className="h-0 flex-1 border-t border-dashed border-line-strong/80" />
            <span className="font-display text-[13px] text-fg-2">{t.timeline.round(e.round)}</span>
            {e.round === 1 + feedback.length && r.status !== 'done' && <Badge tone="accent">{t.timeline.current}</Badge>}
            <span aria-hidden className="h-0 flex-1 border-t border-dashed border-line-strong/80" />
          </div>
        </Item>,
      )
    }
    last = e.round
    items.push(
      <Item key={e.key} space={16}>
        {e.node}
      </Item>,
    )
  }
  return <List space={16}>{items}</List>
}
