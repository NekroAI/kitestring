import type { ReactNode } from 'react'
import { Link } from 'react-router'
import type { HubEvent } from '../api/client'
import { useSessionDetail } from '../api/queries'
import { t } from '../i18n'
import { absTime, relTime } from '../lib/time'
import { ToolChip } from '../pages/Wall'
import { CountBadge, Drawer, ErrorBox, Loading, ReqStatusChip, StateBadge, useNow } from './ui'

function Row({ label, children }: { label: string; children: ReactNode }) {
  if (children === '' || children == null) return null
  return (
    <div className="grid grid-cols-[6rem_1fr] gap-2 py-1.5 text-sm">
      <dt className="muted">{label}</dt>
      <dd className="min-w-0 break-words">{children}</dd>
    </div>
  )
}

function eventText(e: HubEvent): string {
  return e.prompt || e.tool_brief || e.summary || e.reason || e.source || e.notification_type || e.tool_name || ''
}

export function SessionDrawer({ sessionKey, onClose }: { sessionKey: string; onClose: () => void }) {
  const q = useSessionDetail(sessionKey)
  const now = useNow()
  return (
    <Drawer title={t.session.title} onClose={onClose}>
      {q.isPending ? (
        <Loading />
      ) : q.isError ? (
        <ErrorBox error={q.error} onRetry={() => q.refetch()} />
      ) : (
        (() => {
          const s = q.data.session
          const events = q.data.events ?? []
          return (
            <div className="space-y-4">
              <div className="card space-y-2 p-3">
                <div className="flex flex-wrap items-center gap-2">
                  <ToolChip tool={s.tool} />
                  <StateBadge state={s.state} />
                  {s.pending_decisions > 0 && (
                    <Link to="/decisions" className="inline-flex items-center gap-1 text-xs font-medium text-amber-700 dark:text-amber-400">
                      <CountBadge n={s.pending_decisions} />
                      {t.decisions.tabs.pending}
                    </Link>
                  )}
                </div>
                {s.state_detail && <div className="rounded-md bg-amber-50 px-2 py-1.5 font-mono text-xs break-all text-amber-900 dark:bg-amber-950/50 dark:text-amber-200">{s.state_detail}</div>}
                {s.last_prompt && (
                  <div className="text-sm break-words whitespace-pre-wrap">
                    <div className="muted text-xs">{t.wall.lastPrompt}</div>
                    {s.last_prompt}
                  </div>
                )}
                {s.summary && (
                  <div className="text-sm break-words">
                    <div className="muted text-xs">{t.wall.summary}</div>
                    {s.summary}
                  </div>
                )}
              </div>

              <dl className="card divide-y divide-slate-100 px-3 py-1 dark:divide-slate-800">
                <Row label={t.session.project}>{s.project_name}</Row>
                <Row label={t.session.device}>{s.device_name}</Row>
                <Row label={t.session.client}>{s.client}</Row>
                <Row label={t.session.model}>{s.model && <span className="font-mono">{s.model}</span>}</Row>
                <Row label={t.session.branch}>
                  {s.branch && (
                    <span className="font-mono">
                      {s.branch}
                      {s.dirty > 0 && <span className="muted ml-2">{t.wall.dirty(s.dirty)}</span>}
                    </span>
                  )}
                </Row>
                <Row label={t.session.cwd}>{s.cwd && <span className="font-mono text-xs">{s.cwd}</span>}</Row>
                <Row label={t.session.startedAt}>{absTime(s.started_at)}</Row>
                <Row label={t.session.lastActive}>{relTime(s.last_active_at, now)}</Row>
                <Row label={t.session.endedAt}>{s.ended_at ? absTime(s.ended_at) : ''}</Row>
                <Row label={t.session.key}>
                  <span className="font-mono text-xs">{s.key}</span>
                </Row>
              </dl>

              {(s.requirements?.length ?? 0) > 0 && (
                <section>
                  <h3 className="section-title mb-2">{t.session.requirements}</h3>
                  <div className="card divide-y divide-slate-100 dark:divide-slate-800">
                    {s.requirements!.map((r) => (
                      <Link key={r.id} to={`/requirements?r=${encodeURIComponent(r.code)}`} className="flex items-center gap-2 px-3 py-2.5 text-sm hover:bg-slate-50 dark:hover:bg-slate-800/50">
                        <span className="muted font-mono text-xs">{r.code}</span>
                        <span className="min-w-0 flex-1 truncate">{r.title}</span>
                        <ReqStatusChip status={r.status} />
                      </Link>
                    ))}
                  </div>
                </section>
              )}

              <section>
                <h3 className="section-title mb-2">{t.session.events}</h3>
                {events.length === 0 ? (
                  <p className="muted text-sm">{t.session.noEvents}</p>
                ) : (
                  <ol className="card divide-y divide-slate-100 dark:divide-slate-800">
                    {events.map((e) => (
                      <li key={e.id} className="px-3 py-2 text-sm">
                        <div className="flex items-center gap-2">
                          <span className="font-medium">{t.event[e.name] ?? e.name}</span>
                          {e.tool_name && <span className="chip chip-gray font-mono">{e.tool_name}</span>}
                          <span className="muted ml-auto shrink-0 text-xs tabular-nums">{absTime(e.time)}</span>
                        </div>
                        {eventText(e) && eventText(e) !== e.tool_name && <p className="muted mt-0.5 line-clamp-3 text-xs break-words">{eventText(e)}</p>}
                      </li>
                    ))}
                  </ol>
                )}
              </section>
            </div>
          )
        })()
      )}
    </Drawer>
  )
}
