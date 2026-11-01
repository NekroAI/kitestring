import { useLayoutEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { ArrowLeftRight, ChevronDown, ChevronRight, Copy, MonitorDot, Plus, RefreshCw, Send, Unlink } from 'lucide-react'
import { toast } from 'sonner'
import { useNavigate } from 'react-router'
import type { HubEvent, Session } from '../api/client'
import { useDecisions, useRequirements, useSessionDetail } from '../api/queries'
import { t } from '../i18n'
import { Collapse, Item, List } from '../ui/motion'
import { absTime, clockTime, copyText, cx, duration, relTime, shortPath, useNow } from '../lib/util'
import { Button, ErrorState, Skeleton, Tip } from '../ui/base'
import { Menu, Panel } from '../ui/overlay'
import { Md } from '../ui/markdown'
import { displayState, RoleBadge, Sparkline, StatusPill, ToolMark } from '../ui/marks'
import { useActions } from './actions'
import { DecisionRow } from './DecisionCard'
import { useOpenAsk } from './nav'
import { PeekScope, RefPeek, usePeek } from './peek'
import { AskItem, decisionAsk } from './asks'
import { decKey } from './outbox'
import { usePendingMap } from './verdict'
import { CtxRing, ctxLoad, useAssign } from './assign'
import { canHandoff, CompactRow, HandoffCard, HandoffIcon, IdleRow, useRequestHandoff, type Handoff } from './compact'
import { NewReqModal } from './requirements'
import { OwnBadge, useOwnership } from './ownership'
import { Callout, sessionTitle, toolIcon } from './SessionCard'
import { canMessage, MessageBox } from './messageBox'
import { bgCount, liveTasks, TaskList, tasksUnknown } from './tasks'
import { Linkify, RefChip, ReqGlyph, ReqStatus } from '../ui/refmark'

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

// Fold：收起的一行入口（方案 A，R-75）——历史和细节默认不展开，点开再看。
function Fold({ label, hint, children }: { label: ReactNode; hint?: ReactNode; children: ReactNode }) {
  const [open, setOpen] = useState(false)
  return (
    <div className="overflow-hidden rounded-2xl border border-line">
      <button onClick={() => setOpen(!open)} aria-expanded={open} className="flex w-full items-center gap-2 px-3.5 py-3 text-left text-sm transition hover:bg-hover">
        <span className="min-w-0 flex-1 truncate font-medium text-fg">{label}</span>
        {hint != null && <span className="shrink-0 truncate text-[13px] text-fg-3">{hint}</span>}
        <ChevronRight className={cx('size-4 shrink-0 text-fg-3 transition-transform', open && 'rotate-90')} />
      </button>
      <Collapse show={open}>
        <div className="border-t border-line">{children}</div>
      </Collapse>
    </div>
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

// LastReply：这一轮结束时 Agent 的最后一条回复（R-109），Markdown 显示；超过一屏的默认折叠，点开看全文。
const replyFold = 280
function LastReply({ text, at, now }: { text: string; at: string; now: number }) {
  const ref = useRef<HTMLDivElement>(null)
  const [long, setLong] = useState(false)
  const [open, setOpen] = useState(false)
  useLayoutEffect(() => {
    setOpen(false)
    if (ref.current) setLong(ref.current.scrollHeight > replyFold + 80)
  }, [text])
  const folded = long && !open
  return (
    <Section
      title={t.session.lastReply}
      right={
        <span className="flex items-center gap-1">
          <span className="text-xs text-fg-3" title={absTime(at)}>
            {t.session.lastReplyAt(relTime(at, now))}
          </span>
          <Button size="icon-sm" variant="ghost" aria-label={t.common.copy} title={t.common.copy} onClick={async () => (await copyText(text)) && toast.success(t.common.copied)}>
            <Copy className="size-3.5" />
          </Button>
        </span>
      }
    >
      <div className="overflow-hidden rounded-2xl border border-line">
        <div ref={ref} className="relative px-3.5 py-3" style={folded ? { maxHeight: replyFold, overflow: 'hidden' } : undefined}>
          <Md text={text} />
          {folded && <div className="pointer-events-none absolute inset-x-0 bottom-0 h-16 bg-linear-to-t from-surface to-transparent" />}
        </div>
        {long && (
          <button onClick={() => setOpen(!open)} aria-expanded={open} className="flex w-full items-center justify-center gap-1 border-t border-line py-2 text-[13px] text-accent transition hover:bg-hover">
            {open ? t.session.collapseReply : t.session.expandReply}
            <ChevronDown className={cx('size-4 transition-transform', open && 'rotate-180')} />
          </button>
        )}
      </div>
    </Section>
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

// 时间线：每个事件一行。工具调用用图标代替名称（悬停显示），一行只占很小的高度。
function Timeline({ events }: { events: HubEvent[] }) {
  const rows = useMemo(() => events.filter((e) => e.name !== 'PostToolUse'), [events])
  if (!rows.length) return <p className="text-sm text-fg-3">{t.session.noEvents}</p>
  return (
    <ol className="relative before:absolute before:top-2 before:bottom-2 before:left-[7px] before:w-px before:bg-line">
      {rows.map((e) => {
        const userFacing = asks(e.tool_name) || e.tool_name === 'ExitPlanMode'
        const isTool = (e.name === 'PreToolUse' || e.name === 'PermissionRequest') && !userFacing
        if (isTool) {
          const Icon = toolIcon(e.tool_name ?? '')
          const name = e.tool_name?.replace(/^mcp__/, '') ?? ''
          const waiting = e.name === 'PermissionRequest'
          return (
            <li key={e.id} className="relative flex items-center gap-3 py-[3px]">
              <span className="relative z-10 flex size-[15px] shrink-0 items-center justify-center">
                <span className={cx('size-[7px] rounded-full border-2 border-surface', waiting ? 'bg-wait' : 'bg-line-strong')} />
              </span>
              <Tip content={waiting ? `${t.session.ev.PermissionRequest} · ${name}` : name}>
                <span className={cx('flex size-5 shrink-0 items-center justify-center rounded-md', waiting ? 'bg-wait-soft text-wait' : 'bg-sunken text-fg-2')} aria-label={name}>
                  <Icon className="size-3.5" />
                </span>
              </Tip>
              <span className={cx('min-w-0 flex-1 truncate font-mono text-[12px]', waiting ? 'text-fg' : 'text-fg-3')}>{e.tool_brief || name}</span>
              <span className="shrink-0 text-[11px] text-fg-3 tabular">{clockTime(e.time)}</span>
            </li>
          )
        }
        const auto = e.name === 'UserPromptSubmit' && e.source === 'auto'
        const emph = (e.name === 'UserPromptSubmit' && !auto) || e.name === 'Stop' || userFacing
        const dot = userFacing ? 'bg-reply' : e.name === 'Stop' ? (e.bg_tasks ? 'bg-accent' : 'bg-reply') : e.name === 'UserPromptSubmit' && !auto ? 'bg-accent' : 'bg-line-strong'
        return (
          <li key={e.id} className="relative flex gap-3 py-1.5">
            <span className={cx('relative z-10 mt-1 size-[15px] shrink-0 rounded-full border-[3px] border-surface', dot)} />
            <div className="min-w-0 flex-1">
              <div className="flex items-baseline gap-2">
                <span className={cx('text-[13px]', emph ? 'font-medium text-fg' : 'text-fg-2')}>{evLabel(e)}</span>
                {e.name === 'Stop' && !!e.bg_tasks && <span className="truncate text-[12px] text-accent">{t.state.background}</span>}
                <span className="ml-auto shrink-0 text-[11px] text-fg-3 tabular">{clockTime(e.time)}</span>
              </div>
              {userFacing && e.tool_brief && <p className="mt-1 rounded-lg bg-reply-soft px-2.5 py-1.5 text-[13px] leading-relaxed text-fg">{e.tool_brief}</p>}
              {e.prompt && !auto && <p className="mt-1 rounded-lg bg-surface-2 px-2.5 py-1.5 text-[13px] leading-relaxed break-words text-fg-2">{e.prompt}</p>}
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
  const assign = useAssign()
  const ownership = useOwnership()
  const [creating, setCreating] = useState(false)
  const openAsk = useOpenAsk()
  // 点需求 / 引用在会话抽屉左边再展开一张纸（R-196，和需求、审批、计划抽屉同一张纸）
  const peek = usePeek(null, sessionKey)
  const shown = peek.stack.length === 1 ? peek.stack[0] : null
  const s: Session | undefined = q.data?.session
  const mine = (decisions.data ?? []).filter((d) => d.session_key === sessionKey)
  const pendMap = usePendingMap()
  // 撤销期一结束就在本地离开（R-230）
  const pending = mine.filter((d) => d.status === 'pending' && !(pendMap.get(decKey(d.code))?.phase && pendMap.get(decKey(d.code))?.phase !== 'wait'))
  // 能交给它开工的需求：没做完、没锁在计划后面阶段、不是它已经在做的；同项目、高优先级排前面
  const rank: Record<string, number> = { P0: 0, P1: 1, P2: 2, P3: 3 }
  const givable = (reqs.data ?? [])
    .filter((r) => ['draft', 'ready', 'in_progress', 'blocked'].includes(r.status) && r.stage_status !== 'locked' && !(r.status === 'in_progress' && r.sessions.includes(sessionKey ?? '')) && r.offered_to !== sessionKey)
    .sort((a, b) => Number(b.project_id === s?.project_id) - Number(a.project_id === s?.project_id) || (rank[a.priority] ?? 9) - (rank[b.priority] ?? 9) || a.code.localeCompare(b.code, undefined, { numeric: true }))
  const full = new Map((reqs.data ?? []).map((r) => [r.id, r]))
  // 方案 A（R-75）：第一屏只列还没做完的需求，已完成 / 搁置的收进“更多”
  const closed = (st: string) => st === 'done' || st === 'shelved'
  const activeReqs = (s?.requirements ?? []).filter((r) => !closed(r.status))
  const doneReqs = (s?.requirements ?? []).filter((r) => closed(r.status))
  const answered = mine.filter((d) => d.status !== 'pending')
  const events = q.data?.events ?? []
  const handoffs = (q.data?.handoffs ?? []) as Handoff[]
  // 交接也记进时间线（R-224）：写了交接 / 接任读了交接
  const timeline = useMemo(() => {
    const extra = handoffs.flatMap((h) => {
      const rows: HubEvent[] = []
      if (h.written_at && h.from_session === sessionKey) rows.push({ id: `hw-${h.id}`, name: 'KsHandoffWritten', time: h.written_at } as HubEvent)
      if (h.taken_at && h.taken_by === sessionKey) rows.push({ id: `ht-${h.id}`, name: 'KsHandoffTaken', time: h.taken_at } as HubEvent)
      return rows
    })
    return extra.length ? [...events, ...extra].sort((a, b) => (b.time ?? '').localeCompare(a.time ?? '')) : events
  }, [events, handoffs, sessionKey])
  const requestHandoff = useRequestHandoff()
  const navigate = useNavigate()
  const lastAt = events.reduce((m, e) => (e.time && e.time > m ? e.time : m), '')
  const reqRow = (r: Session['requirements'][number]) => (
    <div key={r.id} className="flex items-center gap-2 px-3 py-2">
      <button onClick={() => peek.open(r.code)} className={cx('flex min-w-0 flex-1 items-center gap-2 rounded-md text-left', shown === r.code && 'bg-accent-soft/60 ring-1 ring-accent/30')}>
        <RefChip code={r.code} status={r.status} link={false} />
        <span className="truncate text-sm text-fg">{r.title}</span>
      </button>
      {(() => {
        const own = full.get(r.id) && ownership(full.get(r.id)!)
        return own && own.session?.key === s?.key && own.tone !== 'working' ? <OwnBadge own={own} /> : <ReqStatus status={r.status} />
      })()}
      <Button size="icon-sm" variant="ghost" aria-label={t.session.unlink} title={t.session.unlink} onClick={() => s && void linkReq(r.code, s.key, true)}>
        <Unlink className="size-3.5" />
      </Button>
    </div>
  )

  const aside = peek.stack.length && sessionKey ? <RefPeek peek={peek} from={`s:${sessionKey}`} /> : undefined

  return (
    <PeekScope peek={peek}>
    <Panel
      open={!!sessionKey}
      aside={aside}
      onAsideClose={peek.close}
      onClose={onClose}
      wide
      actions={
        s && (
          <>
            {/* 通讯页入口（R-249）：面板里不列往来消息，只放这一个入口 */}
            {s.project_id && (
              <Button size="sm" variant="ghost" icon={<ArrowLeftRight className="size-3.5" />} onClick={() => navigate(`/comms?project=${encodeURIComponent(s.project_id ?? "")}&session=${encodeURIComponent(s.key)}`)}>
                {t.comms.open}
              </Button>
            )}
            {canHandoff(s) && (
              <Tip content={t.menu.handoffHint}>
                <Button size="sm" variant="ghost" icon={<HandoffIcon className="size-3.5" />} onClick={() => void requestHandoff(s)}>
                  {t.handoff.action}
                </Button>
              </Tip>
            )}
          </>
        )
      }
      title={
        s ? (
          <span className="flex items-center gap-2.5">
            <ToolMark tool={s.tool} size="sm" />
            <span className="truncate">{sessionTitle(s)}</span>
            <RoleBadge s={s} />
          </span>
        ) : (
          <Skeleton className="h-5 w-40" />
        )
      }
      subtitle={
        s && (
          <span className="flex flex-wrap items-center gap-2">
            <StatusPill state={displayState(s)} since={s.state_since} now={now} bg={bgCount(s)} />
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
          <div className="mb-5 empty:hidden">
            <CompactRow s={s} />
          </div>
          <div className="mb-5 empty:hidden">
            <IdleRow s={s} now={now} />
          </div>
          {handoffs.length > 0 && (
            <Section title={t.handoff.title}>
              <div className="space-y-2">
                {handoffs.slice(0, 2).map((h) => (
                  <HandoffCard key={h.id} h={h} me={s.key} />
                ))}
              </div>
            </Section>
          )}

          <Collapse show={pending.length > 0}>
            <Section title={t.decision.status.pending}>
              <List space={12}>
                {/* 只放固定大小的条目，点开审批抽屉作答（R-177） */}
                {pending.map((d) => (
                  <Item key={d.id} space={12}>
                    <AskItem a={decisionAsk(d)} now={now} onOpen={openAsk} swipe={false} />
                  </Item>
                ))}
              </List>
            </Section>
          </Collapse>

          {q.data?.lastReply && <LastReply text={q.data.lastReply.text} at={q.data.lastReply.at} now={now} />}

          {canMessage(s) && (
            <Section title={t.session.message}>
              <MessageBox s={s} />
            </Section>
          )}

          <Section title={t.session.current} right={<Sparkline data={s.activity} state={s.state} />}>
            <div className="space-y-2.5 rounded-2xl border border-line p-3.5">
              {s.summary && (
                <div>
                  <div className="text-xs text-fg-3">{t.session.summary}</div>
                  <p className="mt-0.5 text-sm leading-relaxed text-fg">
                    <Linkify text={s.summary} />
                  </p>
                </div>
              )}
              {s.last_prompt && (
                <div>
                  <div className="text-xs text-fg-3">{t.session.lastPrompt}</div>
                  <p className="mt-0.5 text-sm leading-relaxed break-words text-fg-2">
                    <Linkify text={s.last_prompt} />
                  </p>
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


          {(liveTasks(s).length > 0 || (tasksUnknown(s) && s.state !== 'ended')) && (
            <Section title={<span>{t.session.tasks}<span className="ml-1.5 font-normal text-fg-3">{liveTasks(s).length || ''}</span></span>}>
              <TaskList s={s} now={now} />
            </Section>
          )}

          <Section
            title={
              <span>
                {t.session.activeReqs}
                <span className="ml-1.5 font-normal text-fg-3">{doneReqs.length ? t.session.activeReqsHint(activeReqs.length, doneReqs.length) : activeReqs.length || ''}</span>
              </span>
            }
            right={
              <span className="flex items-center gap-1">
                <Button size="sm" variant="ghost" icon={<Plus className="size-3.5" />} onClick={() => setCreating(true)}>
                  {t.session.giveNew}
                </Button>
                <Menu
                  width="min-w-[300px]"
                  trigger={
                    <Button size="sm" variant="ghost" icon={<Send className="size-3.5" />} disabled={!givable.length}>
                      {t.session.give}
                    </Button>
                  }
                  items={[
                    { type: 'label', label: t.session.givePick },
                    ...givable.slice(0, 30).map((r) => ({
                      label: `${r.code} ${r.title}`,
                      icon: <ReqGlyph status={r.status} />,
                      hint: [t.reqStatus[r.status], r.priority, r.project_id !== s.project_id && r.project_name].filter(Boolean).join(t.common.sep),
                      onSelect: () => void assign(r.code, s),
                    })),
                  ]}
                />
              </span>
            }
          >
            {activeReqs.length ? (
              <div className="divide-y divide-line rounded-2xl border border-line">{activeReqs.map(reqRow)}</div>
            ) : (
              <p className="text-sm text-fg-3">{s.requirements.length ? t.session.noActiveReqs : t.session.noReqs}</p>
            )}
          </Section>

          {s.tools_stale && (
          <div className="mb-3 flex items-start gap-2 rounded-xl bg-wait-soft/70 px-3 py-2.5 text-[13px] leading-relaxed text-fg-2">
            <RefreshCw className="mt-0.5 size-4 shrink-0 text-wait" />
            {t.session.toolsStaleTip(s.tools_stale)}
          </div>
          )}
          {s.other_terminal && (
          <div className="mb-3 flex items-start gap-2 rounded-xl bg-accent-soft/70 px-3 py-2.5 text-[13px] leading-relaxed text-fg-2">
            <MonitorDot className="mt-0.5 size-4 shrink-0 text-accent" />
            {t.session.otherTerminal}
          </div>
          )}

          <Section title={t.session.more}>
            <div className="space-y-2">
              {doneReqs.length > 0 && (
                <Fold label={t.session.doneReqs} hint={doneReqs.length}>
                  <div className="divide-y divide-line">{doneReqs.map(reqRow)}</div>
                </Fold>
              )}
              {answered.length > 0 && (
                <Fold label={t.session.answered} hint={answered.length}>
                  {answered.map((d) => (
                    <DecisionRow key={d.id} d={d} />
                  ))}
                </Fold>
              )}
              <Fold label={t.session.timeline} hint={events.length ? t.session.lastActivity(relTime(lastAt, now)) : undefined}>
                <div className="p-3">
                  <Timeline events={timeline} />
                </div>
              </Fold>
              <Fold label={t.session.sessionInfo} hint={t.session.infoHint}>
                <div className="p-3.5">
            <dl className="grid grid-cols-2 gap-x-4 gap-y-3 ">
              <Info label={t.session.device}>{s.device_name}</Info>
              <Info label={t.session.client}>{[t.tool[s.tool], s.profile, s.client].filter(Boolean).join(t.common.sep)}</Info>
              <Info label={t.session.model} mono>
                {s.model}
              </Info>
              <Info label={t.session.ctxLabel}>
                {ctxLoad(s) && (
                  <span className="inline-flex items-center gap-1.5">
                    <CtxRing c={ctxLoad(s)!} />
                    {ctxLoad(s)!.label}
                    {ctxLoad(s)!.level === 'high' && ` · ${t.session.compact}`}
                  </span>
                )}
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
                </div>
              </Fold>
            </div>
          </Section>
          <NewReqModal open={creating} onClose={() => setCreating(false)} defaultSession={s.key} />
        </>
      )}
    </Panel>
    </PeekScope>
  )
}
