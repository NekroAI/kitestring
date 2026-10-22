import type { ReactNode } from 'react'
import { AnimatePresence, motion } from 'motion/react'
import { CheckCheck, ChevronRight, FileCheck2, MessageSquareReply, Send, ShieldAlert } from 'lucide-react'
import type { Requirement, Session } from '../api/client'
import { t } from '../i18n'
import { cx, duration, relTime, useNow } from '../lib/util'
import { Badge, Button, Card, CardSkeleton, Empty, ErrorState, PageHeader, SectionTitle } from '../ui/base'
import { StatusDot, StatusPill, ToolMark } from '../ui/marks'
import { DecisionComposer, OrphanCard } from '../app/DecisionCard'
import { useInbox } from '../app/inbox'
import { useOpenReq, useOpenSession } from '../app/nav'
import { composeAnswer, useOutbox } from '../app/outbox'
import { ReviewActions } from '../app/requirements'
import { toolIcon } from '../app/SessionCard'

const item = { initial: { opacity: 0, y: 8 }, animate: { opacity: 1, y: 0 }, exit: { opacity: 0, scale: 0.97, transition: { duration: 0.15 } } }

function Stack({ children }: { children: ReactNode }) {
  return (
    <div className="space-y-3">
      <AnimatePresence initial={false} mode="popLayout">
        {children}
      </AnimatePresence>
    </div>
  )
}

function SessionLine({ s, now, children, tone }: { s: Session; now: number; children?: ReactNode; tone: 'wait' | 'reply' }) {
  const open = useOpenSession()
  return (
    <Card role="button" tabIndex={0} onClick={() => open(s.key)} onKeyDown={(e) => e.key === 'Enter' && open(s.key)} className="group flex cursor-pointer items-start gap-3 p-3.5 transition hover:shadow-pop sm:p-4">
      <ToolMark tool={s.tool} />
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2 text-sm">
          <span className="truncate font-semibold text-fg">{s.project_name || s.cwd}</span>
          <span className="shrink-0 text-fg-3">· {s.device_name}</span>
        </div>
        {children}
      </div>
      <div className="flex shrink-0 flex-col items-end gap-1.5">
        <span className={cx('text-xs font-medium tabular', tone === 'wait' ? 'text-wait' : 'text-reply')}>{t.inbox.waitingFor(duration(s.state_since, now))}</span>
        <ChevronRight className="size-4 text-fg-3 transition group-hover:translate-x-0.5" />
      </div>
    </Card>
  )
}

function ReviewLine({ r }: { r: Requirement }) {
  const open = useOpenReq()
  const latest = r.evidence?.filter((e) => !e.voided).at(-1)
  return (
    <Card className="p-3.5 sm:p-4">
      <button onClick={() => open(r.code)} className="flex w-full items-start gap-3 text-left">
        <span className="flex size-7 shrink-0 items-center justify-center rounded-lg bg-reply-soft text-reply">
          <FileCheck2 className="size-4" />
        </span>
        <span className="min-w-0 flex-1">
          <span className="flex items-center gap-2 text-xs text-fg-3">
            <span className="font-mono font-semibold">{r.code}</span>
            {r.project_name}
          </span>
          <span className="mt-0.5 block text-sm font-medium text-fg">{r.title}</span>
          {latest && <span className="mt-1 block truncate text-[13px] text-fg-3">{latest.summary}</span>}
        </span>
      </button>
      <div className="mt-3 flex justify-end">
        <ReviewActions r={r} />
      </div>
    </Card>
  )
}

function SendAllBar() {
  const inbox = useInbox()
  const { drafts, send, clearDrafts, queued } = useOutbox()
  const ready = inbox.pending
    .map((d) => ({ code: d.code, answer: composeAnswer(drafts[d.code], d.options ?? []) }))
    .filter((x) => x.answer && !queued[x.code])
  return (
    <AnimatePresence>
      {ready.length >= 2 && (
        <motion.div initial={{ y: 80, opacity: 0 }} animate={{ y: 0, opacity: 1 }} exit={{ y: 80, opacity: 0 }} transition={{ type: 'spring', bounce: 0.2, duration: 0.4 }} className="fixed inset-x-0 bottom-20 z-20 flex justify-center px-4 lg:bottom-8 lg:pl-[232px]">
          <div className="flex items-center gap-3 rounded-2xl border border-line bg-surface/95 py-2 pr-2 pl-4 shadow-pop backdrop-blur">
            <span className="text-sm text-fg-2">{t.decision.drafts(ready.length)}</span>
            <Button size="sm" variant="ghost" onClick={() => clearDrafts(ready.map((x) => x.code))}>
              {t.decision.clearDrafts}
            </Button>
            <Button size="sm" variant="primary" icon={<Send className="size-3.5" />} onClick={() => send(ready)}>
              {t.decision.sendAll(ready.length)}
            </Button>
          </div>
        </motion.div>
      )}
    </AnimatePresence>
  )
}

function Aside({ now }: { now: number }) {
  const inbox = useInbox()
  const open = useOpenSession()
  return (
    <div className="space-y-6">
      <div>
        <SectionTitle count={inbox.running.length}>{t.inbox.running}</SectionTitle>
        <Card className="divide-y divide-line overflow-hidden">
          {inbox.running.length ? (
            inbox.running.map((s) => {
              const Icon = s.last_tool ? toolIcon(s.last_tool) : null
              return (
                <button key={s.key} onClick={() => open(s.key)} className="flex w-full items-start gap-3 px-3.5 py-3 text-left hover:bg-hover/60">
                  <ToolMark tool={s.tool} size="sm" />
                  <div className="min-w-0 flex-1">
                    <div className="flex items-center gap-1.5 text-[13px] font-medium text-fg">
                      <span className="truncate">{s.project_name || s.cwd}</span>
                      <span className="ml-auto shrink-0 text-xs font-normal text-fg-3 tabular">{duration(s.state_since, now)}</span>
                    </div>
                    <div className="mt-0.5 line-clamp-1 text-xs text-fg-3">{s.summary || s.last_prompt}</div>
                    {s.last_tool && (
                      <div className="mt-1 flex items-center gap-1 font-mono text-[11px] text-fg-3">
                        {Icon && <Icon className="size-3 shrink-0" />}
                        <span className="truncate">{s.last_tool}</span>
                      </div>
                    )}
                  </div>
                </button>
              )
            })
          ) : (
            <p className="px-4 py-6 text-center text-sm text-fg-3">{t.inbox.noneRunning}</p>
          )}
        </Card>
      </div>
      {inbox.delivered.length > 0 && (
        <div>
          <SectionTitle>{t.inbox.recentDelivered}</SectionTitle>
          <div className="space-y-2.5">
            {inbox.delivered.map((d) => (
              <div key={d.id} className="flex gap-2.5 text-[13px]">
                <StatusDot state="running" className="mt-1.5" />
                <div className="min-w-0">
                  <div className="truncate text-fg-2">
                    <span className="mr-1 font-mono text-[11px] font-semibold text-fg-3">{d.code}</span>
                    {d.question}
                  </div>
                  <div className="truncate text-fg-3">
                    → {d.answer} · {relTime(d.delivered_at, now)}
                  </div>
                </div>
              </div>
            ))}
          </div>
        </div>
      )}
    </div>
  )
}

export default function InboxPage() {
  const now = useNow()
  const inbox = useInbox()

  if (inbox.error) return <ErrorState error={inbox.error} onRetry={inbox.refetch} />

  const sections: { key: string; show: boolean; node: ReactNode }[] = [
    {
      key: 'decisions',
      show: inbox.pending.length > 0,
      node: (
        <section>
          <SectionTitle count={inbox.pending.length} hint={t.inbox.sections.decisionsHint}>
            {t.inbox.sections.decisions}
          </SectionTitle>
          <Stack>
            {inbox.pending.map((d) => (
              <motion.div key={d.id} layout {...item}>
                <DecisionComposer d={d} />
              </motion.div>
            ))}
          </Stack>
        </section>
      ),
    },
    {
      key: 'approvals',
      show: inbox.approvals.length > 0,
      node: (
        <section>
          <SectionTitle count={inbox.approvals.length} hint={t.inbox.sections.approvalsHint}>
            {t.inbox.sections.approvals}
          </SectionTitle>
          <Stack>
            {inbox.approvals.map((s) => (
              <motion.div key={s.key} layout {...item}>
                <SessionLine s={s} now={now} tone="wait">
                  <div className="mt-1.5 flex items-start gap-2 rounded-lg bg-wait-soft px-2.5 py-1.5">
                    <ShieldAlert className="mt-0.5 size-3.5 shrink-0 text-wait" />
                    <span className="font-mono text-[12px] leading-relaxed break-all text-fg">{s.state_detail}</span>
                  </div>
                </SessionLine>
              </motion.div>
            ))}
          </Stack>
        </section>
      ),
    },
    {
      key: 'reviews',
      show: inbox.reviews.length > 0,
      node: (
        <section>
          <SectionTitle count={inbox.reviews.length} hint={t.inbox.sections.reviewsHint}>
            {t.inbox.sections.reviews}
          </SectionTitle>
          <Stack>
            {inbox.reviews.map((r) => (
              <motion.div key={r.id} layout {...item}>
                <ReviewLine r={r} />
              </motion.div>
            ))}
          </Stack>
        </section>
      ),
    },
    {
      key: 'orphans',
      show: inbox.orphans.length > 0,
      node: (
        <section>
          <SectionTitle count={inbox.orphans.length} hint={t.inbox.sections.orphansHint}>
            {t.inbox.sections.orphans}
          </SectionTitle>
          <Stack>
            {inbox.orphans.map((d) => (
              <motion.div key={d.id} layout {...item}>
                <OrphanCard d={d} sessions={inbox.sessions} />
              </motion.div>
            ))}
          </Stack>
        </section>
      ),
    },
    {
      key: 'replies',
      show: inbox.replies.length > 0,
      node: (
        <section>
          <SectionTitle count={inbox.replies.length} hint={t.inbox.sections.repliesHint}>
            {t.inbox.sections.replies}
          </SectionTitle>
          <Stack>
            {inbox.replies.map((s) => (
              <motion.div key={s.key} layout {...item}>
                <SessionLine s={s} now={now} tone="reply">
                  <p className="mt-1 line-clamp-2 text-[13px] leading-relaxed text-fg-2">
                    <MessageSquareReply className="mr-1 mb-0.5 inline size-3.5 text-fg-3" />
                    {s.summary || s.last_prompt}
                  </p>
                </SessionLine>
              </motion.div>
            ))}
          </Stack>
        </section>
      ),
    },
  ]
  const visible = sections.filter((s) => s.show)

  return (
    <>
      <PageHeader
        title={t.inbox.title}
        subtitle={inbox.loading ? ' ' : t.inbox.subtitle(inbox.urgent, inbox.replies.length)}
        actions={
          inbox.running.length > 0 && (
            <span className="lg:hidden">
              <StatusPill state="running" now={now} compact />
              <Badge className="ml-1.5">{inbox.running.length}</Badge>
            </span>
          )
        }
      />
      <div className="grid grid-cols-1 gap-8 xl:grid-cols-[minmax(0,1fr)_340px]">
        <div className="min-w-0 space-y-8">
          {inbox.loading ? (
            <CardSkeleton rows={3} />
          ) : visible.length ? (
            visible.map((s) => <div key={s.key}>{s.node}</div>)
          ) : (
            <Card>
              <Empty icon={<CheckCheck />} title={t.inbox.allClear}>
                {t.inbox.allClearSub(inbox.running.length)}
              </Empty>
            </Card>
          )}
        </div>
        <div className="hidden xl:block">
          <div className="sticky top-9">
            <Aside now={now} />
          </div>
        </div>
      </div>
      <SendAllBar />
    </>
  )
}
