import { useMemo, useState } from 'react'
import { Link } from 'react-router'
import { AnimatePresence, motion } from 'motion/react'
import { Archive, CheckCheck, FolderGit2, History, LayoutGrid, MessagesSquare, MonitorSmartphone, Search, SearchX, Send } from 'lucide-react'
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
import { Item, List } from '../ui/motion'
import { PlanAttention } from '../app/plans'

type Filter = 'all' | 'todo'

// 分组键是项目 ID 时才有项目页（没有识别出项目的会话按目录分组）
const projectHref = (key: string) => (key && !key.startsWith('cwd:') && key !== 'none' ? `/projects/${key}` : undefined)

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
        <motion.div initial={{ y: 80, opacity: 0 }} animate={{ y: 0, opacity: 1 }} exit={{ y: 80, opacity: 0 }} transition={{ type: 'spring', bounce: 0.2, duration: 0.4 }} className="fixed inset-x-0 bottom-20 z-20 flex justify-center px-4 lg:bottom-8 lg:pl-[var(--sidebar-w,232px)]">
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

function GroupHeader({ icon, title, count, right, href }: { icon: React.ReactNode; title: React.ReactNode; count?: number; right?: React.ReactNode; href?: string }) {
  return (
    <div className="mb-3 flex items-center gap-2.5">
      <span className="flex size-7 items-center justify-center rounded-lg bg-surface text-fg-2 shadow-xs [&_svg]:size-4">{icon}</span>
      <h2 className="truncate font-display text-[17px] font-semibold text-fg">{href ? <Link to={href} className="transition-colors hover:text-accent">{title}</Link> : title}</h2>
      {count != null && <span className="text-xs text-fg-3 tabular">{count}</span>}
      <span aria-hidden className="h-0 min-w-6 flex-1 border-t border-dashed border-line-strong/80" />
      {right}
    </div>
  )
}

// 已结束：所有项目的已结束会话统一放在最底下，不夹在项目之间。
function Ended({ sessions, view, now }: { sessions: Session[]; view: 'threads' | 'cards'; now: number }) {
  if (!sessions.length) return null
  return (
    <section>
      <GroupHeader icon={<Archive />} title={t.thread.endedTitle} count={sessions.length} />
      {view === 'cards' ? (
        <div className="@container">
          <List className="grid grid-cols-1 gap-x-3 @2xl:grid-cols-2 @6xl:grid-cols-3">
            {sessions.map((s) => (
              <Item key={s.key} space={12} layout className="h-full">
                <SessionCard s={s} now={now} />
              </Item>
            ))}
          </List>
        </div>
      ) : (
        <Card className="@container p-1.5">
          <List>
            {sessions.map((s) => (
              <Item key={s.key}>
                <SessionRow s={s} now={now} showProject />
              </Item>
            ))}
          </List>
        </Card>
      )}
    </section>
  )
}

// 实例视图：每个会话一张卡片（改版前的实例墙），按项目分组，需要你处理的在前。
function ProjectCards({ g, filter, k, now }: { g: ProjectGroup; filter: Filter; k: string; now: number }) {
  const attention = g.threads.map((th) => th.s).filter((s): s is Session => !!s && match(s, k))
  const rest = filter === 'all' ? g.active.filter((s) => match(s, k)) : []
  const list = [...attention, ...rest]
  if (!list.length) return null
  return (
    <section>
      <GroupHeader icon={<FolderGit2 />} title={g.name || t.thread.noProject} count={list.length} href={projectHref(g.key)} />
      <div className="@container">
        <List className="grid grid-cols-1 gap-x-3 @2xl:grid-cols-2 @6xl:grid-cols-3">
          {list.map((s) => (
            <Item key={s.key} space={12} layout className="h-full">
              <SessionCard s={s} now={now} />
            </Item>
          ))}
        </List>
      </div>
    </section>
  )
}

// 对话视图：先列需要你处理的对话（线程），再列其余会话（紧凑行）。
function Project({ g, filter, k, now, sessions }: { g: ProjectGroup; filter: Filter; k: string; now: number; sessions: Session[] }) {
  const threads = g.threads.filter((th) => !k || match(th.s, k))
  const active = filter === 'all' ? g.active.filter((s) => match(s, k)) : []
  if (!threads.length && !active.length) return null
  const counts = (['waiting_approval', 'waiting_reply', 'running', 'background'] as const).map((st) => [st, [...g.active, ...g.threads.map((x) => x.s).filter(Boolean)].filter((s) => s!.state === st).length] as const).filter(([, n]) => n)
  return (
    <section>
      <GroupHeader
        icon={<FolderGit2 />}
        title={g.name || t.thread.noProject}
        href={projectHref(g.key)}
        right={
          <span className="flex items-center gap-3 text-xs text-fg-3">
            {counts.map(([st, n]) => (
              <span key={st} className="flex items-center gap-1.5">
                <StatusDot state={st} />
                <span className="tabular">{n}</span>
              </span>
            ))}
          </span>
        }
      />
      <List space={12}>
        {threads.map((th) => (
          <Item key={th.key} space={12}>
            <ThreadCard th={th} now={now} projectName={g.name} sessions={sessions} />
          </Item>
        ))}
        {active.length > 0 && (
          <Item key="active" space={12}>
            <Card className="@container p-1.5">
              <List>
                {active.map((s) => (
                  <Item key={s.key}>
                    <SessionRow s={s} now={now} />
                  </Item>
                ))}
              </List>
            </Card>
          </Item>
        )}
      </List>
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
  const visible = useMemo(() => wb.projects.filter((g) => g.threads.length || (filter === 'all' && g.active.length)), [wb.projects, filter])
  const ended = useMemo(
    () => (filter === 'all' ? wb.projects.flatMap((g) => g.ended).filter((s) => match(s, k)).sort((a, b) => b.last_active_at.localeCompare(a.last_active_at)) : []),
    [wb.projects, filter, k],
  )

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
      ) : filter === 'todo' && !wb.threads.length && !wb.plans.length ? (
        <Card>
          <Empty icon={<CheckCheck />} title={t.home.allClear}>
            {t.home.allClearSub(wb.running.length)}
          </Empty>
        </Card>
      ) : (
        <div>
          <PlanAttention plans={k ? [] : wb.plans} />
          <List space={36}>
            {visible.map((g) => (
              <Item key={g.key} space={36}>
                {view === 'cards' ? <ProjectCards g={g} filter={filter} k={k} now={now} /> : <Project g={g} filter={filter} k={k} now={now} sessions={wb.sessions} />}
              </Item>
            ))}
          </List>
          <div className="mt-9">
            <Ended sessions={ended} view={view} now={now} />
          </div>
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
