import { useMemo, useState } from 'react'
import { Link } from 'react-router'
import { AnimatePresence, motion } from 'motion/react'
import { CheckCheck, FolderGit2, History, LayoutGrid, MessagesSquare, MonitorSmartphone, Search, SearchX, Send } from 'lucide-react'
import type { Session } from '../api/client'
import { useSettings } from '../api/queries'
import { t } from '../i18n'
import { useNow, usePref } from '../lib/util'
import { Button, Card, CardSkeleton, Empty, ErrorState, Input, PageHeader, Segmented } from '../ui/base'
import { StatusDot } from '../ui/marks'
import { useWorkbench, type ProjectGroup } from '../app/inbox'
import { composeAnswer, useOutbox } from '../app/outbox'
import { RecapPanel } from '../app/Recap'
import { SessionCard, sessionTitle } from '../app/SessionCard'
import { SessionRow, ThreadCard } from '../app/thread'

type Filter = 'all' | 'todo'

const match = (s: Session | undefined, k: string) => !k || (!!s && [sessionTitle(s), s.project_name, s.branch, s.cwd, s.last_prompt, s.summary, s.device_name, s.model].some((v) => v?.toLowerCase().includes(k)))

function SendAllBar() {
  const wb = useWorkbench()
  const { drafts, send, clearDrafts, queued } = useOutbox()
  const ready = wb.decisions
    .filter((d) => d.status === 'pending')
    .map((d) => ({ code: d.code, answer: composeAnswer(drafts[d.code], d.options ?? []), files: drafts[d.code]?.files?.map((f) => f.id) }))
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

// 实例视图：每个会话一张卡片（改版前的实例墙），按项目分组，需要你处理的在前。
function ProjectCards({ g, filter, k, now }: { g: ProjectGroup; filter: Filter; k: string; now: number }) {
  const attention = g.threads.map((th) => th.s).filter((s): s is Session => !!s && match(s, k))
  const rest = filter === 'all' ? [...g.active, ...g.ended].filter((s) => match(s, k)) : []
  const list = [...attention, ...rest]
  if (!list.length) return null
  return (
    <section className="rise">
      <div className="mb-3 flex items-center gap-2.5">
        <span className="flex size-7 items-center justify-center rounded-lg bg-surface text-fg-2 shadow-xs">
          <FolderGit2 className="size-4" />
        </span>
        <h2 className="truncate text-[15px] font-semibold text-fg">{g.name || t.thread.noProject}</h2>
        <span className="text-xs text-fg-3 tabular">{list.length}</span>
      </div>
      <div className="@container">
        <div className="grid grid-cols-1 gap-3 @2xl:grid-cols-2 @6xl:grid-cols-3">
          {list.map((s) => (
            <div key={s.key} className="h-full min-w-0">
              <SessionCard s={s} now={now} />
            </div>
          ))}
        </div>
      </div>
    </section>
  )
}

// 对话视图：先列需要你处理的对话（线程），再列其余会话（紧凑行）。
function Project({ g, filter, k, now, sessions }: { g: ProjectGroup; filter: Filter; k: string; now: number; sessions: Session[] }) {
  const threads = g.threads.filter((th) => !k || match(th.s, k))
  const active = filter === 'all' ? g.active.filter((s) => match(s, k)) : []
  const ended = filter === 'all' ? g.ended.filter((s) => match(s, k)) : []
  if (!threads.length && !active.length && !ended.length) return null
  const counts = (['waiting_approval', 'waiting_reply', 'running', 'background'] as const).map((st) => [st, [...g.active, ...g.threads.map((x) => x.s).filter(Boolean)].filter((s) => s!.state === st).length] as const).filter(([, n]) => n)
  return (
    <section className="rise">
      <div className="mb-3 flex items-center gap-2.5">
        <span className="flex size-7 items-center justify-center rounded-lg bg-surface text-fg-2 shadow-xs">
          <FolderGit2 className="size-4" />
        </span>
        <h2 className="truncate text-[15px] font-semibold text-fg">{g.name || t.thread.noProject}</h2>
        <span className="ml-auto flex items-center gap-3 text-xs text-fg-3">
          {counts.map(([st, n]) => (
            <span key={st} className="flex items-center gap-1.5">
              <StatusDot state={st} />
              <span className="tabular">{n}</span>
            </span>
          ))}
        </span>
      </div>
      <div className="space-y-3">
        {threads.map((th) => (
          <ThreadCard key={th.key} th={th} now={now} projectName={g.name} sessions={sessions} />
        ))}
        {(active.length > 0 || ended.length > 0) && (
          <Card className="@container p-1.5">
            {active.map((s) => (
              <SessionRow key={s.key} s={s} now={now} />
            ))}
            {ended.length > 0 && (
              <>
                {active.length > 0 && <div className="mx-3 my-1 border-t border-line" />}
                <div className="px-3 pt-1.5 pb-0.5 text-xs text-fg-3">{t.thread.ended(ended.length)}</div>
                {ended.map((s) => (
                  <SessionRow key={s.key} s={s} now={now} />
                ))}
              </>
            )}
          </Card>
        )}
      </div>
    </section>
  )
}

export default function WorkbenchPage() {
  const now = useNow()
  const wb = useWorkbench()
  const settings = useSettings()
  const [filter, setFilter] = usePref<Filter>('ks.home.filter', 'all')
  const [view, setView] = usePref<'threads' | 'cards'>('ks.home.view', 'threads')
  const [q, setQ] = useState('')
  const [recap, setRecap] = useState(false)
  const k = q.trim().toLowerCase()
  const recapOn = !!settings.data?.ai?.recap
  const visible = useMemo(() => wb.projects.filter((g) => g.threads.length || filter === 'all'), [wb.projects, filter])

  if (wb.error) return <ErrorState error={wb.error} onRetry={wb.refetch} />

  return (
    <>
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
      <div className="mb-6 flex flex-wrap items-center gap-2">
        <Segmented
          value={filter}
          onChange={setFilter}
          options={[
            { value: 'all', label: t.home.filter.all },
            { value: 'todo', label: t.home.filter.todo, count: wb.todo },
          ]}
        />
        <Segmented
          value={view}
          onChange={setView}
          options={[
            { value: 'threads', label: <MessagesSquare className="size-4" />, tip: t.home.viewThreads },
            { value: 'cards', label: <LayoutGrid className="size-4" />, tip: t.home.viewCards },
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
      ) : !wb.sessions.length && !wb.threads.length ? (
        <Card>
          <Empty icon={<MonitorSmartphone />} title={t.home.empty}>
            {t.home.emptySub}
          </Empty>
        </Card>
      ) : filter === 'todo' && !wb.threads.length ? (
        <Card>
          <Empty icon={<CheckCheck />} title={t.home.allClear}>
            {t.home.allClearSub(wb.running.length)}
          </Empty>
        </Card>
      ) : (
        <div className="space-y-9">
          {visible.map((g) => (view === 'cards' ? <ProjectCards key={g.key} g={g} filter={filter} k={k} now={now} /> : <Project key={g.key} g={g} filter={filter} k={k} now={now} sessions={wb.sessions} />))}
          {k && !visible.some((g) => g.threads.some((th) => match(th.s, k)) || g.active.some((s) => match(s, k)) || g.ended.some((s) => match(s, k))) && (
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
