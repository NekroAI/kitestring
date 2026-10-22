import { useMemo, useState } from 'react'
import { Link } from 'react-router'
import { AnimatePresence, motion } from 'motion/react'
import { FolderGit2, Monitor, MonitorSmartphone, Search, SearchX } from 'lucide-react'
import type { Device, Session } from '../api/client'
import { t } from '../i18n'
import { cx, relTime, useNow, usePref } from '../lib/util'
import { Button, Card, CardSkeleton, Empty, ErrorState, Input, PageHeader, Segmented } from '../ui/base'
import { StatusDot } from '../ui/marks'
import { useInbox } from '../app/inbox'
import { SessionCard } from '../app/SessionCard'

type Filter = 'active' | 'mine' | 'all'
type Group = 'project' | 'device'

// 排序：等你的在前，其次运行中，其余按最后活跃。
const rank: Record<string, number> = { waiting_approval: 0, waiting_reply: 1, running: 2, idle: 3, lost: 4, ended: 5 }
const byRank = (a: Session, b: Session) => (rank[a.state] ?? 9) - (rank[b.state] ?? 9) || b.last_active_at.localeCompare(a.last_active_at)

function GroupHeader({ icon, title, sub, sessions, device }: { icon: React.ReactNode; title: string; sub?: string; sessions: Session[]; device?: Device }) {
  const now = useNow()
  const counts = ['waiting_approval', 'waiting_reply', 'running'].map((st) => [st, sessions.filter((s) => s.state === st).length] as const).filter(([, n]) => n)
  return (
    <div className="mb-3 flex flex-wrap items-center gap-x-3 gap-y-1">
      <span className="flex size-7 items-center justify-center rounded-lg bg-surface text-fg-2 shadow-xs [&_svg]:size-4">{icon}</span>
      <h2 className="text-[15px] font-semibold text-fg">{title}</h2>
      {device && (
        <span className="flex items-center gap-1.5 text-xs text-fg-3">
          <span className={cx('size-1.5 rounded-full', device.online ? 'bg-run' : 'bg-fg-3/50')} />
          {device.online ? t.sessions.deviceOnline : device.last_seen ? t.sessions.lastSeen(relTime(device.last_seen, now)) : t.sessions.neverConnected}
        </span>
      )}
      {sub && <span className="truncate font-mono text-xs text-fg-3">{sub}</span>}
      <span className="ml-auto flex items-center gap-3 text-xs text-fg-3">
        {counts.map(([st, n]) => (
          <span key={st} className="flex items-center gap-1.5">
            <StatusDot state={st} />
            <span className="tabular">{n}</span>
          </span>
        ))}
      </span>
    </div>
  )
}

export default function SessionsPage() {
  const now = useNow()
  const inbox = useInbox()
  const [filter, setFilter] = usePref<Filter>('ks.sessions.filter', 'active')
  const [group, setGroup] = usePref<Group>('ks.sessions.group', 'project')
  const [q, setQ] = useState('')

  const list = useMemo(() => {
    let ss = inbox.sessions
    if (filter === 'active') ss = ss.filter((s) => s.state !== 'ended')
    if (filter === 'mine') ss = ss.filter((s) => s.state === 'waiting_approval' || s.state === 'waiting_reply' || s.pending_decisions > 0)
    const k = q.trim().toLowerCase()
    if (k) ss = ss.filter((s) => [s.project_name, s.branch, s.cwd, s.last_prompt, s.summary, s.device_name, s.model].some((v) => v?.toLowerCase().includes(k)))
    return [...ss].sort(byRank)
  }, [inbox.sessions, filter, q])

  const groups = useMemo(() => {
    const m = new Map<string, Session[]>()
    for (const s of list) {
      const key = group === 'project' ? s.project_id || `cwd:${s.cwd}` : s.device_id
      m.set(key, [...(m.get(key) ?? []), s])
    }
    return [...m.entries()].sort((a, b) => byRank(a[1][0], b[1][0]))
  }, [list, group])

  const mine = inbox.sessions.filter((s) => s.state === 'waiting_approval' || s.state === 'waiting_reply' || s.pending_decisions > 0).length
  const active = inbox.sessions.filter((s) => s.state !== 'ended').length
  const devices = new Map(inbox.devices.map((d) => [d.id, d]))

  return (
    <>
      <PageHeader title={t.sessions.title} subtitle={inbox.loading ? ' ' : t.sessions.subtitle(inbox.running.length, inbox.sessions.length)} />

      <div className="mb-6 flex flex-wrap items-center gap-2">
        <Segmented
          id="sess-filter"
          value={filter}
          onChange={setFilter}
          options={[
            { value: 'active', label: t.sessions.filter.active, count: active },
            { value: 'mine', label: t.sessions.filter.mine, count: mine },
            { value: 'all', label: t.sessions.filter.all },
          ]}
        />
        <Segmented
          id="sess-group"
          value={group}
          onChange={setGroup}
          options={[
            { value: 'project', label: <FolderGit2 className="size-4" aria-label={t.sessions.byProject} /> },
            { value: 'device', label: <Monitor className="size-4" aria-label={t.sessions.byDevice} /> },
          ]}
        />
        <div className="relative ml-auto w-full sm:w-64">
          <Search className="pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2 text-fg-3" />
          <Input value={q} onChange={(e) => setQ(e.target.value)} placeholder={t.sessions.search} className="pl-9" />
        </div>
      </div>

      {inbox.error ? (
        <ErrorState error={inbox.error} onRetry={inbox.refetch} />
      ) : inbox.loading ? (
        <CardSkeleton rows={3} />
      ) : !inbox.devices.length ? (
        <Card>
          <Empty
            icon={<MonitorSmartphone />}
            title={t.sessions.noDevices}
            action={
              <Link to="/settings?tab=devices">
                <Button variant="primary">{t.sessions.addDevice}</Button>
              </Link>
            }
          >
            {t.sessions.noDevicesHint}
          </Empty>
        </Card>
      ) : !list.length ? (
        <Card>
          <Empty icon={<SearchX />} title={t.sessions.empty}>
            {t.sessions.emptyHint}
          </Empty>
        </Card>
      ) : (
        <div className="space-y-9">
          {groups.map(([key, ss]) => {
            const first = ss[0]
            const dev = devices.get(first.device_id)
            return (
              <section key={key}>
                {group === 'project' ? (
                  <GroupHeader icon={<FolderGit2 />} title={first.project_name || t.sessions.noProject} sessions={ss} />
                ) : (
                  <GroupHeader icon={<Monitor />} title={first.device_name} sub={dev?.hostname} sessions={ss} device={dev} />
                )}
                <div className="grid grid-cols-1 gap-3 md:grid-cols-2 2xl:grid-cols-3">
                  <AnimatePresence initial={false} mode="popLayout">
                    {ss.map((s) => (
                      <motion.div key={s.key} className="min-w-0" layout initial={{ opacity: 0, scale: 0.98 }} animate={{ opacity: 1, scale: 1 }} exit={{ opacity: 0, scale: 0.98 }} transition={{ duration: 0.2 }}>
                        <SessionCard s={s} now={now} showProject={group === 'device'} showDevice={group === 'project'} />
                      </motion.div>
                    ))}
                  </AnimatePresence>
                </div>
              </section>
            )
          })}
        </div>
      )}
    </>
  )
}
