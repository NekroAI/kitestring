import { useMemo, useState, type ReactNode } from 'react'
import { Link } from 'react-router'
import { AnimatePresence, motion } from 'motion/react'
import { CheckCheck, FileCheck2, History, MonitorSmartphone, Search, SearchX, Send } from 'lucide-react'
import type { Requirement, Session } from '../api/client'
import { useSettings } from '../api/queries'
import { t } from '../i18n'
import { useNow, usePref } from '../lib/util'
import { Button, Card, CardSkeleton, Empty, ErrorState, Input, PageHeader, Segmented } from '../ui/base'
import { DecisionComposer, OrphanCard } from '../app/DecisionCard'
import { useWorkbench } from '../app/inbox'
import { useOpenReq } from '../app/nav'
import { composeAnswer, useOutbox } from '../app/outbox'
import { RecapPanel } from '../app/Recap'
import { ReviewActions } from '../app/requirements'
import { SessionCard, sessionTitle } from '../app/SessionCard'

type Filter = 'all' | 'todo'

function Section({ title, count, children, right }: { title: ReactNode; count?: number; children: ReactNode; right?: ReactNode }) {
  return (
    <section>
      <div className="mb-3 flex items-center gap-2">
        <h2 className="text-[15px] font-semibold text-fg">{title}</h2>
        {count != null && count > 0 && <span className="rounded-full bg-sunken px-2 py-px text-xs font-semibold text-fg-2 tabular">{count}</span>}
        <span className="ml-auto">{right}</span>
      </div>
      {children}
    </section>
  )
}

function ReviewCard({ r }: { r: Requirement }) {
  const open = useOpenReq()
  const latest = r.evidence?.filter((e) => !e.voided).at(-1)
  return (
    <Card className="flex h-full flex-col gap-3 p-4">
      <button onClick={() => open(r.code)} className="flex items-start gap-3 text-left">
        <span className="flex size-8 shrink-0 items-center justify-center rounded-[10px] bg-reply-soft text-reply">
          <FileCheck2 className="size-[18px]" />
        </span>
        <span className="min-w-0 flex-1">
          <span className="flex items-center gap-2 text-xs text-fg-3">
            <span className="font-mono font-semibold">{r.code}</span>
            <span className="truncate">{r.project_name}</span>
          </span>
          <span className="mt-0.5 block text-[15px] font-semibold text-fg">{r.title}</span>
          {latest && <span className="mt-1 block truncate text-[13px] text-fg-3">{latest.summary}</span>}
        </span>
      </button>
      <div className="mt-auto flex items-center justify-between">
        <span className="text-xs text-fg-3">{t.reqs.reviewBanner}</span>
        <ReviewActions r={r} />
      </div>
    </Card>
  )
}

function SendAllBar() {
  const wb = useWorkbench()
  const { drafts, send, clearDrafts, queued } = useOutbox()
  const ready = wb.decisions
    .filter((d) => d.status === 'pending')
    .map((d) => ({ code: d.code, answer: composeAnswer(drafts[d.code], d.options ?? []) }))
    .filter((x) => x.answer && !queued[x.code])
  return (
    <AnimatePresence>
      {ready.length >= 2 && (
        <motion.div initial={{ y: 80, opacity: 0 }} animate={{ y: 0, opacity: 1 }} exit={{ y: 80, opacity: 0 }} transition={{ type: 'spring', bounce: 0.2, duration: 0.4 }} className="fixed inset-x-0 bottom-20 z-20 flex justify-center px-4 lg:bottom-8 lg:pl-[232px]">
          <div className="flex items-center gap-2 rounded-2xl border border-line bg-surface/95 py-2 pr-2 pl-4 shadow-pop backdrop-blur">
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

function Grid({ children }: { children: ReactNode }) {
  // 按容器宽度分列（而不是窗口宽度），竖屏、窄窗口下同样合理。
  return (
    <div className="@container">
      <div className="grid grid-cols-1 gap-3 @2xl:grid-cols-2 @6xl:grid-cols-3">{children}</div>
    </div>
  )
}

const match = (s: Session, k: string) => !k || [sessionTitle(s), s.project_name, s.branch, s.cwd, s.last_prompt, s.summary, s.device_name, s.model].some((v) => v?.toLowerCase().includes(k))

export default function WorkbenchPage() {
  const now = useNow()
  const wb = useWorkbench()
  const settings = useSettings()
  const [filter, setFilter] = usePref<Filter>('ks.home.filter', 'all')
  const [q, setQ] = useState('')
  const [recap, setRecap] = useState(false)
  const k = q.trim().toLowerCase()
  const recapOn = !!settings.data?.ai?.recap

  const attention = useMemo(() => wb.attention.filter((x) => match(x.s, k)), [wb.attention, k])
  const active = useMemo(() => wb.active.filter((s) => match(s, k)), [wb.active, k])
  const ended = useMemo(() => wb.ended.filter((s) => match(s, k)), [wb.ended, k])
  const withDecisions = attention.filter((x) => x.decisions.length)
  const others = attention.filter((x) => !x.decisions.length)
  const hasTodo = attention.length + wb.looseDecisions.length + wb.reviews.length + wb.orphans.length > 0

  const header = (
    <PageHeader
      title={t.home.title}
      subtitle={wb.loading ? ' ' : t.home.summary(wb.todo, wb.running.length)}
      actions={
        recapOn && (
          <Button icon={<History className="size-4" />} onClick={() => setRecap(true)}>
            {t.home.recap}
          </Button>
        )
      }
    />
  )

  if (wb.error) return <ErrorState error={wb.error} onRetry={wb.refetch} />

  return (
    <>
      {header}
      <div className="mb-6 flex flex-wrap items-center gap-2">
        <Segmented
          value={filter}
          onChange={setFilter}
          options={[
            { value: 'all', label: t.home.filter.all },
            { value: 'todo', label: t.home.filter.todo, count: wb.todo },
          ]}
        />
        <div className="relative ml-auto w-full sm:w-64">
          <Search className="pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2 text-fg-3" />
          <Input value={q} onChange={(e) => setQ(e.target.value)} placeholder={t.home.search} className="pl-9" />
        </div>
      </div>

      {wb.loading ? (
        <CardSkeleton rows={3} />
      ) : !wb.devices.length ? (
        <Card>
          <Empty
            icon={<MonitorSmartphone />}
            title={t.home.noDevices}
            action={
              <Link to="/settings?tab=devices">
                <Button variant="primary">{t.home.addDevice}</Button>
              </Link>
            }
          >
            {t.home.noDevicesSub}
          </Empty>
        </Card>
      ) : !wb.sessions.length && !hasTodo ? (
        <Card>
          <Empty icon={<MonitorSmartphone />} title={t.home.empty}>
            {t.home.emptySub}
          </Empty>
        </Card>
      ) : (
        <div className="space-y-9">
          {hasTodo ? (
            <Section title={t.home.sections.attention} count={wb.todo}>
              <div className="space-y-3">
                {withDecisions.map((x) => (
                  <div key={x.s.key} className="rise">
                    <SessionCard s={x.s} now={now} decisions={x.decisions} wide />
                  </div>
                ))}
                {wb.looseDecisions.map((d) => (
                  <div key={d.id} className="rise">
                    <DecisionComposer d={d} />
                  </div>
                ))}
                {(others.length > 0 || wb.reviews.length > 0 || wb.orphans.length > 0) && (
                  <Grid>
                    {others.map((x) => (
                      <div key={x.s.key} className="rise h-full min-w-0">
                        <SessionCard s={x.s} now={now} />
                      </div>
                    ))}
                    {wb.reviews.map((r) => (
                      <div key={r.id} className="rise h-full min-w-0">
                        <ReviewCard r={r} />
                      </div>
                    ))}
                    {wb.orphans.map((d) => (
                      <div key={d.id} className="rise h-full min-w-0">
                        <OrphanCard d={d} sessions={wb.sessions} />
                      </div>
                    ))}
                  </Grid>
                )}
              </div>
            </Section>
          ) : (
            filter === 'todo' && (
              <Card>
                <Empty icon={<CheckCheck />} title={t.home.allClear}>
                  {t.home.allClearSub(wb.running.length)}
                </Empty>
              </Card>
            )
          )}

          {filter === 'all' && active.length > 0 && (
            <Section title={t.home.sections.active} count={active.length}>
              <Grid>
                {active.map((s) => (
                  <div key={s.key} className="rise h-full min-w-0">
                    <SessionCard s={s} now={now} />
                  </div>
                ))}
              </Grid>
            </Section>
          )}

          {filter === 'all' && ended.length > 0 && (
            <Section title={t.home.sections.ended} count={ended.length} right={<span className="text-xs text-fg-3">{t.home.endedHint}</span>}>
              <Grid>
                {ended.map((s) => (
                  <div key={s.key} className="rise h-full min-w-0">
                    <SessionCard s={s} now={now} />
                  </div>
                ))}
              </Grid>
            </Section>
          )}

          {k && !attention.length && !active.length && !ended.length && (
            <Card>
              <Empty icon={<SearchX />} title={t.home.noMatch} />
            </Card>
          )}
        </div>
      )}
      <SendAllBar />
      {recapOn && <RecapPanel open={recap} onClose={() => setRecap(false)} />}
    </>
  )
}
