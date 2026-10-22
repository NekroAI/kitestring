import { useMemo, useState } from 'react'
import { Link, useSearchParams } from 'react-router'
import type { Device, Session } from '../api/client'
import { useDecisions, useSessions } from '../api/queries'
import { CountBadge, Empty, ErrorBox, Loading, StateBadge, Toggle, cx, isWaiting, useNow } from '../components/ui'
import { SessionDrawer } from '../components/SessionDrawer'
import { t } from '../i18n'
import { relTime } from '../lib/time'

const stateOrder: Record<string, number> = { waiting_approval: 0, waiting_reply: 1, running: 2, idle: 3, lost: 4, ended: 5 }

function sortSessions(a: Session, b: Session) {
  const d = (stateOrder[a.state] ?? 9) - (stateOrder[b.state] ?? 9)
  if (d) return d
  return b.last_active_at.localeCompare(a.last_active_at)
}

function loadShowEnded() {
  try {
    return localStorage.getItem('ks.wall.showEnded') === '1'
  } catch {
    return false
  }
}

export default function WallPage() {
  const q = useSessions()
  const pending = useDecisions('pending')
  const now = useNow()
  const [params, setParams] = useSearchParams()
  const openKey = params.get('session')
  const [showEnded, setShowEnded] = useState(loadShowEnded)
  const [filter, setFilter] = useState<'' | 'waiting_approval' | 'waiting_reply'>('')

  const setEnded = (v: boolean) => {
    setShowEnded(v)
    try {
      localStorage.setItem('ks.wall.showEnded', v ? '1' : '0')
    } catch {
      // 忽略存储不可用
    }
  }

  const data = q.data
  const groups = useMemo(() => {
    if (!data) return []
    const byDevice = new Map<string, Session[]>()
    for (const s of data.sessions) {
      if (!showEnded && s.state === 'ended') continue
      if (filter && s.state !== filter) continue
      const arr = byDevice.get(s.device_id) ?? []
      arr.push(s)
      byDevice.set(s.device_id, arr)
    }
    const devices: Device[] = data.devices.filter((d) => !d.revoked || byDevice.has(d.id))
    // 在线设备在前，其次按名称
    devices.sort((a, b) => Number(b.online) - Number(a.online) || a.name.localeCompare(b.name))
    return devices
      .map((d) => ({ device: d, sessions: (byDevice.get(d.id) ?? []).sort(sortSessions) }))
      .filter((g) => !filter || g.sessions.length > 0)
  }, [data, showEnded, filter])

  if (q.isPending) return <Loading />
  if (q.isError) return <ErrorBox error={q.error} onRetry={() => q.refetch()} />

  const sessions = data!.sessions
  const approvalN = sessions.filter((s) => s.state === 'waiting_approval').length
  const replyN = sessions.filter((s) => s.state === 'waiting_reply').length
  const decisionN = pending.data?.length ?? 0
  const endedN = sessions.filter((s) => s.state === 'ended').length

  const open = (key: string | null) => {
    const p = new URLSearchParams(params)
    if (key) p.set('session', key)
    else p.delete('session')
    setParams(p, { replace: !key })
  }

  return (
    <div className="space-y-5">
      <section>
        <h2 className="section-title mb-2">{t.wall.attention}</h2>
        <div className="grid grid-cols-3 gap-2 sm:gap-3">
          <AttentionTile
            label={t.wall.waitingApproval}
            n={approvalN}
            tone="amber"
            active={filter === 'waiting_approval'}
            onClick={() => setFilter(filter === 'waiting_approval' ? '' : 'waiting_approval')}
          />
          <AttentionTile
            label={t.wall.waitingReply}
            n={replyN}
            tone="violet"
            active={filter === 'waiting_reply'}
            onClick={() => setFilter(filter === 'waiting_reply' ? '' : 'waiting_reply')}
          />
          <Link to="/decisions" className="block">
            <AttentionTile label={t.wall.pendingDecisions} n={decisionN} tone="sky" />
          </Link>
        </div>
      </section>

      <div className="flex flex-wrap items-center justify-between gap-2">
        <h1 className="text-lg font-semibold">{t.wall.title}</h1>
        <div className="flex items-center gap-3">
          {!showEnded && endedN > 0 && <span className="muted hidden text-xs sm:inline">{t.wall.endedHidden(endedN)}</span>}
          <Toggle checked={showEnded} onChange={setEnded}>
            {t.wall.showEnded}
          </Toggle>
        </div>
      </div>

      {groups.length === 0 ? (
        <Empty>{t.wall.empty}</Empty>
      ) : (
        groups.map(({ device, sessions }) => (
          <section key={device.id} className="space-y-2">
            <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
              <h2 className="flex items-center gap-2 font-semibold">
                <span className={cx('size-2 rounded-full', device.online ? 'bg-emerald-500' : 'bg-slate-400')} />
                {device.name}
              </h2>
              <span className={cx('text-xs', device.online ? 'text-emerald-700 dark:text-emerald-400' : 'muted')}>
                {device.online ? t.wall.online : t.wall.offline}
              </span>
              <span className="muted text-xs">{device.last_seen ? t.wall.lastSeen(relTime(device.last_seen, now)) : t.settings.devices.never}</span>
              <span className="muted text-xs">{t.wall.sessionCount(sessions.length)}</span>
            </div>
            {sessions.length === 0 ? (
              <p className="muted text-sm">{t.wall.noSessions}</p>
            ) : (
              <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-3">
                {sessions.map((s) => (
                  <SessionCard key={s.key} s={s} now={now} onOpen={() => open(s.key)} />
                ))}
              </div>
            )}
          </section>
        ))
      )}

      {openKey && <SessionDrawer sessionKey={openKey} onClose={() => open(null)} />}
    </div>
  )
}

function AttentionTile({ label, n, tone, active, onClick }: { label: string; n: number; tone: 'amber' | 'violet' | 'sky'; active?: boolean; onClick?: () => void }) {
  const hot = n > 0
  const tones = {
    amber: 'border-amber-400 bg-amber-50 text-amber-900 dark:border-amber-600 dark:bg-amber-950/60 dark:text-amber-200',
    violet: 'border-violet-400 bg-violet-50 text-violet-900 dark:border-violet-600 dark:bg-violet-950/60 dark:text-violet-200',
    sky: 'border-sky-400 bg-sky-50 text-sky-900 dark:border-sky-600 dark:bg-sky-950/60 dark:text-sky-200',
  }
  const Tag = onClick ? 'button' : 'div'
  return (
    <Tag
      type={onClick ? 'button' : undefined}
      onClick={onClick}
      className={cx(
        'flex h-full w-full flex-col items-start rounded-xl border px-3 py-2.5 text-left transition-colors',
        hot ? tones[tone] : 'border-slate-200 bg-white text-slate-500 dark:border-slate-800 dark:bg-slate-900 dark:text-slate-400',
        active && 'ring-2 ring-slate-900 ring-offset-2 ring-offset-slate-50 dark:ring-slate-100 dark:ring-offset-[#0b0f14]',
      )}
    >
      <span className="text-2xl leading-tight font-semibold tabular-nums">{n}</span>
      <span className="text-xs font-medium">{label}</span>
    </Tag>
  )
}

export function ToolChip({ tool }: { tool: string }) {
  return (
    <span
      className={cx(
        'chip',
        tool === 'claude' ? 'bg-orange-100 text-orange-800 dark:bg-orange-950 dark:text-orange-300' : 'bg-slate-900 text-white dark:bg-slate-200 dark:text-slate-900',
      )}
    >
      {t.tool[tool] ?? tool}
    </span>
  )
}

function SessionCard({ s, now, onOpen }: { s: Session; now: number; onOpen: () => void }) {
  const waiting = isWaiting(s.state)
  const dim = s.state === 'ended' || s.state === 'lost'
  return (
    <button
      type="button"
      onClick={onOpen}
      className={cx(
        'card flex w-full min-w-0 flex-col gap-2 p-3 text-left transition-shadow hover:shadow-md',
        s.state === 'waiting_approval' && 'border-amber-400 ring-1 ring-amber-400 dark:border-amber-600 dark:ring-amber-600',
        s.state === 'waiting_reply' && 'border-violet-400 ring-1 ring-violet-400 dark:border-violet-600 dark:ring-violet-600',
        dim && 'opacity-70',
      )}
    >
      <div className="flex w-full items-center gap-1.5">
        <ToolChip tool={s.tool} />
        {s.client && <span className="chip chip-gray max-w-[8rem] truncate">{s.client}</span>}
        {s.model && <span className="muted min-w-0 truncate font-mono text-xs">{s.model}</span>}
        <span className="ml-auto shrink-0">
          <StateBadge state={s.state} />
        </span>
      </div>

      <div className="flex w-full min-w-0 flex-wrap items-center gap-x-2 gap-y-1 text-sm">
        <span className="min-w-0 truncate font-semibold">{s.project_name || t.wall.noProject}</span>
        {s.branch && (
          <span className="muted inline-flex min-w-0 items-center gap-1 font-mono text-xs">
            <svg viewBox="0 0 16 16" className="size-3 shrink-0" fill="none" stroke="currentColor" strokeWidth="1.5">
              <circle cx="4" cy="3.5" r="1.5" />
              <circle cx="4" cy="12.5" r="1.5" />
              <circle cx="12" cy="5.5" r="1.5" />
              <path d="M4 5v6M12 7c0 3-8 2-8 4" />
            </svg>
            <span className="truncate">{s.branch}</span>
          </span>
        )}
        {s.dirty > 0 && <span className="chip bg-orange-50 text-orange-700 dark:bg-orange-950/60 dark:text-orange-300">{t.wall.dirty(s.dirty)}</span>}
      </div>

      {s.state_detail && (
        <div
          className={cx(
            'w-full rounded-md px-2 py-1.5 font-mono text-xs break-all',
            waiting ? 'bg-amber-50 text-amber-900 dark:bg-amber-950/50 dark:text-amber-200' : 'chip-gray',
          )}
        >
          <span className="line-clamp-2">{s.state_detail}</span>
        </div>
      )}

      {s.last_prompt && (
        <p className="line-clamp-2 w-full text-sm break-words">
          <span className="muted mr-1.5 text-xs">{t.wall.lastPrompt}</span>
          {s.last_prompt}
        </p>
      )}
      {s.summary && <p className="muted line-clamp-2 w-full text-sm break-words">{s.summary}</p>}

      {(s.requirements?.length ?? 0) > 0 && (
        <div className="flex w-full flex-wrap gap-1">
          {s.requirements!.map((r) => (
            <span key={r.id} className="chip max-w-full bg-sky-50 text-sky-800 dark:bg-sky-950/60 dark:text-sky-300">
              <span className="font-mono">{r.code}</span>
              <span className="truncate">{r.title}</span>
            </span>
          ))}
        </div>
      )}

      <div className="mt-auto flex w-full items-center gap-2 pt-1 text-xs">
        {s.pending_decisions > 0 && (
          <span className="inline-flex items-center gap-1 font-medium text-amber-700 dark:text-amber-400">
            <CountBadge n={s.pending_decisions} />
            {t.decisions.tabs.pending}
          </span>
        )}
        <span className="muted ml-auto">{relTime(s.last_active_at, now)}</span>
      </div>
    </button>
  )
}
