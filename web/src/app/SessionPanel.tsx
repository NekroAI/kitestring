import { useMemo, type ReactNode } from 'react'
import { Copy, Link2, Plus, Unlink } from 'lucide-react'
import { toast } from 'sonner'
import type { HubEvent, Session } from '../api/client'
import { useDecisions, useRequirements, useSessionDetail } from '../api/queries'
import { t } from '../i18n'
import { absTime, clockTime, copyText, cx, duration, relTime, shortPath, useNow } from '../lib/util'
import { Badge, Button, ErrorState, Skeleton } from '../ui/base'
import { Menu, Panel } from '../ui/overlay'
import { displayState, PhaseBadge, Sparkline, StatusPill, ToolMark } from '../ui/marks'
import { useActions } from './actions'
import { DecisionComposer, DecisionRow } from './DecisionCard'
import { useOpenReq } from './nav'
import { Callout, sessionTitle, toolIcon } from './SessionCard'

function Section({ title, children, right }: { title: ReactNode; children: ReactNode; right?: ReactNode }) {
  return (
    <section className="mb-6">
      <div className="mb-2.5 flex items-center justify-between">
        <h3 className="text-[13px] font-semibold text-fg-2">{title}</h3>
        {right}
      </div>
      {children}
    </section>
  )
}

function Info({ label, children, mono }: { label: string; children: ReactNode; mono?: boolean }) {
  return (
    <div className="min-w-0">
      <dt className="text-xs text-fg-3">{label}</dt>
      <dd className={cx('mt-0.5 truncate text-sm text-fg', mono && 'font-mono text-[13px]')}>{children || '—'}</dd>
    </div>
  )
}

const asks = (tool?: string) => tool === 'AskUserQuestion' || tool === 'request_user_input'

function evLabel(e: HubEvent): string {
  if ((e.name === 'PermissionRequest' || e.name === 'PreToolUse') && asks(e.tool_name)) return t.session.ev.Ask
  if ((e.name === 'PermissionRequest' || e.name === 'PreToolUse') && e.tool_name === 'ExitPlanMode') return t.session.ev.Plan
  if (e.name === 'SessionStart' && e.source === 'resume') return t.session.ev.SessionStartResume
  if (e.name === 'SessionStart' && e.source === 'compact') return t.session.ev.SessionStartCompact
  if (e.name === 'UserPromptSubmit' && e.source === 'auto') return t.session.ev.UserPromptAuto
  return t.session.ev[e.name] ?? e.name
}

// 时间线：按回合（UserPromptSubmit）分段，工具调用合并显示。
function Timeline({ events }: { events: HubEvent[] }) {
  const rows = useMemo(() => {
    const asc = [...events].reverse().filter((e) => e.name !== 'PostToolUse')
    return asc.reverse()
  }, [events])
  if (!rows.length) return <p className="text-sm text-fg-3">{t.session.noEvents}</p>
  return (
    <ol className="relative space-y-0.5 before:absolute before:top-2 before:bottom-2 before:left-[7px] before:w-px before:bg-line">
      {rows.map((e) => {
        const userFacing = asks(e.tool_name) || e.tool_name === 'ExitPlanMode'
        const tool = (e.name === 'PreToolUse' || e.name === 'PermissionRequest') && !userFacing
        const Icon = tool ? toolIcon(e.tool_name ?? '') : null
        const emph = (e.name === 'UserPromptSubmit' && e.source !== 'auto') || e.name === 'PermissionRequest' || e.name === 'Stop'
        const dot = userFacing ? 'bg-reply' : e.name === 'PermissionRequest' ? 'bg-wait' : e.name === 'Stop' ? 'bg-reply' : e.name === 'UserPromptSubmit' ? 'bg-accent' : 'bg-line-strong'
        return (
          <li key={e.id} className="relative flex gap-3 py-1.5 pl-0">
            <span className={cx('relative z-10 mt-1.5 size-[15px] shrink-0 rounded-full border-[3px] border-surface', dot)} />
            <div className="min-w-0 flex-1">
              <div className="flex items-baseline gap-2">
                <span className={cx('text-[13px]', emph ? 'font-medium text-fg' : 'text-fg-2')}>{evLabel(e)}</span>
                {tool && (
                  <span className="inline-flex min-w-0 items-center gap-1 text-[12px] text-fg-2">
                    {Icon && <Icon className="size-3.5 shrink-0 text-fg-3" />}
                    <span className="shrink-0">{e.tool_name?.replace(/^mcp__/, '')}</span>
                    {e.tool_brief && <span className="truncate font-mono text-fg-3">{e.tool_brief}</span>}
                  </span>
                )}
                <span className="ml-auto shrink-0 text-[11px] text-fg-3 tabular">{clockTime(e.time)}</span>
              </div>
              {userFacing && e.tool_brief && <p className="mt-1 rounded-lg bg-reply-soft px-2.5 py-1.5 text-[13px] leading-relaxed text-fg">{e.tool_brief}</p>}
              {e.prompt && <p className="mt-1 rounded-lg bg-surface-2 px-2.5 py-1.5 text-[13px] leading-relaxed break-words text-fg-2">{e.prompt}</p>}
            </div>
          </li>
        )
      })}
    </ol>
  )
}

export function SessionPanel({ sessionKey, onClose }: { sessionKey: string | null; onClose: () => void }) {
  const now = useNow()
  const q = useSessionDetail(sessionKey)
  const decisions = useDecisions()
  const reqs = useRequirements()
  const { linkReq } = useActions()
  const openReq = useOpenReq()
  const s: Session | undefined = q.data?.session
  const mine = (decisions.data ?? []).filter((d) => d.session_key === sessionKey)
  const pending = mine.filter((d) => d.status === 'pending')
  const linkable = (reqs.data ?? []).filter((r) => !s?.requirements.some((x) => x.id === r.id) && r.status !== 'done' && r.status !== 'shelved')

  return (
    <Panel
      open={!!sessionKey}
      onClose={onClose}
      wide
      title={
        s ? (
          <span className="flex items-center gap-2.5">
            <ToolMark tool={s.tool} size="sm" />
            <span className="truncate">{sessionTitle(s)}</span>
            <PhaseBadge phase={s.phase} />
          </span>
        ) : (
          <Skeleton className="h-5 w-40" />
        )
      }
      subtitle={
        s && (
          <span className="flex flex-wrap items-center gap-2">
            <StatusPill state={displayState(s)} since={s.state_since} now={now} />
            {s.title && s.project_name && <span>{s.project_name}</span>}
          </span>
        )
      }
    >
      {q.isError ? (
        <ErrorState error={q.error} onRetry={() => q.refetch()} />
      ) : !s ? (
        <div className="space-y-3">
          <Skeleton className="h-16" />
          <Skeleton className="h-32" />
        </div>
      ) : (
        <>
          <div className="mb-5 empty:hidden">
            <Callout s={s} />
          </div>

          <Section title={t.session.current} right={<Sparkline data={s.activity} state={s.state} />}>
            <div className="space-y-2.5 rounded-2xl border border-line p-3.5">
              {s.summary && (
                <div>
                  <div className="text-xs text-fg-3">{t.session.summary}</div>
                  <p className="mt-0.5 text-sm leading-relaxed text-fg">{s.summary}</p>
                </div>
              )}
              {s.last_prompt && (
                <div>
                  <div className="text-xs text-fg-3">{t.session.lastPrompt}</div>
                  <p className="mt-0.5 text-sm leading-relaxed break-words text-fg-2">{s.last_prompt}</p>
                </div>
              )}
              {s.last_tool && (
                <div className="flex items-center gap-2 text-[13px] text-fg-2">
                  {(() => {
                    const I = toolIcon(s.last_tool)
                    return <I className="size-4 text-fg-3" />
                  })()}
                  <span className="min-w-0 flex-1 truncate font-mono text-[12px]">{s.last_tool}</span>
                  <span className="text-xs text-fg-3">{relTime(s.last_tool_at, now)}</span>
                </div>
              )}
              <div className="flex flex-wrap gap-x-4 gap-y-1 text-xs text-fg-3">
                {s.turns > 0 && <span>{t.session.turns(s.turns)}</span>}
                {s.turn_tools > 0 && <span>{t.session.toolsTip(s.turn_tools)}</span>}
                {s.subagents > 0 && <span>{t.session.subagentsTip(s.subagents)}</span>}
                {s.compactions > 0 && <span>{t.session.compactions(s.compactions)}</span>}
              </div>
            </div>
          </Section>

          {pending.length > 0 && (
            <Section title={t.decision.status.pending}>
              <div className="space-y-3">
                {pending.map((d) => (
                  <DecisionComposer key={d.id} d={d} embedded />
                ))}
              </div>
            </Section>
          )}

          <Section
            title={t.session.requirements}
            right={
              <Menu
                width="min-w-[280px]"
                trigger={
                  <Button size="sm" variant="ghost" icon={<Plus className="size-3.5" />} disabled={!linkable.length}>
                    {t.session.link}
                  </Button>
                }
                items={[
                  { type: 'label', label: t.session.linkPick },
                  ...linkable.slice(0, 30).map((r) => ({
                    label: `${r.code} ${r.title}`,
                    hint: [t.reqStatus[r.status], r.project_name].filter(Boolean).join(t.common.sep),
                    icon: <Link2 />,
                    onSelect: () => void linkReq(r.code, s.key),
                  })),
                ]}
              />
            }
          >
            {s.requirements.length ? (
              <div className="divide-y divide-line rounded-2xl border border-line">
                {s.requirements.map((r) => (
                  <div key={r.id} className="flex items-center gap-2 px-3 py-2">
                    <button onClick={() => openReq(r.code)} className="flex min-w-0 flex-1 items-center gap-2 text-left">
                      <span className="font-mono text-[11px] font-semibold text-accent">{r.code}</span>
                      <span className="truncate text-sm text-fg">{r.title}</span>
                    </button>
                    <Badge>{t.reqStatus[r.status]}</Badge>
                    <Button size="icon-sm" variant="ghost" aria-label={t.session.unlink} title={t.session.unlink} onClick={() => void linkReq(r.code, s.key, true)}>
                      <Unlink className="size-3.5" />
                    </Button>
                  </div>
                ))}
              </div>
            ) : (
              <p className="text-sm text-fg-3">{t.session.noReqs}</p>
            )}
          </Section>

          <Section title={t.session.info}>
            <dl className="grid grid-cols-2 gap-x-4 gap-y-3 rounded-2xl border border-line p-3.5">
              <Info label={t.session.device}>{s.device_name}</Info>
              <Info label={t.session.client}>{[t.tool[s.tool], s.client].filter(Boolean).join(t.common.sep)}</Info>
              <Info label={t.session.model} mono>
                {s.model}
              </Info>
              <Info label={t.session.permMode}>{t.permMode[s.permission_mode] || s.permission_mode}</Info>
              <Info label={t.session.branch} mono>
                {s.branch && `${s.branch}${s.dirty ? ` ±${s.dirty}` : ''}`}
              </Info>
              <Info label={t.session.startedAt}>{`${absTime(s.started_at)}（${duration(s.started_at, now)}）`}</Info>
              <div className="col-span-2">
                <Info label={t.session.cwd} mono>
                  <span title={s.cwd}>{shortPath(s.cwd, 64)}</span>
                </Info>
              </div>
              <div className="col-span-2 flex items-end gap-2">
                <Info label={t.session.sessionId} mono>
                  {s.session_id}
                </Info>
                <Button size="icon-sm" variant="ghost" aria-label={t.common.copy} onClick={async () => (await copyText(s.session_id)) && toast.success(t.common.copied)}>
                  <Copy className="size-3.5" />
                </Button>
              </div>
            </dl>
          </Section>

          {mine.some((d) => d.status !== 'pending') && (
            <Section title={t.session.decisions}>
              <div className="overflow-hidden rounded-2xl border border-line">
                {mine
                  .filter((d) => d.status !== 'pending')
                  .map((d) => (
                    <DecisionRow key={d.id} d={d} />
                  ))}
              </div>
            </Section>
          )}

          <Section title={t.session.timeline}>
            <Timeline events={q.data?.events ?? []} />
          </Section>
        </>
      )}
    </Panel>
  )
}
